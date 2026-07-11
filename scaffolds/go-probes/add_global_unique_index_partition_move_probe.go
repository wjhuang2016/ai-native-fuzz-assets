package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	defaultMoveDSN        = "root@tcp(10.2.12.57:32334)/"
	defaultMoveSchema     = "ai_native_global_move"
	defaultMoveTable      = "rows"
	defaultMoveRows       = 120000
	defaultMovePartitions = 5
	defaultMoveTimeout    = 5 * time.Minute
)

var (
	moveDSN        = flag.String("dsn", defaultMoveDSN, "mysql dsn")
	moveSchema     = flag.String("schema", defaultMoveSchema, "schema")
	moveTable      = flag.String("table", defaultMoveTable, "table")
	moveRows       = flag.Int("rows", defaultMoveRows, "prefill rows")
	movePartitions = flag.Int("partitions", defaultMovePartitions, "hash partition count")
	moveTimeout    = flag.Duration("timeout", defaultMoveTimeout, "overall timeout")
	moveSleep      = flag.Duration("sleep", 50*time.Millisecond, "sleep between transactions")
	moveBatchSize  = flag.Int("reorg-batch-size", 16, "tidb_ddl_reorg_batch_size")
	moveOffset     = flag.Int("move-offset", 1000000, "offset added to b during move")
)

type moveDDLState struct {
	JobID       int64
	State       string
	SchemaState string
	RowCount    int64
}

func main() {
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *moveTimeout)
	defer cancel()

	db := mustOpenMoveDB(ctx)
	defer db.Close()

	mustSetupMoveProbe(ctx, db)

	tableName := fmt.Sprintf("%s.%s", quoteMove(*moveSchema), quoteMove(*moveTable))
	ddlSQL := fmt.Sprintf("alter table %s add unique index idx_b(b) global", tableName)
	jobWatermark := latestMoveJobID(ctx, db, ddlSQL)

	ddlErrCh := make(chan error, 1)
	go func() {
		start := time.Now()
		log.Printf("ddl start: %s", ddlSQL)
		_, err := db.ExecContext(ctx, ddlSQL)
		if err != nil {
			ddlErrCh <- fmt.Errorf("ddl exec after %s: %w", time.Since(start), err)
			return
		}
		log.Printf("ddl finished successfully in %s", time.Since(start))
		ddlErrCh <- nil
	}()

	waitMoveRunning(ctx, db, ddlSQL, jobWatermark)

	var nextVal atomic.Int64
	nextVal.Store(int64(*moveRows))
	dmlCtx, stopDML := context.WithCancel(ctx)
	defer stopDML()
	dmlErrCh := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runMoveWorker(dmlCtx, &nextVal, dmlErrCh)
	}()

	select {
	case err := <-ddlErrCh:
		if err != nil {
			log.Fatalf("ddl failed: %v", err)
		}
		stopDML()
	case err := <-dmlErrCh:
		stopDML()
		wg.Wait()
		cancel()
		log.Fatalf("partition-move worker failed: %v", err)
	case <-ctx.Done():
		stopDML()
		wg.Wait()
		log.Fatalf("probe timeout: %v", ctx.Err())
	}
	wg.Wait()

	runMoveFinalOracle(context.Background(), db)
	log.Printf("probe finished cleanly")
}

func mustOpenMoveDB(ctx context.Context) *sql.DB {
	db, err := sql.Open("mysql", *moveDSN)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(16)
	db.SetConnMaxLifetime(0)
	for {
		if err := db.PingContext(ctx); err == nil {
			return db
		}
		if ctx.Err() != nil {
			log.Fatalf("ping db: %v", err)
		}
		time.Sleep(2 * time.Second)
	}
}

func mustSetupMoveProbe(ctx context.Context, db *sql.DB) {
	mustExecMove(ctx, db, "set global tidb_enable_dist_task = off")
	mustExecMove(ctx, db, "set global tidb_ddl_enable_fast_reorg = off")
	mustExecMove(ctx, db, "set global tidb_ddl_reorg_worker_cnt = 1")
	mustExecMove(ctx, db, fmt.Sprintf("set global tidb_ddl_reorg_batch_size = %d", *moveBatchSize))

	mustExecMove(ctx, db, "create database if not exists "+quoteMove(*moveSchema))
	mustExecMove(ctx, db, "drop table if exists "+quoteMove(*moveSchema)+"."+quoteMove(*moveTable))
	mustExecMove(ctx, db, fmt.Sprintf(`create table %s.%s (
		a int not null,
		b int not null,
		pad varchar(64) not null default '',
		key(a)
	) partition by hash(a) partitions %d`,
		quoteMove(*moveSchema), quoteMove(*moveTable), *movePartitions))

	const batch = 1000
	for start := 1; start <= *moveRows; start += batch {
		end := start + batch - 1
		if end > *moveRows {
			end = *moveRows
		}
		valueStrings := make([]string, 0, end-start+1)
		args := make([]any, 0, (end-start+1)*3)
		for v := start; v <= end; v++ {
			valueStrings = append(valueStrings, "(?, ?, repeat('x', 64))")
			args = append(args, v, v)
		}
		sqlText := fmt.Sprintf("insert into %s.%s (a, b, pad) values %s", quoteMove(*moveSchema), quoteMove(*moveTable), strings.Join(valueStrings, ","))
		mustExecArgsMove(ctx, db, sqlText, args...)
	}

	log.Printf("setup done, rows=%d", queryIntMove(ctx, db, fmt.Sprintf("select count(*) from %s.%s", quoteMove(*moveSchema), quoteMove(*moveTable))))
}

func waitMoveRunning(ctx context.Context, db *sql.DB, ddlSQL string, watermark int64) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait ddl running: %v", ctx.Err())
		case <-ticker.C:
		}
		st, ok := latestMoveDDLState(ctx, db, ddlSQL, watermark)
		if !ok {
			continue
		}
		if st.State == "running" && st.SchemaState == "write reorganization" {
			log.Printf("ddl active window observed: job_id=%d state=%s schema_state=%s row_count=%d", st.JobID, st.State, st.SchemaState, st.RowCount)
			return
		}
		if isMoveTerminal(st.State) {
			log.Fatalf("ddl became terminal before worker start: state=%s row_count=%d", st.State, st.RowCount)
		}
	}
}

func runMoveWorker(ctx context.Context, nextVal *atomic.Int64, errCh chan<- error) {
	db := mustOpenMoveDB(ctx)
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		signalMoveErr(errCh, fmt.Errorf("get conn: %w", err))
		return
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "set @@tidb_enable_async_commit = 1"); err != nil {
		signalMoveErr(errCh, fmt.Errorf("enable async commit: %w", err))
		return
	}
	if _, err := conn.ExecContext(ctx, "set @@tidb_enable_1pc = 0"); err != nil {
		signalMoveErr(errCh, fmt.Errorf("disable 1pc: %w", err))
		return
	}
	if _, err := conn.ExecContext(ctx, "set @@tidb_txn_mode = 'pessimistic'"); err != nil {
		signalMoveErr(errCh, fmt.Errorf("set pessimistic mode: %w", err))
		return
	}

	tableName := fmt.Sprintf("%s.%s", quoteMove(*moveSchema), quoteMove(*moveTable))
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		tmp := int(nextVal.Add(1))
		if _, err := conn.ExecContext(ctx, "begin pessimistic"); err != nil {
			if ctx.Err() != nil {
				return
			}
			if isMoveRetryable(err) {
				time.Sleep(*moveSleep)
				continue
			}
			signalMoveErr(errCh, fmt.Errorf("begin pessimistic: %w", err))
			return
		}

		if _, err := conn.ExecContext(ctx, "insert into "+tableName+" values (?, ?, repeat('x', 64))", tmp, tmp); err != nil {
			_, _ = conn.ExecContext(context.Background(), "rollback")
			if ctx.Err() != nil {
				return
			}
			if isMoveRetryable(err) {
				time.Sleep(*moveSleep)
				continue
			}
			signalMoveErr(errCh, fmt.Errorf("insert tmp=%d: %w", tmp, err))
			return
		}

		if _, err := conn.ExecContext(ctx,
			"update "+tableName+" set b = b + ?, a = b where b = ?",
			*moveOffset, tmp-1,
		); err != nil {
			_, _ = conn.ExecContext(context.Background(), "rollback")
			if ctx.Err() != nil {
				return
			}
			if isMoveRetryable(err) {
				time.Sleep(*moveSleep)
				continue
			}
			signalMoveErr(errCh, fmt.Errorf("update tmp=%d prev=%d: %w", tmp, tmp-1, err))
			return
		}

		if _, err := conn.ExecContext(ctx, "commit"); err != nil {
			_, _ = conn.ExecContext(context.Background(), "rollback")
			if ctx.Err() != nil {
				return
			}
			if isMoveRetryable(err) {
				time.Sleep(*moveSleep)
				continue
			}
			signalMoveErr(errCh, fmt.Errorf("commit tmp=%d: %w", tmp, err))
			return
		}

		time.Sleep(*moveSleep)
	}
}

func runMoveFinalOracle(ctx context.Context, db *sql.DB) {
	tableName := fmt.Sprintf("%s.%s", quoteMove(*moveSchema), quoteMove(*moveTable))
	mustExecMove(ctx, db, "admin check table "+tableName)

	tableCnt := queryIntMove(ctx, db, "select count(*) from "+tableName+" ignore index(idx_b)")
	indexCnt := queryIntMove(ctx, db, "select count(*) from "+tableName+" use index(idx_b)")
	if tableCnt != indexCnt {
		log.Fatalf("row count mismatch between table and global index: table=%d index=%d", tableCnt, indexCnt)
	}

	dupCnt := queryIntMove(ctx, db, "select count(*) from (select b from "+tableName+" group by b having count(*) > 1) x")
	if dupCnt != 0 {
		log.Fatalf("unexpected duplicate b values remain after unique global index add: dup_groups=%d", dupCnt)
	}

	for _, b := range []int{1, *moveRows / 2, *moveRows, *moveRows + 10, *moveRows + *moveOffset} {
		tablePath := queryIntArgsMove(ctx, db, "select count(*) from "+tableName+" ignore index(idx_b) where b = ?", b)
		indexPath := queryIntArgsMove(ctx, db, "select count(*) from "+tableName+" use index(idx_b) where b = ?", b)
		if tablePath != indexPath {
			log.Fatalf("point-path mismatch on b=%d: table=%d index=%d", b, tablePath, indexPath)
		}
	}
}

func latestMoveJobID(ctx context.Context, db *sql.DB, ddlSQL string) int64 {
	var jobID sql.NullInt64
	err := db.QueryRowContext(ctx, `
select max(job_id)
from information_schema.ddl_jobs
where db_name = ? and table_name = ? and query = ?`, *moveSchema, *moveTable, ddlSQL).Scan(&jobID)
	if err != nil {
		log.Fatalf("latest move job id: %v", err)
	}
	if !jobID.Valid {
		return 0
	}
	return jobID.Int64
}

func latestMoveDDLState(ctx context.Context, db *sql.DB, ddlSQL string, watermark int64) (moveDDLState, bool) {
	rows, err := db.QueryContext(ctx, `
select job_id, state, schema_state, row_count
from information_schema.ddl_jobs
where db_name = ? and table_name = ? and query = ?
  and job_id > ?
order by job_id desc
limit 1`, *moveSchema, *moveTable, ddlSQL, watermark)
	if err != nil {
		return moveDDLState{}, false
	}
	defer rows.Close()
	if !rows.Next() {
		return moveDDLState{}, false
	}
	var st moveDDLState
	if err := rows.Scan(&st.JobID, &st.State, &st.SchemaState, &st.RowCount); err != nil {
		return moveDDLState{}, false
	}
	return st, true
}

func isMoveTerminal(state string) bool {
	switch state {
	case "done", "synced", "rollback done", "cancelled":
		return true
	default:
		return false
	}
}

func isMoveRetryable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "write conflict") ||
		strings.Contains(msg, "try again later") ||
		strings.Contains(msg, "information schema is changed") ||
		strings.Contains(msg, "has changed") ||
		strings.Contains(msg, "invalid connection") ||
		strings.Contains(msg, "deadlock")
}

func signalMoveErr(errCh chan<- error, err error) {
	select {
	case errCh <- err:
	default:
	}
}

func mustExecMove(ctx context.Context, db *sql.DB, sqlText string) {
	mustExecArgsMove(ctx, db, sqlText)
}

func mustExecArgsMove(ctx context.Context, db *sql.DB, sqlText string, args ...any) {
	if _, err := db.ExecContext(ctx, sqlText, args...); err != nil {
		log.Fatalf("exec failed: %s: %v", sqlText, err)
	}
}

func queryIntMove(ctx context.Context, db *sql.DB, sqlText string) int {
	return queryIntArgsMove(ctx, db, sqlText)
}

func queryIntArgsMove(ctx context.Context, db *sql.DB, sqlText string, args ...any) int {
	var v int
	if err := db.QueryRowContext(ctx, sqlText, args...).Scan(&v); err != nil {
		log.Fatalf("query int failed: %s args=%v err=%v", sqlText, args, err)
	}
	return v
}

func quoteMove(ident string) string {
	return "`" + strings.ReplaceAll(ident, "`", "``") + "`"
}
