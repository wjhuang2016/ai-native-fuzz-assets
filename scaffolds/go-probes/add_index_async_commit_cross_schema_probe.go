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
	defaultCrossDSN       = "root@tcp(127.0.0.1:14000)/"
	defaultCrossStatusURL = "http://127.0.0.1:18080"
	defaultCrossSchema    = "ai_native_async_cross"
	defaultCrossTable     = "rows"
	defaultCrossTimeout   = 3 * time.Minute
	defaultCrossHold      = 6 * time.Second
	defaultCrossStartGap  = time.Second
)

var (
	crossDSN             = flag.String("dsn", defaultCrossDSN, "mysql dsn")
	crossStatusURL       = flag.String("status-url", defaultCrossStatusURL, "TiDB status/failpoint base URL")
	crossFailpointURL    = flag.String("failpoint-base-url", "", "optional standalone failpoint HTTP base URL; when set, failpoints are addressed as <base>/<failpoint-name>")
	crossSchema          = flag.String("schema", defaultCrossSchema, "target schema")
	crossTable           = flag.String("table", defaultCrossTable, "target table")
	crossDDLKind         = flag.String("ddl-kind", "add-index", "add-index|add-unique-index|add-primary-key|multi-add-index|multi-add-index-rich|add-composite-index-rich|add-generated-index|add-generated-index-rich|add-virtual-generated-index-rich")
	crossTxnKind         = flag.String("txn-kind", "async-commit", "async-commit|1pc")
	crossTxnShape        = flag.String("txn-shape", "basic", "basic|basicrev|insert1|insert2|update1|update2|stmtinsert1|stmtinsert2|stmtupdate1|stmtupdate2|fanout3|mixed3|mixed4")
	crossTimeout         = flag.Duration("timeout", defaultCrossTimeout, "overall runtime budget")
	crossHold            = flag.Duration("hold", defaultCrossHold, "how long to keep async commit prewrite paused before release")
	crossStartGap        = flag.Duration("ddl-start-gap", defaultCrossStartGap, "delay between starting async commit and submitting DDL")
	crossDDLLead         = flag.Duration("ddl-lead", 0, "if >0, start DDL first and wait this long before starting the transaction")
	crossPausePrewrite   = flag.Bool("pause-prewrite", true, "enable tikvclient/beforePrewrite failpoint around the critical window")
	crossObserveDDLJob   = flag.Bool("observe-ddl-job", false, "log latest information_schema.DDL_JOBS snapshot around the critical window")
	crossMDL             = flag.Bool("metadata-lock", false, "set tidb_enable_metadata_lock on or off during the probe")
	crossExpectSchemaErr = flag.Bool("expect-schema-changed", false, "expect commit to fail with information schema changed")
)

const beforePrewriteFailpoint = "tikvclient/beforePrewrite"

type crossRestoreVars struct {
	mdl       string
	fastReorg string
	distTask  string
}

func main() {
	flag.Parse()
	if err := runCrossProbe(); err != nil {
		log.Printf("probe failed: %v", err)
		os.Exit(1)
	}
}

func runCrossProbe() error {
	ctx, cancel := context.WithTimeout(context.Background(), *crossTimeout)
	defer cancel()

	txnKind, err := normalizeCrossTxnKind()
	if err != nil {
		return err
	}

	db, err := sql.Open("mysql", *crossDSN)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping db: %w", err)
	}

	restore, err := captureCrossVars(ctx, db)
	if err != nil {
		return fmt.Errorf("capture globals: %w", err)
	}
	defer restoreCrossVars(context.Background(), db, restore)

	if err := setupCrossProbe(ctx, db); err != nil {
		return fmt.Errorf("setup probe: %w", err)
	}

	fpURL := crossFailpointEndpoint(beforePrewriteFailpoint)
	if *crossPausePrewrite {
		if err := putCrossFailpoint(ctx, fpURL, "1*pause"); err != nil {
			return fmt.Errorf("enable beforePrewrite failpoint: %w", err)
		}
		defer func() { _ = deleteCrossFailpoint(context.Background(), fpURL) }()
	}

	log.Printf("CONFIG ddl_kind=%s txn_kind=%s txn_shape=%s metadata_lock=%t pause_prewrite=%t hold=%s ddl_start_gap=%s ddl_lead=%s expect_schema_changed=%t",
		*crossDDLKind, txnKind, *crossTxnShape, *crossMDL, *crossPausePrewrite, crossHold.String(), crossStartGap.String(), crossDDLLead.String(), *crossExpectSchemaErr)

	txnErrCh := make(chan error, 1)
	ddlErrCh := make(chan error, 1)
	if *crossDDLLead > 0 {
		go runCrossDDL(ctx, db, ddlErrCh)
		if err := sleepCross(ctx, *crossDDLLead); err != nil {
			return err
		}
		if *crossObserveDDLJob {
			logCrossDDLJobSnapshot(ctx, db, "before_txn_after_ddl_lead")
		}
		go runCrossTxn(ctx, txnKind, txnErrCh)
	} else {
		go runCrossTxn(ctx, txnKind, txnErrCh)

		if err := sleepCross(ctx, *crossStartGap); err != nil {
			return err
		}

		go runCrossDDL(ctx, db, ddlErrCh)
	}

	if err := sleepCross(ctx, *crossHold); err != nil {
		return err
	}

	ddlStatus := "running"
	ddlFinishedEarly := false
	var ddlErr error
	select {
	case ddlErr = <-ddlErrCh:
		if ddlErr != nil {
			return fmt.Errorf("ddl failed before failpoint release: %w", ddlErr)
		}
		ddlFinishedEarly = true
		ddlStatus = "finished"
	default:
	}
	log.Printf("AFTER_HOLD ddl_status=%s hold=%s", ddlStatus, crossHold.String())
	if *crossObserveDDLJob {
		logCrossDDLJobSnapshot(ctx, db, "after_hold")
	}

	if *crossPausePrewrite {
		if err := deleteCrossFailpoint(ctx, fpURL); err != nil {
			return fmt.Errorf("release beforePrewrite failpoint: %w", err)
		}
	}

	txnErr := <-txnErrCh
	if !ddlFinishedEarly {
		ddlErr = <-ddlErrCh
	}
	if ddlErr != nil {
		return fmt.Errorf("ddl failed after release: %w", ddlErr)
	}

	if err := runCrossFinalOracle(context.Background(), db); err != nil {
		return fmt.Errorf("final oracle failed: %w", err)
	}

	if txnErr == nil {
		log.Printf("TXN_RESULT success")
		if *crossExpectSchemaErr {
			return fmt.Errorf("expected Information schema is changed but %s succeeded", txnKind)
		}
		if err := runCrossSuccessOracle(context.Background(), db); err != nil {
			return fmt.Errorf("success oracle failed: %w", err)
		}
		log.Printf("GREEN txn_kind=%s metadata_lock=%t", txnKind, *crossMDL)
		return nil
	}

	log.Printf("TXN_RESULT err=%v", txnErr)
	if *crossExpectSchemaErr && strings.Contains(strings.ToLower(txnErr.Error()), "information schema is changed") {
		return fmt.Errorf("RED txn_kind=%s hit Information schema is changed with metadata_lock=%t", txnKind, *crossMDL)
	}
	return fmt.Errorf("unexpected txn error: %w", txnErr)
}

func captureCrossVars(ctx context.Context, db *sql.DB) (crossRestoreVars, error) {
	vars := crossRestoreVars{}
	var err error
	if vars.mdl, err = queryCrossGlobalVar(ctx, db, "tidb_enable_metadata_lock"); err != nil {
		return vars, err
	}
	if vars.fastReorg, err = queryCrossGlobalVar(ctx, db, "tidb_ddl_enable_fast_reorg"); err != nil {
		return vars, err
	}
	if vars.distTask, err = queryCrossGlobalVar(ctx, db, "tidb_enable_dist_task"); err != nil {
		return vars, err
	}
	return vars, nil
}

func restoreCrossVars(ctx context.Context, db *sql.DB, vars crossRestoreVars) {
	_ = execCross(ctx, db, fmt.Sprintf("set global tidb_enable_metadata_lock=%s", quoteCrossValue(vars.mdl)))
	_ = execCross(ctx, db, fmt.Sprintf("set global tidb_ddl_enable_fast_reorg=%s", quoteCrossValue(vars.fastReorg)))
	_ = execCross(ctx, db, fmt.Sprintf("set global tidb_enable_dist_task=%s", quoteCrossValue(vars.distTask)))
}

func setupCrossProbe(ctx context.Context, db *sql.DB) error {
	if err := execCross(ctx, db, fmt.Sprintf("set global tidb_enable_metadata_lock=%s", boolCrossSysVar(*crossMDL))); err != nil {
		return err
	}
	if err := execCross(ctx, db, "set global tidb_enable_dist_task=off"); err != nil {
		return err
	}
	if err := execCross(ctx, db, "set global tidb_ddl_enable_fast_reorg=off"); err != nil {
		return err
	}
	if err := execCross(ctx, db, "create database if not exists "+quoteCrossIdent(*crossSchema)); err != nil {
		return err
	}
	tableName := quoteCrossIdent(*crossSchema) + "." + quoteCrossIdent(*crossTable)
	if err := execCross(ctx, db, "drop table if exists "+tableName); err != nil {
		return err
	}
	createSQL := "create table " + tableName + " (id int not null, b int not null, pad varchar(32) not null default '')"
	switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
	case "add-index", "add-unique-index", "multi-add-index", "multi-add-index-rich", "add-composite-index-rich":
		createSQL = "create table " + tableName + " (id int primary key, b int not null, pad varchar(32) not null default '')"
	case "add-generated-index":
		createSQL = "create table " + tableName + " (id int primary key, b int not null, pad varchar(32) not null default '', g int as (b + 1) stored)"
	case "add-generated-index-rich":
		createSQL = "create table " + tableName + " (id int primary key, b int not null, pad varchar(32) not null default '', g int as (b + char_length(pad)) stored)"
	case "add-virtual-generated-index-rich":
		createSQL = "create table " + tableName + " (id int primary key, b int not null, pad varchar(32) not null default '', g int as (b + char_length(pad)) virtual)"
	}
	if err := execCross(ctx, db, createSQL); err != nil {
		return err
	}
	initSQL, err := buildCrossInitSQL(tableName)
	if err != nil {
		return err
	}
	if err := execCross(ctx, db, initSQL); err != nil {
		return err
	}
	return nil
}

func runCrossTxn(ctx context.Context, txnKind string, errCh chan<- error) {
	db, err := sql.Open("mysql", *crossDSN)
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

	tableName := quoteCrossIdent(*crossSchema) + "." + quoteCrossIdent(*crossTable)
	dmlStmts, err := buildCrossTxnDML(tableName)
	if err != nil {
		errCh <- err
		return
	}
	stmts := []string{
		"use " + quoteCrossIdent(*crossSchema),
		buildCrossTxnModeStmt(txnKind, "tidb_enable_async_commit"),
		buildCrossTxnModeStmt(txnKind, "tidb_enable_1pc"),
	}
	explicitTxn := !isCrossAutocommitStmtShape()
	if explicitTxn {
		stmts = append(stmts, "set @@tidb_txn_mode='pessimistic'", "begin pessimistic")
	}
	stmts = append(stmts, dmlStmts...)
	if explicitTxn {
		stmts = append(stmts, "commit")
	}
	for i, stmt := range stmts {
		_, err = conn.ExecContext(ctx, stmt)
		if err != nil {
			if explicitTxn && i >= 4 {
				_, _ = conn.ExecContext(context.Background(), "rollback")
			}
			errCh <- err
			return
		}
	}
	errCh <- nil
}

func normalizeCrossTxnKind() (string, error) {
	switch strings.ToLower(strings.TrimSpace(*crossTxnKind)) {
	case "async-commit", "1pc":
		return strings.ToLower(strings.TrimSpace(*crossTxnKind)), nil
	default:
		return "", fmt.Errorf("unsupported txn-kind=%q", *crossTxnKind)
	}
}

func buildCrossTxnModeStmt(txnKind, varName string) string {
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

func runCrossDDL(ctx context.Context, db *sql.DB, errCh chan<- error) {
	tableName := quoteCrossIdent(*crossSchema) + "." + quoteCrossIdent(*crossTable)
	ddlSQL, err := buildCrossDDL(tableName)
	if err == nil {
		_, err = db.ExecContext(ctx, ddlSQL)
	}
	errCh <- err
}

func runCrossFinalOracle(ctx context.Context, db *sql.DB) error {
	tableName := quoteCrossIdent(*crossSchema) + "." + quoteCrossIdent(*crossTable)
	if err := execCross(ctx, db, "admin check table "+tableName); err != nil {
		return err
	}
	var idxCnt, tblCnt int
	indexSQL, tableSQL, err := buildCrossCountSQL(tableName)
	if err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, indexSQL).Scan(&idxCnt); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, tableSQL).Scan(&tblCnt); err != nil {
		return err
	}
	if idxCnt != tblCnt {
		return fmt.Errorf("index/table mismatch: index_cnt=%d table_cnt=%d", idxCnt, tblCnt)
	}
	if err := runCrossExtraOracle(ctx, db, tableName); err != nil {
		return err
	}
	return nil
}

func runCrossSuccessOracle(ctx context.Context, db *sql.DB) error {
	tableName := quoteCrossIdent(*crossSchema) + "." + quoteCrossIdent(*crossTable)
	querySQL, wantRows, err := buildCrossSuccessOracle(tableName)
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, querySQL)
	if err != nil {
		return err
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-generated-index":
			var id, b, g int
			if err := rows.Scan(&id, &b, &g); err != nil {
				return err
			}
			got = append(got, fmt.Sprintf("%d:%d:%d", id, b, g))
		case "add-generated-index-rich", "add-virtual-generated-index-rich":
			var id, b, g int
			var pad string
			if err := rows.Scan(&id, &b, &pad, &g); err != nil {
				return err
			}
			got = append(got, fmt.Sprintf("%d:%d:%s:%d", id, b, pad, g))
		case "multi-add-index-rich", "add-composite-index-rich":
			var id, b int
			var pad string
			if err := rows.Scan(&id, &b, &pad); err != nil {
				return err
			}
			got = append(got, fmt.Sprintf("%d:%d:%s", id, b, pad))
		default:
			var id, b int
			if err := rows.Scan(&id, &b); err != nil {
				return err
			}
			got = append(got, fmt.Sprintf("%d:%d", id, b))
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if strings.Join(got, ",") != strings.Join(wantRows, ",") {
		return fmt.Errorf("unexpected final rows: got=%v want=%v", got, wantRows)
	}
	return nil
}

func logCrossDDLJobSnapshot(ctx context.Context, db *sql.DB, tag string) {
	query := `
select JOB_ID, JOB_TYPE, SCHEMA_STATE, STATE, ROW_COUNT
from information_schema.DDL_JOBS
where DB_NAME = ? and TABLE_NAME = ?
order by JOB_ID desc
limit 1`
	var (
		jobID       int64
		jobType     string
		schemaState string
		state       string
		rowCount    int64
	)
	err := db.QueryRowContext(ctx, query, *crossSchema, *crossTable).Scan(&jobID, &jobType, &schemaState, &state, &rowCount)
	if err == sql.ErrNoRows {
		log.Printf("DDL_JOB_SNAPSHOT tag=%s none", tag)
		return
	}
	if err != nil {
		log.Printf("DDL_JOB_SNAPSHOT tag=%s err=%v", tag, err)
		return
	}
	log.Printf("DDL_JOB_SNAPSHOT tag=%s job_id=%d job_type=%s schema_state=%s state=%s row_count=%d",
		tag, jobID, jobType, schemaState, state, rowCount)
}

func putCrossFailpoint(ctx context.Context, fpURL, action string) error {
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

func deleteCrossFailpoint(ctx context.Context, fpURL string) error {
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

func crossFailpointEndpoint(name string) string {
	if base := strings.TrimRight(strings.TrimSpace(*crossFailpointURL), "/"); base != "" {
		return base + "/" + name
	}
	return strings.TrimRight(*crossStatusURL, "/") + "/fail/" + name
}

func execCross(ctx context.Context, db *sql.DB, sqlText string) error {
	if _, err := db.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("%s: %w", sqlText, err)
	}
	return nil
}

func queryCrossGlobalVar(ctx context.Context, db *sql.DB, name string) (string, error) {
	var (
		varName string
		value   string
	)
	if err := db.QueryRowContext(ctx, "show variables like ?", name).Scan(&varName, &value); err != nil {
		return "", err
	}
	return value, nil
}

func boolCrossSysVar(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func quoteCrossValue(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}

func quoteCrossIdent(ident string) string {
	return "`" + strings.ReplaceAll(ident, "`", "``") + "`"
}

func buildCrossDDL(tableName string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
	case "add-index":
		return "alter table " + tableName + " add index idx_b(b)", nil
	case "multi-add-index":
		return "alter table " + tableName + " add index idx_b(b), add index idx_pad(pad)", nil
	case "multi-add-index-rich":
		return "alter table " + tableName + " add index idx_b(b), add index idx_pad(pad)", nil
	case "add-composite-index-rich":
		return "alter table " + tableName + " add index idx_bp(b, pad)", nil
	case "add-unique-index":
		return "alter table " + tableName + " add unique index idx_b(b)", nil
	case "add-generated-index":
		return "alter table " + tableName + " add index idx_g(g)", nil
	case "add-generated-index-rich":
		return "alter table " + tableName + " add index idx_g(g)", nil
	case "add-virtual-generated-index-rich":
		return "alter table " + tableName + " add index idx_g(g)", nil
	case "add-primary-key":
		return "alter table " + tableName + " add primary key(id)", nil
	default:
		return "", fmt.Errorf("unsupported ddl-kind=%q", *crossDDLKind)
	}
}

func buildCrossCountSQL(tableName string) (string, string, error) {
	fanout := isCrossTxnShape("fanout3")
	mixed3 := isCrossTxnShape("mixed3")
	mixed := isCrossTxnShape("mixed4")
	switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
	case "add-index", "add-unique-index", "multi-add-index", "multi-add-index-rich":
		if isCrossTxnShape("basicrev") {
			return "select count(*) from " + tableName + " use index(idx_b) where b in (2,10)",
				"select count(*) from " + tableName + " ignore index(idx_b) where b in (2,10)",
				nil
		}
		if isCrossTxnShape("stmtinsert1") {
			return "select count(*) from " + tableName + " use index(idx_b) where b in (2)",
				"select count(*) from " + tableName + " ignore index(idx_b) where b in (2)",
				nil
		}
		if isCrossTxnShape("stmtinsert2") {
			return "select count(*) from " + tableName + " use index(idx_b) where b in (2,3)",
				"select count(*) from " + tableName + " ignore index(idx_b) where b in (2,3)",
				nil
		}
		if isCrossTxnShape("stmtupdate1") {
			return "select count(*) from " + tableName + " use index(idx_b) where b in (10)",
				"select count(*) from " + tableName + " ignore index(idx_b) where b in (10)",
				nil
		}
		if isCrossTxnShape("stmtupdate2") {
			return "select count(*) from " + tableName + " use index(idx_b) where b in (10,20)",
				"select count(*) from " + tableName + " ignore index(idx_b) where b in (10,20)",
				nil
		}
		if isCrossTxnShape("insert1") {
			return "select count(*) from " + tableName + " use index(idx_b) where b in (2)",
				"select count(*) from " + tableName + " ignore index(idx_b) where b in (2)",
				nil
		}
		if isCrossTxnShape("insert2") {
			return "select count(*) from " + tableName + " use index(idx_b) where b in (2,3)",
				"select count(*) from " + tableName + " ignore index(idx_b) where b in (2,3)",
				nil
		}
		if isCrossTxnShape("update1") {
			return "select count(*) from " + tableName + " use index(idx_b) where b in (10)",
				"select count(*) from " + tableName + " ignore index(idx_b) where b in (10)",
				nil
		}
		if isCrossTxnShape("update2") {
			return "select count(*) from " + tableName + " use index(idx_b) where b in (10,20)",
				"select count(*) from " + tableName + " ignore index(idx_b) where b in (10,20)",
				nil
		}
		if mixed3 {
			return "select count(*) from " + tableName + " use index(idx_b) where b in (2,4,10)",
				"select count(*) from " + tableName + " ignore index(idx_b) where b in (2,4,10)",
				nil
		}
		if fanout {
			return "select count(*) from " + tableName + " use index(idx_b) where b in (3,4,10,20)",
				"select count(*) from " + tableName + " ignore index(idx_b) where b in (3,4,10,20)",
				nil
		}
		return "select count(*) from " + tableName + " use index(idx_b) where b in (2,10)",
			"select count(*) from " + tableName + " ignore index(idx_b) where b in (2,10)",
			nil
	case "add-composite-index-rich":
		if fanout {
			return "select count(*) from " + tableName + " use index(idx_bp) where (b = 3 and pad = 'ccc') or (b = 4 and pad = 'dddd') or (b = 10 and pad = 'zzz') or (b = 20 and pad = 'yy')",
				"select count(*) from " + tableName + " ignore index(idx_bp) where (b = 3 and pad = 'ccc') or (b = 4 and pad = 'dddd') or (b = 10 and pad = 'zzz') or (b = 20 and pad = 'yy')",
				nil
		}
		return "select count(*) from " + tableName + " use index(idx_bp) where (b = 2 and pad = 'bb') or (b = 10 and pad = 'zzz')",
			"select count(*) from " + tableName + " ignore index(idx_bp) where (b = 2 and pad = 'bb') or (b = 10 and pad = 'zzz')",
			nil
	case "add-generated-index":
		return "select count(*) from " + tableName + " use index(idx_g) where g in (3,11)",
			"select count(*) from " + tableName + " ignore index(idx_g) where g in (3,11)",
			nil
	case "add-generated-index-rich":
		if mixed3 {
			return "select count(*) from " + tableName + " use index(idx_g) where g in (4,8,13)",
				"select count(*) from " + tableName + " ignore index(idx_g) where g in (4,8,13)",
				nil
		}
		if mixed {
			return "select count(*) from " + tableName + " use index(idx_g) where g in (8,13,22)",
				"select count(*) from " + tableName + " ignore index(idx_g) where g in (8,13,22)",
				nil
		}
		if fanout {
			return "select count(*) from " + tableName + " use index(idx_g) where g in (6,8,13,22)",
				"select count(*) from " + tableName + " ignore index(idx_g) where g in (6,8,13,22)",
				nil
		}
		return "select count(*) from " + tableName + " use index(idx_g) where g in (4,13)",
			"select count(*) from " + tableName + " ignore index(idx_g) where g in (4,13)",
			nil
	case "add-virtual-generated-index-rich":
		if mixed3 {
			return "select count(*) from " + tableName + " use index(idx_g) where g in (4,8,13)",
				"select count(*) from " + tableName + " ignore index(idx_g) where g in (4,8,13)",
				nil
		}
		if mixed {
			return "select count(*) from " + tableName + " use index(idx_g) where g in (8,13,22)",
				"select count(*) from " + tableName + " ignore index(idx_g) where g in (8,13,22)",
				nil
		}
		if fanout {
			return "select count(*) from " + tableName + " use index(idx_g) where g in (6,8,13,22)",
				"select count(*) from " + tableName + " ignore index(idx_g) where g in (6,8,13,22)",
				nil
		}
		return "select count(*) from " + tableName + " use index(idx_g) where g in (4,13)",
			"select count(*) from " + tableName + " ignore index(idx_g) where g in (4,13)",
			nil
	case "add-primary-key":
		if isCrossTxnShape("basicrev") {
			return "select count(*) from " + tableName + " use index(primary) where id in (1,2)",
				"select count(*) from " + tableName + " ignore index(primary) where id in (1,2)",
				nil
		}
		if isCrossTxnShape("stmtinsert1") {
			return "select count(*) from " + tableName + " use index(primary) where id in (1,2)",
				"select count(*) from " + tableName + " ignore index(primary) where id in (1,2)",
				nil
		}
		if isCrossTxnShape("stmtinsert2") {
			return "select count(*) from " + tableName + " use index(primary) where id in (1,2,3)",
				"select count(*) from " + tableName + " ignore index(primary) where id in (1,2,3)",
				nil
		}
		if isCrossTxnShape("stmtupdate1") {
			return "select count(*) from " + tableName + " use index(primary) where id in (1)",
				"select count(*) from " + tableName + " ignore index(primary) where id in (1)",
				nil
		}
		if isCrossTxnShape("stmtupdate2") {
			return "select count(*) from " + tableName + " use index(primary) where id in (1,2)",
				"select count(*) from " + tableName + " ignore index(primary) where id in (1,2)",
				nil
		}
		if isCrossTxnShape("insert1") {
			return "select count(*) from " + tableName + " use index(primary) where id in (1,2)",
				"select count(*) from " + tableName + " ignore index(primary) where id in (1,2)",
				nil
		}
		if isCrossTxnShape("insert2") {
			return "select count(*) from " + tableName + " use index(primary) where id in (1,2,3)",
				"select count(*) from " + tableName + " ignore index(primary) where id in (1,2,3)",
				nil
		}
		if isCrossTxnShape("update1") {
			return "select count(*) from " + tableName + " use index(primary) where id in (1)",
				"select count(*) from " + tableName + " ignore index(primary) where id in (1)",
				nil
		}
		if isCrossTxnShape("update2") {
			return "select count(*) from " + tableName + " use index(primary) where id in (1,2)",
				"select count(*) from " + tableName + " ignore index(primary) where id in (1,2)",
				nil
		}
		if mixed3 {
			return "select count(*) from " + tableName + " use index(primary) where id in (1,2,4)",
				"select count(*) from " + tableName + " ignore index(primary) where id in (1,2,4)",
				nil
		}
		if fanout {
			return "select count(*) from " + tableName + " use index(primary) where id in (1,2,3,4)",
				"select count(*) from " + tableName + " ignore index(primary) where id in (1,2,3,4)",
				nil
		}
		return "select count(*) from " + tableName + " use index(primary) where id in (1,2)",
			"select count(*) from " + tableName + " ignore index(primary) where id in (1,2)",
			nil
	default:
		return "", "", fmt.Errorf("unsupported ddl-kind=%q", *crossDDLKind)
	}
}

func runCrossExtraOracle(ctx context.Context, db *sql.DB, tableName string) error {
	switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
	case "multi-add-index":
		var idxCnt, tblCnt int
		if err := db.QueryRowContext(ctx, "select count(*) from "+tableName+" use index(idx_pad) where pad in ('a','b')").Scan(&idxCnt); err != nil {
			return err
		}
		if err := db.QueryRowContext(ctx, "select count(*) from "+tableName+" ignore index(idx_pad) where pad in ('a','b')").Scan(&tblCnt); err != nil {
			return err
		}
		if idxCnt != tblCnt {
			return fmt.Errorf("idx_pad/table mismatch: index_cnt=%d table_cnt=%d", idxCnt, tblCnt)
		}
	case "multi-add-index-rich":
		if err := compareCrossOrderedQuery(ctx, db,
			"select id, b, pad from "+tableName+" use index(idx_b) order by b, id, pad",
			"select id, b, pad from "+tableName+" ignore index(idx_b, idx_pad) order by b, id, pad",
		); err != nil {
			return fmt.Errorf("idx_b exact-row mismatch: %w", err)
		}
		if err := compareCrossOrderedQuery(ctx, db,
			"select id, b, pad from "+tableName+" use index(idx_pad) order by pad, id, b",
			"select id, b, pad from "+tableName+" ignore index(idx_b, idx_pad) order by pad, id, b",
		); err != nil {
			return fmt.Errorf("idx_pad exact-row mismatch: %w", err)
		}
	case "add-composite-index-rich":
		if err := compareCrossOrderedQuery(ctx, db,
			"select id, b, pad from "+tableName+" use index(idx_bp) order by b, pad, id",
			"select id, b, pad from "+tableName+" ignore index(idx_bp) order by b, pad, id",
		); err != nil {
			return fmt.Errorf("idx_bp exact-row mismatch: %w", err)
		}
	case "add-generated-index-rich":
		if err := compareCrossOrderedQuery(ctx, db,
			"select id, b, pad, g from "+tableName+" use index(idx_g) order by g, id, b, pad",
			"select id, b, pad, g from "+tableName+" ignore index(idx_g) order by g, id, b, pad",
		); err != nil {
			return fmt.Errorf("idx_g exact-row mismatch: %w", err)
		}
	case "add-virtual-generated-index-rich":
		if err := compareCrossOrderedQuery(ctx, db,
			"select id, b, pad, g from "+tableName+" use index(idx_g) order by g, id, b, pad",
			"select id, b, pad, g from "+tableName+" ignore index(idx_g) order by g, id, b, pad",
		); err != nil {
			return fmt.Errorf("idx_g exact-row mismatch: %w", err)
		}
	}
	return nil
}

func buildCrossInitSQL(tableName string) (string, error) {
	if isCrossTxnShape("update2") || isCrossTxnShape("stmtupdate2") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-index", "add-unique-index", "add-primary-key":
			return "insert into " + tableName + " values (1,1,'a'),(2,2,'b')", nil
		}
	}
	if isCrossTxnShape("mixed3") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-generated-index-rich", "add-virtual-generated-index-rich":
			return "insert into " + tableName + " (id, b, pad) values (1,1,'a'),(2,2,'bb'),(3,3,'ccc')", nil
		case "add-index", "add-unique-index", "add-primary-key":
			return "insert into " + tableName + " values (1,1,'a'),(2,2,'bb'),(3,3,'ccc')", nil
		}
	}
	if isCrossTxnShape("mixed4") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-generated-index-rich", "add-virtual-generated-index-rich":
			return "insert into " + tableName + " (id, b, pad) values (1,1,'a'),(2,2,'bb'),(3,3,'ccc')", nil
		}
	}
	if isCrossTxnShape("fanout3") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-generated-index-rich", "add-virtual-generated-index-rich":
			return "insert into " + tableName + " (id, b, pad) values (1,1,'a'),(2,2,'bb'),(3,3,'ccc')", nil
		case "multi-add-index-rich", "add-composite-index-rich":
			return "insert into " + tableName + " values (1,1,'a'),(2,2,'bb'),(3,3,'ccc')", nil
		case "add-index", "add-unique-index", "add-primary-key":
			return "insert into " + tableName + " values (1,1,'a'),(2,2,'bb'),(3,3,'ccc')", nil
		}
	}
	switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
	case "add-generated-index", "add-generated-index-rich", "add-virtual-generated-index-rich":
		return "insert into " + tableName + " (id, b, pad) values (1,1,'a')", nil
	case "add-index", "add-unique-index", "add-primary-key", "multi-add-index", "multi-add-index-rich", "add-composite-index-rich":
		return "insert into " + tableName + " values (1,1,'a')", nil
	default:
		return "", fmt.Errorf("unsupported ddl-kind=%q", *crossDDLKind)
	}
}

func buildCrossTxnDML(tableName string) ([]string, error) {
	if isCrossTxnShape("stmtinsert1") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-index", "add-unique-index", "add-primary-key":
			return []string{
				"insert into " + tableName + " values (2,2,'b')",
			}, nil
		default:
			return nil, fmt.Errorf("txn-shape=%q is not supported for ddl-kind=%q", *crossTxnShape, *crossDDLKind)
		}
	}
	if isCrossTxnShape("stmtinsert2") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-index", "add-unique-index", "add-primary-key":
			return []string{
				"insert into " + tableName + " values (2,2,'b'),(3,3,'c')",
			}, nil
		default:
			return nil, fmt.Errorf("txn-shape=%q is not supported for ddl-kind=%q", *crossTxnShape, *crossDDLKind)
		}
	}
	if isCrossTxnShape("stmtupdate1") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-index", "add-unique-index", "add-primary-key":
			return []string{
				"update " + tableName + " set b=10 where id=1",
			}, nil
		default:
			return nil, fmt.Errorf("txn-shape=%q is not supported for ddl-kind=%q", *crossTxnShape, *crossDDLKind)
		}
	}
	if isCrossTxnShape("stmtupdate2") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-index", "add-unique-index", "add-primary-key":
			return []string{
				"update " + tableName + " set b = case id when 1 then 10 when 2 then 20 end where id in (1,2)",
			}, nil
		default:
			return nil, fmt.Errorf("txn-shape=%q is not supported for ddl-kind=%q", *crossTxnShape, *crossDDLKind)
		}
	}
	if isCrossTxnShape("basicrev") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-index", "add-unique-index", "add-primary-key":
			return []string{
				"update " + tableName + " set b=10 where id=1",
				"insert into " + tableName + " values (2,2,'b')",
			}, nil
		default:
			return nil, fmt.Errorf("txn-shape=%q is not supported for ddl-kind=%q", *crossTxnShape, *crossDDLKind)
		}
	}
	if isCrossTxnShape("insert1") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-index", "add-unique-index", "add-primary-key":
			return []string{
				"insert into " + tableName + " values (2,2,'b')",
			}, nil
		default:
			return nil, fmt.Errorf("txn-shape=%q is not supported for ddl-kind=%q", *crossTxnShape, *crossDDLKind)
		}
	}
	if isCrossTxnShape("insert2") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-index", "add-unique-index", "add-primary-key":
			return []string{
				"insert into " + tableName + " values (2,2,'b')",
				"insert into " + tableName + " values (3,3,'c')",
			}, nil
		default:
			return nil, fmt.Errorf("txn-shape=%q is not supported for ddl-kind=%q", *crossTxnShape, *crossDDLKind)
		}
	}
	if isCrossTxnShape("update1") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-index", "add-unique-index", "add-primary-key":
			return []string{
				"update " + tableName + " set b=10 where id=1",
			}, nil
		default:
			return nil, fmt.Errorf("txn-shape=%q is not supported for ddl-kind=%q", *crossTxnShape, *crossDDLKind)
		}
	}
	if isCrossTxnShape("update2") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-index", "add-unique-index", "add-primary-key":
			return []string{
				"update " + tableName + " set b=10 where id=1",
				"update " + tableName + " set b=20 where id=2",
			}, nil
		default:
			return nil, fmt.Errorf("txn-shape=%q is not supported for ddl-kind=%q", *crossTxnShape, *crossDDLKind)
		}
	}
	if isCrossTxnShape("mixed3") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-generated-index-rich", "add-virtual-generated-index-rich":
			return []string{
				"delete from " + tableName + " where id=3",
				"insert into " + tableName + " (id, b, pad) values (4,4,'dddd')",
				"update " + tableName + " set b=10, pad='zzz' where id=1",
			}, nil
		case "add-index", "add-unique-index", "add-primary-key":
			return []string{
				"delete from " + tableName + " where id=3",
				"insert into " + tableName + " values (4,4,'dddd')",
				"update " + tableName + " set b=10 where id=1",
			}, nil
		default:
			return nil, fmt.Errorf("txn-shape=%q is not supported for ddl-kind=%q", *crossTxnShape, *crossDDLKind)
		}
	}
	if isCrossTxnShape("mixed4") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-generated-index-rich", "add-virtual-generated-index-rich":
			return []string{
				"delete from " + tableName + " where id=3",
				"insert into " + tableName + " (id, b, pad) values (4,4,'dddd')",
				"update " + tableName + " set b=10, pad='zzz' where id=1",
				"update " + tableName + " set b=20, pad='yy' where id=2",
			}, nil
		default:
			return nil, fmt.Errorf("txn-shape=%q is not supported for ddl-kind=%q", *crossTxnShape, *crossDDLKind)
		}
	}
	if isCrossTxnShape("fanout3") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-generated-index-rich", "add-virtual-generated-index-rich":
			return []string{
				"insert into " + tableName + " (id, b, pad) values (4,4,'dddd')",
				"update " + tableName + " set b=10, pad='zzz' where id=1",
				"update " + tableName + " set b=20, pad='yy' where id=2",
			}, nil
		case "multi-add-index-rich", "add-composite-index-rich":
			return []string{
				"insert into " + tableName + " values (4,4,'dddd')",
				"update " + tableName + " set b=10, pad='zzz' where id=1",
				"update " + tableName + " set b=20, pad='yy' where id=2",
			}, nil
		case "add-index", "add-unique-index", "add-primary-key":
			return []string{
				"insert into " + tableName + " values (4,4,'dddd')",
				"update " + tableName + " set b=10 where id=1",
				"update " + tableName + " set b=20 where id=2",
			}, nil
		default:
			return nil, fmt.Errorf("txn-shape=%q is not supported for ddl-kind=%q", *crossTxnShape, *crossDDLKind)
		}
	}
	switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
	case "add-generated-index":
		return []string{
			"insert into " + tableName + " (id, b, pad) values (2,2,'b')",
			"update " + tableName + " set b=10 where id=1",
		}, nil
	case "add-generated-index-rich":
		return []string{
			"insert into " + tableName + " (id, b, pad) values (2,2,'bb')",
			"update " + tableName + " set b=10, pad='zzz' where id=1",
		}, nil
	case "add-virtual-generated-index-rich":
		return []string{
			"insert into " + tableName + " (id, b, pad) values (2,2,'bb')",
			"update " + tableName + " set b=10, pad='zzz' where id=1",
		}, nil
	case "multi-add-index-rich":
		return []string{
			"insert into " + tableName + " values (2,2,'bb')",
			"update " + tableName + " set b=10, pad='zzz' where id=1",
		}, nil
	case "add-composite-index-rich":
		return []string{
			"insert into " + tableName + " values (2,2,'bb')",
			"update " + tableName + " set b=10, pad='zzz' where id=1",
		}, nil
	case "add-index", "add-unique-index", "add-primary-key", "multi-add-index":
		return []string{
			"insert into " + tableName + " values (2,2,'b')",
			"update " + tableName + " set b=10 where id=1",
		}, nil
	default:
		return nil, fmt.Errorf("unsupported ddl-kind=%q", *crossDDLKind)
	}
}

func buildCrossSuccessOracle(tableName string) (string, []string, error) {
	if isCrossTxnShape("stmtinsert1") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-index", "add-unique-index", "add-primary-key":
			return "select id, b from " + tableName + " order by id",
				[]string{"1:1", "2:2"},
				nil
		default:
			return "", nil, fmt.Errorf("txn-shape=%q is not supported for ddl-kind=%q", *crossTxnShape, *crossDDLKind)
		}
	}
	if isCrossTxnShape("stmtinsert2") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-index", "add-unique-index", "add-primary-key":
			return "select id, b from " + tableName + " order by id",
				[]string{"1:1", "2:2", "3:3"},
				nil
		default:
			return "", nil, fmt.Errorf("txn-shape=%q is not supported for ddl-kind=%q", *crossTxnShape, *crossDDLKind)
		}
	}
	if isCrossTxnShape("stmtupdate1") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-index", "add-unique-index", "add-primary-key":
			return "select id, b from " + tableName + " order by id",
				[]string{"1:10"},
				nil
		default:
			return "", nil, fmt.Errorf("txn-shape=%q is not supported for ddl-kind=%q", *crossTxnShape, *crossDDLKind)
		}
	}
	if isCrossTxnShape("stmtupdate2") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-index", "add-unique-index", "add-primary-key":
			return "select id, b from " + tableName + " order by id",
				[]string{"1:10", "2:20"},
				nil
		default:
			return "", nil, fmt.Errorf("txn-shape=%q is not supported for ddl-kind=%q", *crossTxnShape, *crossDDLKind)
		}
	}
	if isCrossTxnShape("basicrev") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-index", "add-unique-index", "add-primary-key":
			return "select id, b from " + tableName + " order by id",
				[]string{"1:10", "2:2"},
				nil
		default:
			return "", nil, fmt.Errorf("txn-shape=%q is not supported for ddl-kind=%q", *crossTxnShape, *crossDDLKind)
		}
	}
	if isCrossTxnShape("insert1") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-index", "add-unique-index", "add-primary-key":
			return "select id, b from " + tableName + " order by id",
				[]string{"1:1", "2:2"},
				nil
		default:
			return "", nil, fmt.Errorf("txn-shape=%q is not supported for ddl-kind=%q", *crossTxnShape, *crossDDLKind)
		}
	}
	if isCrossTxnShape("insert2") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-index", "add-unique-index", "add-primary-key":
			return "select id, b from " + tableName + " order by id",
				[]string{"1:1", "2:2", "3:3"},
				nil
		default:
			return "", nil, fmt.Errorf("txn-shape=%q is not supported for ddl-kind=%q", *crossTxnShape, *crossDDLKind)
		}
	}
	if isCrossTxnShape("update1") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-index", "add-unique-index", "add-primary-key":
			return "select id, b from " + tableName + " order by id",
				[]string{"1:10"},
				nil
		default:
			return "", nil, fmt.Errorf("txn-shape=%q is not supported for ddl-kind=%q", *crossTxnShape, *crossDDLKind)
		}
	}
	if isCrossTxnShape("update2") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-index", "add-unique-index", "add-primary-key":
			return "select id, b from " + tableName + " order by id",
				[]string{"1:10", "2:20"},
				nil
		default:
			return "", nil, fmt.Errorf("txn-shape=%q is not supported for ddl-kind=%q", *crossTxnShape, *crossDDLKind)
		}
	}
	if isCrossTxnShape("mixed3") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-generated-index-rich", "add-virtual-generated-index-rich":
			return "select id, b, pad, g from " + tableName + " order by id",
				[]string{"1:10:zzz:13", "2:2:bb:4", "4:4:dddd:8"},
				nil
		case "add-index", "add-unique-index", "add-primary-key":
			return "select id, b from " + tableName + " order by id",
				[]string{"1:10", "2:2", "4:4"},
				nil
		default:
			return "", nil, fmt.Errorf("txn-shape=%q is not supported for ddl-kind=%q", *crossTxnShape, *crossDDLKind)
		}
	}
	if isCrossTxnShape("mixed4") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-generated-index-rich", "add-virtual-generated-index-rich":
			return "select id, b, pad, g from " + tableName + " order by id",
				[]string{"1:10:zzz:13", "2:20:yy:22", "4:4:dddd:8"},
				nil
		default:
			return "", nil, fmt.Errorf("txn-shape=%q is not supported for ddl-kind=%q", *crossTxnShape, *crossDDLKind)
		}
	}
	if isCrossTxnShape("fanout3") {
		switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
		case "add-generated-index-rich", "add-virtual-generated-index-rich":
			return "select id, b, pad, g from " + tableName + " order by id",
				[]string{"1:10:zzz:13", "2:20:yy:22", "3:3:ccc:6", "4:4:dddd:8"},
				nil
		case "multi-add-index-rich", "add-composite-index-rich":
			return "select id, b, pad from " + tableName + " order by id",
				[]string{"1:10:zzz", "2:20:yy", "3:3:ccc", "4:4:dddd"},
				nil
		case "add-index", "add-unique-index", "add-primary-key":
			return "select id, b from " + tableName + " order by id",
				[]string{"1:10", "2:20", "3:3", "4:4"},
				nil
		default:
			return "", nil, fmt.Errorf("txn-shape=%q is not supported for ddl-kind=%q", *crossTxnShape, *crossDDLKind)
		}
	}
	switch strings.ToLower(strings.TrimSpace(*crossDDLKind)) {
	case "add-generated-index":
		return "select id, b, g from " + tableName + " order by id", []string{"1:10:11", "2:2:3"}, nil
	case "add-generated-index-rich":
		return "select id, b, pad, g from " + tableName + " order by id", []string{"1:10:zzz:13", "2:2:bb:4"}, nil
	case "add-virtual-generated-index-rich":
		return "select id, b, pad, g from " + tableName + " order by id", []string{"1:10:zzz:13", "2:2:bb:4"}, nil
	case "multi-add-index-rich":
		return "select id, b, pad from " + tableName + " order by id", []string{"1:10:zzz", "2:2:bb"}, nil
	case "add-composite-index-rich":
		return "select id, b, pad from " + tableName + " order by id", []string{"1:10:zzz", "2:2:bb"}, nil
	case "add-index", "add-unique-index", "add-primary-key", "multi-add-index":
		return "select id, b from " + tableName + " order by id", []string{"1:10", "2:2"}, nil
	default:
		return "", nil, fmt.Errorf("unsupported ddl-kind=%q", *crossDDLKind)
	}
}

func compareCrossOrderedQuery(ctx context.Context, db *sql.DB, indexSQL, tableSQL string) error {
	indexRows, err := fetchCrossRows(ctx, db, indexSQL)
	if err != nil {
		return err
	}
	tableRows, err := fetchCrossRows(ctx, db, tableSQL)
	if err != nil {
		return err
	}
	if strings.Join(indexRows, ",") != strings.Join(tableRows, ",") {
		return fmt.Errorf("index_rows=%v table_rows=%v", indexRows, tableRows)
	}
	return nil
}

func fetchCrossRows(ctx context.Context, db *sql.DB, query string) ([]string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	raw := make([]sql.RawBytes, len(cols))
	dest := make([]any, len(cols))
	for i := range raw {
		dest[i] = &raw[i]
	}

	var out []string
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		parts := make([]string, len(cols))
		for i := range raw {
			parts[i] = string(raw[i])
		}
		out = append(out, strings.Join(parts, ":"))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func sleepCross(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isCrossTxnShape(want string) bool {
	return strings.EqualFold(strings.TrimSpace(*crossTxnShape), want)
}

func isCrossAutocommitStmtShape() bool {
	return isCrossTxnShape("stmtinsert1") || isCrossTxnShape("stmtinsert2") || isCrossTxnShape("stmtupdate1") || isCrossTxnShape("stmtupdate2")
}

func init() {
	log.SetOutput(os.Stdout)
}
