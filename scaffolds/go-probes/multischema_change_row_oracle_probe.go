package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	dsn = "root@tcp(localhost:4000)/"

	schemaName   = "ai_native_ms_probe"
	tableName    = "rows"
	paddingSize  = 256
	workerCount  = 12
	shardWidth   = 1000
	prefillStart = 100001
	prefillEnd   = 160000
)

var runFor = flag.Duration("run-for", 2*time.Minute, "how long to run before stopping")

func main() {
	flag.Parse()
	db := mustOpenDB()
	defer db.Close()

	mustSetupDatabase(db)

	ctx, cancel := context.WithTimeout(context.Background(), *runFor)
	defer cancel()

	var ddlEpoch atomic.Uint64
	var wg sync.WaitGroup

	for workerID := 0; workerID < workerCount; workerID++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			dmlWorker(ctx, db, id)
		}(workerID)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		ddlWorker(ctx, db, &ddlEpoch)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		oracleWorker(ctx, db, &ddlEpoch)
	}()

	wg.Wait()
	log.Printf("probe finished cleanly after %s", *runFor)
}

func mustOpenDB() *sql.DB {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(workerCount + 8)
	db.SetMaxIdleConns(workerCount + 8)
	db.SetConnMaxLifetime(0)
	if err := db.Ping(); err != nil {
		log.Fatalf("ping db: %v", err)
	}
	return db
}

func mustSetupDatabase(db *sql.DB) {
	mustExec(db, "create database if not exists "+schemaName)
	mustExec(db, "drop table if exists "+schemaName+"."+tableName)
	mustExec(db, fmt.Sprintf(`create table %s.%s (
		id int not null,
		x int not null,
		y int not null,
		padding varchar(%d) not null default '',
		primary key (id) clustered,
		key idx_x(x),
		key idx_y(y)
	)`, schemaName, tableName, paddingSize))
	mustExec(db, "set @@global.tidb_ddl_reorg_worker_cnt = 1")
	mustExec(db, "set @@global.tidb_ddl_reorg_batch_size = 32")

	prefillRows(db)
	log.Printf("setup complete")
}

func prefillRows(db *sql.DB) {
	const batchSize = 1000
	paddingBuffer := make([]byte, paddingSize/2)
	for start := prefillStart; start <= prefillEnd; start += batchSize {
		end := start + batchSize - 1
		if end > prefillEnd {
			end = prefillEnd
		}
		valueStrings := make([]string, 0, end-start+1)
		args := make([]any, 0, (end-start+1)*4)
		for id := start; id <= end; id++ {
			rand.Read(paddingBuffer)
			valueStrings = append(valueStrings, "(?, ?, ?, ?)")
			args = append(args, id, valueForX(id), valueForY(id), hex.EncodeToString(paddingBuffer))
		}
		sqlText := fmt.Sprintf("insert into %s.%s values %s", schemaName, tableName, strings.Join(valueStrings, ","))
		mustExecArgs(db, sqlText, args...)
	}
	var cnt int
	mustQueryRow(db, "select count(*) from "+schemaName+"."+tableName).Scan(&cnt)
	log.Printf("prefill done, row_count=%d", cnt)
}

func dmlWorker(ctx context.Context, db *sql.DB, workerID int) {
	localRand := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerID)*17))
	paddingBuffer := make([]byte, paddingSize/2)
	shardStart := workerID*shardWidth + 1
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		rowCount := []int{10, 30, 50, 100}[localRand.Intn(4)]
		offsetCap := shardWidth - rowCount - 1
		if offsetCap < 1 {
			offsetCap = 1
		}
		base := shardStart + localRand.Intn(offsetCap)

		valueStrings := make([]string, 0, rowCount)
		args := make([]any, 0, rowCount*4)
		ids := make([]int, 0, rowCount)
		for i := 0; i < rowCount; i++ {
			id := base + i
			rand.Read(paddingBuffer)
			valueStrings = append(valueStrings, "(?, ?, ?, ?)")
			args = append(args, id, valueForX(id), valueForY(id), hex.EncodeToString(paddingBuffer))
			ids = append(ids, id)
		}

		insertSQL := fmt.Sprintf("insert into %s.%s values %s", schemaName, tableName, strings.Join(valueStrings, ","))
		if err := execRetryable(db, insertSQL, args...); err != nil {
			log.Fatalf("worker %d insert failed: %v", workerID, err)
		}

		time.Sleep(250 * time.Millisecond)

		deletePlaceholders := strings.Repeat("?,", len(ids))
		deleteSQL := fmt.Sprintf("delete from %s.%s where id in (%s)", schemaName, tableName, strings.TrimRight(deletePlaceholders, ","))
		deleteArgs := make([]any, len(ids))
		for i, id := range ids {
			deleteArgs[i] = id
		}
		if err := execRetryable(db, deleteSQL, deleteArgs...); err != nil {
			log.Fatalf("worker %d delete failed: %v", workerID, err)
		}

		time.Sleep(250 * time.Millisecond)
	}
}

func ddlWorker(ctx context.Context, db *sql.DB, ddlEpoch *atomic.Uint64) {
	statements := []string{
		fmt.Sprintf("alter table %s.%s change column x a smallint not null, change column y b smallint not null", schemaName, tableName),
		fmt.Sprintf("alter table %s.%s change column a x int not null, change column b y int not null", schemaName, tableName),
	}
	round := 0
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		sqlText := statements[round%len(statements)]
		log.Printf("ddl start: %s", sqlText)
		start := time.Now()
		if _, err := db.Exec(sqlText); err != nil {
			if !isRetryableSchemaErr(err) {
				log.Fatalf("ddl failed: %v", err)
			}
			log.Printf("ddl transient error: %v", err)
		} else {
			epoch := ddlEpoch.Add(1)
			log.Printf("ddl ok: round=%d epoch=%d cost=%s", round, epoch, time.Since(start))
		}
		round++
		time.Sleep(700 * time.Millisecond)
	}
}

func oracleWorker(ctx context.Context, db *sql.DB, ddlEpoch *atomic.Uint64) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	lastEpoch := uint64(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		cols, err := currentValueColumns(db)
		if err != nil {
			if isRetryableSchemaErr(err) {
				continue
			}
			log.Fatalf("oracle columns failed: %v", err)
		}

		if err := adminCheck(db); err != nil {
			if isRetryableSchemaErr(err) {
				continue
			}
			log.Fatalf("admin check failed: %v", err)
		}

		badCount, err := formulaMismatchCount(db, cols[0], cols[1])
		if err != nil {
			if isRetryableSchemaErr(err) {
				continue
			}
			log.Fatalf("formula oracle failed: %v", err)
		}
		if badCount != 0 {
			log.Fatalf("formula mismatch detected: bad_count=%d cols=%v", badCount, cols)
		}

		epoch := ddlEpoch.Load()
		if epoch != lastEpoch {
			lastEpoch = epoch
			log.Printf("oracle green after epoch=%d cols=%v", epoch, cols)
		}
	}
}

func currentValueColumns(db *sql.DB) ([2]string, error) {
	rows, err := db.Query(`
		select column_name
		from information_schema.columns
		where table_schema = ? and table_name = ? and ordinal_position in (2, 3)
		order by ordinal_position`, schemaName, tableName)
	if err != nil {
		return [2]string{}, err
	}
	defer rows.Close()

	var cols [2]string
	i := 0
	for rows.Next() {
		if i >= 2 {
			break
		}
		if err := rows.Scan(&cols[i]); err != nil {
			return [2]string{}, err
		}
		i++
	}
	if err := rows.Err(); err != nil {
		return [2]string{}, err
	}
	if i != 2 {
		return [2]string{}, fmt.Errorf("expected 2 value columns, got %d", i)
	}
	return cols, nil
}

func adminCheck(db *sql.DB) error {
	_, err := db.Exec("admin check table " + schemaName + "." + tableName)
	return err
}

func formulaMismatchCount(db *sql.DB, col1, col2 string) (int, error) {
	sqlText := fmt.Sprintf(`
		select count(*)
		from %s.%s
		where %s != mod(id, 10000) or %s != mod(id, 10000) * 2`,
		schemaName, tableName, col1, col2)
	var cnt int
	err := db.QueryRow(sqlText).Scan(&cnt)
	return cnt, err
}

func valueForX(id int) int {
	return id % 10000
}

func valueForY(id int) int {
	return (id % 10000) * 2
}

func execRetryable(db *sql.DB, sqlText string, args ...any) error {
	_, err := db.Exec(sqlText, args...)
	if err == nil || isRetryableSchemaErr(err) || isDuplicateKeyErr(err) {
		return nil
	}
	return err
}

func mustExec(db *sql.DB, sqlText string) {
	mustExecArgs(db, sqlText)
}

func mustExecArgs(db *sql.DB, sqlText string, args ...any) {
	if _, err := db.Exec(sqlText, args...); err != nil {
		log.Fatalf("exec failed: %s: %v", sqlText, err)
	}
}

func mustQueryRow(db *sql.DB, sqlText string, args ...any) *sql.Row {
	return db.QueryRow(sqlText, args...)
}

func isRetryableSchemaErr(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "public column") && strings.Contains(msg, "has changed") ||
		strings.Contains(msg, "Information schema is changed") ||
		strings.Contains(msg, "column .* has changed") ||
		strings.Contains(msg, "Unknown column")
}

func isDuplicateKeyErr(err error) bool {
	return strings.Contains(err.Error(), "Duplicate entry")
}
