package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	defaultPinnedDSN             = "root@tcp(127.0.0.1:14000)/"
	defaultPinnedStatusURL       = "http://127.0.0.1:18080"
	defaultPinnedSchema          = "ai_native_issue62531_pinned_broad"
	defaultPinnedTable           = "rows"
	defaultPinnedWarmup          = 2 * time.Second
	defaultPinnedHold            = 12 * time.Second
	defaultPinnedPostRelease     = 12 * time.Second
	defaultPinnedTimeout         = 5 * time.Minute
	defaultPinnedWorkers         = 16
	defaultPinnedPrefillRows     = 120000
	defaultPinnedDMLSleep        = 500 * time.Millisecond
	defaultPinnedMaxValue0       = 1000
	defaultPinnedReorgBatchSize  = 32
	defaultPinnedPaddingSize     = 256
	deleteScanHoldFailpointName  = "github.com/pingcap/tidb/pkg/ddl/beforeUpdateColumnBackfillApply"
	disableLossyOptFailpointName = "github.com/pingcap/tidb/pkg/ddl/disableLossyDDLOptimization"
)

var (
	pinnedDSN           = flag.String("dsn", defaultPinnedDSN, "mysql dsn")
	pinnedStatusURL     = flag.String("status-url", defaultPinnedStatusURL, "TiDB status/failpoint base URL")
	pinnedFailpointURL  = flag.String("failpoint-base-url", "", "optional standalone failpoint HTTP base URL; when set, failpoints are addressed as <base>/<failpoint-name>")
	pinnedSchema        = flag.String("schema", defaultPinnedSchema, "target schema")
	pinnedTable         = flag.String("table", defaultPinnedTable, "target table")
	pinnedWorkers       = flag.Int("workers", defaultPinnedWorkers, "number of concurrent DML workers")
	pinnedPrefill       = flag.Int("prefill", defaultPinnedPrefillRows, "rows to prefill before starting concurrent workload")
	pinnedWarmup        = flag.Duration("warmup", defaultPinnedWarmup, "how long to let DML warm up before submitting DDL")
	pinnedHold          = flag.Duration("hold", defaultPinnedHold, "how long to keep the DDL pinned in apply window")
	pinnedPostRelease   = flag.Duration("post-release", defaultPinnedPostRelease, "how long to keep DML running after releasing the DDL")
	pinnedTimeout       = flag.Duration("timeout", defaultPinnedTimeout, "overall runtime budget")
	pinnedDMLSleep      = flag.Duration("dml-sleep", defaultPinnedDMLSleep, "pause between insert and delete")
	pinnedMaxValue0     = flag.Int("max-val0", defaultPinnedMaxValue0, "value domain for val0")
	pinnedVal0Default   = flag.String("val0-default", "", "optional SQL literal used as an explicit val0 column default")
	pinnedPrefillBase   = flag.Int("prefill-value-base", 0, "base value added to prefill val0 domain")
	pinnedWorkerBase    = flag.Int("worker-value-base", 0, "base value added to worker/oracle val0 domain")
	pinnedBatchSize     = flag.Int("reorg-batch-size", defaultPinnedReorgBatchSize, "tidb_ddl_reorg_batch_size")
	pinnedWithIndex     = flag.Bool("with-index", false, "create secondary index on val0")
	pinnedSeedBase      = flag.Int64("seed-base", 0, "base seed for deterministic workers; 0 picks current time")
	pinnedRowsPerOp     = flag.Int("rows-per-op", 0, "fixed rows per DML operation; 0 uses random choice from 10/50/100/200")
	pinnedDeleteHint    = flag.String("delete-index-hint", "auto", "auto|use|ignore")
	pinnedDeleteSession = flag.String("delete-session", "pool", "pool|old|fresh")
	pinnedDeleteStart   = flag.String("delete-start", "immediate", "immediate|after-pause")
	pinnedSkipInsert    = flag.Bool("skip-insert", false, "skip insert phase in each DML worker loop")
	pinnedSkipDelete    = flag.Bool("skip-delete", false, "skip delete phase in each DML worker loop")
	pinnedWorkerMode    = flag.String("worker-mode", "combined", "combined|paired-split|paired-follow")
	pinnedOpOrder       = flag.String("op-order", "insert-delete", "insert-delete|delete-insert")
	pinnedDeleteShift   = flag.Int("delete-shift", 0, "shift delete val0 window by N modulo max-val0 relative to insert window")
	pinnedBeforeDelete  = flag.String("before-delete-reader", "none", "none|use|ignore")
	pinnedOracleMode    = flag.String("oracle-mode", "none", "none|delete|select|both")
	pinnedOracleWorkers = flag.Int("oracle-workers", 0, "number of prepared oracle workers")
	pinnedOracleWindow  = flag.Int("oracle-window", 100, "number of values in each prepared oracle window")
	pinnedOracleSleep   = flag.Duration("oracle-sleep", 300*time.Millisecond, "pause between prepared oracle executions")
	pinnedFinalStarts   = flag.String("final-starts", "0,191,511", "comma-separated start values for final full-row reader scans")
	pinnedAfterRed      = flag.Bool("after-red-oracle", false, "after severe signature, release DDL and inspect final table state instead of exiting immediately")
)

type pinnedProbeStats struct {
	insertOps       atomic.Int64
	deleteOps       atomic.Int64
	deleteErrs      atomic.Int64
	insertErrs      atomic.Int64
	oracleDeleteOps atomic.Int64
	oracleSelectOps atomic.Int64
	oracleErrs      atomic.Int64
}

type pinnedSevereSignal struct {
	err error
}

func (s *pinnedSevereSignal) Error() string {
	if s == nil || s.err == nil {
		return ""
	}
	return s.err.Error()
}

type pinnedQueryExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func main() {
	flag.Parse()
	if err := runPinnedProbe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runPinnedProbe() error {
	ctx, cancel := context.WithTimeout(context.Background(), *pinnedTimeout)
	defer cancel()

	db, err := sql.Open("mysql", *pinnedDSN)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(*pinnedWorkers + *pinnedOracleWorkers + 8)
	db.SetMaxIdleConns(*pinnedWorkers + *pinnedOracleWorkers + 8)

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping db: %w", err)
	}

	seedBase := *pinnedSeedBase
	if seedBase == 0 {
		seedBase = time.Now().UnixNano()
	}

	tableName := fmt.Sprintf("%s.%s", quotePinnedIdent(*pinnedSchema), quotePinnedIdent(*pinnedTable))
	if err := setupPinnedProbe(ctx, db, tableName); err != nil {
		return err
	}

	if *pinnedPrefill > 0 {
		if err := prefillPinnedProbe(ctx, db, tableName); err != nil {
			return err
		}
	}

	holdURL := pinnedFailpointEndpoint(deleteScanHoldFailpointName)
	lossyURL := pinnedFailpointEndpoint(disableLossyOptFailpointName)

	if err := putFailpoint(ctx, lossyURL, "return(true)"); err != nil {
		return fmt.Errorf("enable disableLossyDDLOptimization: %w", err)
	}
	defer func() { _ = deleteFailpoint(context.Background(), lossyURL) }()

	if err := putFailpoint(ctx, holdURL, "pause"); err != nil {
		return fmt.Errorf("enable apply-window hold failpoint: %w", err)
	}
	defer func() { _ = deleteFailpoint(context.Background(), holdURL) }()

	stats := &pinnedProbeStats{}
	errCh := make(chan error, 1)
	pauseCh := make(chan struct{})
	var wg sync.WaitGroup
	var pairStarts []atomic.Int64
	if strings.EqualFold(strings.TrimSpace(*pinnedWorkerMode), "paired-follow") {
		pairCnt := (*pinnedWorkers + 1) / 2
		pairStarts = make([]atomic.Int64, pairCnt)
		for i := range pairStarts {
			pairStarts[i].Store(-1)
		}
	}

	for workerID := 0; workerID < *pinnedWorkers; workerID++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			runPinnedDMLWorker(ctx, db, tableName, stats, id, seedBase, errCh, pairStarts, pauseCh)
		}(workerID)
	}
	for oracleID := 0; oracleID < *pinnedOracleWorkers; oracleID++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			runPinnedOracleWorker(ctx, db, tableName, stats, id, seedBase, errCh)
		}(oracleID)
	}

	fmt.Printf("CONFIG seed_base=%d rows_per_op=%d val0_default=%s delete_hint=%s delete_session=%s delete_start=%s before_delete_reader=%s worker_mode=%s op_order=%s delete_shift=%d prefill_base=%d worker_base=%d skip_insert=%t skip_delete=%t with_index=%t workers=%d oracle_workers=%d prefill=%d max_val0=%d hold=%s post_release=%s\n",
		seedBase,
		*pinnedRowsPerOp,
		strings.TrimSpace(*pinnedVal0Default),
		strings.ToLower(strings.TrimSpace(*pinnedDeleteHint)),
		strings.ToLower(strings.TrimSpace(*pinnedDeleteSession)),
		strings.ToLower(strings.TrimSpace(*pinnedDeleteStart)),
		strings.ToLower(strings.TrimSpace(*pinnedBeforeDelete)),
		strings.ToLower(strings.TrimSpace(*pinnedWorkerMode)),
		strings.ToLower(strings.TrimSpace(*pinnedOpOrder)),
		*pinnedDeleteShift,
		*pinnedPrefillBase,
		*pinnedWorkerBase,
		*pinnedSkipInsert,
		*pinnedSkipDelete,
		*pinnedWithIndex,
		*pinnedWorkers,
		*pinnedOracleWorkers,
		*pinnedPrefill,
		*pinnedMaxValue0,
		pinnedHold.String(),
		pinnedPostRelease.String(),
	)

	sleepPinned(ctx, *pinnedWarmup)

	ddlErrCh := make(chan error, 1)
	ddlSQL := fmt.Sprintf("alter table %s modify column val0 varchar(16) not null", tableName)
	go func() {
		_, err := db.ExecContext(ctx, ddlSQL)
		ddlErrCh <- err
	}()

	if err := waitForPinnedPause(ddlErrCh, errCh); err != nil {
		return handlePinnedEarlyExit(ctx, err, db, tableName, holdURL, ddlErrCh, cancel, &wg, stats)
	}
	close(pauseCh)

	fmt.Printf("PAUSED hold=%s workers=%d prefill=%d table=%s delete_session=%s delete_start=%s\n",
		pinnedHold.String(),
		*pinnedWorkers,
		*pinnedPrefill,
		tableName,
		strings.ToLower(strings.TrimSpace(*pinnedDeleteSession)),
		strings.ToLower(strings.TrimSpace(*pinnedDeleteStart)),
	)

	if err := sleepOrPinnedRed(ctx, *pinnedHold, errCh); err != nil {
		return handlePinnedEarlyExit(ctx, err, db, tableName, holdURL, ddlErrCh, cancel, &wg, stats)
	}

	if err := deleteFailpoint(ctx, holdURL); err != nil {
		cancel()
		wg.Wait()
		return fmt.Errorf("release apply-window hold failpoint: %w", err)
	}

	if err := waitForPinnedDDLFinish(ctx, ddlErrCh, errCh); err != nil {
		return handlePinnedEarlyExit(ctx, err, db, tableName, holdURL, ddlErrCh, cancel, &wg, stats)
	}

	fmt.Printf("DDL released and finished, post_release=%s\n", pinnedPostRelease.String())

	if err := sleepOrPinnedRed(ctx, *pinnedPostRelease, errCh); err != nil {
		cancel()
		wg.Wait()
		return err
	}

	cancel()
	wg.Wait()

	select {
	case err := <-errCh:
		return err
	default:
	}

	if err := runPinnedFinalOracle(context.Background(), db, tableName, stats); err != nil {
		return err
	}
	return nil
}

func setupPinnedProbe(ctx context.Context, db *sql.DB, tableName string) error {
	if err := execPinned(ctx, db, "create database if not exists "+quotePinnedIdent(*pinnedSchema)); err != nil {
		return err
	}
	if err := execPinned(ctx, db, "drop table if exists "+tableName); err != nil {
		return err
	}
	val0Default := ""
	if defaultValue := strings.TrimSpace(*pinnedVal0Default); defaultValue != "" {
		val0Default = " DEFAULT " + defaultValue
	}
	createSQL := fmt.Sprintf(`create table %s (
		id int not null auto_increment,
		val0 int not null%s,
		val1 int not null,
		padding varchar(%d) not null default '',
		primary key (id)
	)`, tableName, val0Default, defaultPinnedPaddingSize)
	if err := execPinned(ctx, db, createSQL); err != nil {
		return err
	}
	if *pinnedWithIndex {
		if err := execPinned(ctx, db, "create index val0_idx on "+tableName+" (val0)"); err != nil {
			return err
		}
	}
	if err := execPinned(ctx, db, "set @@global.tidb_ddl_reorg_worker_cnt = 1"); err != nil {
		return err
	}
	if err := execPinned(ctx, db, fmt.Sprintf("set @@global.tidb_ddl_reorg_batch_size = %d", *pinnedBatchSize)); err != nil {
		return err
	}
	if err := execPinned(ctx, db, "set @@global.tidb_enable_dist_task = off"); err != nil {
		return err
	}
	if err := execPinned(ctx, db, "set @@global.tidb_ddl_enable_fast_reorg = off"); err != nil {
		return err
	}
	if err := execPinned(ctx, db, "set @@global.tidb_ddl_reorg_max_write_speed = 0"); err != nil {
		return err
	}
	return nil
}

func prefillPinnedProbe(ctx context.Context, db *sql.DB, tableName string) error {
	const batchRows = 1000
	paddingBuf := make([]byte, defaultPinnedPaddingSize/2)
	for start := 0; start < *pinnedPrefill; start += batchRows {
		end := start + batchRows
		if end > *pinnedPrefill {
			end = *pinnedPrefill
		}
		valueStrings := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*3)
		for i := start; i < end; i++ {
			val0 := *pinnedPrefillBase + (i % *pinnedMaxValue0)
			val1 := val0 * 10
			if _, err := rand.Read(paddingBuf); err != nil {
				return fmt.Errorf("prefill random padding: %w", err)
			}
			valueStrings = append(valueStrings, "(?, ?, ?)")
			args = append(args, val0, val1, hex.EncodeToString(paddingBuf))
		}
		sqlText := fmt.Sprintf("insert into %s (val0, val1, padding) values %s", tableName, strings.Join(valueStrings, ","))
		if _, err := db.ExecContext(ctx, sqlText, args...); err != nil {
			return fmt.Errorf("prefill batch failed: %w", err)
		}
	}
	fmt.Printf("PREFILL rows=%d table=%s\n", queryPinnedInt(ctx, db, "select count(*) from "+tableName), tableName)
	return nil
}

func runPinnedDMLWorker(
	ctx context.Context,
	db *sql.DB,
	tableName string,
	stats *pinnedProbeStats,
	workerID int,
	seedBase int64,
	errCh chan<- error,
	pairStarts []atomic.Int64,
	pauseCh <-chan struct{},
) {
	pairID := workerID
	mode := strings.ToLower(strings.TrimSpace(*pinnedWorkerMode))
	if mode == "paired-split" || mode == "paired-follow" {
		pairID = workerID / 2
	}
	rng := rand.New(rand.NewSource(seedBase + int64(pairID)*131))
	paddingBuf := make([]byte, defaultPinnedPaddingSize/2)
	skipInsert := *pinnedSkipInsert
	skipDelete := *pinnedSkipDelete
	if mode == "paired-split" || mode == "paired-follow" {
		if workerID%2 == 0 {
			skipDelete = true
		} else {
			skipInsert = true
		}
	}
	deleteSession := strings.ToLower(strings.TrimSpace(*pinnedDeleteSession))
	if deleteSession == "" {
		deleteSession = "pool"
	}
	deleteStart := strings.ToLower(strings.TrimSpace(*pinnedDeleteStart))
	if deleteStart == "" {
		deleteStart = "immediate"
	}

	var (
		runner pinnedQueryExecer = db
		conn   *sql.Conn
	)
	if !skipDelete {
		switch deleteSession {
		case "pool":
		case "old":
			var err error
			conn, err = db.Conn(ctx)
			if err != nil {
				signalPinnedRed(errCh, fmt.Errorf("worker %d open old delete session failed: %w", workerID, err))
				return
			}
			runner = conn
		case "fresh":
		default:
			signalPinnedRed(errCh, fmt.Errorf("worker %d invalid delete-session=%q", workerID, *pinnedDeleteSession))
			return
		}
		defer func() {
			if conn != nil {
				_ = conn.Close()
			}
		}()

		switch deleteStart {
		case "immediate":
		case "after-pause":
			if err := waitForPinnedPauseSignal(ctx, pauseCh); err != nil {
				return
			}
		default:
			signalPinnedRed(errCh, fmt.Errorf("worker %d invalid delete-start=%q", workerID, *pinnedDeleteStart))
			return
		}

		if deleteSession == "fresh" {
			var err error
			conn, err = db.Conn(ctx)
			if err != nil {
				signalPinnedRed(errCh, fmt.Errorf("worker %d open fresh delete session failed: %w", workerID, err))
				return
			}
			runner = conn
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		rowsPerOperation := *pinnedRowsPerOp
		if rowsPerOperation == 0 {
			rowsPerOperation = [...]int{10, 50, 100, 200}[rng.Intn(4)]
		}
		if rowsPerOperation <= 0 {
			signalPinnedRed(errCh, fmt.Errorf("worker %d invalid rows-per-op=%d", workerID, rowsPerOperation))
			return
		}
		val0Values := make([]int, rowsPerOperation)
		val1Values := make([]int, rowsPerOperation)
		paddingValues := make([]string, rowsPerOperation)

		val0 := *pinnedWorkerBase + rng.Intn(*pinnedMaxValue0)
		for i := 0; i < rowsPerOperation; i++ {
			val0Values[i] = val0
			val1Values[i] = val0 * 10
			if _, err := rng.Read(paddingBuf[:rowsPerOperation/2]); err != nil {
				signalPinnedRed(errCh, fmt.Errorf("worker %d random padding failed: %w", workerID, err))
				return
			}
			paddingValues[i] = hex.EncodeToString(paddingBuf[:rowsPerOperation/2])
			val0 = *pinnedWorkerBase + (((val0 - *pinnedWorkerBase) + 1) % *pinnedMaxValue0)
		}

		sourceVal0Values := slicesCloneInts(val0Values)
		deleteVal0Values := buildPinnedDeleteValues(sourceVal0Values, *pinnedDeleteShift, *pinnedMaxValue0, *pinnedWorkerBase)
		if mode == "paired-follow" {
			if workerID%2 != 0 {
				start := int(pairStarts[pairID].Load())
				if start < 0 {
					sleepPinned(ctx, 100*time.Millisecond)
					continue
				}
				sourceVal0Values = buildPinnedWindowValues(start, rowsPerOperation, *pinnedMaxValue0, *pinnedWorkerBase)
				deleteVal0Values = buildPinnedDeleteValues(sourceVal0Values, *pinnedDeleteShift, *pinnedMaxValue0, *pinnedWorkerBase)
			}
		}

		if err := runPinnedWorkerOps(ctx, runner, tableName, stats, workerID, errCh, skipInsert, skipDelete, mode, pairID, pairStarts, val0Values, sourceVal0Values, deleteVal0Values, val1Values, paddingValues); err != nil {
			return
		}
	}
}

func runPinnedWorkerOps(
	ctx context.Context,
	runner pinnedQueryExecer,
	tableName string,
	stats *pinnedProbeStats,
	workerID int,
	errCh chan<- error,
	skipInsert, skipDelete bool,
	mode string,
	pairID int,
	pairStarts []atomic.Int64,
	insertVal0Values, sourceVal0Values, deleteVal0Values, val1Values []int,
	paddingValues []string,
) error {
	runInsert := func() error {
		if skipInsert {
			return nil
		}
		if err := insertPinnedRows(ctx, runner, tableName, insertVal0Values, val1Values, paddingValues); err != nil {
			if ctx.Err() != nil {
				return err
			}
			if isPinnedSevereOracleError(err) {
				signalPinnedRed(errCh, fmt.Errorf("worker %d insert path hit severe signature: %w", workerID, err))
				return err
			}
			stats.insertErrs.Add(1)
			return nil
		}
		stats.insertOps.Add(1)
		if mode == "paired-follow" && !skipInsert && pairID >= 0 && pairID < len(pairStarts) {
			pairStarts[pairID].Store(int64(insertVal0Values[0]))
		}
		return nil
	}
	runDelete := func() error {
		if skipDelete {
			return nil
		}
		preDeleteCount := -1
		if cnt, err := runPinnedBeforeDeleteReader(ctx, runner, tableName, deleteVal0Values); err != nil {
			if ctx.Err() != nil {
				return err
			}
			if isPinnedSevereOracleError(err) {
				signalPinnedRed(errCh, fmt.Errorf("worker %d pre-delete reader hit issue62531 signature on [%d..%d] (source [%d..%d], pre_delete_rows=%d): %w", workerID, deleteVal0Values[0], deleteVal0Values[len(deleteVal0Values)-1], sourceVal0Values[0], sourceVal0Values[len(sourceVal0Values)-1], preDeleteCount, err))
				return err
			}
			if !isPinnedIgnorableDMLError(err) && !isPinnedRetryableDMLError(err) {
				signalPinnedRed(errCh, fmt.Errorf("worker %d pre-delete reader failed on [%d..%d] (source [%d..%d], pre_delete_rows=%d): %w", workerID, deleteVal0Values[0], deleteVal0Values[len(deleteVal0Values)-1], sourceVal0Values[0], sourceVal0Values[len(sourceVal0Values)-1], preDeleteCount, err))
				return err
			}
		} else {
			preDeleteCount = cnt
		}
		if err := deletePinnedRows(ctx, runner, tableName, deleteVal0Values); err != nil {
			if ctx.Err() != nil {
				return err
			}
			if isPinnedSevereOracleError(err) {
				signalPinnedRed(errCh, fmt.Errorf("worker %d delete path hit issue62531 signature on [%d..%d] (source [%d..%d], pre_delete_rows=%d): %w", workerID, deleteVal0Values[0], deleteVal0Values[len(deleteVal0Values)-1], sourceVal0Values[0], sourceVal0Values[len(sourceVal0Values)-1], preDeleteCount, err))
				return err
			}
			if !isPinnedIgnorableDMLError(err) {
				stats.deleteErrs.Add(1)
			}
			return nil
		}
		stats.deleteOps.Add(1)
		return nil
	}

	order := strings.ToLower(strings.TrimSpace(*pinnedOpOrder))
	switch order {
	case "", "insert-delete":
		if err := runInsert(); err != nil {
			return err
		}
		sleepPinned(ctx, *pinnedDMLSleep)
		if err := runDelete(); err != nil {
			return err
		}
	case "delete-insert":
		if err := runDelete(); err != nil {
			return err
		}
		sleepPinned(ctx, *pinnedDMLSleep)
		if err := runInsert(); err != nil {
			return err
		}
	default:
		signalPinnedRed(errCh, fmt.Errorf("worker %d invalid op-order=%q", workerID, *pinnedOpOrder))
		return fmt.Errorf("invalid op-order=%q", *pinnedOpOrder)
	}

	sleepPinned(ctx, *pinnedDMLSleep)
	return nil
}

func buildPinnedDeleteValues(insertVals []int, shift, modulo, base int) []int {
	deleteVals := make([]int, len(insertVals))
	if modulo <= 0 {
		copy(deleteVals, insertVals)
		return deleteVals
	}
	for i, v := range insertVals {
		rel := (v - base + shift) % modulo
		if rel < 0 {
			rel += modulo
		}
		deleteVals[i] = base + rel
	}
	return deleteVals
}

func buildPinnedWindowValues(start, windowSize, modulo, base int) []int {
	vals := make([]int, windowSize)
	if modulo <= 0 {
		for i := range vals {
			vals[i] = start + i
		}
		return vals
	}
	for i := range vals {
		rel := (start - base + i) % modulo
		if rel < 0 {
			rel += modulo
		}
		vals[i] = base + rel
	}
	return vals
}

func slicesCloneInts(src []int) []int {
	dst := make([]int, len(src))
	copy(dst, src)
	return dst
}

func insertPinnedRows(ctx context.Context, runner pinnedQueryExecer, tableName string, val0Values, val1Values []int, paddingValues []string) error {
	valueStrings := make([]string, len(val0Values))
	args := make([]any, 0, len(val0Values)*3)
	for i, val0 := range val0Values {
		valueStrings[i] = "(?, ?, ?)"
		args = append(args, val0, val1Values[i], paddingValues[i])
	}
	query := fmt.Sprintf("insert into %s (val0, val1, padding) values %s", tableName, strings.Join(valueStrings, ", "))
	return execPinnedWithRetry(ctx, runner, query, args...)
}

func deletePinnedRows(ctx context.Context, runner pinnedQueryExecer, tableName string, val0Values []int) error {
	placeholders := make([]string, len(val0Values))
	args := make([]any, len(val0Values))
	for i, val0 := range val0Values {
		placeholders[i] = "?"
		args[i] = val0
	}
	query := fmt.Sprintf("delete from %s%s where val0 in (%s)", tableName, buildPinnedDeleteHintClause(), strings.Join(placeholders, ", "))
	return execPinnedWithRetry(ctx, runner, query, args...)
}

func runPinnedBeforeDeleteReader(ctx context.Context, runner pinnedQueryExecer, tableName string, val0Values []int) (int, error) {
	mode := strings.ToLower(strings.TrimSpace(*pinnedBeforeDelete))
	if mode == "" || mode == "none" {
		return 0, nil
	}
	indexHint := ""
	switch mode {
	case "use":
		if *pinnedWithIndex {
			indexHint = " use index(val0_idx)"
		}
	case "ignore":
		if *pinnedWithIndex {
			indexHint = " ignore index(val0_idx)"
		}
	default:
		return 0, fmt.Errorf("invalid before-delete-reader=%q", *pinnedBeforeDelete)
	}
	query := fmt.Sprintf(
		"select id, val0, val1, padding from %s%s where val0 in (%s) order by id",
		tableName,
		indexHint,
		strings.Join(buildPinnedPlaceholders(len(val0Values)), ", "),
	)
	args := make([]any, len(val0Values))
	for i, v := range val0Values {
		args[i] = v
	}
	return execPinnedReaderQuery(ctx, runner, query, args)
}

func buildPinnedDeleteHintClause() string {
	if !*pinnedWithIndex {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(*pinnedDeleteHint)) {
	case "", "auto":
		return ""
	case "use":
		return " use index(val0_idx)"
	case "ignore":
		return " ignore index(val0_idx)"
	default:
		return ""
	}
}

func runPinnedOracleWorker(
	ctx context.Context,
	db *sql.DB,
	tableName string,
	stats *pinnedProbeStats,
	workerID int,
	seedBase int64,
	errCh chan<- error,
) {
	if *pinnedOracleWorkers <= 0 || strings.EqualFold(*pinnedOracleMode, "none") {
		return
	}

	windowSize := *pinnedOracleWindow
	if windowSize <= 0 {
		signalPinnedRed(errCh, fmt.Errorf("oracle worker %d invalid oracle-window=%d", workerID, windowSize))
		return
	}

	mode := strings.ToLower(strings.TrimSpace(*pinnedOracleMode))
	doDelete := mode == "delete" || mode == "both"
	doSelect := mode == "select" || mode == "both"
	if !doDelete && !doSelect {
		signalPinnedRed(errCh, fmt.Errorf("oracle worker %d invalid oracle-mode=%q", workerID, *pinnedOracleMode))
		return
	}

	deleteSQL := buildPinnedPreparedDeleteSQL(tableName, windowSize)
	selectSQL := buildPinnedPreparedSelectSQL(tableName, windowSize, *pinnedWithIndex)
	var deleteStmt *sql.Stmt
	var selectStmt *sql.Stmt
	defer func() {
		if deleteStmt != nil {
			_ = deleteStmt.Close()
		}
		if selectStmt != nil {
			_ = selectStmt.Close()
		}
	}()

	rng := rand.New(rand.NewSource(seedBase + 1000003 + int64(workerID)*7919))
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		start := *pinnedWorkerBase + rng.Intn(*pinnedMaxValue0)
		ints, args := buildPinnedWindowArgs(start, windowSize)

		if doDelete {
			if deleteStmt == nil {
				stmt, err := db.PrepareContext(ctx, deleteSQL)
				if err != nil {
					signalPinnedRed(errCh, fmt.Errorf("oracle worker %d prepare delete failed: %w", workerID, err))
					return
				}
				deleteStmt = stmt
			}
			if err := execPinnedPreparedDelete(ctx, deleteStmt, args); err != nil {
				if ctx.Err() != nil {
					return
				}
				if isPinnedSevereOracleError(err) {
					stats.oracleErrs.Add(1)
					signalPinnedRed(errCh, fmt.Errorf("oracle worker %d prepared delete hit severe signature on [%d..%d]: %w", workerID, ints[0], ints[len(ints)-1], err))
					return
				}
				if shouldPinnedReprepareStmt(err) {
					_ = deleteStmt.Close()
					deleteStmt = nil
				}
				if !isPinnedIgnorableDMLError(err) && !isPinnedRetryableDMLError(err) {
					stats.oracleErrs.Add(1)
					signalPinnedRed(errCh, fmt.Errorf("oracle worker %d prepared delete failed on [%d..%d]: %w", workerID, ints[0], ints[len(ints)-1], err))
					return
				}
			} else {
				stats.oracleDeleteOps.Add(1)
			}
		}

		if doSelect {
			if selectStmt == nil {
				stmt, err := db.PrepareContext(ctx, selectSQL)
				if err != nil {
					signalPinnedRed(errCh, fmt.Errorf("oracle worker %d prepare select failed: %w", workerID, err))
					return
				}
				selectStmt = stmt
			}
			if _, err := execPinnedPreparedSelect(ctx, selectStmt, args); err != nil {
				if ctx.Err() != nil {
					return
				}
				if isPinnedSevereOracleError(err) {
					stats.oracleErrs.Add(1)
					signalPinnedRed(errCh, fmt.Errorf("oracle worker %d prepared select hit severe signature on [%d..%d]: %w", workerID, ints[0], ints[len(ints)-1], err))
					return
				}
				if shouldPinnedReprepareStmt(err) {
					_ = selectStmt.Close()
					selectStmt = nil
				}
				if !isPinnedIgnorableDMLError(err) && !isPinnedRetryableDMLError(err) {
					stats.oracleErrs.Add(1)
					signalPinnedRed(errCh, fmt.Errorf("oracle worker %d prepared select failed on [%d..%d]: %w", workerID, ints[0], ints[len(ints)-1], err))
					return
				}
			} else {
				stats.oracleSelectOps.Add(1)
			}
		}

		sleepPinned(ctx, *pinnedOracleSleep)
	}
}

func buildPinnedPreparedDeleteSQL(tableName string, windowSize int) string {
	return fmt.Sprintf("delete from %s where val0 in (%s)", tableName, strings.Join(buildPinnedPlaceholders(windowSize), ", "))
}

func buildPinnedPreparedSelectSQL(tableName string, windowSize int, withIndex bool) string {
	indexHint := ""
	if withIndex {
		indexHint = " use index(val0_idx)"
	}
	return fmt.Sprintf(
		"select id, val0, val1, padding from %s%s where val0 in (%s) order by id",
		tableName,
		indexHint,
		strings.Join(buildPinnedPlaceholders(windowSize), ", "),
	)
}

func buildPinnedPlaceholders(n int) []string {
	placeholders := make([]string, n)
	for i := range placeholders {
		placeholders[i] = "?"
	}
	return placeholders
}

func buildPinnedWindowArgs(start, windowSize int) ([]int, []any) {
	ints := buildPinnedWindowValues(start, windowSize, *pinnedMaxValue0, *pinnedWorkerBase)
	args := make([]any, windowSize)
	for i, v := range ints {
		args[i] = v
	}
	return ints, args
}

func execPinnedPreparedDelete(ctx context.Context, stmt *sql.Stmt, args []any) error {
	_, err := stmt.ExecContext(ctx, args...)
	return err
}

func execPinnedPreparedSelect(ctx context.Context, stmt *sql.Stmt, args []any) (int, error) {
	rows, err := stmt.QueryContext(ctx, args...)
	if err != nil {
		return 0, err
	}
	return scanPinnedReaderRows(rows)
}

func execPinnedReaderQuery(ctx context.Context, runner pinnedQueryExecer, query string, args []any) (int, error) {
	rows, err := runner.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return scanPinnedReaderRows(rows)
}

func scanPinnedReaderRows(rows *sql.Rows) (int, error) {
	defer rows.Close()

	var count int
	for rows.Next() {
		var (
			id      int
			val0    string
			val1    int
			padding string
		)
		if err := rows.Scan(&id, &val0, &val1, &padding); err != nil {
			return count, err
		}
		n, err := strconv.Atoi(val0)
		if err != nil {
			return count, fmt.Errorf("reader returned non-numeric val0=%q for id=%d: %w", val0, id, err)
		}
		if n*10 != val1 {
			return count, fmt.Errorf("reader formula mismatch for id=%d val0=%q val1=%d", id, val0, val1)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, err
	}
	return count, nil
}

func execPinnedWithRetry(ctx context.Context, runner pinnedQueryExecer, query string, args ...any) error {
	for {
		_, err := runner.ExecContext(ctx, query, args...)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if isPinnedSevereOracleError(err) {
			return err
		}
		if isPinnedIgnorableDMLError(err) {
			return nil
		}
		if isPinnedRetryableDMLError(err) {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		return err
	}
}

func waitForPinnedPauseSignal(ctx context.Context, pauseCh <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-pauseCh:
		return nil
	}
}

func waitForPinnedPause(ddlErrCh <-chan error, errCh <-chan error) error {
	select {
	case err := <-ddlErrCh:
		if err != nil {
			return fmt.Errorf("ddl returned early with error: %w", err)
		}
		return fmt.Errorf("ddl finished before pause could be inferred")
	case err := <-errCh:
		return err
	case <-time.After(2 * time.Second):
		return nil
	}
}

func sleepOrPinnedRed(ctx context.Context, d time.Duration, errCh <-chan error) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	case <-timer.C:
		return nil
	}
}

func waitForPinnedDDLFinish(ctx context.Context, ddlErrCh <-chan error, errCh <-chan error) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	case err := <-ddlErrCh:
		if err != nil {
			return fmt.Errorf("ddl failed after release: %w", err)
		}
		return nil
	}
}

func runPinnedFinalOracle(ctx context.Context, db *sql.DB, tableName string, stats *pinnedProbeStats) error {
	if err := execPinned(ctx, db, "admin check table "+tableName); err != nil {
		return err
	}

	bad := queryPinnedInt(ctx, db, "select count(*) from "+tableName+" where val0 is null or val1 is null or cast(val0 as signed) * 10 != val1")
	if bad != 0 {
		return fmt.Errorf("final formula oracle mismatch: bad_rows=%d", bad)
	}

	if err := runPinnedFinalReaderOracle(ctx, db, tableName); err != nil {
		return err
	}

	if *pinnedWithIndex {
		for _, val0 := range []int{0, 7, 17, 191, 511} {
			valText := strconv.Itoa(*pinnedWorkerBase + val0)
			indexCnt := queryPinnedIntArgs(ctx, db, "select count(*) from "+tableName+" use index(val0_idx) where val0 = ?", valText)
			tableCnt := queryPinnedIntArgs(ctx, db, "select count(*) from "+tableName+" use index() where val0 = ?", valText)
			if indexCnt != tableCnt {
				return fmt.Errorf("final rowset mismatch on val0=%d: index_cnt=%d table_cnt=%d", val0, indexCnt, tableCnt)
			}
		}
	}

	fmt.Printf(
		"GREEN workers=%d prefill=%d hold=%s post_release=%s insert_ops=%d delete_ops=%d insert_errs=%d delete_errs=%d oracle_delete_ops=%d oracle_select_ops=%d oracle_errs=%d final_rows=%d\n",
		*pinnedWorkers,
		*pinnedPrefill,
		pinnedHold.String(),
		pinnedPostRelease.String(),
		stats.insertOps.Load(),
		stats.deleteOps.Load(),
		stats.insertErrs.Load(),
		stats.deleteErrs.Load(),
		stats.oracleDeleteOps.Load(),
		stats.oracleSelectOps.Load(),
		stats.oracleErrs.Load(),
		queryPinnedInt(ctx, db, "select count(*) from "+tableName),
	)
	return nil
}

func runPinnedFinalReaderOracle(ctx context.Context, db *sql.DB, tableName string) error {
	starts, err := parsePinnedFinalStarts(*pinnedFinalStarts)
	if err != nil {
		return err
	}
	for _, start := range starts {
		tableCnt, err := runPinnedWindowScan(ctx, db, tableName, start, false)
		if err != nil {
			return fmt.Errorf("final table-scan reader oracle failed at start=%d: %w", start, err)
		}
		if *pinnedWithIndex {
			indexCnt, err := runPinnedWindowScan(ctx, db, tableName, start, true)
			if err != nil {
				return fmt.Errorf("final index reader oracle failed at start=%d: %w", start, err)
			}
			if indexCnt != tableCnt {
				return fmt.Errorf("final full-row reader mismatch at start=%d: index_cnt=%d table_cnt=%d", start, indexCnt, tableCnt)
			}
		}
	}
	return nil
}

func parsePinnedFinalStarts(text string) ([]int, error) {
	parts := strings.Split(text, ",")
	starts := make([]int, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("parse final-starts %q: %w", part, err)
		}
		starts = append(starts, v)
	}
	if len(starts) == 0 {
		return nil, fmt.Errorf("final-starts must not be empty")
	}
	return starts, nil
}

func runPinnedWindowScan(ctx context.Context, db *sql.DB, tableName string, start int, withIndex bool) (int, error) {
	ints, args := buildPinnedWindowArgs(start, *pinnedOracleWindow)
	indexHint := ""
	if withIndex && *pinnedWithIndex {
		indexHint = " use index(val0_idx)"
	} else if *pinnedWithIndex {
		indexHint = " ignore index(val0_idx)"
	}
	query := fmt.Sprintf(
		"select id, val0, val1, padding from %s%s where val0 in (%s) order by id",
		tableName,
		indexHint,
		strings.Join(buildPinnedPlaceholders(len(args)), ", "),
	)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		var (
			id      int
			val0    string
			val1    int
			padding string
		)
		if err := rows.Scan(&id, &val0, &val1, &padding); err != nil {
			return count, err
		}
		n, err := strconv.Atoi(val0)
		if err != nil {
			return count, fmt.Errorf("final reader returned non-numeric val0=%q for id=%d: %w", val0, id, err)
		}
		if n*10 != val1 {
			return count, fmt.Errorf("final reader formula mismatch start=%d id=%d val0=%q val1=%d window=[%d..%d]", start, id, val0, val1, ints[0], ints[len(ints)-1])
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, err
	}
	return count, nil
}

func putFailpoint(ctx context.Context, fpURL, action string) error {
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

func pinnedFailpointEndpoint(name string) string {
	if base := strings.TrimRight(strings.TrimSpace(*pinnedFailpointURL), "/"); base != "" {
		return base + "/" + name
	}
	return strings.TrimRight(*pinnedStatusURL, "/") + "/fail/" + name
}

func deleteFailpoint(ctx context.Context, fpURL string) error {
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

func execPinned(ctx context.Context, db *sql.DB, sqlText string) error {
	if _, err := db.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("%s: %w", sqlText, err)
	}
	return nil
}

func queryPinnedInt(ctx context.Context, db *sql.DB, sqlText string) int {
	return queryPinnedIntArgs(ctx, db, sqlText)
}

func queryPinnedIntArgs(ctx context.Context, db *sql.DB, sqlText string, args ...any) int {
	for {
		var v int
		err := db.QueryRowContext(ctx, sqlText, args...).Scan(&v)
		if err == nil {
			return v
		}
		if isPinnedRetryableDMLError(err) {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		panic(fmt.Sprintf("query int failed: %s args=%v err=%v", sqlText, args, err))
	}
}

func isPinnedRowImageError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "data is corrupted") &&
		strings.Contains(msg, "missing data for not null column")
}

func isPinnedLookupInconsistencyError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "data inconsistency in table") ||
		(strings.Contains(msg, "index-count") && strings.Contains(msg, "record-count")) ||
		strings.Contains(msg, "indexlookup found data inconsistency")
}

func isPinnedSevereOracleError(err error) bool {
	return isPinnedRowImageError(err) || isPinnedLookupInconsistencyError(err)
}

func isPinnedIgnorableDMLError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "public column") && strings.Contains(msg, "has changed")
}

func shouldPinnedReprepareStmt(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return (strings.Contains(msg, "public column") && strings.Contains(msg, "has changed")) ||
		strings.Contains(msg, "information schema is changed") ||
		strings.Contains(msg, "unknown column")
}

func isPinnedRetryableDMLError(err error) bool {
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
		strings.Contains(msg, "stale command") ||
		strings.Contains(msg, "region is unavailable") ||
		strings.Contains(msg, "connection refused")
}

func signalPinnedRed(errCh chan<- error, err error) {
	select {
	case errCh <- &pinnedSevereSignal{err: err}:
	default:
	}
}

func handlePinnedEarlyExit(
	ctx context.Context,
	err error,
	db *sql.DB,
	tableName, holdURL string,
	ddlErrCh <-chan error,
	cancel context.CancelFunc,
	wg *sync.WaitGroup,
	stats *pinnedProbeStats,
) error {
	severe := isPinnedSevereSignal(err)
	if !severe || !*pinnedAfterRed {
		cancel()
		wg.Wait()
		return err
	}

	fmt.Printf("SEVERE signal observed, switching to aftermath oracle: %v\n", err)
	_ = deleteFailpoint(ctx, holdURL)

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer waitCancel()
	if waitErr := waitForPinnedDDLFinish(waitCtx, ddlErrCh, make(chan error)); waitErr != nil {
		cancel()
		wg.Wait()
		return fmt.Errorf("severe signature observed, but DDL did not settle for aftermath oracle: %w", waitErr)
	}

	cancel()
	wg.Wait()

	finalErr := runPinnedFinalOracle(context.Background(), db, tableName, stats)
	if finalErr != nil {
		return fmt.Errorf("severe signature observed and final oracle failed afterwards: %w", finalErr)
	}
	return fmt.Errorf("severe signature observed but final oracle stayed green afterwards: %w", err)
}

func isPinnedSevereSignal(err error) bool {
	var severe *pinnedSevereSignal
	return errors.As(err, &severe)
}

func sleepPinned(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func quotePinnedIdent(ident string) string {
	return "`" + strings.ReplaceAll(ident, "`", "``") + "`"
}

func init() {
	http.DefaultClient.Timeout = 10 * time.Second
	if defaultPinnedHold <= 0 || defaultPinnedPostRelease <= 0 || defaultPinnedTimeout <= 0 {
		panic(errors.New("invalid default probe durations"))
	}
}
