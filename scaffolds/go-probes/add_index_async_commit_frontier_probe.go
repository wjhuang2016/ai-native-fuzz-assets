package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	defaultAsyncDSN            = "root@tcp(10.2.12.57:32334)/"
	defaultAsyncSchema         = "ai_native_async_frontier"
	defaultAsyncTable          = "rows"
	defaultAsyncRows           = 120000
	defaultAsyncWorkers        = 4
	defaultAsyncFrontierOffset = 128
	defaultAsyncBatchSize      = 32
	defaultAsyncTimeout        = 8 * time.Minute
)

var (
	asyncDSN            = flag.String("dsn", defaultAsyncDSN, "mysql dsn")
	asyncSchema         = flag.String("schema", defaultAsyncSchema, "target schema")
	asyncTable          = flag.String("table", defaultAsyncTable, "target table")
	asyncRows           = flag.Int("rows", defaultAsyncRows, "rows to prefill")
	asyncWorkers        = flag.Int("workers", defaultAsyncWorkers, "number of async-commit workers")
	asyncFrontierOffset = flag.Int("frontier-offset", defaultAsyncFrontierOffset, "distance ahead of ddl row_count for targeted updates")
	asyncStartRowCount  = flag.Int64("start-row-count", 0, "minimum ddl row_count before starting async-commit workers")
	asyncTimeout        = flag.Duration("timeout", defaultAsyncTimeout, "overall timeout")
	asyncSleep          = flag.Duration("sleep", 80*time.Millisecond, "sleep between async-commit updates")
	asyncBatchSize      = flag.Int("reorg-batch-size", defaultAsyncBatchSize, "tidb_ddl_reorg_batch_size")
	asyncPartitioned    = flag.Bool("partitioned", false, "create a partitioned table")
	asyncPartitions     = flag.Int("partitions", 8, "number of hash partitions when partitioned")
	asyncGlobalIndex    = flag.Bool("global-index", false, "add a global index on b")
)

type asyncDDLState struct {
	JobID       int64
	State       string
	SchemaState string
	RowCount    int64
}

type frontierTracker struct {
	mu sync.RWMutex
	st asyncDDLState
}

func main() {
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *asyncTimeout)
	defer cancel()

	db := mustOpenAsyncDB(ctx)
	defer db.Close()

	mustSetupAsyncProbe(ctx, db)

	tableName := fmt.Sprintf("%s.%s", quoteAsync(*asyncSchema), quoteAsync(*asyncTable))
	ddlSQL := fmt.Sprintf("alter table %s add index idx_b(b)", tableName)
	if *asyncGlobalIndex {
		ddlSQL = fmt.Sprintf("alter table %s add index idx_b(b) global", tableName)
	}
	jobWatermark := latestAsyncJobID(ctx, db, ddlSQL)

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

	tracker := &frontierTracker{}
	go pollAsyncDDLState(ctx, db, ddlSQL, jobWatermark, tracker)
	readyState := waitAsyncRunning(ctx, tracker)
	log.Printf("ddl active window observed: job_id=%d state=%s schema_state=%s row_count=%d", readyState.JobID, readyState.State, readyState.SchemaState, readyState.RowCount)

	updateCtx, stopUpdates := context.WithCancel(ctx)
	defer stopUpdates()

	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	for workerID := 0; workerID < *asyncWorkers; workerID++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			runAsyncFrontierWorker(updateCtx, tracker, errCh, id)
		}(workerID)
	}

	var ddlErr error
	select {
	case ddlErr = <-ddlErrCh:
		stopUpdates()
	case err := <-errCh:
		stopUpdates()
		cancel()
		wg.Wait()
		log.Fatalf("async frontier red signature: %v", err)
	case <-ctx.Done():
		stopUpdates()
		ddlErr = ctx.Err()
	}

	wg.Wait()
	if ddlErr != nil {
		log.Fatalf("ddl did not finish cleanly: %v", ddlErr)
	}

	runAsyncFinalOracle(context.Background(), db)
	log.Printf("probe finished cleanly, touched_rows=%d", queryIntAsync(context.Background(), db, "select count(*) from "+quoteAsync(*asyncSchema)+".`audit`"))
}

func mustOpenAsyncDB(ctx context.Context) *sql.DB {
	db, err := sql.Open("mysql", *asyncDSN)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(*asyncWorkers + 8)
	db.SetMaxIdleConns(*asyncWorkers + 8)
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

func mustSetupAsyncProbe(ctx context.Context, db *sql.DB) {
	mustExecAsync(ctx, db, "set global tidb_enable_dist_task = off")
	mustExecAsync(ctx, db, "set global tidb_ddl_enable_fast_reorg = off")
	mustExecAsync(ctx, db, "set global tidb_ddl_reorg_worker_cnt = 1")
	mustExecAsync(ctx, db, fmt.Sprintf("set global tidb_ddl_reorg_batch_size = %d", *asyncBatchSize))

	mustExecAsync(ctx, db, "create database if not exists "+quoteAsync(*asyncSchema))
	mustExecAsync(ctx, db, "drop table if exists "+quoteAsync(*asyncSchema)+"."+quoteAsync(*asyncTable))
	mustExecAsync(ctx, db, "drop table if exists "+quoteAsync(*asyncSchema)+".`audit`")
	createTableSQL := fmt.Sprintf(`create table %s.%s (
		id int not null,
		b int not null,
		pad varchar(128) not null,
		primary key (id) clustered
	)`, quoteAsync(*asyncSchema), quoteAsync(*asyncTable))
	if *asyncPartitioned {
		createTableSQL += fmt.Sprintf(" partition by hash(id) partitions %d", *asyncPartitions)
	}
	mustExecAsync(ctx, db, createTableSQL)
	mustExecAsync(ctx, db, "create table "+quoteAsync(*asyncSchema)+".`audit` (id int primary key, delta int not null)")

	const batch = 1000
	for start := 1; start <= *asyncRows; start += batch {
		end := start + batch - 1
		if end > *asyncRows {
			end = *asyncRows
		}
		valueStrings := make([]string, 0, end-start+1)
		args := make([]any, 0, (end-start+1)*3)
		for id := start; id <= end; id++ {
			valueStrings = append(valueStrings, "(?, ?, repeat('x', 128))")
			args = append(args, id, id)
		}
		sqlText := fmt.Sprintf("insert into %s.%s (id, b, pad) values %s", quoteAsync(*asyncSchema), quoteAsync(*asyncTable), strings.Join(valueStrings, ","))
		mustExecArgsAsync(ctx, db, sqlText, args...)
	}
	log.Printf("setup done, rows=%d", queryIntAsync(ctx, db, fmt.Sprintf("select count(*) from %s.%s", quoteAsync(*asyncSchema), quoteAsync(*asyncTable))))
}

func waitAsyncRunning(ctx context.Context, tracker *frontierTracker) asyncDDLState {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait ddl running: %v", ctx.Err())
		case <-ticker.C:
		}
		st := tracker.get()
		if st.State == "running" && st.SchemaState == "write reorganization" && st.RowCount >= *asyncStartRowCount {
			return st
		}
		if isAsyncTerminal(st.State) {
			log.Fatalf("ddl became terminal before async frontier updates started: state=%s row_count=%d", st.State, st.RowCount)
		}
	}
}

func pollAsyncDDLState(ctx context.Context, db *sql.DB, ddlSQL string, watermark int64, tracker *frontierTracker) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		st, ok := latestAsyncDDLState(ctx, db, ddlSQL, watermark)
		if !ok {
			continue
		}
		tracker.set(st)
		if isAsyncTerminal(st.State) {
			return
		}
	}
}

func runAsyncFrontierWorker(
	ctx context.Context,
	tracker *frontierTracker,
	errCh chan<- error,
	workerID int,
) {
	db := mustOpenAsyncDB(ctx)
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		signalAsyncRed(errCh, fmt.Errorf("worker %d get dedicated conn: %w", workerID, err))
		return
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "set @@tidb_enable_async_commit = 1"); err != nil {
		signalAsyncRed(errCh, fmt.Errorf("worker %d enable async commit: %w", workerID, err))
		return
	}
	if _, err := conn.ExecContext(ctx, "set @@tidb_enable_1pc = 0"); err != nil {
		signalAsyncRed(errCh, fmt.Errorf("worker %d disable 1pc: %w", workerID, err))
		return
	}
	if _, err := conn.ExecContext(ctx, "set @@tidb_txn_mode = 'pessimistic'"); err != nil {
		signalAsyncRed(errCh, fmt.Errorf("worker %d set pessimistic mode: %w", workerID, err))
		return
	}

	tableName := fmt.Sprintf("%s.%s", quoteAsync(*asyncSchema), quoteAsync(*asyncTable))
	auditTable := quoteAsync(*asyncSchema) + ".`audit`"
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		st := tracker.get()
		if isAsyncTerminal(st.State) {
			return
		}
		if st.State != "running" {
			time.Sleep(*asyncSleep)
			continue
		}

		targetID := int(st.RowCount) + *asyncFrontierOffset + workerID*13
		if targetID < 1 {
			targetID = 1
		}
		if targetID > *asyncRows {
			targetID = *asyncRows - workerID
		}
		if targetID < 1 {
			targetID = 1
		}

		if _, err := conn.ExecContext(ctx, "begin pessimistic"); err != nil {
			if ctx.Err() != nil {
				return
			}
			if isAsyncRetryable(err) {
				time.Sleep(*asyncSleep)
				continue
			}
			signalAsyncRed(errCh, fmt.Errorf("worker %d begin pessimistic on id=%d: %w", workerID, targetID, err))
			return
		}

		if _, err := conn.ExecContext(ctx, "update "+tableName+" set b = b + 1 where id = ?", targetID); err != nil {
			_, _ = conn.ExecContext(context.Background(), "rollback")
			if ctx.Err() != nil {
				return
			}
			if isAsyncRetryable(err) {
				time.Sleep(*asyncSleep)
				continue
			}
			signalAsyncRed(errCh, fmt.Errorf("worker %d update id=%d row_count=%d: %w", workerID, targetID, st.RowCount, err))
			return
		}

		if _, err := conn.ExecContext(ctx, "insert into "+auditTable+" values (?, 1) on duplicate key update delta = delta + 1", targetID); err != nil {
			_, _ = conn.ExecContext(context.Background(), "rollback")
			if ctx.Err() != nil {
				return
			}
			if isAsyncRetryable(err) {
				time.Sleep(*asyncSleep)
				continue
			}
			signalAsyncRed(errCh, fmt.Errorf("worker %d audit id=%d row_count=%d: %w", workerID, targetID, st.RowCount, err))
			return
		}

		if _, err := conn.ExecContext(ctx, "commit"); err != nil {
			_, _ = conn.ExecContext(context.Background(), "rollback")
			if ctx.Err() != nil {
				return
			}
			if isAsyncRetryable(err) {
				time.Sleep(*asyncSleep)
				continue
			}
			signalAsyncRed(errCh, fmt.Errorf("worker %d commit id=%d row_count=%d: %w", workerID, targetID, st.RowCount, err))
			return
		}

		time.Sleep(*asyncSleep)
	}
}

func runAsyncFinalOracle(ctx context.Context, db *sql.DB) {
	tableName := fmt.Sprintf("%s.%s", quoteAsync(*asyncSchema), quoteAsync(*asyncTable))
	mustExecAsync(ctx, db, "admin check table "+tableName)

	rows, err := db.QueryContext(ctx, "select id, delta from "+quoteAsync(*asyncSchema)+".`audit` order by id")
	if err != nil {
		log.Fatalf("read audit rows: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id    int
			delta int
		)
		if err := rows.Scan(&id, &delta); err != nil {
			log.Fatalf("scan audit row: %v", err)
		}
		got := queryIntArgsAsync(ctx, db, "select b from "+tableName+" where id = ?", id)
		want := id + delta
		if got != want {
			log.Fatalf("final row mismatch: id=%d got=%d want=%d", id, got, want)
		}
		indexCnt := queryIntArgsAsync(ctx, db, "select count(*) from "+tableName+" use index(idx_b) where b = ?", want)
		tableCnt := queryIntArgsAsync(ctx, db, "select count(*) from "+tableName+" ignore index(idx_b) where b = ?", want)
		if indexCnt != tableCnt {
			log.Fatalf("final index/table mismatch: id=%d b=%d index_cnt=%d table_cnt=%d", id, want, indexCnt, tableCnt)
		}
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("iterate audit rows: %v", err)
	}
}

func latestAsyncJobID(ctx context.Context, db *sql.DB, ddlSQL string) int64 {
	var jobID sql.NullInt64
	err := db.QueryRowContext(ctx, `
select max(job_id)
from information_schema.ddl_jobs
where db_name = ? and table_name = ? and query = ?`, *asyncSchema, *asyncTable, ddlSQL).Scan(&jobID)
	if err != nil {
		log.Fatalf("latest job id: %v", err)
	}
	if !jobID.Valid {
		return 0
	}
	return jobID.Int64
}

func latestAsyncDDLState(ctx context.Context, db *sql.DB, ddlSQL string, watermark int64) (asyncDDLState, bool) {
	rows, err := db.QueryContext(ctx, `
select job_id, state, schema_state, row_count
from information_schema.ddl_jobs
where db_name = ? and table_name = ? and query = ?
  and job_id > ?
order by job_id desc
limit 1`, *asyncSchema, *asyncTable, ddlSQL, watermark)
	if err != nil {
		return asyncDDLState{}, false
	}
	defer rows.Close()
	if !rows.Next() {
		return asyncDDLState{}, false
	}
	var st asyncDDLState
	if err := rows.Scan(&st.JobID, &st.State, &st.SchemaState, &st.RowCount); err != nil {
		return asyncDDLState{}, false
	}
	return st, true
}

func (t *frontierTracker) set(st asyncDDLState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.st = st
}

func (t *frontierTracker) get() asyncDDLState {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.st
}

func isAsyncTerminal(state string) bool {
	switch state {
	case "done", "synced", "rollback done", "cancelled":
		return true
	default:
		return false
	}
}

func isAsyncRetryable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "write conflict") ||
		strings.Contains(msg, "try again later") ||
		strings.Contains(msg, "information schema is changed") ||
		strings.Contains(msg, "has changed") ||
		strings.Contains(msg, "invalid connection") ||
		strings.Contains(msg, "txn too large") ||
		strings.Contains(msg, "deadlock")
}

func signalAsyncRed(errCh chan<- error, err error) {
	select {
	case errCh <- err:
	default:
	}
}

func mustExecAsync(ctx context.Context, db *sql.DB, sqlText string) {
	mustExecArgsAsync(ctx, db, sqlText)
}

func mustExecArgsAsync(ctx context.Context, db *sql.DB, sqlText string, args ...any) {
	if _, err := db.ExecContext(ctx, sqlText, args...); err != nil {
		log.Fatalf("exec failed: %s: %v", sqlText, err)
	}
}

func queryIntAsync(ctx context.Context, db *sql.DB, sqlText string) int {
	return queryIntArgsAsync(ctx, db, sqlText)
}

func queryIntArgsAsync(ctx context.Context, db *sql.DB, sqlText string, args ...any) int {
	var v int
	if err := db.QueryRowContext(ctx, sqlText, args...).Scan(&v); err != nil {
		log.Fatalf("query int failed: %s args=%v err=%v", sqlText, args, err)
	}
	return v
}

func quoteAsync(ident string) string {
	return "`" + strings.ReplaceAll(ident, "`", "``") + "`"
}
