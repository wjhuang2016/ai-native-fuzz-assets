package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	defaultModifyDownscaleDSN       = "root@tcp(127.0.0.1:14101)/"
	defaultModifyDownscaleStatusURL = "http://127.0.0.1:18182"
)

var (
	modifyDownscaleDSN       = flag.String("dsn", defaultModifyDownscaleDSN, "mysql dsn")
	modifyDownscaleStatusURL = flag.String("status-url", defaultModifyDownscaleStatusURL, "TiDB status/failpoint base URL")
	modifyDownscaleSchema    = flag.String("schema", "ai_native_modify_downscale_live", "schema name")
	modifyDownscaleTable     = flag.String("table", "rows", "table name")
	modifyDownscaleRows      = flag.Int("rows", 120000, "rows to prefill before modify column")
	modifyDownscaleWorkers   = flag.Int("workers", 4, "ddl reorg worker count")
	modifyDownscaleBatchSize = flag.Int("batch-size", 32, "ddl reorg batch size")
	modifyDownscaleTarget    = flag.String("post-batch-target", "", "mockBackfillPostBatchErrForWorker target, e.g. tail")
	modifyDownscaleSleepMS   = flag.Int("post-batch-sleep-ms", 0, "mockBackfillPostBatchErrSleepMs")
	modifyDownscaleTo        = flag.Int("downscale-to", 1, "ADMIN ALTER DDL JOBS ... THREAD = n")
	modifyDownscaleDelay     = flag.Duration("downscale-delay", 2*time.Second, "delay before downscale after ddl enters running window")
	modifyDownscaleMinRows   = flag.Int64("min-row-count", 10000, "minimum row_count before downscale")
	modifyMergePause         = flag.Bool("merge-pause", false, "pause on beforeBackfillMerge and downscale after releasing merge pause")
	modifyPauseInsertRows    = flag.Int("pause-insert-rows", 0, "rows to insert during merge pause")
)

type modifyDDLState struct {
	JobID       int64
	State       string
	SchemaState string
	RowCount    int64
}

func main() {
	flag.Parse()
	if err := runModifyDownscaleProbe(); err != nil {
		panic(err)
	}
}

func runModifyDownscaleProbe() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	db, err := sql.Open("mysql", *modifyDownscaleDSN)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping db: %w", err)
	}

	schemaTable := fmt.Sprintf("`%s`.`%s`", *modifyDownscaleSchema, *modifyDownscaleTable)
	if err := mustExec3(ctx, db, "create database if not exists `"+*modifyDownscaleSchema+"`"); err != nil {
		return err
	}
	if err := mustExec3(ctx, db, "drop table if exists "+schemaTable); err != nil {
		return err
	}
	if err := mustExec3(ctx, db, fmt.Sprintf(`create table %s (
		id int not null,
		x int not null,
		y int not null,
		padding varchar(64) not null default '',
		primary key (id) clustered,
		key idx_x(x)
	)`, schemaTable)); err != nil {
		return err
	}
	if err := mustExec3(ctx, db, "set global tidb_enable_dist_task = off"); err != nil {
		return err
	}
	if err := mustExec3(ctx, db, "set global tidb_ddl_enable_fast_reorg = off"); err != nil {
		return err
	}
	if err := mustExec3(ctx, db, fmt.Sprintf("set global tidb_ddl_reorg_worker_cnt = %d", *modifyDownscaleWorkers)); err != nil {
		return err
	}
	if err := mustExec3(ctx, db, fmt.Sprintf("set global tidb_ddl_reorg_batch_size = %d", *modifyDownscaleBatchSize)); err != nil {
		return err
	}

	if err := prefillModifyRows(ctx, db, schemaTable, *modifyDownscaleRows); err != nil {
		return err
	}

	mergePauseURL := strings.TrimRight(*modifyDownscaleStatusURL, "/") + "/fail/github.com/pingcap/tidb/pkg/ddl/beforeBackfillMerge"
	if *modifyMergePause {
		if err := setFailpoint3(ctx, mergePauseURL, "pause"); err != nil {
			return fmt.Errorf("enable beforeBackfillMerge pause: %w", err)
		}
		defer func() {
			_ = clearFailpoint3(context.Background(), mergePauseURL)
		}()
	}

	postBatchURL := strings.TrimRight(*modifyDownscaleStatusURL, "/") + "/fail/github.com/pingcap/tidb/pkg/ddl/mockBackfillPostBatchErrForWorker"
	postBatchSleepURL := strings.TrimRight(*modifyDownscaleStatusURL, "/") + "/fail/github.com/pingcap/tidb/pkg/ddl/mockBackfillPostBatchErrSleepMs"

	ddlSQL := fmt.Sprintf("alter table %s change column x a varchar(16) not null", schemaTable)
	jobIDWatermark, err := latestMatchingJobID3(ctx, db, ddlSQL)
	if err != nil {
		return err
	}

	ddlErrCh := make(chan error, 1)
	go func() {
		_, err := db.ExecContext(ctx, ddlSQL)
		ddlErrCh <- err
	}()

	expectedRows := *modifyDownscaleRows
	var runningState modifyDDLState
	if *modifyMergePause {
		runningState, err = waitForMergePause3(ctx, db, ddlSQL, jobIDWatermark, ddlErrCh, int64(*modifyDownscaleRows))
		if err != nil {
			return err
		}
		fmt.Printf("PAUSED job_id=%d state=%s schema_state=%s row_count=%d\n",
			runningState.JobID, runningState.State, runningState.SchemaState, runningState.RowCount)
		if *modifyPauseInsertRows > 0 {
			if err := insertModifyRowRange(ctx, db, schemaTable, *modifyDownscaleRows+1, *modifyDownscaleRows+*modifyPauseInsertRows); err != nil {
				return fmt.Errorf("insert rows during merge pause: %w", err)
			}
			expectedRows += *modifyPauseInsertRows
		}
	} else {
		runningState, err = waitForRunning3(ctx, db, ddlSQL, jobIDWatermark, ddlErrCh, *modifyDownscaleMinRows)
		if err != nil {
			return err
		}
		fmt.Printf("RUNNING job_id=%d state=%s schema_state=%s row_count=%d\n",
			runningState.JobID, runningState.State, runningState.SchemaState, runningState.RowCount)
	}

	if *modifyDownscaleTarget != "" {
		if err := setFailpoint3(ctx, postBatchURL, fmt.Sprintf(`return("%s")`, *modifyDownscaleTarget)); err != nil {
			return fmt.Errorf("enable mockBackfillPostBatchErrForWorker: %w", err)
		}
		defer func() {
			_ = clearFailpoint3(context.Background(), postBatchURL)
		}()
	}
	if *modifyDownscaleSleepMS > 0 {
		if err := setFailpoint3(ctx, postBatchSleepURL, fmt.Sprintf("return(%d)", *modifyDownscaleSleepMS)); err != nil {
			return fmt.Errorf("enable mockBackfillPostBatchErrSleepMs: %w", err)
		}
		defer func() {
			_ = clearFailpoint3(context.Background(), postBatchSleepURL)
		}()
	}

	if *modifyMergePause {
		if err := clearFailpoint3(ctx, mergePauseURL); err != nil {
			return fmt.Errorf("disable beforeBackfillMerge pause: %w", err)
		}
	}

	if *modifyDownscaleTo > 0 && *modifyDownscaleTo < *modifyDownscaleWorkers {
		time.Sleep(*modifyDownscaleDelay)
		downscaleSQL := fmt.Sprintf("admin alter ddl jobs %d thread = %d", runningState.JobID, *modifyDownscaleTo)
		if err := mustExec3(ctx, db, downscaleSQL); err != nil {
			return fmt.Errorf("downscale ddl job: %w", err)
		}
		fmt.Printf("DOWNSCALE job_id=%d to=%d delay=%s\n", runningState.JobID, *modifyDownscaleTo, modifyDownscaleDelay.String())
	}

	state, err := waitForTerminal3(ctx, db, ddlSQL, jobIDWatermark, ddlErrCh)
	if err != nil {
		return err
	}
	fmt.Printf("TERMINAL job_id=%d state=%s schema_state=%s row_count=%d\n",
		state.JobID, state.State, state.SchemaState, state.RowCount)

	if err := runFinalOracle3(ctx, db, state, expectedRows); err != nil {
		return err
	}
	fmt.Printf("GREEN final_state=%s rows=%d\n", state.State, expectedRows)
	return nil
}

func prefillModifyRows(ctx context.Context, db *sql.DB, schemaTable string, rowCount int) error {
	return insertModifyRowRange(ctx, db, schemaTable, 1, rowCount)
}

func insertModifyRowRange(ctx context.Context, db *sql.DB, schemaTable string, startRow, endRow int) error {
	for start := startRow; start <= endRow; start += 1000 {
		end := min3(endRow, start+999)
		values := make([]string, 0, end-start+1)
		for id := start; id <= end; id++ {
			v := id % 10000
			values = append(values, fmt.Sprintf("(%d,%d,%d,repeat('x',64))", id, v, v*2))
		}
		if err := mustExec3(ctx, db, "insert into "+schemaTable+" values "+strings.Join(values, ",")); err != nil {
			return err
		}
	}
	return nil
}

func waitForRunning3(
	ctx context.Context,
	db *sql.DB,
	ddlSQL string,
	jobIDWatermark int64,
	ddlErrCh <-chan error,
	minRows int64,
) (modifyDDLState, error) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-ddlErrCh:
			if err != nil {
				return modifyDDLState{}, fmt.Errorf("ddl returned early with error: %w", err)
			}
			return modifyDDLState{}, fmt.Errorf("ddl finished before running window was observed")
		case <-ctx.Done():
			return modifyDDLState{}, fmt.Errorf("wait running ddl: %w", ctx.Err())
		case <-ticker.C:
		}
		st, ok, err := lookupDDLState3(ctx, db, ddlSQL, jobIDWatermark)
		if err != nil || !ok {
			continue
		}
		if st.RowCount >= minRows && strings.EqualFold(st.State, "running") {
			return st, nil
		}
	}
}

func waitForTerminal3(
	ctx context.Context,
	db *sql.DB,
	ddlSQL string,
	jobIDWatermark int64,
	ddlErrCh <-chan error,
) (modifyDDLState, error) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-ddlErrCh:
			if err != nil {
				// Keep polling terminal state; DDL may have moved to rollback done.
			}
		case <-ctx.Done():
			return modifyDDLState{}, fmt.Errorf("wait terminal ddl: %w", ctx.Err())
		case <-ticker.C:
		}
		st, ok, err := lookupDDLState3(ctx, db, ddlSQL, jobIDWatermark)
		if err != nil || !ok {
			continue
		}
		if isTerminalState3(st.State) {
			return st, nil
		}
	}
}

func waitForMergePause3(
	ctx context.Context,
	db *sql.DB,
	ddlSQL string,
	jobIDWatermark int64,
	ddlErrCh <-chan error,
	minRows int64,
) (modifyDDLState, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var last modifyDDLState
	for {
		select {
		case err := <-ddlErrCh:
			if err != nil {
				return last, fmt.Errorf("ddl returned early with error: %w", err)
			}
			return last, fmt.Errorf("ddl finished before merge pause was observed")
		case <-ctx.Done():
			return last, fmt.Errorf("wait merge pause: %w", ctx.Err())
		case <-ticker.C:
		}
		st, ok, err := lookupDDLState3(ctx, db, ddlSQL, jobIDWatermark)
		if err != nil || !ok {
			continue
		}
		last = st
		if strings.EqualFold(st.State, "running") &&
			strings.EqualFold(st.SchemaState, "write reorganization") &&
			st.RowCount >= minRows {
			time.Sleep(2 * time.Second)
			st2, ok, err := lookupDDLState3(ctx, db, ddlSQL, jobIDWatermark)
			if err != nil {
				return last, err
			}
			if ok &&
				st2.JobID == st.JobID &&
				strings.EqualFold(st2.State, "running") &&
				strings.EqualFold(st2.SchemaState, "write reorganization") &&
				st2.RowCount >= minRows {
				return st2, nil
			}
		}
	}
}

func latestMatchingJobID3(ctx context.Context, db *sql.DB, ddlSQL string) (int64, error) {
	var jobID sql.NullInt64
	err := db.QueryRowContext(ctx, `
select max(job_id)
from information_schema.ddl_jobs
where db_name = ? and table_name = ? and query = ?`,
		*modifyDownscaleSchema, *modifyDownscaleTable, ddlSQL).Scan(&jobID)
	if err != nil {
		return 0, fmt.Errorf("lookup latest matching job id: %w", err)
	}
	if !jobID.Valid {
		return 0, nil
	}
	return jobID.Int64, nil
}

func lookupDDLState3(ctx context.Context, db *sql.DB, ddlSQL string, jobIDWatermark int64) (modifyDDLState, bool, error) {
	rows, err := db.QueryContext(ctx, `
select job_id, state, schema_state, row_count
from information_schema.ddl_jobs
where db_name = ? and table_name = ? and query = ?
  and job_id > ?
order by job_id desc
limit 1`, *modifyDownscaleSchema, *modifyDownscaleTable, ddlSQL, jobIDWatermark)
	if err != nil {
		return modifyDDLState{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return modifyDDLState{}, false, nil
	}
	var st modifyDDLState
	if err := rows.Scan(&st.JobID, &st.State, &st.SchemaState, &st.RowCount); err != nil {
		return modifyDDLState{}, false, err
	}
	return st, true, nil
}

func isTerminalState3(state string) bool {
	switch state {
	case "done", "synced", "rollback done", "cancelled":
		return true
	default:
		return false
	}
}

func runFinalOracle3(ctx context.Context, db *sql.DB, state modifyDDLState, expectedRows int) error {
	successPath := state.State == "done" || state.State == "synced"
	rollbackPath := state.State == "rollback done" || state.State == "cancelled"
	if !successPath && !rollbackPath {
		return fmt.Errorf("unexpected terminal state: %s", state.State)
	}

	cols, err := currentValueColumns3(ctx, db)
	if err != nil {
		return err
	}
	if successPath && (cols[0] != "a" || cols[1] != "y") {
		return fmt.Errorf("unexpected success-path columns: got=%v want=[a y]", cols)
	}
	if rollbackPath && (cols[0] != "x" || cols[1] != "y") {
		return fmt.Errorf("unexpected rollback-path columns: got=%v want=[x y]", cols)
	}

	if err := mustExec3(ctx, db, "admin check table `"+*modifyDownscaleSchema+"`.`"+*modifyDownscaleTable+"`"); err != nil {
		return err
	}
	totalRows, err := queryInt3(ctx, db, "select count(*) from `"+*modifyDownscaleSchema+"`.`"+*modifyDownscaleTable+"`")
	if err != nil {
		return err
	}
	if totalRows != expectedRows {
		return fmt.Errorf("row count drift: got=%d want=%d", totalRows, expectedRows)
	}
	bad, err := formulaMismatchCount3(ctx, db, cols[0], cols[1])
	if err != nil {
		return err
	}
	if bad != 0 {
		return fmt.Errorf("formula mismatch detected: bad_count=%d cols=%v", bad, cols)
	}
	if err := probeIndexPath3(ctx, db, cols[0], []int{1, 2048, 8192, 65536}); err != nil {
		return err
	}
	return nil
}

func currentValueColumns3(ctx context.Context, db *sql.DB) ([2]string, error) {
	rows, err := db.QueryContext(ctx, `
select column_name
from information_schema.columns
where table_schema = ? and table_name = ? and ordinal_position in (2,3)
order by ordinal_position`, *modifyDownscaleSchema, *modifyDownscaleTable)
	if err != nil {
		return [2]string{}, err
	}
	defer rows.Close()
	var cols [2]string
	i := 0
	for rows.Next() {
		if err := rows.Scan(&cols[i]); err != nil {
			return [2]string{}, err
		}
		i++
	}
	if i != 2 {
		return [2]string{}, fmt.Errorf("unexpected column count: %d", i)
	}
	return cols, nil
}

func formulaMismatchCount3(ctx context.Context, db *sql.DB, col1, col2 string) (int, error) {
	sqlText := fmt.Sprintf(`
select count(*)
from %s.%s
where %s is null
   or cast(%s as char) != cast(mod(id,10000) as char)
   or %s != mod(id,10000) * 2`,
		quoteIdent3(*modifyDownscaleSchema), quoteIdent3(*modifyDownscaleTable), col1, col1, col2)
	return queryInt3(ctx, db, sqlText)
}

func probeIndexPath3(ctx context.Context, db *sql.DB, changedCol string, ids []int) error {
	for _, id := range ids {
		if id > *modifyDownscaleRows {
			continue
		}
		var (
			indexCnt int
			tableCnt int
			indexSQL string
			tableSQL string
			arg      any
		)
		switch changedCol {
		case "a":
			indexSQL = fmt.Sprintf("select count(*) from `%s`.`%s` use index(idx_x) where %s = ?",
				*modifyDownscaleSchema, *modifyDownscaleTable, changedCol)
			tableSQL = fmt.Sprintf("select count(*) from `%s`.`%s` ignore index(idx_x) where %s = ?",
				*modifyDownscaleSchema, *modifyDownscaleTable, changedCol)
			arg = strconv.Itoa(id % 10000)
		case "x":
			indexSQL = fmt.Sprintf("select count(*) from `%s`.`%s` use index(idx_x) where %s = ?",
				*modifyDownscaleSchema, *modifyDownscaleTable, changedCol)
			tableSQL = fmt.Sprintf("select count(*) from `%s`.`%s` ignore index(idx_x) where %s = ?",
				*modifyDownscaleSchema, *modifyDownscaleTable, changedCol)
			arg = id % 10000
		default:
			return fmt.Errorf("unexpected changed column for index-path oracle: %s", changedCol)
		}
		if err := db.QueryRowContext(ctx, indexSQL, arg).Scan(&indexCnt); err != nil {
			return fmt.Errorf("index-path oracle failed for id=%d: %w", id, err)
		}
		if err := db.QueryRowContext(ctx, tableSQL, arg).Scan(&tableCnt); err != nil {
			return fmt.Errorf("table-path oracle failed for id=%d: %w", id, err)
		}
		if indexCnt != tableCnt || indexCnt == 0 {
			return fmt.Errorf("index-path oracle mismatch for id=%d: index_cnt=%d table_cnt=%d", id, indexCnt, tableCnt)
		}
	}
	return nil
}

func quoteIdent3(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}

func queryInt3(ctx context.Context, db *sql.DB, sqlText string) (int, error) {
	var v int
	if err := db.QueryRowContext(ctx, sqlText).Scan(&v); err != nil {
		return 0, fmt.Errorf("%s: %w", sqlText, err)
	}
	return v, nil
}

func setFailpoint3(ctx context.Context, fpURL, action string) error {
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

func clearFailpoint3(ctx context.Context, fpURL string) error {
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

func mustExec3(ctx context.Context, db *sql.DB, sqlText string) error {
	if _, err := db.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("%s: %w", sqlText, err)
	}
	return nil
}

func init() {
	http.DefaultClient.Timeout = 10 * time.Second
	if _, err := url.Parse(defaultModifyDownscaleStatusURL); err != nil {
		panic(err)
	}
}

func min3(a, b int) int {
	if a < b {
		return a
	}
	return b
}
