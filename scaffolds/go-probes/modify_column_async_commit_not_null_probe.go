package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	defaultModifyConstraintSetupDSN     = "root@tcp(127.0.0.1:14001)/"
	defaultModifyConstraintTxnDSN       = "root@tcp(127.0.0.1:14000)/"
	defaultModifyConstraintTxnStatusURL = "http://127.0.0.1:18080"
	defaultModifyConstraintSchema       = "ai_native_modify_not_null"
	defaultModifyConstraintTable        = "rows"
	defaultModifyConstraintTimeout      = 2 * time.Minute
	defaultModifyConstraintStartGap     = 500 * time.Millisecond
	defaultModifyConstraintReleaseGap   = 500 * time.Millisecond
)

var (
	modifyConstraintSetupDSN     = flag.String("setup-dsn", defaultModifyConstraintSetupDSN, "setup/ddl mysql dsn")
	modifyConstraintTxnDSN       = flag.String("txn-dsn", defaultModifyConstraintTxnDSN, "transaction mysql dsn")
	modifyConstraintTxnStatusURL = flag.String("txn-status-url", defaultModifyConstraintTxnStatusURL, "transaction TiDB status/failpoint base URL")
	modifyConstraintSchema       = flag.String("schema", defaultModifyConstraintSchema, "target schema")
	modifyConstraintTable        = flag.String("table", defaultModifyConstraintTable, "target table")
	modifyConstraintTimeout      = flag.Duration("timeout", defaultModifyConstraintTimeout, "overall runtime budget")
	modifyConstraintStartGap     = flag.Duration("ddl-start-gap", defaultModifyConstraintStartGap, "delay between starting blocked txn commit and submitting DDL")
	modifyConstraintReleaseGap   = flag.Duration("release-gap", defaultModifyConstraintReleaseGap, "delay between releasing prewrite and async commit completion")
	modifyConstraintMDL          = flag.Bool("metadata-lock", false, "set tidb_enable_metadata_lock on or off during the probe")
	modifyConstraintTxnKind      = flag.String("txn-kind", "async-commit", "async-commit|1pc")
	modifyConstraintScenario     = flag.String("scenario", "not-null", "not-null|tinyint-shrink")
	modifyConstraintDMLKind      = flag.String("dml-kind", "insert-null", "insert-null|update-null|insert-wide|update-wide")
	modifyConstraintReleaseWhen  = flag.String("release-when", "ddl-finish", "ddl-finish|null-reject-marker")
	modifyConstraintPollInterval = flag.Duration("poll-interval", 100*time.Millisecond, "poll interval for the null-reject marker")
	modifyConstraintWideValue    = flag.Int64("wide-value", 512, "wide integer used by the tinyint-shrink scenario")
)

const (
	modifyConstraintBeforePrewriteFP   = "tikvclient/beforePrewrite"
	modifyConstraintAsyncCommitPauseFP = "tikvclient/asyncCommitDoNothing"
)

type modifyConstraintRestoreVars struct {
	mdl string
}

func main() {
	flag.Parse()
	if err := runModifyConstraintProbe(); err != nil {
		log.Printf("probe failed: %v", err)
		os.Exit(1)
	}
}

func runModifyConstraintProbe() error {
	ctx, cancel := context.WithTimeout(context.Background(), *modifyConstraintTimeout)
	defer cancel()

	txnKind, err := normalizeModifyConstraintTxnKind()
	if err != nil {
		return err
	}

	setupDB, err := sql.Open("mysql", *modifyConstraintSetupDSN)
	if err != nil {
		return fmt.Errorf("open setup db: %w", err)
	}
	defer setupDB.Close()
	setupDB.SetMaxOpenConns(8)
	setupDB.SetMaxIdleConns(8)
	if err := setupDB.PingContext(ctx); err != nil {
		return fmt.Errorf("ping setup db: %w", err)
	}

	restore, err := captureModifyConstraintVars(ctx, setupDB)
	if err != nil {
		return fmt.Errorf("capture globals: %w", err)
	}
	defer restoreModifyConstraintVars(context.Background(), setupDB, restore)

	if err := setupModifyConstraintProbe(ctx, setupDB); err != nil {
		return fmt.Errorf("setup probe: %w", err)
	}

	beforePrewriteURL := strings.TrimRight(*modifyConstraintTxnStatusURL, "/") + "/fail/" + modifyConstraintBeforePrewriteFP
	asyncCommitURL := strings.TrimRight(*modifyConstraintTxnStatusURL, "/") + "/fail/" + modifyConstraintAsyncCommitPauseFP
	if err := putModifyConstraintFailpoint(ctx, beforePrewriteURL, "pause"); err != nil {
		return fmt.Errorf("enable beforePrewrite failpoint: %w", err)
	}
	defer func() { _ = deleteModifyConstraintFailpoint(context.Background(), beforePrewriteURL) }()
	if txnKind == "async-commit" {
		if err := putModifyConstraintFailpoint(ctx, asyncCommitURL, "pause"); err != nil {
			return fmt.Errorf("enable asyncCommitDoNothing failpoint: %w", err)
		}
		defer func() { _ = deleteModifyConstraintFailpoint(context.Background(), asyncCommitURL) }()
	}

	log.Printf("CONFIG txn_kind=%s dml_kind=%s metadata_lock=%t ddl_start_gap=%s release_gap=%s",
		txnKind, *modifyConstraintDMLKind, *modifyConstraintMDL, modifyConstraintStartGap.String(), modifyConstraintReleaseGap.String())

	txnErrCh := make(chan error, 1)
	ddlErrCh := make(chan error, 1)
	go runModifyConstraintTxn(ctx, txnKind, txnErrCh)

	if err := waitModifyConstraintTxnBlocked(txnErrCh); err != nil {
		return err
	}
	if err := sleepModifyConstraint(ctx, *modifyConstraintStartGap); err != nil {
		return err
	}

	go runModifyConstraintDDL(ctx, setupDB, ddlErrCh)

	releasedPrewrite := false
	ddlFinishedDuringMarkerWait := false
	var ddlErr error
	if strings.EqualFold(strings.TrimSpace(*modifyConstraintReleaseWhen), "null-reject-marker") {
		var markerSeen bool
		markerSeen, ddlFinishedDuringMarkerWait, ddlErr, err = waitModifyConstraintNullRejectMarker(ctx, setupDB, ddlErrCh)
		if err != nil {
			return err
		}
		if markerSeen {
			log.Printf("NULL_REJECT_MARKER seen=true releasing_prewrite_early=true")
			if err := deleteModifyConstraintFailpoint(ctx, beforePrewriteURL); err != nil {
				return fmt.Errorf("release beforePrewrite failpoint on marker: %w", err)
			}
			releasedPrewrite = true
		}
	}

	if !ddlFinishedDuringMarkerWait {
		ddlErr = <-ddlErrCh
	}
	log.Printf("DDL_RESULT err=%v", ddlErr)

	if !releasedPrewrite {
		if err := deleteModifyConstraintFailpoint(ctx, beforePrewriteURL); err != nil {
			return fmt.Errorf("release beforePrewrite failpoint: %w", err)
		}
	}
	if err := sleepModifyConstraint(ctx, *modifyConstraintReleaseGap); err != nil {
		return err
	}
	if txnKind == "async-commit" {
		if err := deleteModifyConstraintFailpoint(ctx, asyncCommitURL); err != nil {
			return fmt.Errorf("release asyncCommitDoNothing failpoint: %w", err)
		}
	}

	txnErr := <-txnErrCh
	log.Printf("TXN_RESULT err=%v", txnErr)

	result, err := runModifyConstraintFinalOracle(context.Background(), setupDB)
	if err != nil {
		return fmt.Errorf("final oracle failed: %w", err)
	}
	log.Printf("FINAL ddl_succeeded=%t txn_succeeded=%t null_rows=%d rows=%s create=%s",
		ddlErr == nil, txnErr == nil, result.nullRows, result.rows, result.createStmt)

	scenario := strings.ToLower(strings.TrimSpace(*modifyConstraintScenario))
	if ddlErr == nil && txnErr == nil {
		switch scenario {
		case "not-null":
			if result.nullRows > 0 {
				return fmt.Errorf("RED severe: DDL succeeded, %s succeeded, and table still contains %d NULL rows under NOT NULL schema",
					txnKind, result.nullRows)
			}
			log.Printf("GREEN both succeeded without NULL escape")
			return nil
		case "tinyint-shrink":
			return fmt.Errorf("RED severe: DDL succeeded, %s succeeded, and out-of-range write crossed a bigint->tinyint shrink boundary; final rows=%s",
				txnKind, result.rows)
		default:
			return fmt.Errorf("unsupported scenario=%q", scenario)
		}
	}
	if ddlErr == nil && txnErr != nil {
		log.Printf("GREEN protective sibling: DDL succeeded and %s failed", txnKind)
		return nil
	}
	if ddlErr != nil && txnErr == nil {
		log.Printf("INFO protective sibling: DDL failed and %s succeeded", txnKind)
		return nil
	}
	log.Printf("INFO both failed")
	return nil
}

func normalizeModifyConstraintTxnKind() (string, error) {
	switch strings.ToLower(strings.TrimSpace(*modifyConstraintTxnKind)) {
	case "async-commit", "1pc":
		return strings.ToLower(strings.TrimSpace(*modifyConstraintTxnKind)), nil
	default:
		return "", fmt.Errorf("unsupported txn-kind=%q", *modifyConstraintTxnKind)
	}
}

func captureModifyConstraintVars(ctx context.Context, db *sql.DB) (modifyConstraintRestoreVars, error) {
	vars := modifyConstraintRestoreVars{}
	var err error
	if vars.mdl, err = queryModifyConstraintGlobalVar(ctx, db, "tidb_enable_metadata_lock"); err != nil {
		return vars, err
	}
	return vars, nil
}

func restoreModifyConstraintVars(ctx context.Context, db *sql.DB, vars modifyConstraintRestoreVars) {
	_ = execModifyConstraint(ctx, db, fmt.Sprintf("set global tidb_enable_metadata_lock=%s", quoteModifyConstraintValue(vars.mdl)))
}

func setupModifyConstraintProbe(ctx context.Context, db *sql.DB) error {
	if err := execModifyConstraint(ctx, db, fmt.Sprintf("set global tidb_enable_metadata_lock=%s", boolModifyConstraintSysVar(*modifyConstraintMDL))); err != nil {
		return err
	}
	if err := execModifyConstraint(ctx, db, "create database if not exists "+quoteModifyConstraintIdent(*modifyConstraintSchema)); err != nil {
		return err
	}
	tableName := quoteModifyConstraintIdent(*modifyConstraintSchema) + "." + quoteModifyConstraintIdent(*modifyConstraintTable)
	if err := execModifyConstraint(ctx, db, "drop table if exists "+tableName); err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(*modifyConstraintScenario)) {
	case "not-null":
		if err := execModifyConstraint(ctx, db, "create table "+tableName+" (id int primary key, a int null)"); err != nil {
			return err
		}
		if err := execModifyConstraint(ctx, db, "insert into "+tableName+" values (1, 1)"); err != nil {
			return err
		}
	case "tinyint-shrink":
		if err := execModifyConstraint(ctx, db, "create table "+tableName+" (id int primary key, a bigint not null)"); err != nil {
			return err
		}
		if err := execModifyConstraint(ctx, db, "insert into "+tableName+" values (1, 1)"); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported scenario=%q", *modifyConstraintScenario)
	}
	return nil
}

func runModifyConstraintTxn(ctx context.Context, txnKind string, errCh chan<- error) {
	db, err := sql.Open("mysql", *modifyConstraintTxnDSN)
	if err != nil {
		errCh <- fmt.Errorf("open txn db: %w", err)
		return
	}
	defer db.Close()

	conn, err := db.Conn(ctx)
	if err != nil {
		errCh <- fmt.Errorf("get txn conn: %w", err)
		return
	}
	defer conn.Close()

	stmts := []string{
		"use " + quoteModifyConstraintIdent(*modifyConstraintSchema),
		buildModifyConstraintTxnModeStmt(txnKind, "tidb_enable_async_commit"),
		buildModifyConstraintTxnModeStmt(txnKind, "tidb_enable_1pc"),
		"set @@tidb_txn_mode='pessimistic'",
		"begin pessimistic",
	}
	switch strings.ToLower(strings.TrimSpace(*modifyConstraintDMLKind)) {
	case "insert-null":
		stmts = append(stmts, "insert into "+quoteModifyConstraintIdent(*modifyConstraintTable)+" values (2, null)")
	case "update-null":
		stmts = append(stmts, "update "+quoteModifyConstraintIdent(*modifyConstraintTable)+" set a = null where id = 1")
	case "insert-wide":
		stmts = append(stmts, fmt.Sprintf("insert into %s values (2, %d)", quoteModifyConstraintIdent(*modifyConstraintTable), *modifyConstraintWideValue))
	case "update-wide":
		stmts = append(stmts, fmt.Sprintf("update %s set a = %d where id = 1", quoteModifyConstraintIdent(*modifyConstraintTable), *modifyConstraintWideValue))
	default:
		errCh <- fmt.Errorf("unsupported dml-kind=%q", *modifyConstraintDMLKind)
		return
	}
	stmts = append(stmts, "commit")

	for i, stmt := range stmts {
		log.Printf("TXN_STEP %d stmt=%s", i, stmt)
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			if i >= 4 {
				_, _ = conn.ExecContext(context.Background(), "rollback")
			}
			errCh <- err
			return
		}
	}
	errCh <- nil
}

func buildModifyConstraintTxnModeStmt(txnKind, varName string) string {
	switch varName {
	case "tidb_enable_async_commit":
		if txnKind == "1pc" {
			return "set @@tidb_enable_async_commit=0"
		}
		return "set @@tidb_enable_async_commit=1"
	case "tidb_enable_1pc":
		if txnKind == "1pc" {
			return "set @@tidb_enable_1pc=1"
		}
		return "set @@tidb_enable_1pc=0"
	default:
		return ""
	}
}

func waitModifyConstraintTxnBlocked(txnErrCh <-chan error) error {
	select {
	case err := <-txnErrCh:
		if err != nil {
			return fmt.Errorf("txn returned early with error before DDL start: %w", err)
		}
		return fmt.Errorf("txn finished before DDL start; failed to hold commit window")
	case <-time.After(300 * time.Millisecond):
		log.Printf("TXN_BLOCKED inferred_by_runtime=true wait=300ms")
		return nil
	}
}

func runModifyConstraintDDL(ctx context.Context, db *sql.DB, errCh chan<- error) {
	tableName := quoteModifyConstraintIdent(*modifyConstraintSchema) + "." + quoteModifyConstraintIdent(*modifyConstraintTable)
	ddlSQL := "alter table " + tableName + " modify column a int not null"
	switch strings.ToLower(strings.TrimSpace(*modifyConstraintScenario)) {
	case "not-null":
		ddlSQL = "alter table " + tableName + " modify column a int not null"
	case "tinyint-shrink":
		ddlSQL = "alter table " + tableName + " modify column a tinyint not null"
	}
	_, err := db.ExecContext(ctx, ddlSQL)
	errCh <- err
}

func waitModifyConstraintNullRejectMarker(ctx context.Context, db *sql.DB, ddlErrCh <-chan error) (bool, bool, error, error) {
	ticker := time.NewTicker(*modifyConstraintPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, false, nil, ctx.Err()
		case ddlErr := <-ddlErrCh:
			log.Printf("NULL_REJECT_MARKER ddl_finished_before_marker err=%v", ddlErr)
			return false, true, ddlErr, nil
		case <-ticker.C:
			markerSeen, err := probeModifyConstraintNullReject(ctx, db)
			if err != nil {
				log.Printf("NULL_REJECT_MARKER probe_err=%v", err)
				continue
			}
			if markerSeen {
				return true, false, nil, nil
			}
		}
	}
}

func probeModifyConstraintNullReject(ctx context.Context, db *sql.DB) (bool, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()

	probeID := 9000001
	tableName := quoteModifyConstraintIdent(*modifyConstraintSchema) + "." + quoteModifyConstraintIdent(*modifyConstraintTable)
	if _, err := conn.ExecContext(ctx, "begin pessimistic"); err != nil {
		return false, err
	}
	var insertSQL string
	switch strings.ToLower(strings.TrimSpace(*modifyConstraintScenario)) {
	case "not-null":
		insertSQL = fmt.Sprintf("insert into %s values (%d, null)", tableName, probeID)
	case "tinyint-shrink":
		insertSQL = fmt.Sprintf("insert into %s values (%d, %d)", tableName, probeID, *modifyConstraintWideValue)
	default:
		return false, fmt.Errorf("unsupported scenario=%q", *modifyConstraintScenario)
	}
	_, err = conn.ExecContext(ctx, insertSQL)
	if err == nil {
		_, rbErr := conn.ExecContext(context.Background(), "rollback")
		if rbErr != nil {
			return false, rbErr
		}
		return false, nil
	}
	_, _ = conn.ExecContext(context.Background(), "rollback")
	errText := strings.ToLower(err.Error())
	switch strings.ToLower(strings.TrimSpace(*modifyConstraintScenario)) {
	case "not-null":
		if strings.Contains(errText, "cannot be null") {
			return true, nil
		}
	case "tinyint-shrink":
		if strings.Contains(errText, "data truncated") || strings.Contains(errText, "out of range") || strings.Contains(errText, "overflow") {
			return true, nil
		}
	}
	if strings.Contains(errText, "information schema is changed") {
		return false, nil
	}
	return false, err
}

type modifyConstraintOracleResult struct {
	createStmt string
	rows       string
	nullRows   int
}

func runModifyConstraintFinalOracle(ctx context.Context, db *sql.DB) (modifyConstraintOracleResult, error) {
	result := modifyConstraintOracleResult{}
	tableName := quoteModifyConstraintIdent(*modifyConstraintSchema) + "." + quoteModifyConstraintIdent(*modifyConstraintTable)

	var (
		tableNameOut string
		createStmt   string
	)
	if err := db.QueryRowContext(ctx, "show create table "+tableName).Scan(&tableNameOut, &createStmt); err != nil {
		return result, err
	}
	result.createStmt = strings.ReplaceAll(createStmt, "\n", " ")

	rows, err := db.QueryContext(ctx, "select id, a from "+tableName+" order by id")
	if err != nil {
		return result, err
	}
	defer rows.Close()
	var rowParts []string
	for rows.Next() {
		var (
			id int
			a  sql.NullInt64
		)
		if err := rows.Scan(&id, &a); err != nil {
			return result, err
		}
		if a.Valid {
			rowParts = append(rowParts, fmt.Sprintf("%d:%d", id, a.Int64))
		} else {
			rowParts = append(rowParts, fmt.Sprintf("%d:NULL", id))
		}
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	result.rows = strings.Join(rowParts, ",")

	if err := db.QueryRowContext(ctx, "select count(*) from "+tableName+" where a is null").Scan(&result.nullRows); err != nil {
		return result, err
	}
	return result, nil
}

func putModifyConstraintFailpoint(ctx context.Context, fpURL, action string) error {
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
		return fmt.Errorf("unexpected put status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func deleteModifyConstraintFailpoint(ctx context.Context, fpURL string) error {
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
		return fmt.Errorf("unexpected delete status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func execModifyConstraint(ctx context.Context, db *sql.DB, sqlText string) error {
	if _, err := db.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("%s: %w", sqlText, err)
	}
	return nil
}

func queryModifyConstraintGlobalVar(ctx context.Context, db *sql.DB, name string) (string, error) {
	var (
		varName string
		value   string
	)
	if err := db.QueryRowContext(ctx, "show variables like ?", name).Scan(&varName, &value); err != nil {
		return "", err
	}
	return value, nil
}

func sleepModifyConstraint(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func boolModifyConstraintSysVar(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func quoteModifyConstraintValue(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}

func quoteModifyConstraintIdent(ident string) string {
	return "`" + strings.ReplaceAll(ident, "`", "``") + "`"
}
