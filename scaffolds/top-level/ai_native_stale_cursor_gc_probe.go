// Command ai_native_stale_cursor_gc_probe opens a real MySQL protocol cursor
// on a stale TiDB snapshot and keeps it alive for GC ownership observation.
package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"flag"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	mysqlcursor "github.com/YangKeao/go-mysql-driver"
)

func main() {
	host := flag.String("host", "127.0.0.1", "TiDB host")
	port := flag.Int("port", 4000, "TiDB port")
	user := flag.String("user", "root", "TiDB user")
	password := flag.String("password", "", "TiDB password")
	database := flag.String("database", "ai_native_stale_cursor_probe", "dedicated probe database")
	rows := flag.Int("rows", 20000, "number of rows")
	valueSize := flag.Int("value-size", 4096, "payload bytes per row")
	splitRegions := flag.Int("split-regions", 0, "pre-split the table into this many Regions")
	scanConcurrency := flag.Int("scan-concurrency", 0, "session tidb_distsql_scan_concurrency; zero keeps the default")
	fetchSize := flag.Int("fetch-size", 1, "rows per COM_STMT_FETCH")
	prefetch := flag.Int("prefetch", 0, "rows to fetch before the hold window")
	overwriteProcessInfo := flag.Bool("overwrite-process-info", false, "issue an extra SET after opening the cursor")
	hold := flag.Duration("hold", 60*time.Second, "time to keep the cursor open before fetching")
	flag.Parse()

	baseDSN := fmt.Sprintf("%s:%s@tcp(%s:%d)/", *user, *password, *host, *port)
	db, err := sql.Open("mysql-with-cursor", baseDSN)
	must(err)
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	mustExec(ctx, db, "DROP DATABASE IF EXISTS `"+*database+"`")
	mustExec(ctx, db, "CREATE DATABASE `"+*database+"`")
	mustExec(ctx, db, "USE `"+*database+"`")
	mustExec(ctx, db, "CREATE TABLE t(id BIGINT PRIMARY KEY, v LONGTEXT, note VARCHAR(16))")
	if *splitRegions > 0 {
		mustExec(ctx, db, fmt.Sprintf(
			"SPLIT TABLE t BETWEEN (0) AND (%d) REGIONS %d", *rows+1, *splitRegions,
		))
	}

	valueA := strings.Repeat("A", *valueSize)
	const batchSize = 200
	for start := 1; start <= *rows; start += batchSize {
		end := min(start+batchSize, *rows+1)
		values := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*2)
		for id := start; id < end; id++ {
			values = append(values, "(?, ?, 'A')")
			args = append(args, id, valueA)
		}
		mustExec(ctx, db, "INSERT INTO t VALUES "+strings.Join(values, ","), args...)
	}

	snapshotTS := currentTS(ctx, db)
	var snapshotTime string
	must(db.QueryRowContext(ctx,
		"SELECT DATE_FORMAT(TIDB_PARSE_TSO(?), '%Y-%m-%d %H:%i:%s.%f')", snapshotTS,
	).Scan(&snapshotTime))

	mustExec(ctx, db, "UPDATE t SET v=REPEAT('B', ?), note='B'", *valueSize)
	currentAfterB := currentTS(ctx, db)
	var tableID int64
	must(db.QueryRowContext(ctx,
		"SELECT tidb_table_id FROM information_schema.tables WHERE table_schema=? AND table_name='t'",
		*database,
	).Scan(&tableID))

	cursorDSN := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?fetchSize=%d",
		*user, *password, *host, *port, *database, *fetchSize)
	mysqlDriver := &mysqlcursor.MySQLDriver{}
	rawConn, err := mysqlDriver.Open(cursorDSN)
	must(err)
	conn := rawConn.(mysqlcursor.Connection)
	defer conn.Close()

	_, err = conn.ExecContext(ctx, "SET tidb_enable_lazy_cursor_fetch=ON", nil)
	must(err)
	if *scanConcurrency > 0 {
		_, err = conn.ExecContext(ctx, fmt.Sprintf("SET tidb_distsql_scan_concurrency=%d", *scanConcurrency), nil)
		must(err)
	}
	query := fmt.Sprintf(
		"/* ai-native-stale-cursor */ SELECT id, LENGTH(v), note FROM t AS OF TIMESTAMP %d ORDER BY id",
		snapshotTS,
	)
	rawStmt, err := conn.Prepare(query)
	must(err)
	stmt := rawStmt.(mysqlcursor.Statement)
	defer stmt.Close()
	cursorRows, err := stmt.QueryContext(ctx, nil)
	must(err)
	defer cursorRows.Close()

	dest := make([]driver.Value, 3)
	count := 0
	wrong := 0
	consumeOne := func() error {
		err := cursorRows.Next(dest)
		if err != nil {
			return err
		}
		count++
		length, lengthOK := asInt64(dest[1])
		note := asString(dest[2])
		if !lengthOK || length != int64(*valueSize) || note != "A" {
			wrong++
			if wrong == 1 {
				fmt.Printf("FIRST_WRONG row=%d values=%#v\n", count, dest)
			}
		}
		return nil
	}
	for count < *prefetch {
		if err := consumeOne(); err != nil {
			fmt.Printf("CURSOR_PREFETCH_ERROR fetched=%d wrong=%d error=%q\n", count, wrong, err)
			return
		}
	}
	if *overwriteProcessInfo {
		// This is useful for isolating cursor ownership from any stale process-info
		// entry left by COM_STMT_EXECUTE. Normal FETCH is the production control.
		_, err = conn.ExecContext(ctx, "SET @ai_native_cursor_owner_overwrite=1", nil)
		must(err)
	}
	fmt.Printf("CURSOR_OPEN snapshot_ts=%d snapshot_time=%s current_after_b=%d table_id=%d rows=%d regions=%d scan_concurrency=%d prefetch=%d overwrite_process_info=%t hold=%s\n",
		snapshotTS, snapshotTime, currentAfterB, tableID, *rows, *splitRegions, *scanConcurrency, count, *overwriteProcessInfo, hold.String())
	time.Sleep(*hold)

	for {
		err = consumeOne()
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Printf("CURSOR_ERROR fetched=%d wrong=%d error=%q\n", count, wrong, err)
			return
		}
	}
	fmt.Printf("CURSOR_DONE fetched=%d wrong=%d expected=%d\n", count, wrong, *rows)
}

func currentTS(ctx context.Context, db *sql.DB) uint64 {
	tx, err := db.BeginTx(ctx, nil)
	must(err)
	var ts uint64
	must(tx.QueryRowContext(ctx, "SELECT @@tidb_current_ts").Scan(&ts))
	must(tx.Commit())
	return ts
}

func mustExec(ctx context.Context, db *sql.DB, query string, args ...any) {
	_, err := db.ExecContext(ctx, query, args...)
	must(err)
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func asString(value driver.Value) string {
	switch v := value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return fmt.Sprint(v)
	}
}

func asInt64(value driver.Value) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case uint64:
		if v <= uint64(^uint64(0)>>1) {
			return int64(v), true
		}
	}
	return 0, false
}
