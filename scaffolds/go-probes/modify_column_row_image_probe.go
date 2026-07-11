package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
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
	defaultRowImageDSN      = "root@tcp(10.2.12.57:32334)/"
	defaultRowImageSchema   = "ai_native_modify_row_image"
	defaultRowImageTable    = "rows"
	defaultRowImageWorkers  = 16
	defaultRowImageMaxValue = 1000
	paddingBytes            = 256
)

var (
	rowImageDSN      = flag.String("dsn", defaultRowImageDSN, "mysql dsn")
	rowImageDuration = flag.Duration("duration", 2*time.Minute, "how long to run the concurrent workload")
	rowImageWorkers  = flag.Int("workers", defaultRowImageWorkers, "number of concurrent DML workers")
	rowImageMaxValue = flag.Int("max-val0", defaultRowImageMaxValue, "max val0 value range")
	rowImageWithIdx  = flag.Bool("with-index", false, "create index on val0")
	rowImageDDLSleep = flag.Duration("ddl-sleep", time.Second, "delay between DDL rounds")
	rowImageDMLSleep = flag.Duration("dml-sleep", 500*time.Millisecond, "delay between insert and delete")
)

type runStats struct {
	insertOps int64
	deleteOps int64
	ddlOps    int64
}

func main() {
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *rowImageDuration)
	defer cancel()

	db := mustOpenRowImageDB(ctx)
	defer db.Close()
	mustSetupRowImage(ctx, db)

	errCh := make(chan error, *rowImageWorkers+4)
	var stats runStats
	var wg sync.WaitGroup

	for workerID := 0; workerID < *rowImageWorkers; workerID++ {
		workerDB := mustOpenRowImageDB(ctx)
		mustExecRowImage(ctx, workerDB, "set @@tidb_txn_mode = 'pessimistic'")
		wg.Add(1)
		go func(id int, db *sql.DB) {
			defer wg.Done()
			defer db.Close()
			if err := rowImageWorker(ctx, db, id, &stats); err != nil && !isExpectedStop(err, ctx) {
				select {
				case errCh <- fmt.Errorf("worker %d: %w", id, err):
				default:
				}
			}
		}(workerID, workerDB)
	}

	ddlDB := mustOpenRowImageDB(ctx)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer ddlDB.Close()
		if err := rowImageDDLWorker(ctx, ddlDB, &stats); err != nil && !isExpectedStop(err, ctx) {
			select {
			case errCh <- fmt.Errorf("ddl: %w", err):
			default:
			}
		}
	}()

	var runErr error
	select {
	case err := <-errCh:
		runErr = err
		cancel()
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) && !errors.Is(ctx.Err(), context.Canceled) {
			runErr = ctx.Err()
		}
	}

	wg.Wait()

	finalDB := mustOpenRowImageDB(context.Background())
	defer finalDB.Close()
	runFinalRowImageOracle(context.Background(), finalDB)

	log.Printf("run summary: with_index=%v insert_ops=%d delete_ops=%d ddl_ops=%d",
		*rowImageWithIdx,
		atomic.LoadInt64(&stats.insertOps),
		atomic.LoadInt64(&stats.deleteOps),
		atomic.LoadInt64(&stats.ddlOps),
	)

	if runErr != nil {
		log.Fatalf("oracle violation: %v", runErr)
	}
	log.Printf("probe finished without oracle violation after %s", *rowImageDuration)
}

func mustOpenRowImageDB(ctx context.Context) *sql.DB {
	db, err := sql.Open("mysql", *rowImageDSN)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(*rowImageWorkers + 8)
	db.SetMaxIdleConns(*rowImageWorkers + 8)
	db.SetConnMaxLifetime(0)
	for {
		if err := db.PingContext(ctx); err == nil {
			return db
		}
		if ctx.Err() != nil {
			log.Fatalf("ping db: %v", ctx.Err())
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func mustSetupRowImage(ctx context.Context, db *sql.DB) {
	mustExecRowImage(ctx, db, "create database if not exists "+defaultRowImageSchema)
	mustExecRowImage(ctx, db, "drop table if exists "+defaultRowImageSchema+"."+defaultRowImageTable)
	mustExecRowImage(ctx, db, fmt.Sprintf(`create table %s.%s (
		id int not null auto_increment,
		val0 int not null,
		val1 int not null,
		padding varchar(%d) not null default '',
		primary key (id)
	)`, defaultRowImageSchema, defaultRowImageTable, paddingBytes))
	if *rowImageWithIdx {
		mustExecRowImage(ctx, db, "create index val0_idx on "+defaultRowImageSchema+"."+defaultRowImageTable+" (val0)")
	}
	mustExecRowImage(ctx, db, "set @@global.tidb_ddl_reorg_worker_cnt = 1")
	mustExecRowImage(ctx, db, "set @@global.tidb_ddl_reorg_batch_size = 32")
	log.Printf("setup complete: with_index=%v", *rowImageWithIdx)
}

func rowImageWorker(ctx context.Context, db *sql.DB, workerID int, stats *runStats) error {
	choices := []int{10, 50, 100, 200}
	rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerID)*31))
	paddingBuffer := make([]byte, paddingBytes/2)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		rowsPerOp := choices[rng.Intn(len(choices))]
		val0Values := make([]int, rowsPerOp)
		val1Values := make([]int, rowsPerOp)
		paddingValues := make([]string, rowsPerOp)
		val0 := rng.Intn(*rowImageMaxValue)
		for i := 0; i < rowsPerOp; i++ {
			val0Values[i] = val0
			val1Values[i] = val0 * 10
			rng.Read(paddingBuffer)
			paddingValues[i] = hex.EncodeToString(paddingBuffer[:rowsPerOp/2])
			val0 = (val0 + 1) % *rowImageMaxValue
		}

		if err := rowImageInsert(ctx, db, val0Values, val1Values, paddingValues); err != nil && !isIgnorableInsertErr(err) {
			log.Printf("worker %d insert error: %v", workerID, err)
		} else {
			atomic.AddInt64(&stats.insertOps, 1)
		}
		sleepWithCtx(ctx, *rowImageDMLSleep)

		if err := rowImageDelete(ctx, db, val0Values); err != nil {
			return err
		}
		atomic.AddInt64(&stats.deleteOps, 1)
		sleepWithCtx(ctx, *rowImageDMLSleep)
	}
}

func rowImageInsert(ctx context.Context, db *sql.DB, val0Values, val1Values []int, paddingValues []string) error {
	valueStrings := make([]string, len(val0Values))
	args := make([]any, 0, len(val0Values)*3)
	for i, val0 := range val0Values {
		valueStrings[i] = "(?, ?, ?)"
		args = append(args, val0, val1Values[i], paddingValues[i])
	}
	sqlText := fmt.Sprintf("insert into %s.%s (val0, val1, padding) values %s",
		defaultRowImageSchema, defaultRowImageTable, strings.Join(valueStrings, ","))
	_, err := db.ExecContext(ctx, sqlText, args...)
	return err
}

func rowImageDelete(ctx context.Context, db *sql.DB, val0Values []int) error {
	placeholders := make([]string, len(val0Values))
	args := make([]any, len(val0Values))
	for i, val0 := range val0Values {
		placeholders[i] = "?"
		args[i] = val0
	}
	sqlText := fmt.Sprintf("delete from %s.%s where val0 in (%s)",
		defaultRowImageSchema, defaultRowImageTable, strings.Join(placeholders, ","))
	_, err := db.ExecContext(ctx, sqlText, args...)
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "public column") && strings.Contains(err.Error(), "has changed") {
		return nil
	}
	return err
}

func rowImageDDLWorker(ctx context.Context, db *sql.DB, stats *runStats) error {
	for {
		sleepWithCtx(ctx, *rowImageDDLSleep)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		for _, ddlSQL := range []string{
			fmt.Sprintf("alter table %s.%s modify column val0 bigint not null", defaultRowImageSchema, defaultRowImageTable),
			fmt.Sprintf("alter table %s.%s modify column val0 int not null", defaultRowImageSchema, defaultRowImageTable),
		} {
			start := time.Now()
			if _, err := db.ExecContext(ctx, ddlSQL); err != nil {
				log.Printf("ddl returned error after %s: %v", time.Since(start), err)
				continue
			}
			atomic.AddInt64(&stats.ddlOps, 1)
			log.Printf("ddl success after %s: %s", time.Since(start), ddlSQL)
		}
	}
}

func runFinalRowImageOracle(ctx context.Context, db *sql.DB) {
	if *rowImageWithIdx {
		mustExecRowImage(ctx, db, "admin check table "+defaultRowImageSchema+"."+defaultRowImageTable)
	}
	mustQueryRowImage(ctx, db, "select count(*) from "+defaultRowImageSchema+"."+defaultRowImageTable)
	mustQueryRowImage(ctx, db, "select count(*) from "+defaultRowImageSchema+"."+defaultRowImageTable+" where val0 is null")
}

func mustExecRowImage(ctx context.Context, db *sql.DB, sqlText string) {
	if _, err := db.ExecContext(ctx, sqlText); err != nil {
		log.Fatalf("exec %q: %v", sqlText, err)
	}
}

func mustQueryRowImage(ctx context.Context, db *sql.DB, sqlText string) int64 {
	var n int64
	if err := db.QueryRowContext(ctx, sqlText).Scan(&n); err != nil {
		log.Fatalf("query %q: %v", sqlText, err)
	}
	return n
}

func isIgnorableInsertErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "Duplicate entry") || strings.Contains(err.Error(), "Write conflict")
}

func isExpectedStop(err error, ctx context.Context) bool {
	return err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil
}

func sleepWithCtx(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
