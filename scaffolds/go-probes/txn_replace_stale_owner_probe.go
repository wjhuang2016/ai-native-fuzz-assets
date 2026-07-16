package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type row struct {
	id      int
	u1      int
	u2      int
	payload string
}

func readRows(db *sql.DB, suffix string) ([]row, error) {
	rows, err := db.Query("SELECT id,u1,u2,payload FROM ai_txn_replace_stale_owner.t " + suffix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []row
	for rows.Next() {
		var value row
		if err = rows.Scan(&value.id, &value.u1, &value.u2, &value.payload); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func rowString(rows []row) string {
	parts := make([]string, 0, len(rows))
	for _, value := range rows {
		parts = append(parts, fmt.Sprintf("(%d,%d,%d,%s)", value.id, value.u1, value.u2, value.payload))
	}
	return strings.Join(parts, ",")
}

func main() {
	var dsn string
	flag.StringVar(&dsn, "dsn", os.Getenv("AI_TXN_DSN"), "go-sql-driver DSN")
	flag.Parse()
	if dsn == "" {
		log.Fatal("-dsn or AI_TXN_DSN is required")
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(8)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	setup := []string{
		"DROP DATABASE IF EXISTS ai_txn_replace_stale_owner",
		"CREATE DATABASE ai_txn_replace_stale_owner",
		"CREATE TABLE ai_txn_replace_stale_owner.t(id INT PRIMARY KEY, u1 INT NOT NULL UNIQUE, u2 INT NOT NULL UNIQUE, payload VARCHAR(32))",
		"INSERT INTO ai_txn_replace_stale_owner.t VALUES(1,10,100,'old')",
	}
	for _, statement := range setup {
		if _, err = db.ExecContext(ctx, statement); err != nil {
			log.Fatalf("setup %q: %v", statement, err)
		}
	}

	a, err := db.Conn(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer a.Close()
	b, err := db.Conn(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer b.Close()
	if _, err = a.ExecContext(ctx, "SET transaction_isolation='REPEATABLE-READ'"); err != nil {
		log.Fatal(err)
	}
	if _, err = a.ExecContext(ctx, "SET tidb_txn_mode='pessimistic'"); err != nil {
		log.Fatal(err)
	}

	txA, err := a.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer txA.Rollback()
	var snapshot row
	if err = txA.QueryRowContext(ctx, "SELECT id,u1,u2,payload FROM ai_txn_replace_stale_owner.t WHERE u1=10").Scan(&snapshot.id, &snapshot.u1, &snapshot.u2, &snapshot.payload); err != nil {
		log.Fatalf("A pin snapshot: %v", err)
	}
	log.Printf("WITNESS A_snapshot=%s", rowString([]row{snapshot}))

	txB, err := b.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	if _, err = txB.ExecContext(ctx, "UPDATE ai_txn_replace_stale_owner.t SET u1=30,u2=300,payload='moved' WHERE id=1"); err != nil {
		log.Fatalf("B move old owner: %v", err)
	}
	if _, err = txB.ExecContext(ctx, "INSERT INTO ai_txn_replace_stale_owner.t VALUES(2,10,200,'owner-u1'),(3,40,100,'owner-u2')"); err != nil {
		log.Fatalf("B install split owners: %v", err)
	}
	if err = txB.Commit(); err != nil {
		log.Fatalf("B commit: %v", err)
	}
	currentAfterB, err := readRows(db, "ORDER BY id")
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("WITNESS B_committed=%s", rowString(currentAfterB))

	result, replaceErr := txA.ExecContext(ctx, "REPLACE INTO ai_txn_replace_stale_owner.t(id,u1,u2,payload) VALUES(4,10,100,'replace')")
	var affected int64 = -1
	if replaceErr == nil {
		affected, _ = result.RowsAffected()
	}
	commitErr := txA.Commit()
	log.Printf("TERMINAL replace_err=%v affected=%d commit_err=%v", replaceErr, affected, commitErr)

	tableRows, tableErr := readRows(db, "IGNORE INDEX(u1,u2) ORDER BY id")
	index1Rows, index1Err := readRows(db, "FORCE INDEX(u1) ORDER BY id")
	index2Rows, index2Err := readRows(db, "FORCE INDEX(u2) ORDER BY id")
	adminRows, adminErr := db.QueryContext(ctx, "ADMIN CHECK TABLE ai_txn_replace_stale_owner.t")
	if adminErr == nil {
		adminErr = adminRows.Close()
	}
	log.Printf("ORACLE table_err=%v table=%s index1_err=%v index1=%s index2_err=%v index2=%s admin=%v", tableErr, rowString(tableRows), index1Err, rowString(index1Rows), index2Err, rowString(index2Rows), adminErr)

	expected := "(1,30,300,moved),(4,10,100,replace)"
	red := replaceErr == nil && commitErr == nil && (tableErr != nil || index1Err != nil || index2Err != nil || adminErr != nil || rowString(tableRows) != expected || rowString(index1Rows) != expected || rowString(index2Rows) != expected)
	if red {
		log.Fatalf("VERDICT RED expected=%s", expected)
	}
	if replaceErr != nil || commitErr != nil {
		log.Printf("VERDICT GREEN_SAFE_ERROR")
		return
	}
	log.Printf("VERDICT GREEN expected=%s", expected)
}
