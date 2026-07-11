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
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	defaultApplyWindowDSN       = "root@tcp(127.0.0.1:14000)/"
	defaultApplyWindowStatusURL = "http://127.0.0.1:18080"
)

var (
	applyWindowDSN       = flag.String("dsn", defaultApplyWindowDSN, "mysql dsn")
	applyWindowStatusURL = flag.String("status-url", defaultApplyWindowStatusURL, "TiDB status/failpoint base URL")
	applyWindowSchema    = flag.String("schema", "ai_native_issue62531_apply_window", "schema name")
	applyWindowTable     = flag.String("table", "rows", "table name")
	applyWindowRows      = flag.Int("rows", 128, "rows to preload")
	applyWindowBatchSize = flag.Int("batch-size", 32, "ddl reorg batch size")
)

func main() {
	flag.Parse()

	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db, err := sql.Open("mysql", *applyWindowDSN)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping db: %w", err)
	}

	failpointURL := strings.TrimRight(*applyWindowStatusURL, "/") + "/fail/github.com/pingcap/tidb/pkg/ddl/beforeUpdateColumnBackfillApply"
	if err := setFailpoint(ctx, failpointURL, "pause"); err != nil {
		return fmt.Errorf("enable failpoint: %w", err)
	}
	defer func() {
		_ = clearFailpoint(context.Background(), failpointURL)
	}()

	schemaTable := fmt.Sprintf("`%s`.`%s`", *applyWindowSchema, *applyWindowTable)
	if err := mustExec(ctx, db, "create database if not exists `"+*applyWindowSchema+"`"); err != nil {
		return err
	}
	if err := mustExec(ctx, db, "drop table if exists "+schemaTable); err != nil {
		return err
	}
	if err := mustExec(ctx, db, fmt.Sprintf(`create table %s (
		id int not null,
		val0 int not null,
		val1 int not null,
		padding varchar(256) not null default '',
		primary key (id)
	)`, schemaTable)); err != nil {
		return err
	}
	if err := mustExec(ctx, db, "create index val0_idx on "+schemaTable+" (val0)"); err != nil {
		return err
	}
	if err := mustExec(ctx, db, "set @@global.tidb_ddl_reorg_worker_cnt = 1"); err != nil {
		return err
	}
	if err := mustExec(ctx, db, fmt.Sprintf("set @@global.tidb_ddl_reorg_batch_size = %d", *applyWindowBatchSize)); err != nil {
		return err
	}
	if err := mustExec(ctx, db, "set @@global.tidb_enable_dist_task = off"); err != nil {
		return err
	}
	if err := mustExec(ctx, db, "set @@global.tidb_ddl_enable_fast_reorg = off"); err != nil {
		return err
	}
	if err := mustExec(ctx, db, "set @@global.tidb_ddl_reorg_max_write_speed = 0"); err != nil {
		return err
	}

	valueRows := make([]string, 0, *applyWindowRows)
	for i := 1; i <= *applyWindowRows; i++ {
		valueRows = append(valueRows, fmt.Sprintf("(%d,%d,%d,'%03d')", i, i, i, i))
	}
	if err := mustExec(ctx, db, "insert into "+schemaTable+" values "+strings.Join(valueRows, ",")); err != nil {
		return err
	}

	ddlSQL := "alter table " + schemaTable + " modify column val0 varchar(16) not null"
	ddlErrCh := make(chan error, 1)
	go func() {
		_, err := db.ExecContext(ctx, ddlSQL)
		ddlErrCh <- err
	}()

	if err := waitForPausedDDL(ddlErrCh); err != nil {
		return fmt.Errorf("wait paused ddl: %w", err)
	}

	deleteVals := make([]string, 0, *applyWindowBatchSize)
	for i := 1; i <= *applyWindowBatchSize; i++ {
		deleteVals = append(deleteVals, fmt.Sprintf("%d", i))
	}
	deleteSQL := "delete from " + schemaTable + " where val0 in (" + strings.Join(deleteVals, ",") + ")"
	if _, err := db.ExecContext(ctx, deleteSQL); err != nil {
		return fmt.Errorf("delete during apply window failed: %w", err)
	}

	if err := clearFailpoint(ctx, failpointURL); err != nil {
		return fmt.Errorf("disable failpoint: %w", err)
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

	if err := expectCount(ctx, db, "select count(*) from "+schemaTable, 96); err != nil {
		return err
	}
	if err := expectCount(ctx, db, "select count(*) from "+schemaTable+" where val0 = '1'", 0); err != nil {
		return err
	}
	if err := expectCount(ctx, db, "select count(*) from "+schemaTable+" where val0 in ('33','64','96','128')", 4); err != nil {
		return err
	}
	if err := expectCount(ctx, db, "select count(*) from "+schemaTable+" ignore index(val0_idx) where val0 in ('33','64','96','128')", 4); err != nil {
		return err
	}

	fmt.Printf("GREEN table=%s rows=%d batch=%d\n", schemaTable, *applyWindowRows, *applyWindowBatchSize)
	return nil
}

func waitForPausedDDL(ddlErrCh <-chan error) error {
	select {
	case err := <-ddlErrCh:
		if err != nil {
			return fmt.Errorf("ddl returned early with error: %w", err)
		}
		return fmt.Errorf("ddl finished before pause could be inferred")
	case <-time.After(2 * time.Second):
		fmt.Println("PAUSED inferred_by_runtime=true wait=2s")
		return nil
	}
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

func expectCount(ctx context.Context, db *sql.DB, sqlText string, want int64) error {
	var got int64
	if err := db.QueryRowContext(ctx, sqlText).Scan(&got); err != nil {
		return fmt.Errorf("%s: %w", sqlText, err)
	}
	if got != want {
		return fmt.Errorf("%s: got %d want %d", sqlText, got, want)
	}
	return nil
}

func init() {
	http.DefaultClient.Timeout = 10 * time.Second
	if _, err := url.Parse(defaultApplyWindowStatusURL); err != nil {
		panic(err)
	}
}
