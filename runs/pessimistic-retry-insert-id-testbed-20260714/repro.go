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
	Version         string     `json:"version"`
	MDLEnabled      int        `json:"mdl_enabled"`
	RetryAffected   int64      `json:"retry_affected"`
	RetryInsertID   int64      `json:"retry_insert_id"`
	ControlAffected int64      `json:"control_affected"`
	ControlInsertID int64      `json:"control_insert_id"`
	DestinationRows [][2]int64 `json:"destination_rows"`
	SinkRows        []sinkRow  `json:"sink_rows"`
}

type sinkRow struct {
	Arm        string `json:"arm"`
	ReportedID int64  `json:"reported_id"`
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	dsn := os.Getenv("AI_INSERT_ID_DSN")
	if dsn == "" {
		dsn = "root@tcp(127.0.0.1:4407)/?charset=utf8mb4&parseTime=true"
	}
	db, err := sql.Open("mysql", dsn)
	must(err)
	defer db.Close()
	db.SetMaxOpenConns(4)
	must(db.PingContext(ctx))

	setup, err := db.Conn(ctx)
	must(err)
	defer setup.Close()
	_, _ = setup.ExecContext(ctx, "drop database if exists ai_insert_id_retry_0714")
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = setup.ExecContext(cleanupCtx, "drop database if exists ai_insert_id_retry_0714")
	}()

	for _, stmt := range []string{
		"create database ai_insert_id_retry_0714",
		"create table ai_insert_id_retry_0714.src (id int primary key, explicit_id bigint, u int)",
		"create table ai_insert_id_retry_0714.gate (id int primary key)",
		"create table ai_insert_id_retry_0714.dst (id bigint auto_increment primary key, u int unique)",
		"create table ai_insert_id_retry_0714.sink (arm varchar(16) primary key, reported_id bigint)",
		"insert into ai_insert_id_retry_0714.src values (1, 42, 1)",
	} {
		_, err = setup.ExecContext(ctx, stmt)
		must(err)
	}

	var out result
	must(setup.QueryRowContext(ctx, "select version(), @@global.tidb_enable_metadata_lock").
		Scan(&out.Version, &out.MDLEnabled))

	owner, err := db.Conn(ctx)
	must(err)
	defer owner.Close()
	competitor, err := db.Conn(ctx)
	must(err)
	defer competitor.Close()
	control, err := db.Conn(ctx)
	must(err)
	defer control.Close()

	for _, conn := range []*sql.Conn{owner, competitor, control} {
		_, err = conn.ExecContext(ctx, "set tidb_txn_mode='pessimistic'")
		must(err)
		_, err = conn.ExecContext(ctx, "set transaction_isolation='READ-COMMITTED'")
		must(err)
		_, err = conn.ExecContext(ctx, "set tidb_pessimistic_txn_fair_locking=0")
		must(err)
	}

	ownerTx, err := owner.BeginTx(ctx, nil)
	must(err)
	insertSQL := `insert into ai_insert_id_retry_0714.dst(id, u)
		select explicit_id, u + sleep(8) * 0
		from ai_insert_id_retry_0714.src s
		where not exists (
			select 1 from ai_insert_id_retry_0714.gate g where g.id = s.id
		)`

	type execResult struct {
		result sql.Result
		err    error
	}
	ownerDone := make(chan execResult, 1)
	go func() {
		r, execErr := ownerTx.ExecContext(ctx, insertSQL)
		ownerDone <- execResult{result: r, err: execErr}
	}()

	time.Sleep(time.Second)
	competitorTx, err := competitor.BeginTx(ctx, nil)
	must(err)
	_, err = competitorTx.ExecContext(ctx, "insert into ai_insert_id_retry_0714.dst values (2, 1)")
	must(err)
	_, err = competitorTx.ExecContext(ctx, "insert into ai_insert_id_retry_0714.gate values (1)")
	must(err)
	must(competitorTx.Commit())

	retryResult := <-ownerDone
	must(retryResult.err)
	out.RetryAffected, err = retryResult.result.RowsAffected()
	must(err)
	out.RetryInsertID, err = retryResult.result.LastInsertId()
	must(err)
	must(ownerTx.Commit())
	_, err = owner.ExecContext(ctx,
		"insert into ai_insert_id_retry_0714.sink values ('retry', ?)", out.RetryInsertID)
	must(err)

	controlResult, err := control.ExecContext(ctx, insertSQL)
	must(err)
	out.ControlAffected, err = controlResult.RowsAffected()
	must(err)
	out.ControlInsertID, err = controlResult.LastInsertId()
	must(err)
	_, err = control.ExecContext(ctx,
		"insert into ai_insert_id_retry_0714.sink values ('control', ?)", out.ControlInsertID)
	must(err)

	rows, err := setup.QueryContext(ctx, "select id, u from ai_insert_id_retry_0714.dst order by id")
	must(err)
	for rows.Next() {
		var row [2]int64
		must(rows.Scan(&row[0], &row[1]))
		out.DestinationRows = append(out.DestinationRows, row)
	}
	must(rows.Close())

	rows, err = setup.QueryContext(ctx, "select arm, reported_id from ai_insert_id_retry_0714.sink order by arm")
	must(err)
	for rows.Next() {
		var row sinkRow
		must(rows.Scan(&row.Arm, &row.ReportedID))
		out.SinkRows = append(out.SinkRows, row)
	}
	must(rows.Close())

	encoded, err := json.MarshalIndent(out, "", "  ")
	must(err)
	fmt.Println(string(encoded))
}
