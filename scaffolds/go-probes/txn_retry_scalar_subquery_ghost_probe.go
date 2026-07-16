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

func values(db *sql.DB, table string) (string, error) {
	rows, err := db.Query("SELECT id,u,v FROM ai_txn_retry_scalar_ghost." + table + " ORDER BY id")
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
		"DROP DATABASE IF EXISTS ai_txn_retry_scalar_ghost",
		"CREATE DATABASE ai_txn_retry_scalar_ghost",
		"CREATE TABLE ai_txn_retry_scalar_ghost.target(id INT PRIMARY KEY, u INT NOT NULL UNIQUE, v INT NOT NULL)",
		"CREATE TABLE ai_txn_retry_scalar_ghost.control(id INT PRIMARY KEY, u INT NOT NULL UNIQUE, v INT NOT NULL)",
		"CREATE TABLE ai_txn_retry_scalar_ghost.src(id INT PRIMARY KEY, next_u INT NOT NULL)",
		"INSERT INTO ai_txn_retry_scalar_ghost.src VALUES(1,200)",
		"INSERT INTO ai_txn_retry_scalar_ghost.target VALUES(1,10,10),(2,20,20)",
		"INSERT INTO ai_txn_retry_scalar_ghost.control VALUES(1,10,10),(2,20,20),(3,200,999)",
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

	updateSQL := `UPDATE ai_txn_retry_scalar_ghost.target d
		JOIN ai_txn_retry_scalar_ghost.src src ON src.id=1
		SET d.u=IF(d.id=1,100,src.next_u+SLEEP(3)*0),
			d.v=(SELECT SUM(s.v) FROM ai_txn_retry_scalar_ghost.target s)+d.id
		WHERE d.id IN (1,2)`
	type terminal struct {
		affected int64
		err      error
	}
	done := make(chan terminal, 1)
	go func() {
		result, execErr := txA.ExecContext(ctx, updateSQL)
		var affected int64 = -1
		if execErr == nil {
			affected, _ = result.RowsAffected()
		}
		done <- terminal{affected: affected, err: execErr}
	}()

	sleepWitness := false
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var state sql.NullString
		var info sql.NullString
		var elapsed int
		queryErr := db.QueryRowContext(ctx, "SELECT STATE,INFO,TIME FROM information_schema.processlist WHERE ID=?", connectionID).Scan(&state, &info, &elapsed)
		if queryErr == nil && strings.Contains(strings.ToLower(info.String), "update ai_txn_retry_scalar_ghost.target") && elapsed >= 1 {
			sleepWitness = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !sleepWitness {
		log.Fatal("INVALID: target statement never reached the sleep scheduling window")
	}
	log.Printf("WITNESS first_attempt_in_user_sleep connection_id=%d", connectionID)

	txB, err := b.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	if _, err = txB.ExecContext(ctx, "UPDATE ai_txn_retry_scalar_ghost.src SET next_u=300 WHERE id=1"); err != nil {
		log.Fatalf("competitor source update: %v", err)
	}
	if _, err = txB.ExecContext(ctx, "INSERT INTO ai_txn_retry_scalar_ghost.target VALUES(3,200,999)"); err != nil {
		log.Fatalf("competitor unique-key insert: %v", err)
	}
	if err = txB.Commit(); err != nil {
		log.Fatalf("competitor commit: %v", err)
	}
	log.Printf("WITNESS competitor_committed src.next_u=300 target_row=(3,200,999)")

	term := <-done
	commitErr := error(nil)
	if term.err == nil {
		commitErr = txA.Commit()
	}
	log.Printf("TERMINAL update_err=%v affected=%d commit_err=%v", term.err, term.affected, commitErr)

	controlSQL := `UPDATE ai_txn_retry_scalar_ghost.control d
		JOIN ai_txn_retry_scalar_ghost.src src ON src.id=1
		SET d.u=IF(d.id=1,100,src.next_u),
			d.v=(SELECT SUM(s.v) FROM ai_txn_retry_scalar_ghost.control s)+d.id
		WHERE d.id IN (1,2)`
	_, controlErr := db.ExecContext(ctx, controlSQL)
	targetValues, targetErr := values(db, "target")
	controlValues, controlReadErr := values(db, "control")
	adminTarget, adminTargetErr := db.QueryContext(ctx, "ADMIN CHECK TABLE ai_txn_retry_scalar_ghost.target")
	if adminTargetErr == nil {
		adminTargetErr = adminTarget.Close()
	}
	log.Printf("ORACLE target_err=%v target=%s control_exec=%v control_read=%v control=%s admin=%v", targetErr, targetValues, controlErr, controlReadErr, controlValues, adminTargetErr)

	if term.err == nil && commitErr == nil && controlErr == nil && targetErr == nil && controlReadErr == nil && targetValues != controlValues {
		log.Fatalf("VERDICT RED retry=%s one_shot=%s", targetValues, controlValues)
	}
	if term.err != nil || commitErr != nil {
		log.Printf("VERDICT GREEN_SAFE_ERROR")
		return
	}
	log.Printf("VERDICT GREEN rows=%s", targetValues)
}
