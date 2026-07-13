package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type result struct {
	Contaminated int  `json:"dedup_on_after_clear"`
	Control      int  `json:"dedup_off_after_clear"`
	Red          bool `json:"wrong_result_red"`
}

func mustExec(ctx context.Context, conn *sql.Conn, query string) {
	if _, err := conn.ExecContext(ctx, query); err != nil {
		panic(fmt.Sprintf("%s: %v", query, err))
	}
}

func queryPrepared(ctx context.Context, conn *sql.Conn, query string) int {
	stmt, err := conn.PrepareContext(ctx, query)
	if err != nil {
		panic(err)
	}
	defer stmt.Close()
	var got int
	if err := stmt.QueryRowContext(ctx).Scan(&got); err != nil {
		panic(err)
	}
	return got
}

func main() {
	if len(os.Args) != 2 {
		panic("usage: prepare_dedup_staleness_repro <dsn>")
	}
	db, err := sql.Open("mysql", os.Args[1])
	if err != nil {
		panic(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	mustExec(ctx, conn, "drop database if exists ai_native_prepare_dedup")
	mustExec(ctx, conn, "create database ai_native_prepare_dedup")
	defer conn.ExecContext(context.Background(), "drop database if exists ai_native_prepare_dedup")
	mustExec(ctx, conn, "use ai_native_prepare_dedup")
	mustExec(ctx, conn, "set @@tidb_enable_cache_prepare_stmt = 1")
	defer conn.ExecContext(context.Background(), "set @@tidb_enable_cache_prepare_stmt = 0")
	mustExec(ctx, conn, "create table t (id int primary key, v int)")
	mustExec(ctx, conn, "insert into t values (1, 1)")
	time.Sleep(1500 * time.Millisecond)

	const query = "select v from t where id = 1"
	mustExec(ctx, conn, "set @@tidb_read_staleness = -1")
	_ = queryPrepared(ctx, conn, query)
	mustExec(ctx, conn, "set @@tidb_read_staleness = ''")
	defer conn.ExecContext(context.Background(), "set @@tidb_read_staleness = ''")
	mustExec(ctx, conn, "update t set v = 2 where id = 1")

	contaminated := queryPrepared(ctx, conn, query)
	mustExec(ctx, conn, "set @@tidb_enable_cache_prepare_stmt = 0")
	control := queryPrepared(ctx, conn, query)
	mustExec(ctx, conn, "set @@tidb_read_staleness = ''")
	mustExec(ctx, conn, "drop database if exists ai_native_prepare_dedup")

	out := result{Contaminated: contaminated, Control: control, Red: contaminated != control && control == 2}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		panic(err)
	}
	if out.Red {
		os.Exit(2)
	}
}
