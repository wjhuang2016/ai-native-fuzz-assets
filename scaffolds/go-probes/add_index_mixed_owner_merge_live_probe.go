package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	defaultMixedMergeDSN       = "root@tcp(127.0.0.1:14000)/"
	defaultMixedMergeStatusURL = "http://127.0.0.1:18080"
)

var (
	mixedMergeDSN       = flag.String("dsn", defaultMixedMergeDSN, "mysql dsn")
	mixedMergeStatusURL = flag.String("status-url", defaultMixedMergeStatusURL, "TiDB status/failpoint base URL")
	mixedMergeSchema    = flag.String("schema", "ai_native_issue61255_dist_merge", "schema name")
	mixedMergeTable     = flag.String("table", "rows", "table name")
	mixedMergeRows      = flag.Int("rows", 5000, "rows to preload")
	mixedMergeRegions   = flag.Int("regions", 8, "regions to split table into")
	mixedMergeBatchSize = flag.Int("batch-size", 32, "ddl reorg batch size")
	mixedMergeDistTask  = flag.Bool("dist-task", true, "whether to enable tidb_enable_dist_task")
	mixedMergeFastReorg = flag.Bool("fast-reorg", true, "whether to enable tidb_ddl_enable_fast_reorg")
)

type ddlJobState struct {
	JobID       int64
	JobType     string
	SchemaState string
	State       string
	RowCount    int64
}

func main() {
	flag.Parse()
	if err := runMixedMergeProbe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runMixedMergeProbe() error {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	db, err := sql.Open("mysql", *mixedMergeDSN)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping db: %w", err)
	}

	failpointURL := strings.TrimRight(*mixedMergeStatusURL, "/") + "/fail/github.com/pingcap/tidb/pkg/ddl/beforeBackfillMerge"
	if err := setFailpoint(ctx, failpointURL, "pause"); err != nil {
		return fmt.Errorf("enable beforeBackfillMerge pause: %w", err)
	}
	defer func() {
		_ = clearFailpoint(context.Background(), failpointURL)
	}()

	schemaTable := fmt.Sprintf("`%s`.`%s`", *mixedMergeSchema, *mixedMergeTable)
	if err := mustExec(ctx, db, "create database if not exists `"+*mixedMergeSchema+"`"); err != nil {
		return err
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_ = mustExec(cleanupCtx, db, "set global tidb_enable_dist_task = off")
		_ = mustExec(cleanupCtx, db, "set global tidb_ddl_enable_fast_reorg = off")
		_ = mustExec(cleanupCtx, db, "drop database if exists `"+*mixedMergeSchema+"`")
	}()

	if err := mustExec(ctx, db, "drop table if exists "+schemaTable); err != nil {
		return err
	}
	if err := mustExec(ctx, db, fmt.Sprintf(`create table %s (
		id int not null,
		b int not null,
		c int not null,
		pad varchar(64) not null default '',
		primary key (id)
	) partition by hash(id) partitions 2`, schemaTable)); err != nil {
		return err
	}

	if *mixedMergeDistTask {
		if err := mustExec(ctx, db, "set global tidb_enable_dist_task = on"); err != nil {
			return err
		}
	} else {
		if err := mustExec(ctx, db, "set global tidb_enable_dist_task = off"); err != nil {
			return err
		}
	}
	if *mixedMergeFastReorg {
		if err := mustExec(ctx, db, "set global tidb_ddl_enable_fast_reorg = on"); err != nil {
			return err
		}
	} else {
		if err := mustExec(ctx, db, "set global tidb_ddl_enable_fast_reorg = off"); err != nil {
			return err
		}
	}
	if err := mustExec(ctx, db, "set global tidb_ddl_reorg_worker_cnt = 1"); err != nil {
		return err
	}
	if err := mustExec(ctx, db, fmt.Sprintf("set global tidb_ddl_reorg_batch_size = %d", *mixedMergeBatchSize)); err != nil {
		return err
	}

	if err := mustExec(ctx, db, fmt.Sprintf("insert into %s values (1,1,1,'seed')", schemaTable)); err != nil {
		return err
	}
	for start := 10; start < *mixedMergeRows+10; start += 250 {
		end := start + 250
		if end > *mixedMergeRows+10 {
			end = *mixedMergeRows + 10
		}
		vals := make([]string, 0, end-start)
		for i := start; i < end; i++ {
			vals = append(vals, fmt.Sprintf("(%d,%d,%d,'x')", i, i, i))
		}
		if err := mustExec(ctx, db, "insert into "+schemaTable+" values "+strings.Join(vals, ",")); err != nil {
			return err
		}
	}
	effectiveRegions := *mixedMergeRegions
	maxRegionsByRows := max(1, *mixedMergeRows/1000)
	if effectiveRegions > maxRegionsByRows {
		effectiveRegions = maxRegionsByRows
	}
	if effectiveRegions > 1 {
		if err := mustQueryDiscard(ctx, db, fmt.Sprintf("split table %s between (1) and (%d) regions %d",
			schemaTable, *mixedMergeRows+100, effectiveRegions)); err != nil {
			return err
		}
	}
	if err := mustExec(ctx, db, "admin check table "+schemaTable); err != nil {
		return err
	}

	jobIDWatermark, err := currentDDLWatermark(ctx, db)
	if err != nil {
		return err
	}

	ddlSQL := fmt.Sprintf("alter table %s add unique index idx_b(b) global, add index idx_c(c)", schemaTable)
	ddlErrCh := make(chan error, 1)
	go func() {
		_, err := db.ExecContext(ctx, ddlSQL)
		ddlErrCh <- err
	}()

	dmlCtx, dmlCancel := context.WithCancel(ctx)
	var dmlOps atomic.Int64
	go runDeleteInsertDeleteLoop(dmlCtx, *mixedMergeDSN, schemaTable, &dmlOps)

	job, err := waitForMergePause(ctx, db, ddlErrCh, jobIDWatermark, int64(*mixedMergeRows))
	if err != nil {
		dmlCancel()
		return err
	}
	fmt.Printf("PAUSED job_id=%d row_count=%d state=%s schema_state=%s dml_ops=%d\n",
		job.JobID, job.RowCount, job.State, job.SchemaState, dmlOps.Load())

	dmlCancel()
	time.Sleep(200 * time.Millisecond)
	if err := insertBeforeMerge(ctx, db, schemaTable); err != nil {
		return err
	}

	if err := clearFailpoint(ctx, failpointURL); err != nil {
		return fmt.Errorf("disable beforeBackfillMerge pause: %w", err)
	}

	select {
	case err := <-ddlErrCh:
		if err != nil {
			return fmt.Errorf("ddl failed after release: %w", err)
		}
	case <-ctx.Done():
		return fmt.Errorf("ddl did not finish after release: %w", ctx.Err())
	}

	if err := mustExec(ctx, db, "admin check table "+schemaTable); err != nil {
		return err
	}
	if err := expectRows(ctx, db, "select id,b,c from "+schemaTable+" where b = 1 order by id", []string{"4 1 1"}); err != nil {
		return err
	}
	if err := expectRows(ctx, db, "select id,b,c from "+schemaTable+" use index(idx_b) where b = 1 order by id", []string{"4 1 1"}); err != nil {
		return err
	}
	if err := expectRows(ctx, db, "select id,b,c from "+schemaTable+" use index(idx_c) where c = 1 order by id", []string{"4 1 1"}); err != nil {
		return err
	}

	fmt.Printf("GREEN job_id=%d dml_ops=%d dist_task=%v fast_reorg=%v\n",
		job.JobID, dmlOps.Load(), *mixedMergeDistTask, *mixedMergeFastReorg)
	return nil
}

func runDeleteInsertDeleteLoop(ctx context.Context, dsn, schemaTable string, ops *atomic.Int64) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		fmt.Printf("DML loop open failed: %v\n", err)
		return
	}
	defer db.Close()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if _, err := db.ExecContext(ctx, "delete from "+schemaTable+" where id = 1"); err == nil {
			ops.Add(1)
		}
		if _, err := db.ExecContext(ctx, "insert into "+schemaTable+" values (2,1,1,'hot')"); err == nil {
			ops.Add(1)
		}
		if _, err := db.ExecContext(ctx, "delete from "+schemaTable+" where id = 2"); err == nil {
			ops.Add(1)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func insertBeforeMerge(ctx context.Context, db *sql.DB, schemaTable string) error {
	if _, err := db.ExecContext(ctx, "delete from "+schemaTable+" where id in (1,2)"); err != nil {
		return fmt.Errorf("settle rows before merge failed: %w", err)
	}
	if _, err := db.ExecContext(ctx, "insert into "+schemaTable+" values (4,1,1,'merge')"); err != nil {
		return fmt.Errorf("insert before merge failed: %w", err)
	}
	return nil
}

func waitForMergePause(ctx context.Context, db *sql.DB, ddlErrCh <-chan error, watermark, minRows int64) (ddlJobState, error) {
	deadline := time.After(90 * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var last ddlJobState
	for {
		select {
		case err := <-ddlErrCh:
			if err != nil {
				return last, fmt.Errorf("ddl returned early with error: %w", err)
			}
			return last, fmt.Errorf("ddl finished before pause was observed")
		case <-deadline:
			return last, fmt.Errorf("timeout waiting for merge pause, last=%+v", last)
		case <-ticker.C:
			job, ok, err := latestDDLJob(ctx, db, watermark)
			if err != nil {
				return last, err
			}
			if !ok {
				continue
			}
			last = job
			if strings.EqualFold(job.State, "running") && job.RowCount >= minRows && strings.EqualFold(job.SchemaState, "write reorganization") {
				time.Sleep(2 * time.Second)
				job2, ok, err := latestDDLJob(ctx, db, watermark)
				if err != nil {
					return last, err
				}
				if ok && job2.JobID == job.JobID && strings.EqualFold(job2.State, "running") && job2.RowCount >= minRows {
					return job2, nil
				}
			}
		}
	}
}

func latestDDLJob(ctx context.Context, db *sql.DB, watermark int64) (ddlJobState, bool, error) {
	const q = `select job_id, job_type, schema_state, state, row_count
from information_schema.ddl_jobs
where job_id > ? and db_name = ? and table_name = ?
order by job_id desc
limit 1`
	var st ddlJobState
	err := db.QueryRowContext(ctx, q, watermark, *mixedMergeSchema, *mixedMergeTable).
		Scan(&st.JobID, &st.JobType, &st.SchemaState, &st.State, &st.RowCount)
	if err == sql.ErrNoRows {
		return ddlJobState{}, false, nil
	}
	if err != nil {
		return ddlJobState{}, false, fmt.Errorf("query latest ddl job: %w", err)
	}
	return st, true, nil
}

func currentDDLWatermark(ctx context.Context, db *sql.DB) (int64, error) {
	var id sql.NullInt64
	if err := db.QueryRowContext(ctx, "select max(job_id) from information_schema.ddl_jobs").Scan(&id); err != nil {
		return 0, fmt.Errorf("query ddl watermark: %w", err)
	}
	if !id.Valid {
		return 0, nil
	}
	return id.Int64, nil
}

func setFailpoint(ctx context.Context, fpURL, action string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, fpURL, strings.NewReader(action))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func clearFailpoint(ctx context.Context, fpURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, fpURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func mustExec(ctx context.Context, db *sql.DB, sqlText string) error {
	if _, err := db.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("%s: %w", sqlText, err)
	}
	return nil
}

func mustQueryDiscard(ctx context.Context, db *sql.DB, sqlText string) error {
	rows, err := db.QueryContext(ctx, sqlText)
	if err != nil {
		return fmt.Errorf("%s: %w", sqlText, err)
	}
	defer rows.Close()
	for rows.Next() {
	}
	return rows.Err()
}

func expectRows(ctx context.Context, db *sql.DB, sqlText string, want []string) error {
	rows, err := db.QueryContext(ctx, sqlText)
	if err != nil {
		return fmt.Errorf("%s: %w", sqlText, err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id, b, c int64
		if err := rows.Scan(&id, &b, &c); err != nil {
			return fmt.Errorf("%s scan: %w", sqlText, err)
		}
		got = append(got, fmt.Sprintf("%d %d %d", id, b, c))
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%s rows: %w", sqlText, err)
	}
	if len(got) != len(want) {
		return fmt.Errorf("%s: got %v want %v", sqlText, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			return fmt.Errorf("%s: got %v want %v", sqlText, got, want)
		}
	}
	return nil
}

func init() {
	http.DefaultClient.Timeout = 10 * time.Second
	if _, err := url.Parse(defaultMixedMergeStatusURL); err != nil {
		panic(err)
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
