package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type workerResult struct {
	id          int
	lastSuccess int
	terminalErr string
}

func mustExec(db *sql.DB, query string, args ...any) {
	if _, err := db.Exec(query, args...); err != nil {
		log.Fatalf("exec %q: %v", query, err)
	}
}

func payload(id, version int) string {
	return fmt.Sprintf("id=%d/version=%d/%s", id, version, strings.Repeat("x", 1024))
}

func updatePair(ctx context.Context, db *sql.DB, id, version int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	u := int64(version)*1_000_000 + int64(id)
	value := payload(id, version)
	if _, err = tx.ExecContext(ctx, "UPDATE ai_txn_atomic_fault.ai_txn_atomic_left SET u=?, version=?, payload=? WHERE id=?", u, version, value, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE ai_txn_atomic_fault.ai_txn_atomic_right SET u=?, version=?, payload=? WHERE id=?", u, version, value, id); err != nil {
		return err
	}
	return tx.Commit()
}

func queryInt(db *sql.DB, query string) int {
	var value int
	if err := db.QueryRow(query).Scan(&value); err != nil {
		log.Fatalf("query %q: %v", query, err)
	}
	return value
}

func adminCheck(db *sql.DB, table string) error {
	rows, err := db.Query("ADMIN CHECK TABLE " + table)
	if err != nil {
		return err
	}
	return rows.Close()
}

func main() {
	var dsn string
	var workers int
	var versions int
	var startDelay time.Duration
	flag.StringVar(&dsn, "dsn", os.Getenv("AI_TXN_DSN"), "go-sql-driver DSN")
	flag.IntVar(&workers, "workers", 24, "independent logical rows")
	flag.IntVar(&versions, "versions", 200, "maximum committed updates per row")
	flag.DurationVar(&startDelay, "start-delay", 5*time.Second, "delay before workload starts")
	flag.Parse()
	if dsn == "" {
		log.Fatal("-dsn or AI_TXN_DSN is required")
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(workers + 8)
	db.SetConnMaxLifetime(2 * time.Minute)
	if err = db.Ping(); err != nil {
		log.Fatal(err)
	}

	mustExec(db, "CREATE DATABASE IF NOT EXISTS ai_txn_atomic_fault")
	mustExec(db, "DROP TABLE IF EXISTS ai_txn_atomic_fault.ai_txn_atomic_left")
	mustExec(db, "DROP TABLE IF EXISTS ai_txn_atomic_fault.ai_txn_atomic_right")
	ddl := `(
		id BIGINT PRIMARY KEY,
		u BIGINT NOT NULL,
		version INT NOT NULL,
		payload TEXT NOT NULL,
		payload_len INT AS (CHAR_LENGTH(payload)) STORED,
		UNIQUE KEY uk_u(u),
		KEY idx_version(version),
		KEY idx_payload_len(payload_len)
	)`
	mustExec(db, "CREATE TABLE ai_txn_atomic_fault.ai_txn_atomic_left "+ddl)
	mustExec(db, "CREATE TABLE ai_txn_atomic_fault.ai_txn_atomic_right "+ddl)
	for i := 1; i <= workers; i++ {
		value := payload(i, 0)
		mustExec(db, "INSERT INTO ai_txn_atomic_fault.ai_txn_atomic_left(id,u,version,payload) VALUES(?,?,0,?)", i, i, value)
		mustExec(db, "INSERT INTO ai_txn_atomic_fault.ai_txn_atomic_right(id,u,version,payload) VALUES(?,?,0,?)", i, i, value)
	}

	// Natural large tables already span Regions. Explicit splits make the small probe retain that
	// production dimension without manufacturing a protocol or storage error.
	for _, table := range []string{"ai_txn_atomic_left", "ai_txn_atomic_right"} {
		if _, splitErr := db.Exec("SPLIT TABLE ai_txn_atomic_fault." + table + " BETWEEN (0) AND (1000000000) REGIONS 8"); splitErr != nil {
			log.Printf("split %s: %v", table, splitErr)
		}
	}

	var successes atomic.Int64
	results := make(chan workerResult, workers)
	log.Printf("READY workers=%d versions=%d start_delay=%s", workers, versions, startDelay)
	time.Sleep(startDelay)
	started := time.Now()
	var wg sync.WaitGroup
	for id := 1; id <= workers; id++ {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			last := 0
			for version := 1; version <= versions; version++ {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				err := updatePair(ctx, db, id, version)
				cancel()
				if err != nil {
					results <- workerResult{id: id, lastSuccess: last, terminalErr: err.Error()}
					return
				}
				last = version
				successes.Add(1)
			}
			results <- workerResult{id: id, lastSuccess: last}
		}()
	}
	wg.Wait()
	close(results)

	workerResults := make([]workerResult, 0, workers)
	for result := range results {
		workerResults = append(workerResults, result)
	}
	sort.Slice(workerResults, func(i, j int) bool { return workerResults[i].id < workerResults[j].id })
	for _, result := range workerResults {
		if result.terminalErr != "" {
			log.Printf("TERMINAL id=%d last_success=%d err=%q", result.id, result.lastSuccess, result.terminalErr)
		}
	}

	deadline := time.Now().Add(2 * time.Minute)
	for {
		err = db.Ping()
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		log.Fatalf("cluster did not recover for oracle: %v", err)
	}
	partial := queryInt(db, `
		SELECT COUNT(*) FROM (
			SELECT l.id FROM ai_txn_atomic_fault.ai_txn_atomic_left l
			LEFT JOIN ai_txn_atomic_fault.ai_txn_atomic_right r ON l.id=r.id
			WHERE r.id IS NULL OR l.version<>r.version OR l.u<>r.u
			   OR l.payload<>r.payload OR l.payload_len<>r.payload_len
			UNION ALL
			SELECT r.id FROM ai_txn_atomic_fault.ai_txn_atomic_right r
			LEFT JOIN ai_txn_atomic_fault.ai_txn_atomic_left l ON l.id=r.id
			WHERE l.id IS NULL
		) mismatches`)
	generatedMismatch := queryInt(db, `
		SELECT (SELECT COUNT(*) FROM ai_txn_atomic_fault.ai_txn_atomic_left WHERE payload_len<>CHAR_LENGTH(payload)) +
		       (SELECT COUNT(*) FROM ai_txn_atomic_fault.ai_txn_atomic_right WHERE payload_len<>CHAR_LENGTH(payload))`)
	leftTableCount := queryInt(db, "SELECT COUNT(*) FROM ai_txn_atomic_fault.ai_txn_atomic_left IGNORE INDEX(uk_u,idx_version,idx_payload_len)")
	leftIndexCount := queryInt(db, "SELECT COUNT(*) FROM ai_txn_atomic_fault.ai_txn_atomic_left FORCE INDEX(uk_u)")
	rightTableCount := queryInt(db, "SELECT COUNT(*) FROM ai_txn_atomic_fault.ai_txn_atomic_right IGNORE INDEX(uk_u,idx_version,idx_payload_len)")
	rightIndexCount := queryInt(db, "SELECT COUNT(*) FROM ai_txn_atomic_fault.ai_txn_atomic_right FORCE INDEX(uk_u)")
	leftCheck := adminCheck(db, "ai_txn_atomic_fault.ai_txn_atomic_left")
	rightCheck := adminCheck(db, "ai_txn_atomic_fault.ai_txn_atomic_right")

	red := partial != 0 || generatedMismatch != 0 || leftTableCount != leftIndexCount || rightTableCount != rightIndexCount || leftCheck != nil || rightCheck != nil
	log.Printf("ORACLE verdict=%s successes=%d duration=%s partial=%d generated_mismatch=%d left_table=%d left_index=%d right_table=%d right_index=%d left_admin=%v right_admin=%v",
		map[bool]string{true: "RED", false: "GREEN"}[red], successes.Load(), time.Since(started), partial, generatedMismatch,
		leftTableCount, leftIndexCount, rightTableCount, rightIndexCount, leftCheck, rightCheck)
	if red {
		log.Fatal(errors.New("atomic keyset or physical consistency oracle failed"))
	}
}
