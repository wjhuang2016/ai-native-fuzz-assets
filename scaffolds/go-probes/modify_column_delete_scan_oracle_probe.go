package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	defaultDeleteScanDSN      = "root@tcp(10.2.12.57:32334)/"
	defaultDeleteScanSchema   = "ai_native_issue62531"
	defaultDeleteScanTable    = "rows"
	defaultDeleteScanDuration = 3 * time.Minute
	defaultDDLSleep           = 1 * time.Second
	defaultDMLSleep           = 500 * time.Millisecond
	defaultPaddingSize        = 256
	defaultMaxValue0          = 1000
	defaultWorkers            = 16
	defaultPrefillRows        = 120000
	defaultReorgBatchSize     = 32
	defaultRecentDDLWindow    = 10 * time.Second
)

var (
	deleteScanDSN       = flag.String("dsn", defaultDeleteScanDSN, "mysql dsn")
	deleteScanSchema    = flag.String("schema", defaultDeleteScanSchema, "target schema")
	deleteScanTable     = flag.String("table", defaultDeleteScanTable, "target table")
	deleteScanWithIndex = flag.Bool("with-index", false, "create secondary index on val0")
	deleteScanWorkers   = flag.Int("workers", defaultWorkers, "number of concurrent DML workers")
	deleteScanPrefill   = flag.Int("prefill", defaultPrefillRows, "rows to prefill before starting concurrent workload")
	deleteScanDuration  = flag.Duration("duration", defaultDeleteScanDuration, "overall runtime budget")
	deleteScanDDLSleep  = flag.Duration("ddl-sleep", defaultDDLSleep, "pause between DDL rounds")
	deleteScanDMLSleep  = flag.Duration("dml-sleep", defaultDMLSleep, "pause between insert and delete")
	deleteScanMaxValue0 = flag.Int("max-val0", defaultMaxValue0, "value domain for val0")
	deleteScanBatchSize = flag.Int("reorg-batch-size", defaultReorgBatchSize, "tidb_ddl_reorg_batch_size")
	deleteScanRecentDDL = flag.Duration("recent-ddl-window", defaultRecentDDLWindow, "recent running DDL window when annotating a red signature")
)

type ddlSnapshot struct {
	JobID        int64
	State        string
	SchemaState  string
	RowCount     int64
	Query        string
	LastSeen     time.Time
	LastRunning  time.Time
	RecentActive bool
}

type ddlTracker struct {
	mu          sync.RWMutex
	jobID       int64
	state       string
	schemaState string
	rowCount    int64
	query       string
	lastSeen    time.Time
	lastRunning time.Time
}

type probeStats struct {
	insertOps    atomic.Int64
	deleteOps    atomic.Int64
	ddlAttempts  atomic.Int64
	ddlSuccesses atomic.Int64
}

func main() {
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *deleteScanDuration)
	defer cancel()

	db := mustOpenDeleteScanDB(ctx)
	defer db.Close()

	mustSetupDeleteScan(ctx, db)
	if *deleteScanPrefill > 0 {
		mustPrefillDeleteScan(ctx, db)
	}

	tracker := &ddlTracker{}
	stats := &probeStats{}
	go pollDDLDeleteScan(ctx, db, tracker)

	errCh := make(chan error, 1)
	var wg sync.WaitGroup

	for workerID := 0; workerID < *deleteScanWorkers; workerID++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			runDeleteScanDMLWorker(ctx, db, tracker, stats, id, errCh)
		}(workerID)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		runDeleteScanDDLWorker(ctx, db, stats)
	}()

	select {
	case err := <-errCh:
		cancel()
		wg.Wait()
		log.Fatalf("RED signature observed: %v", err)
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			wg.Wait()
			log.Fatalf("probe terminated early: %v", ctx.Err())
		}
		cancel()
		wg.Wait()
		runDeleteScanFinalOracle(context.Background(), db, stats)
	}
}

func mustOpenDeleteScanDB(ctx context.Context) *sql.DB {
	db, err := sql.Open("mysql", *deleteScanDSN)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(*deleteScanWorkers + 8)
	db.SetMaxIdleConns(*deleteScanWorkers + 8)
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

func mustSetupDeleteScan(ctx context.Context, db *sql.DB) {
	mustExecDeleteScan(ctx, db, "create database if not exists `"+*deleteScanSchema+"`")
	mustExecDeleteScan(ctx, db, "drop table if exists `"+*deleteScanSchema+"`.`"+*deleteScanTable+"`")

	createSQL := fmt.Sprintf(`create table %s.%s (
		id int not null auto_increment,
		val0 int not null,
		val1 int not null,
		padding varchar(%d) not null default '',
		primary key (id)
	)`, quoteIdent(*deleteScanSchema), quoteIdent(*deleteScanTable), defaultPaddingSize)
	mustExecDeleteScan(ctx, db, createSQL)

	if *deleteScanWithIndex {
		mustExecDeleteScan(ctx, db, fmt.Sprintf(
			"create index val0_idx on %s.%s (val0)",
			quoteIdent(*deleteScanSchema), quoteIdent(*deleteScanTable),
		))
	}

	mustExecDeleteScan(ctx, db, "set @@global.tidb_ddl_reorg_worker_cnt = 1")
	mustExecDeleteScan(ctx, db, fmt.Sprintf("set @@global.tidb_ddl_reorg_batch_size = %d", *deleteScanBatchSize))
	log.Printf("setup done, schema=%s table=%s with_index=%v", *deleteScanSchema, *deleteScanTable, *deleteScanWithIndex)
}

func mustPrefillDeleteScan(ctx context.Context, db *sql.DB) {
	const batchRows = 1000
	paddingBuf := make([]byte, defaultPaddingSize/2)
	tableName := fmt.Sprintf("%s.%s", quoteIdent(*deleteScanSchema), quoteIdent(*deleteScanTable))

	for start := 0; start < *deleteScanPrefill; start += batchRows {
		end := start + batchRows
		if end > *deleteScanPrefill {
			end = *deleteScanPrefill
		}
		valueStrings := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*3)
		for i := start; i < end; i++ {
			val0 := i % *deleteScanMaxValue0
			val1 := val0 * 10
			if _, err := rand.Read(paddingBuf); err != nil {
				log.Fatalf("prefill random padding: %v", err)
			}
			valueStrings = append(valueStrings, "(?, ?, ?)")
			args = append(args, val0, val1, hex.EncodeToString(paddingBuf))
		}
		sqlText := fmt.Sprintf("insert into %s (val0, val1, padding) values %s", tableName, strings.Join(valueStrings, ","))
		mustExecArgsDeleteScan(ctx, db, sqlText, args...)
	}

	log.Printf("prefill done, rows=%d", queryIntDeleteScan(ctx, db, "select count(*) from "+tableName))
}

func runDeleteScanDMLWorker(
	ctx context.Context,
	db *sql.DB,
	tracker *ddlTracker,
	stats *probeStats,
	workerID int,
	errCh chan<- error,
) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerID)*97))
	paddingBuf := make([]byte, defaultPaddingSize/2)
	tableName := fmt.Sprintf("%s.%s", quoteIdent(*deleteScanSchema), quoteIdent(*deleteScanTable))

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		rowsPerOperation := [...]int{10, 50, 100, 200}[rng.Intn(4)]
		val0Values := make([]int, rowsPerOperation)
		val1Values := make([]int, rowsPerOperation)
		paddingValues := make([]string, rowsPerOperation)

		val0 := rng.Intn(*deleteScanMaxValue0)
		for i := 0; i < rowsPerOperation; i++ {
			val0Values[i] = val0
			val1Values[i] = val0 * 10
			if _, err := rng.Read(paddingBuf[:rowsPerOperation/2]); err != nil {
				log.Printf("worker %d random padding failed: %v", workerID, err)
				return
			}
			paddingValues[i] = hex.EncodeToString(paddingBuf[:rowsPerOperation/2])
			val0 = (val0 + 1) % *deleteScanMaxValue0
		}

		if err := insertDeleteScanRows(ctx, db, tableName, val0Values, val1Values, paddingValues); err != nil {
			if ctx.Err() != nil {
				return
			}
			if isSevereDeleteScanRowImageError(err) {
				snap := tracker.snapshot(*deleteScanRecentDDL)
				signalDeleteScanRed(errCh, fmt.Errorf(
					"insert path saw row-image corruption: err=%v recent_active=%v job_id=%d state=%s schema_state=%s row_count=%d last_query=%q last_seen=%s",
					err, snap.RecentActive, snap.JobID, snap.State, snap.SchemaState, snap.RowCount, snap.Query,
					snap.LastSeen.Format(time.RFC3339Nano),
				))
				return
			}
			log.Printf("worker %d insert error: %v", workerID, err)
		} else {
			stats.insertOps.Add(1)
		}

		sleepDeleteScan(ctx, *deleteScanDMLSleep)

		if err := deleteDeleteScanRows(ctx, db, tableName, val0Values); err != nil {
			if ctx.Err() != nil {
				return
			}
			if isSevereDeleteScanRowImageError(err) {
				snap := tracker.snapshot(*deleteScanRecentDDL)
				signalDeleteScanRed(errCh, fmt.Errorf(
					"delete path hit issue62531 signature: err=%v recent_active=%v job_id=%d state=%s schema_state=%s row_count=%d last_query=%q last_seen=%s last_running=%s vals=[%d..%d]",
					err, snap.RecentActive, snap.JobID, snap.State, snap.SchemaState, snap.RowCount, snap.Query,
					snap.LastSeen.Format(time.RFC3339Nano), snap.LastRunning.Format(time.RFC3339Nano),
					val0Values[0], val0Values[len(val0Values)-1],
				))
				return
			}
			if !isIgnorableDeleteScanDMLError(err) {
				log.Printf("worker %d delete error: %v", workerID, err)
			}
		} else {
			stats.deleteOps.Add(1)
		}

		sleepDeleteScan(ctx, *deleteScanDMLSleep)
	}
}

func insertDeleteScanRows(ctx context.Context, db *sql.DB, tableName string, val0Values, val1Values []int, paddingValues []string) error {
	valueStrings := make([]string, len(val0Values))
	args := make([]any, 0, len(val0Values)*3)
	for i, val0 := range val0Values {
		valueStrings[i] = "(?, ?, ?)"
		args = append(args, val0, val1Values[i], paddingValues[i])
	}
	query := fmt.Sprintf("insert into %s (val0, val1, padding) values %s", tableName, strings.Join(valueStrings, ", "))
	return execDeleteScanWithRetry(ctx, db, query, args...)
}

func deleteDeleteScanRows(ctx context.Context, db *sql.DB, tableName string, val0Values []int) error {
	placeholders := make([]string, len(val0Values))
	args := make([]any, len(val0Values))
	for i, val0 := range val0Values {
		placeholders[i] = "?"
		args[i] = val0
	}
	query := fmt.Sprintf("delete from %s where val0 in (%s)", tableName, strings.Join(placeholders, ", "))
	return execDeleteScanWithRetry(ctx, db, query, args...)
}

func execDeleteScanWithRetry(ctx context.Context, db *sql.DB, query string, args ...any) error {
	for {
		_, err := db.ExecContext(ctx, query, args...)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if isSevereDeleteScanRowImageError(err) {
			return err
		}
		if isIgnorableDeleteScanDMLError(err) {
			return nil
		}
		if isRetryableDeleteScanDMLError(err) {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		return err
	}
}

func runDeleteScanDDLWorker(ctx context.Context, db *sql.DB, stats *probeStats) {
	tableName := fmt.Sprintf("%s.%s", quoteIdent(*deleteScanSchema), quoteIdent(*deleteScanTable))
	columnTypes := []string{"bigint", "int"}

	sleepDeleteScan(ctx, *deleteScanDDLSleep)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		for _, columnType := range columnTypes {
			stats.ddlAttempts.Add(1)
			ddlSQL := fmt.Sprintf("alter table %s modify column val0 %s not null", tableName, columnType)
			start := time.Now()
			_, err := db.ExecContext(ctx, ddlSQL)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if isRetryableDeleteScanDMLError(err) {
					log.Printf("ddl transient error type=%s after=%s err=%v", columnType, time.Since(start), err)
				} else {
					log.Printf("ddl error type=%s after=%s err=%v", columnType, time.Since(start), err)
				}
			} else {
				stats.ddlSuccesses.Add(1)
				log.Printf("ddl success type=%s after=%s", columnType, time.Since(start))
			}
			sleepDeleteScan(ctx, *deleteScanDDLSleep)
		}
	}
}

func pollDDLDeleteScan(ctx context.Context, db *sql.DB, tracker *ddlTracker) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		rows, err := db.QueryContext(ctx, `
select job_id, state, schema_state, row_count, query
from information_schema.ddl_jobs
where db_name = ? and table_name = ?
order by job_id desc
limit 8`, *deleteScanSchema, *deleteScanTable)
		if err != nil {
			continue
		}

		var observed bool
		for rows.Next() {
			var (
				jobID       int64
				state       string
				schemaState string
				rowCount    int64
				query       string
			)
			if err := rows.Scan(&jobID, &state, &schemaState, &rowCount, &query); err != nil {
				continue
			}
			if !strings.Contains(strings.ToLower(query), "modify column") {
				continue
			}
			tracker.record(jobID, state, schemaState, rowCount, query)
			observed = true
			break
		}
		_ = rows.Close()
		if !observed {
			continue
		}
	}
}

func (t *ddlTracker) record(jobID int64, state, schemaState string, rowCount int64, query string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	t.jobID = jobID
	t.state = state
	t.schemaState = schemaState
	t.rowCount = rowCount
	t.query = query
	t.lastSeen = now
	if state == "running" {
		t.lastRunning = now
	}
}

func (t *ddlTracker) snapshot(recentWindow time.Duration) ddlSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()

	snap := ddlSnapshot{
		JobID:       t.jobID,
		State:       t.state,
		SchemaState: t.schemaState,
		RowCount:    t.rowCount,
		Query:       t.query,
		LastSeen:    t.lastSeen,
		LastRunning: t.lastRunning,
	}
	if !t.lastRunning.IsZero() && time.Since(t.lastRunning) <= recentWindow {
		snap.RecentActive = true
	}
	return snap
}

func runDeleteScanFinalOracle(ctx context.Context, db *sql.DB, stats *probeStats) {
	tableName := fmt.Sprintf("%s.%s", quoteIdent(*deleteScanSchema), quoteIdent(*deleteScanTable))
	mustExecDeleteScan(ctx, db, "admin check table "+tableName)

	bad := queryIntDeleteScan(ctx, db, "select count(*) from "+tableName+" where val0 is null or val1 is null or val1 != val0 * 10")
	if bad != 0 {
		log.Fatalf("final formula oracle mismatch: bad_rows=%d", bad)
	}

	if *deleteScanWithIndex {
		for _, val0 := range []int{0, 7, 17, 191, 511} {
			indexCnt := queryIntArgsDeleteScan(ctx, db,
				"select count(*) from "+tableName+" use index(val0_idx) where val0 = ?", val0)
			tableCnt := queryIntArgsDeleteScan(ctx, db,
				"select count(*) from "+tableName+" ignore index(val0_idx) where val0 = ?", val0)
			if indexCnt != tableCnt {
				log.Fatalf("final index/table mismatch on val0=%d: index_cnt=%d table_cnt=%d", val0, indexCnt, tableCnt)
			}
		}
	}

	log.Printf(
		"GREEN after %s, with_index=%v, insert_ops=%d delete_ops=%d ddl_attempts=%d ddl_successes=%d final_rows=%d",
		*deleteScanDuration,
		*deleteScanWithIndex,
		stats.insertOps.Load(),
		stats.deleteOps.Load(),
		stats.ddlAttempts.Load(),
		stats.ddlSuccesses.Load(),
		queryIntDeleteScan(ctx, db, "select count(*) from "+tableName),
	)
}

func isSevereDeleteScanRowImageError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "data is corrupted") &&
		strings.Contains(msg, "missing data for not null column")
}

func isIgnorableDeleteScanDMLError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "public column") && strings.Contains(msg, "has changed")
}

func isRetryableDeleteScanDMLError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "write conflict") ||
		strings.Contains(msg, "try again later") ||
		strings.Contains(msg, "information schema is changed") ||
		strings.Contains(msg, "has changed") ||
		strings.Contains(msg, "invalid connection") ||
		strings.Contains(msg, "unknown column") ||
		strings.Contains(msg, "stale command")
}

func signalDeleteScanRed(errCh chan<- error, err error) {
	select {
	case errCh <- err:
	default:
	}
}

func sleepDeleteScan(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func mustExecDeleteScan(ctx context.Context, db *sql.DB, sqlText string) {
	mustExecArgsDeleteScan(ctx, db, sqlText)
}

func mustExecArgsDeleteScan(ctx context.Context, db *sql.DB, sqlText string, args ...any) {
	if _, err := db.ExecContext(ctx, sqlText, args...); err != nil {
		log.Fatalf("exec failed: %s: %v", sqlText, err)
	}
}

func queryIntDeleteScan(ctx context.Context, db *sql.DB, sqlText string) int {
	return queryIntArgsDeleteScan(ctx, db, sqlText)
}

func queryIntArgsDeleteScan(ctx context.Context, db *sql.DB, sqlText string, args ...any) int {
	for {
		var v int
		err := db.QueryRowContext(ctx, sqlText, args...).Scan(&v)
		if err == nil {
			return v
		}
		if isRetryableDeleteScanDMLError(err) {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		log.Fatalf("query int failed: %s args=%v err=%v", sqlText, args, err)
	}
}

func quoteIdent(ident string) string {
	return "`" + strings.ReplaceAll(ident, "`", "``") + "`"
}

func _lintUse() string {
	return strconv.Itoa(defaultMaxValue0)
}
