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
	defaultFKSetupDSN       = "root@tcp(127.0.0.1:14001)/"
	defaultFKTxnDSN         = "root@tcp(127.0.0.1:14000)/"
	defaultFKTxnStatusURL   = "http://127.0.0.1:18080"
	defaultFKSchema         = "ai_native_fk_async"
	defaultFKParentTable    = "parent"
	defaultFKChildTable     = "child"
	defaultFKTimeout        = 2 * time.Minute
	defaultFKDDLStartGap    = 500 * time.Millisecond
	defaultFKPollInterval   = 100 * time.Millisecond
	defaultFKConstraintName = "fk_val"
	defaultFKMarkerState    = "write reorganization"
	defaultFKPauseFailpoint = "10*pause"
)

var (
	fkSetupDSN     = flag.String("setup-dsn", defaultFKSetupDSN, "setup/ddl mysql dsn")
	fkTxnDSN       = flag.String("txn-dsn", defaultFKTxnDSN, "transaction mysql dsn")
	fkTxnStatusURL = flag.String("txn-status-url", defaultFKTxnStatusURL, "transaction TiDB status/failpoint base URL")
	fkFailpointURL = flag.String("failpoint-base-url", "", "optional standalone failpoint HTTP base URL; when set, failpoints are addressed as <base>/<failpoint-name>")
	fkSchema       = flag.String("schema", defaultFKSchema, "target schema")
	fkParentTable  = flag.String("parent-table", defaultFKParentTable, "parent table")
	fkChildTable   = flag.String("child-table", defaultFKChildTable, "child table")
	fkTimeout      = flag.Duration("timeout", defaultFKTimeout, "overall runtime budget")
	fkDDLStartGap  = flag.Duration("ddl-start-gap", defaultFKDDLStartGap, "delay between blocking the DML commit and submitting DDL")
	fkPollInterval = flag.Duration("poll-interval", defaultFKPollInterval, "poll interval while waiting for the FK marker")
	fkMarkerState  = flag.String("marker-state", defaultFKMarkerState, "DDL schema_state that proves foreign-key validation already passed")
	fkMetadataLock = flag.Bool("metadata-lock", false, "set tidb_enable_metadata_lock on or off during the probe")
)

const (
	fkBeforePrewriteFailpoint   = "tikvclient/beforePrewrite"
	fkAsyncCommitPauseFailpoint = "tikvclient/asyncCommitDoNothing"
)

type fkRestoreVars struct {
	mdl string
}

type fkDDLState struct {
	jobID       int64
	state       string
	schemaState string
}

type fkFinalResult struct {
	fkPublished        bool
	childRows          string
	orphanRows         int
	createStmt         string
	newInvalidInsertOK bool
}

func main() {
	flag.Parse()
	if err := runFKAsyncCommitProbe(); err != nil {
		log.Printf("probe failed: %v", err)
		os.Exit(1)
	}
}

func runFKAsyncCommitProbe() error {
	ctx, cancel := context.WithTimeout(context.Background(), *fkTimeout)
	defer cancel()

	setupDB, err := sql.Open("mysql", *fkSetupDSN)
	if err != nil {
		return fmt.Errorf("open setup db: %w", err)
	}
	defer setupDB.Close()
	setupDB.SetMaxOpenConns(8)
	setupDB.SetMaxIdleConns(8)
	if err := setupDB.PingContext(ctx); err != nil {
		return fmt.Errorf("ping setup db: %w", err)
	}

	restore, err := captureFKVars(ctx, setupDB)
	if err != nil {
		return fmt.Errorf("capture globals: %w", err)
	}
	defer restoreFKVars(context.Background(), setupDB, restore)

	if err := setupFKProbe(ctx, setupDB); err != nil {
		return fmt.Errorf("setup probe: %w", err)
	}

	beforePrewriteURL := fkFailpointEndpoint(fkBeforePrewriteFailpoint)
	asyncCommitURL := fkFailpointEndpoint(fkAsyncCommitPauseFailpoint)
	if err := putFKFailpoint(ctx, beforePrewriteURL, defaultFKPauseFailpoint); err != nil {
		return fmt.Errorf("enable beforePrewrite failpoint: %w", err)
	}
	defer func() { _ = deleteFKFailpoint(context.Background(), beforePrewriteURL) }()
	if err := putFKFailpoint(ctx, asyncCommitURL, defaultFKPauseFailpoint); err != nil {
		return fmt.Errorf("enable asyncCommitDoNothing failpoint: %w", err)
	}
	defer func() { _ = deleteFKFailpoint(context.Background(), asyncCommitURL) }()

	jobIDWatermark, err := queryFKMaxJobID(ctx, setupDB)
	if err != nil {
		return fmt.Errorf("query ddl job watermark: %w", err)
	}

	dmlErrCh := make(chan error, 1)
	ddlErrCh := make(chan error, 1)
	go runFKDML(ctx, dmlErrCh)

	if err := waitFKDMLBlocked(dmlErrCh); err != nil {
		return err
	}
	if err := sleepFK(ctx, *fkDDLStartGap); err != nil {
		return err
	}

	go runFKDDL(ctx, setupDB, ddlErrCh)

	markerSeen, markerState, ddlFinishedEarly, ddlErr, err := waitFKMarkerOrDDLFinish(ctx, setupDB, ddlErrCh, jobIDWatermark)
	if err != nil {
		return err
	}
	log.Printf("DDL_MARKER seen=%t state=%s finished_before_marker=%t", markerSeen, markerState, ddlFinishedEarly)

	if err := deleteFKFailpoint(ctx, beforePrewriteURL); err != nil {
		return fmt.Errorf("release beforePrewrite failpoint: %w", err)
	}

	if !ddlFinishedEarly {
		ddlErr = <-ddlErrCh
	}
	log.Printf("DDL_RESULT err=%v", ddlErr)

	if err := deleteFKFailpoint(ctx, asyncCommitURL); err != nil {
		return fmt.Errorf("release asyncCommitDoNothing failpoint: %w", err)
	}

	dmlErr := <-dmlErrCh
	log.Printf("DML_RESULT err=%v", dmlErr)

	result, err := runFKFinalOracle(context.Background(), setupDB)
	if err != nil {
		return fmt.Errorf("final oracle failed: %w", err)
	}
	log.Printf("FINAL ddl_succeeded=%t dml_succeeded=%t fk_published=%t orphan_rows=%d child_rows=%s new_invalid_insert_ok=%t create=%s",
		ddlErr == nil, dmlErr == nil, result.fkPublished, result.orphanRows, result.childRows, result.newInvalidInsertOK, result.createStmt)

	if ddlErr == nil && dmlErr == nil {
		switch {
		case !result.fkPublished:
			return fmt.Errorf("RED severe: DDL returned success but FK is not published")
		case result.newInvalidInsertOK:
			return fmt.Errorf("RED severe: FK is published but a fresh orphan insert still succeeds")
		case result.orphanRows > 0:
			return fmt.Errorf("RED severe: DDL success + DML success left %d orphan rows under a published FK", result.orphanRows)
		case !strings.Contains(result.childRows, "1:1"):
			return fmt.Errorf("RED severe: DML returned success but expected child row 1:1 is missing, child_rows=%s", result.childRows)
		default:
			log.Printf("GREEN both succeeded and final FK oracle stayed consistent")
			return nil
		}
	}
	if ddlErr == nil && dmlErr != nil {
		log.Printf("GREEN protective sibling: DDL succeeded and DML failed")
		return nil
	}
	if ddlErr != nil && dmlErr == nil {
		log.Printf("INFO protective sibling: DDL failed and DML succeeded")
		return nil
	}
	log.Printf("INFO both failed")
	return nil
}

func captureFKVars(ctx context.Context, db *sql.DB) (fkRestoreVars, error) {
	vars := fkRestoreVars{}
	var err error
	if vars.mdl, err = queryFKGlobalVar(ctx, db, "tidb_enable_metadata_lock"); err != nil {
		return vars, err
	}
	return vars, nil
}

func restoreFKVars(ctx context.Context, db *sql.DB, vars fkRestoreVars) {
	_ = execFK(ctx, db, fmt.Sprintf("set global tidb_enable_metadata_lock=%s", quoteFKValue(vars.mdl)))
}

func setupFKProbe(ctx context.Context, db *sql.DB) error {
	if err := execFK(ctx, db, fmt.Sprintf("set global tidb_enable_metadata_lock=%s", boolFKSetting(*fkMetadataLock))); err != nil {
		return err
	}
	if err := execFK(ctx, db, "drop database if exists "+quoteFKIdent(*fkSchema)); err != nil {
		return err
	}
	if err := execFK(ctx, db, "create database "+quoteFKIdent(*fkSchema)); err != nil {
		return err
	}
	if err := execFK(ctx, db, fmt.Sprintf("create table %s.%s (id int primary key, val int, index(val))",
		quoteFKIdent(*fkSchema), quoteFKIdent(*fkParentTable))); err != nil {
		return err
	}
	if err := execFK(ctx, db, fmt.Sprintf("create table %s.%s (id int primary key, val int, index(val))",
		quoteFKIdent(*fkSchema), quoteFKIdent(*fkChildTable))); err != nil {
		return err
	}
	return nil
}

func queryFKMaxJobID(ctx context.Context, db *sql.DB) (int64, error) {
	row := db.QueryRowContext(ctx, `
select ifnull(max(job_id), 0)
from information_schema.ddl_jobs
where db_name = ? and table_name = ?`, *fkSchema, *fkChildTable)
	var jobID int64
	if err := row.Scan(&jobID); err != nil {
		return 0, err
	}
	return jobID, nil
}

func runFKDML(ctx context.Context, errCh chan<- error) {
	db, err := sql.Open("mysql", *fkTxnDSN)
	if err != nil {
		errCh <- fmt.Errorf("open txn db: %w", err)
		return
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		errCh <- fmt.Errorf("ping txn db: %w", err)
		return
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		errCh <- fmt.Errorf("open txn conn: %w", err)
		return
	}
	defer conn.Close()

	if err := execFKConn(ctx, conn, "set @@tidb_enable_async_commit=1"); err != nil {
		errCh <- err
		return
	}
	log.Printf("DML_STEP async_commit enabled")
	if err := execFKConn(ctx, conn, "set @@tidb_enable_1pc=0"); err != nil {
		errCh <- err
		return
	}
	log.Printf("DML_STEP 1pc disabled")
	if err := execFKConn(ctx, conn, "set @@tidb_txn_mode='pessimistic'"); err != nil {
		errCh <- err
		return
	}
	log.Printf("DML_STEP txn_mode set to pessimistic")
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		errCh <- err
		return
	}
	log.Printf("DML_STEP begin tx done")
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("insert into %s.%s values (1, 1)",
		quoteFKIdent(*fkSchema), quoteFKIdent(*fkChildTable))); err != nil {
		_ = tx.Rollback()
		errCh <- err
		return
	}
	log.Printf("DML_STEP insert done, entering commit")
	errCh <- tx.Commit()
}

func runFKDDL(ctx context.Context, db *sql.DB, errCh chan<- error) {
	log.Printf("DDL_STEP submitting add foreign key")
	errCh <- execFK(ctx, db, fmt.Sprintf(
		"alter table %s.%s add constraint %s foreign key (val) references %s.%s (val)",
		quoteFKIdent(*fkSchema),
		quoteFKIdent(*fkChildTable),
		quoteFKIdent(fkConstraintName()),
		quoteFKIdent(*fkSchema),
		quoteFKIdent(*fkParentTable),
	))
}

func waitFKDMLBlocked(errCh <-chan error) error {
	select {
	case err := <-errCh:
		if err == nil {
			return fmt.Errorf("dml returned success before ddl started")
		}
		return fmt.Errorf("dml failed before ddl started: %w", err)
	case <-time.After(1 * time.Second):
		log.Printf("DML_BLOCKED confirmed by timeout")
		return nil
	}
}

func waitFKMarkerOrDDLFinish(
	ctx context.Context,
	db *sql.DB,
	ddlErrCh <-chan error,
	jobIDWatermark int64,
) (bool, string, bool, error, error) {
	ticker := time.NewTicker(*fkPollInterval)
	defer ticker.Stop()

	for {
		select {
		case err := <-ddlErrCh:
			return false, "", true, err, nil
		case <-ctx.Done():
			return false, "", false, nil, ctx.Err()
		case <-ticker.C:
			state, ok, err := lookupFKDDLState(ctx, db, jobIDWatermark)
			if err != nil {
				log.Printf("DDL_STATE poll error: %v", err)
				continue
			}
			if !ok {
				continue
			}
			log.Printf("DDL_STATE job_id=%d state=%s schema_state=%s", state.jobID, state.state, state.schemaState)
			if strings.EqualFold(strings.TrimSpace(state.schemaState), strings.TrimSpace(*fkMarkerState)) {
				return true, state.schemaState, false, nil, nil
			}
		}
	}
}

func lookupFKDDLState(ctx context.Context, db *sql.DB, jobIDWatermark int64) (fkDDLState, bool, error) {
	row := db.QueryRowContext(ctx, `
select job_id, state, schema_state
from information_schema.ddl_jobs
where db_name = ? and table_name = ? and job_id > ?
order by job_id desc
limit 1`, *fkSchema, *fkChildTable, jobIDWatermark)
	var st fkDDLState
	if err := row.Scan(&st.jobID, &st.state, &st.schemaState); err != nil {
		if err == sql.ErrNoRows {
			return fkDDLState{}, false, nil
		}
		return fkDDLState{}, false, err
	}
	return st, true, nil
}

func runFKFinalOracle(ctx context.Context, db *sql.DB) (fkFinalResult, error) {
	result := fkFinalResult{}

	createStmt, err := queryFKCreateStmt(ctx, db)
	if err != nil {
		return result, err
	}
	result.createStmt = createStmt

	fkPublished, err := queryFKPublished(ctx, db)
	if err != nil {
		return result, err
	}
	result.fkPublished = fkPublished

	childRows, err := queryFKChildRows(ctx, db)
	if err != nil {
		return result, err
	}
	result.childRows = childRows

	orphanRows, err := queryFKOrphanRows(ctx, db)
	if err != nil {
		return result, err
	}
	result.orphanRows = orphanRows

	if err := execFK(ctx, db, fmt.Sprintf("admin check table %s.%s",
		quoteFKIdent(*fkSchema), quoteFKIdent(*fkChildTable))); err != nil {
		return result, err
	}

	freshDB, err := sql.Open("mysql", *fkSetupDSN)
	if err != nil {
		return result, err
	}
	defer freshDB.Close()
	if err := freshDB.PingContext(ctx); err != nil {
		return result, err
	}
	err = execFK(ctx, freshDB, fmt.Sprintf("insert into %s.%s values (2, 2)",
		quoteFKIdent(*fkSchema), quoteFKIdent(*fkChildTable)))
	result.newInvalidInsertOK = err == nil
	if err == nil {
		_ = execFK(ctx, freshDB, fmt.Sprintf("delete from %s.%s where id = 2",
			quoteFKIdent(*fkSchema), quoteFKIdent(*fkChildTable)))
	}

	return result, nil
}

func queryFKCreateStmt(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf("show create table %s.%s",
		quoteFKIdent(*fkSchema), quoteFKIdent(*fkChildTable)))
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var tableName, createStmt string
	if !rows.Next() {
		return "", fmt.Errorf("show create table returned no rows")
	}
	if err := rows.Scan(&tableName, &createStmt); err != nil {
		return "", err
	}
	return createStmt, nil
}

func queryFKPublished(ctx context.Context, db *sql.DB) (bool, error) {
	row := db.QueryRowContext(ctx, `
select count(*)
from information_schema.referential_constraints
where constraint_schema = ? and table_name = ? and constraint_name = ?`,
		*fkSchema, *fkChildTable, fkConstraintName())
	var count int
	if err := row.Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func queryFKChildRows(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(
		"select concat_ws(':', cast(id as char), cast(val as char)) from %s.%s order by id",
		quoteFKIdent(*fkSchema), quoteFKIdent(*fkChildTable)))
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			return "", err
		}
		out = append(out, row)
	}
	return strings.Join(out, ","), rows.Err()
}

func queryFKOrphanRows(ctx context.Context, db *sql.DB) (int, error) {
	row := db.QueryRowContext(ctx, fmt.Sprintf(`
select count(*)
from %s.%s c
left join %s.%s p on c.val = p.val
where c.val is not null and p.val is null`,
		quoteFKIdent(*fkSchema), quoteFKIdent(*fkChildTable),
		quoteFKIdent(*fkSchema), quoteFKIdent(*fkParentTable)))
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func queryFKGlobalVar(ctx context.Context, db *sql.DB, name string) (string, error) {
	row := db.QueryRowContext(ctx, fmt.Sprintf("select @@global.%s", name))
	var value string
	if err := row.Scan(&value); err != nil {
		return "", err
	}
	return value, nil
}

func execFK(ctx context.Context, db *sql.DB, sqlText string) error {
	_, err := db.ExecContext(ctx, sqlText)
	return err
}

func execFKConn(ctx context.Context, conn *sql.Conn, sqlText string) error {
	_, err := conn.ExecContext(ctx, sqlText)
	return err
}

func sleepFK(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func putFKFailpoint(ctx context.Context, url, value string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, strings.NewReader(value))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %s body=%s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func fkFailpointEndpoint(name string) string {
	if base := strings.TrimRight(strings.TrimSpace(*fkFailpointURL), "/"); base != "" {
		return base + "/" + name
	}
	return strings.TrimRight(*fkTxnStatusURL, "/") + "/fail/" + name
}

func deleteFKFailpoint(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %s body=%s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func quoteFKIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

func quoteFKValue(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func boolFKSetting(v bool) string {
	if v {
		return "'ON'"
	}
	return "'OFF'"
}

func fkConstraintName() string {
	return defaultFKConstraintName
}
