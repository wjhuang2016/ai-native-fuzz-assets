package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	defaultMSIngestDSN       = "root@tcp(127.0.0.1:14101)/"
	defaultMSIngestStatusURL = "http://127.0.0.1:18182"
)

var (
	msIngestDSN       = flag.String("dsn", defaultMSIngestDSN, "mysql dsn")
	msIngestStatusURL = flag.String("status-url", defaultMSIngestStatusURL, "TiDB status/failpoint base URL")
	msIngestSchema    = flag.String("schema", "ai_native_ingest_ms_merge_live", "schema name")
	msIngestTable     = flag.String("table", "t", "table name")
	msIngestRows      = flag.Int("rows", 32768, "rows to preload")
	msIngestRegions   = flag.Int("regions", 8, "regions to split table into")
	msIngestInsert    = flag.Int("insert-rows", 1000, "rows to insert during merge pause")
	msIngestWorkers   = flag.Int("workers", 4, "ddl reorg worker count")
	msIngestBatchSize = flag.Int("batch-size", 32, "ddl reorg batch size")
	msPostBatchTarget = flag.String("post-batch-target", "", "mockBackfillPostBatchErrForWorker target, e.g. tail")
	msPostBatchSleep  = flag.Int("post-batch-sleep-ms", 0, "mockBackfillPostBatchErrSleepMs")
	msDownscaleTo     = flag.Int("downscale-to", 0, "issue ADMIN ALTER DDL JOBS ... THREAD = n after releasing merge pause")
	msDownscaleDelay  = flag.Duration("downscale-delay", 2*time.Second, "delay before downscaling after releasing merge pause")
)

type msDDLJobState struct {
	JobID       int64
	JobType     string
	SchemaState string
	State       string
	RowCount    int64
}

func main() {
	flag.Parse()
	if err := runMSIngestProbe(); err != nil {
		panic(err)
	}
}

func runMSIngestProbe() error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	db, err := sql.Open("mysql", *msIngestDSN)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping db: %w", err)
	}

	failpointURL := strings.TrimRight(*msIngestStatusURL, "/") + "/fail/github.com/pingcap/tidb/pkg/ddl/beforeBackfillMerge"
	if err := setFailpoint(ctx, failpointURL, "pause"); err != nil {
		return fmt.Errorf("enable beforeBackfillMerge pause: %w", err)
	}
	defer func() {
		_ = clearFailpoint(context.Background(), failpointURL)
	}()

	schemaTable := fmt.Sprintf("`%s`.`%s`", *msIngestSchema, *msIngestTable)
	if err := mustExec(ctx, db, "create database if not exists `"+*msIngestSchema+"`"); err != nil {
		return err
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_ = mustExec(cleanupCtx, db, "set global tidb_enable_dist_task = off")
		_ = mustExec(cleanupCtx, db, "set global tidb_ddl_enable_fast_reorg = off")
	}()

	if err := mustExec(ctx, db, "drop table if exists "+schemaTable); err != nil {
		return err
	}
	if err := mustExec(ctx, db, fmt.Sprintf(`create table %s (
		id int not null,
		b int not null,
		c int not null,
		pad varchar(64) not null default 'x',
		primary key (id)
	)`, schemaTable)); err != nil {
		return err
	}

	if err := mustExec(ctx, db, "set global tidb_enable_dist_task = off"); err != nil {
		return err
	}
	if err := mustExec(ctx, db, "set global tidb_ddl_enable_fast_reorg = on"); err != nil {
		return err
	}
	if err := mustExec(ctx, db, fmt.Sprintf("set global tidb_ddl_reorg_worker_cnt = %d", *msIngestWorkers)); err != nil {
		return err
	}
	if err := mustExec(ctx, db, fmt.Sprintf("set global tidb_ddl_reorg_batch_size = %d", *msIngestBatchSize)); err != nil {
		return err
	}

	if err := insertMSRows(ctx, db, schemaTable, 1, *msIngestRows); err != nil {
		return err
	}
	effectiveRegions := *msIngestRegions
	maxRegionsByRows := max(1, *msIngestRows/1000)
	if effectiveRegions > maxRegionsByRows {
		effectiveRegions = maxRegionsByRows
	}
	if effectiveRegions > 1 {
		if err := mustQueryDiscard(ctx, db, fmt.Sprintf(
			"split table %s between (1) and (%d) regions %d",
			schemaTable, *msIngestRows+100, effectiveRegions,
		)); err != nil {
			return err
		}
	}
	if err := mustExec(ctx, db, "admin check table "+schemaTable); err != nil {
		return err
	}

	jobIDWatermark, err := currentMSDDLWatermark(ctx, db)
	if err != nil {
		return err
	}

	ddlSQL := fmt.Sprintf("alter table %s add index idx_b(b), add index idx_c(c)", schemaTable)
	ddlErrCh := make(chan error, 1)
	go func() {
		_, err := db.ExecContext(ctx, ddlSQL)
		ddlErrCh <- err
	}()

	job, err := waitForMSMergePause(ctx, db, ddlErrCh, jobIDWatermark, int64(*msIngestRows))
	if err != nil {
		return err
	}
	fmt.Printf("PAUSED job_id=%d row_count=%d state=%s schema_state=%s job_type=%s\n",
		job.JobID, job.RowCount, job.State, job.SchemaState, job.JobType)

	if err := insertMSRows(ctx, db, schemaTable, *msIngestRows+1, *msIngestRows+*msIngestInsert); err != nil {
		return fmt.Errorf("insert rows during merge pause: %w", err)
	}

	postBatchURL := strings.TrimRight(*msIngestStatusURL, "/") + "/fail/github.com/pingcap/tidb/pkg/ddl/mockBackfillPostBatchErrForWorker"
	postBatchSleepURL := strings.TrimRight(*msIngestStatusURL, "/") + "/fail/github.com/pingcap/tidb/pkg/ddl/mockBackfillPostBatchErrSleepMs"
	if *msPostBatchTarget != "" {
		if err := setFailpoint(ctx, postBatchURL, fmt.Sprintf(`return("%s")`, *msPostBatchTarget)); err != nil {
			return fmt.Errorf("enable mockBackfillPostBatchErrForWorker: %w", err)
		}
		defer func() {
			_ = clearFailpoint(context.Background(), postBatchURL)
		}()
	}
	if *msPostBatchSleep > 0 {
		if err := setFailpoint(ctx, postBatchSleepURL, fmt.Sprintf("return(%d)", *msPostBatchSleep)); err != nil {
			return fmt.Errorf("enable mockBackfillPostBatchErrSleepMs: %w", err)
		}
		defer func() {
			_ = clearFailpoint(context.Background(), postBatchSleepURL)
		}()
	}

	if err := clearFailpoint(ctx, failpointURL); err != nil {
		return fmt.Errorf("disable beforeBackfillMerge pause: %w", err)
	}

	if *msDownscaleTo > 0 && *msDownscaleTo < *msIngestWorkers {
		time.Sleep(*msDownscaleDelay)
		downscaleSQL := fmt.Sprintf("admin alter ddl jobs %d thread = %d", job.JobID, *msDownscaleTo)
		if err := mustExec(ctx, db, downscaleSQL); err != nil {
			return fmt.Errorf("downscale ddl job: %w", err)
		}
		fmt.Printf("DOWNSCALE job_id=%d to=%d delay=%s\n", job.JobID, *msDownscaleTo, msDownscaleDelay.String())
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
	wantRows := *msIngestRows + *msIngestInsert
	for _, sqlText := range []string{
		"select count(*) from " + schemaTable,
		"select count(*) from " + schemaTable + " use index(idx_b)",
		"select count(*) from " + schemaTable + " use index(idx_c)",
	} {
		got, err := queryInt(ctx, db, sqlText)
		if err != nil {
			return err
		}
		if got != wantRows {
			return fmt.Errorf("%s: got %d want %d", sqlText, got, wantRows)
		}
	}

	fmt.Printf("GREEN job_id=%d rows=%d pause_insert_rows=%d workers=%d batch_size=%d\n",
		job.JobID, wantRows, *msIngestInsert, *msIngestWorkers, *msIngestBatchSize)
	return nil
}

func insertMSRows(ctx context.Context, db *sql.DB, schemaTable string, start, end int) error {
	for batchStart := start; batchStart <= end; batchStart += 500 {
		batchEnd := min(end, batchStart+499)
		values := make([]string, 0, batchEnd-batchStart+1)
		for i := batchStart; i <= batchEnd; i++ {
			values = append(values, fmt.Sprintf("(%d,%d,%d,repeat('x',64))", i, i, i))
		}
		if err := mustExec(ctx, db, "insert into "+schemaTable+" values "+strings.Join(values, ",")); err != nil {
			return err
		}
	}
	return nil
}

func waitForMSMergePause(
	ctx context.Context,
	db *sql.DB,
	ddlErrCh <-chan error,
	watermark, minRows int64,
) (msDDLJobState, error) {
	deadline := time.After(90 * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var last msDDLJobState
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
			job, ok, err := latestMSDDLJob(ctx, db, watermark)
			if err != nil {
				return last, err
			}
			if !ok {
				continue
			}
			last = job
			if strings.EqualFold(job.State, "running") &&
				job.RowCount >= minRows &&
				strings.EqualFold(job.SchemaState, "write reorganization") {
				time.Sleep(2 * time.Second)
				job2, ok, err := latestMSDDLJob(ctx, db, watermark)
				if err != nil {
					return last, err
				}
				if ok &&
					job2.JobID == job.JobID &&
					strings.EqualFold(job2.State, "running") &&
					job2.RowCount >= minRows &&
					strings.EqualFold(job2.SchemaState, "write reorganization") {
					return job2, nil
				}
			}
		}
	}
}

func latestMSDDLJob(ctx context.Context, db *sql.DB, watermark int64) (msDDLJobState, bool, error) {
	const q = `select job_id, job_type, schema_state, state, row_count
from information_schema.ddl_jobs
where job_id > ? and db_name = ? and table_name = ?
order by job_id desc
limit 1`
	var st msDDLJobState
	err := db.QueryRowContext(ctx, q, watermark, *msIngestSchema, *msIngestTable).
		Scan(&st.JobID, &st.JobType, &st.SchemaState, &st.State, &st.RowCount)
	if err == sql.ErrNoRows {
		return msDDLJobState{}, false, nil
	}
	if err != nil {
		return msDDLJobState{}, false, fmt.Errorf("query latest ddl job: %w", err)
	}
	return st, true, nil
}

func currentMSDDLWatermark(ctx context.Context, db *sql.DB) (int64, error) {
	var id sql.NullInt64
	if err := db.QueryRowContext(ctx, "select max(job_id) from information_schema.ddl_jobs").Scan(&id); err != nil {
		return 0, fmt.Errorf("query ddl watermark: %w", err)
	}
	if !id.Valid {
		return 0, nil
	}
	return id.Int64, nil
}

func queryInt(ctx context.Context, db *sql.DB, sqlText string) (int, error) {
	var v int
	if err := db.QueryRowContext(ctx, sqlText).Scan(&v); err != nil {
		return 0, fmt.Errorf("%s: %w", sqlText, err)
	}
	return v, nil
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

func init() {
	http.DefaultClient.Timeout = 10 * time.Second
	if _, err := url.Parse(defaultMSIngestStatusURL); err != nil {
		panic(err)
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
