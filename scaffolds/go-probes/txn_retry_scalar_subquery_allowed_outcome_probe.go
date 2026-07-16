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

func readAllowedOutcomeRows(db *sql.DB) (string, error) {
	rows, err := db.Query("SELECT id,u,v FROM ai_txn_retry_scalar_allowed.target ORDER BY id")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id, uniqueValue, value int
		if err = rows.Scan(&id, &uniqueValue, &value); err != nil {
			return "", err
		}
		result = append(result, fmt.Sprintf("(%d,%d,%d)", id, uniqueValue, value))
	}
	return strings.Join(result, ","), rows.Err()
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
	for _, statement := range []string{
		"DROP DATABASE IF EXISTS ai_txn_retry_scalar_allowed",
		"CREATE DATABASE ai_txn_retry_scalar_allowed",
		"CREATE TABLE ai_txn_retry_scalar_allowed.target(id INT PRIMARY KEY, u INT NOT NULL UNIQUE, v INT NOT NULL)",
		"CREATE TABLE ai_txn_retry_scalar_allowed.src(id INT PRIMARY KEY, next_u INT NOT NULL)",
		"INSERT INTO ai_txn_retry_scalar_allowed.src VALUES(1,200)",
		"INSERT INTO ai_txn_retry_scalar_allowed.target VALUES(1,10,10),(2,20,20)",
	} {
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
	if _, err = a.ExecContext(ctx, "SET transaction_isolation='READ-COMMITTED'"); err != nil {
		log.Fatal(err)
	}
	if _, err = a.ExecContext(ctx, "SET tidb_txn_mode='pessimistic'"); err != nil {
		log.Fatal(err)
	}
	var connectionID int64
	if err = a.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&connectionID); err != nil {
		log.Fatal(err)
	}
	txA, err := a.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer txA.Rollback()

	updateSQL := `UPDATE /* ai_native_rc_allowed_one_attempt */ ai_txn_retry_scalar_allowed.target d
		JOIN ai_txn_retry_scalar_allowed.src src ON src.id=1
		SET d.u=IF(d.id=1,100,src.next_u),
			d.v=(SELECT SUM(s.v+SLEEP(3)*0) FROM ai_txn_retry_scalar_allowed.target s)+d.id
		WHERE d.id IN (1,2)`
	done := make(chan error, 1)
	go func() {
		_, execErr := txA.ExecContext(ctx, updateSQL)
		done <- execErr
	}()

	sleepWitness := false
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var info sql.NullString
		var elapsed int
		queryErr := db.QueryRowContext(ctx,
			"SELECT INFO,TIME FROM information_schema.processlist WHERE ID=?", connectionID,
		).Scan(&info, &elapsed)
		if queryErr == nil && strings.Contains(info.String, "ai_native_rc_allowed_one_attempt") && elapsed >= 1 {
			sleepWitness = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !sleepWitness {
		log.Fatal("INVALID: scalar subquery never reached its old-snapshot scan window")
	}
	log.Printf("WITNESS scalar_scan_in_flight connection_id=%d", connectionID)

	if _, err = b.ExecContext(ctx, "UPDATE ai_txn_retry_scalar_allowed.src SET next_u=300 WHERE id=1"); err != nil {
		log.Fatalf("publisher update: %v", err)
	}
	log.Printf("WITNESS publisher_committed src.next_u=300 without installing a conflicting target key")

	updateErr := <-done
	commitErr := error(nil)
	if updateErr == nil {
		commitErr = txA.Commit()
	}
	rows, readErr := readAllowedOutcomeRows(db)
	var sourceValue int
	sourceErr := db.QueryRowContext(ctx, "SELECT next_u FROM ai_txn_retry_scalar_allowed.src WHERE id=1").Scan(&sourceValue)
	adminRows, adminErr := db.QueryContext(ctx, "ADMIN CHECK TABLE ai_txn_retry_scalar_allowed.target")
	if adminErr == nil {
		adminErr = adminRows.Close()
	}
	log.Printf("TERMINAL update_err=%v commit_err=%v rows=%s src=%d read_err=%v source_err=%v admin=%v",
		updateErr, commitErr, rows, sourceValue, readErr, sourceErr, adminErr)

	const oldOld = "(1,100,31),(2,200,32)"
	const forbiddenMixed = "(1,100,31),(2,300,32)"
	switch {
	case updateErr != nil || commitErr != nil || readErr != nil || sourceErr != nil || adminErr != nil:
		log.Fatalf("VERDICT INVALID control did not complete cleanly")
	case rows == oldOld && sourceValue == 300:
		log.Printf("VERDICT GREEN_ALLOWED_ONE_ATTEMPT old_scalar_and_old_stmt_source")
	case rows == forbiddenMixed:
		log.Fatalf("VERDICT RED_ALLOWED_OUTCOME_WITNESS retry candidate is reachable without retry")
	default:
		log.Fatalf("VERDICT INVALID unexpected rows=%s", rows)
	}
}
