package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	defaultTiKVBounceDSN        = "root:@tcp(10.2.12.57:32334)/"
	defaultTiKVBounceKubeconfig = "/Users/bba/pc/kubeconfig.yml"
	defaultTiKVBounceNamespace  = "testbed-tps-8220955-1-213"
	defaultTiKVBouncePod        = "tc-tikv-0"

	tikvBounceSchemaName = "ai_native_ingest_live_probe"
	tikvBounceTableName  = "rows"
)

var (
	dsn3            = flag.String("dsn", defaultTiKVBounceDSN, "mysql dsn")
	kubeconfig3     = flag.String("kubeconfig", defaultTiKVBounceKubeconfig, "kubeconfig path")
	namespace3      = flag.String("namespace", defaultTiKVBounceNamespace, "kubernetes namespace")
	faultPod3       = flag.String("fault-pod", defaultTiKVBouncePod, "single tikv pod to delete when fault-pods is empty")
	faultPods3      = flag.String("fault-pods", "", "comma-separated tikv pod sequence to delete")
	faultCount3     = flag.Int("fault-count", 1, "how many times to delete fault-pod when fault-pods is empty")
	targetRows3     = flag.Int("rows", 800000, "rows to prefill before add index")
	splitRegions3   = flag.Int("regions", 256, "number of regions to split the table into")
	distTask3       = flag.String("dist-task", "off", "set global tidb_enable_dist_task to on/off before running the probe")
	fastReorg3      = flag.String("fast-reorg", "on", "set global tidb_ddl_enable_fast_reorg to on/off before running the probe")
	ddlShape3       = flag.String("ddl-shape", "single", "ddl shape: single or multi")
	faultMinRows3   = flag.Int64("fault-min-row-count", 0, "minimum ddl row_count before the first tikv bounce")
	faultMinRun3    = flag.Duration("fault-min-running", 3*time.Second, "minimum time the ddl must stay in the active fault window before the first tikv bounce")
	faultSchema3    = flag.String("fault-schema-state", "write reorganization", "required schema_state before the first tikv bounce; empty disables the check")
	bounceInterval3 = flag.Duration("bounce-interval", 6*time.Second, "interval between tikv bounces after the previous pod becomes ready")
	waitPodReady3   = flag.Bool("wait-pod-ready", true, "wait for each deleted pod to become ready before moving to the next fault")
	timeout3        = flag.Duration("timeout", 25*time.Minute, "overall probe timeout")
)

type ddlJobState3 struct {
	JobID       int64
	State       string
	SchemaState string
	RowCount    int64
}

type ddlExecResult3 struct {
	err      error
	duration time.Duration
}

func main() {
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout3)
	defer cancel()

	db := mustOpenDB3(ctx, *dsn3)
	defer db.Close()

	mustSetupWorkload3(ctx, db)

	ddlSQL, expectedIndexes := buildDDLShape3()
	jobIDWatermark := currentJobIDWatermark3(ctx, db, ddlSQL)
	ddlResultCh := make(chan ddlExecResult3, 1)
	var ddlFinished atomic.Bool
	go func() {
		start := time.Now()
		log.Printf("ddl start: %s", ddlSQL)
		_, err := db.ExecContext(ctx, ddlSQL)
		ddlFinished.Store(true)
		ddlResultCh <- ddlExecResult3{err: err, duration: time.Since(start)}
	}()

	observed := waitForDDLWindow3(ctx, db, ddlSQL, jobIDWatermark, &ddlFinished)
	log.Printf("observed ddl job: job_id=%d state=%s schema_state=%s row_count=%d", observed.JobID, observed.State, observed.SchemaState, observed.RowCount)

	touchedPods := make([]string, 0)
	if observed.State == "running" {
		touchedPods = runTiKVBounceSchedule3(ctx)
	} else {
		log.Printf("skip tikv bounce because ddl already reached state=%s before scheduling", observed.State)
	}

	terminal := waitForDDLTerminal3(ctx, db, ddlSQL, jobIDWatermark, observed.JobID)
	log.Printf("observed terminal ddl job: job_id=%d state=%s schema_state=%s row_count=%d", terminal.JobID, terminal.State, terminal.SchemaState, terminal.RowCount)

	ddlResult := <-ddlResultCh
	if ddlResult.err != nil {
		log.Printf("ddl session returned error after %s: %v", ddlResult.duration, ddlResult.err)
	} else {
		log.Printf("ddl session returned success after %s", ddlResult.duration)
	}

	waitForPodsRecovery3(ctx, touchedPods)
	runFinalOracles3(ctx, db, ddlSQL, jobIDWatermark, expectedIndexes, terminal, ddlResult.err)
	log.Printf("probe finished cleanly")
}

func mustOpenDB3(ctx context.Context, dsn string) *sql.DB {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(0)
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("ping db: %v", err)
	}
	return db
}

func mustSetupWorkload3(ctx context.Context, db *sql.DB) {
	mustExec3(ctx, db, "set global tidb_enable_dist_task = "+normalizeOnOff3(*distTask3, "dist-task"))
	mustExec3(ctx, db, "set global tidb_ddl_enable_fast_reorg = "+normalizeOnOff3(*fastReorg3, "fast-reorg"))
	mustExec3(ctx, db, "set global tidb_ddl_reorg_worker_cnt = 1")
	mustExec3(ctx, db, "set global tidb_ddl_reorg_batch_size = 32")

	mustExec3(ctx, db, "create database if not exists "+tikvBounceSchemaName)
	mustExec3(ctx, db, "drop table if exists "+tikvBounceSchemaName+"."+tikvBounceTableName)
	mustExec3(ctx, db, fmt.Sprintf(`create table %s.%s (
		id bigint not null,
		c varchar(96) not null,
		d bigint not null,
		pad varchar(256) not null,
		primary key (id) clustered
	)`, tikvBounceSchemaName, tikvBounceTableName))

	prefillRows3(ctx, db, *targetRows3)
	log.Printf("prefill done, row_count=%d", queryInt3(ctx, db, "select count(*) from "+tikvBounceSchemaName+"."+tikvBounceTableName))

	effectiveRegions := *splitRegions3
	maxRegions := *targetRows3 / 1000
	if maxRegions < 1 {
		maxRegions = 1
	}
	if effectiveRegions > maxRegions {
		log.Printf("reducing split regions from %d to %d to satisfy split lower bound", effectiveRegions, maxRegions)
		effectiveRegions = maxRegions
	}
	mustQueryDiscard3(ctx, db, fmt.Sprintf("split table %s.%s between (1) and (%d) regions %d",
		tikvBounceSchemaName, tikvBounceTableName, *targetRows3+1, effectiveRegions))
	log.Printf("split table requested: regions=%d", effectiveRegions)

	mustExec3(ctx, db, fmt.Sprintf("admin check table %s.%s", tikvBounceSchemaName, tikvBounceTableName))
}

func prefillRows3(ctx context.Context, db *sql.DB, rows int) {
	if rows < 1 {
		log.Fatalf("rows must be positive")
	}
	mustExec3(ctx, db, fmt.Sprintf(`insert into %s.%s values (1, %s, %s, %s)`,
		tikvBounceSchemaName, tikvBounceTableName, payloadExpr3(1), dExpr3(1), padExpr3()))

	count := 1
	for count < rows {
		inserted := count
		if inserted > rows-count {
			inserted = rows - count
		}
		sqlText := fmt.Sprintf(`insert into %s.%s
select
	id + %d,
	concat('c-', lpad(cast(id + %d as char), 12, '0'), '-', lpad(cast((id + %d) * 17 as char), 18, '0')),
	mod((id + %d) * 29, 100003),
	%s
from %s.%s
where id <= %d and id + %d <= %d`,
			tikvBounceSchemaName, tikvBounceTableName,
			count, count, count, count,
			padExpr3(),
			tikvBounceSchemaName, tikvBounceTableName,
			count, count, rows)
		mustExec3(ctx, db, sqlText)
		count += inserted
		if count == rows || count%(rows/8+1) == 0 {
			log.Printf("prefill progress: %d/%d", count, rows)
		}
	}
}

func buildDDLShape3() (string, []string) {
	switch strings.ToLower(*ddlShape3) {
	case "single":
		return fmt.Sprintf("alter table %s.%s add index idx_c(c)", tikvBounceSchemaName, tikvBounceTableName), []string{"idx_c"}
	case "multi":
		return fmt.Sprintf("alter table %s.%s add unique index uk_c(c), add index idx_d(d)", tikvBounceSchemaName, tikvBounceTableName), []string{"uk_c", "idx_d"}
	default:
		log.Fatalf("unsupported ddl shape: %s", *ddlShape3)
		return "", nil
	}
}

func normalizeOnOff3(value string, flagName string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "1", "true":
		return "on"
	case "off", "0", "false":
		return "off"
	default:
		log.Fatalf("unsupported %s value: %s", flagName, value)
		return ""
	}
}

func currentJobIDWatermark3(ctx context.Context, db *sql.DB, ddlSQL string) int64 {
	rows, err := db.QueryContext(ctx, `
select ifnull(max(job_id), 0)
from information_schema.ddl_jobs
where db_name = ? and table_name = ? and query = ?`, tikvBounceSchemaName, tikvBounceTableName, ddlSQL)
	if err != nil {
		log.Fatalf("query ddl watermark: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return 0
	}
	var maxJobID int64
	if err := rows.Scan(&maxJobID); err != nil {
		log.Fatalf("scan ddl watermark: %v", err)
	}
	log.Printf("ddl job watermark before submit: %d", maxJobID)
	return maxJobID
}

func waitForDDLWindow3(ctx context.Context, db *sql.DB, ddlSQL string, jobIDWatermark int64, ddlFinished *atomic.Bool) ddlJobState3 {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var activeSince time.Time

	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait ddl running: %v", ctx.Err())
		case <-ticker.C:
		}

		st, err := latestDDLJob3(ctx, db, ddlSQL, jobIDWatermark)
		if err != nil {
			if isTransient3(err) {
				continue
			}
			if ddlFinished.Load() {
				log.Fatalf("ddl finished before any observable job state: %v", err)
			}
			log.Fatalf("query ddl job: %v", err)
		}
		if st.JobID == 0 {
			if ddlFinished.Load() {
				log.Fatalf("ddl finished before any observable job row")
			}
			continue
		}
		log.Printf("ddl job observed: job_id=%d state=%s schema_state=%s row_count=%d", st.JobID, st.State, st.SchemaState, st.RowCount)
		if st.State == "running" {
			if ddlFinished.Load() {
				continue
			}
			if matchesFaultSchemaState3(st.SchemaState) {
				if activeSince.IsZero() {
					activeSince = time.Now()
					log.Printf("ddl entered active fault window: job_id=%d schema_state=%s row_count=%d", st.JobID, st.SchemaState, st.RowCount)
				}
				activeFor := time.Since(activeSince)
				log.Printf("ddl active fault window: job_id=%d schema_state=%s row_count=%d active_for=%s", st.JobID, st.SchemaState, st.RowCount, activeFor.Round(100*time.Millisecond))
				if st.RowCount >= *faultMinRows3 && activeFor >= *faultMinRun3 {
					return st
				}
			} else if !activeSince.IsZero() {
				activeSince = time.Time{}
			}
		}
		if isTerminalState3(st.State) {
			return st
		}
	}
}

func waitForDDLTerminal3(ctx context.Context, db *sql.DB, ddlSQL string, jobIDWatermark int64, jobID int64) ddlJobState3 {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait ddl terminal: %v", ctx.Err())
		case <-ticker.C:
		}

		st, err := latestDDLJob3(ctx, db, ddlSQL, jobIDWatermark)
		if err != nil {
			if isTransient3(err) {
				continue
			}
			log.Fatalf("query ddl terminal: %v", err)
		}
		if st.JobID == 0 {
			continue
		}
		if jobID != 0 && st.JobID != jobID {
			log.Printf("ignore different ddl job while waiting terminal: want=%d got=%d state=%s schema_state=%s", jobID, st.JobID, st.State, st.SchemaState)
			continue
		}
		log.Printf("ddl terminal wait sees: job_id=%d state=%s schema_state=%s row_count=%d", st.JobID, st.State, st.SchemaState, st.RowCount)
		if isTerminalState3(st.State) {
			return st
		}
	}
}

func matchesFaultSchemaState3(schemaState string) bool {
	required := strings.TrimSpace(*faultSchema3)
	if required == "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(schemaState), required)
}

func latestDDLJob3(ctx context.Context, db *sql.DB, ddlSQL string, jobIDWatermark int64) (ddlJobState3, error) {
	rows, err := db.QueryContext(ctx, `
select job_id, state, schema_state, row_count
from information_schema.ddl_jobs
where db_name = ? and table_name = ? and query = ?
  and job_id > ?
order by
	case when job_type like 'add index%' then 0 else 1 end,
	case when lower(schema_state) = 'none' then 1 else 0 end,
	row_count desc,
	job_id desc
limit 1`, tikvBounceSchemaName, tikvBounceTableName, ddlSQL, jobIDWatermark)
	if err != nil {
		return ddlJobState3{}, err
	}
	defer rows.Close()

	if !rows.Next() {
		return ddlJobState3{}, rows.Err()
	}

	var st ddlJobState3
	if err := rows.Scan(&st.JobID, &st.State, &st.SchemaState, &st.RowCount); err != nil {
		return ddlJobState3{}, err
	}
	return st, rows.Err()
}

func runTiKVBounceSchedule3(ctx context.Context) []string {
	pods := buildFaultPodSequence3()
	for i, pod := range pods {
		bounce := i + 1
		prevUID := currentPodUID3(ctx, pod)
		log.Printf("tikv bounce %d/%d start: pod=%s uid=%s", bounce, len(pods), pod, prevUID)
		deletePod3(ctx, pod)
		if *waitPodReady3 {
			nextUID := waitForNewReadyPod3(ctx, pod, prevUID)
			log.Printf("tikv bounce %d/%d done: pod=%s new_uid=%s", bounce, len(pods), pod, nextUID)
		}
		if bounce < len(pods) {
			select {
			case <-ctx.Done():
				log.Fatalf("tikv bounce schedule interrupted: %v", ctx.Err())
			case <-time.After(*bounceInterval3):
			}
		}
	}
	return uniquePods3(pods)
}

func buildFaultPodSequence3() []string {
	if strings.TrimSpace(*faultPods3) != "" {
		raw := strings.Split(*faultPods3, ",")
		pods := make([]string, 0, len(raw))
		for _, pod := range raw {
			pod = strings.TrimSpace(pod)
			if pod != "" {
				pods = append(pods, pod)
			}
		}
		if len(pods) == 0 {
			log.Fatalf("fault-pods did not contain any pod names")
		}
		return pods
	}
	if strings.TrimSpace(*faultPod3) == "" {
		log.Fatalf("fault-pod cannot be empty when fault-pods is not set")
	}
	pods := make([]string, 0, *faultCount3)
	for i := 0; i < *faultCount3; i++ {
		pods = append(pods, *faultPod3)
	}
	return pods
}

func uniquePods3(pods []string) []string {
	seen := make(map[string]struct{}, len(pods))
	out := make([]string, 0, len(pods))
	for _, pod := range pods {
		if _, ok := seen[pod]; ok {
			continue
		}
		seen[pod] = struct{}{}
		out = append(out, pod)
	}
	return out
}

func waitForPodsRecovery3(ctx context.Context, pods []string) {
	for _, pod := range pods {
		waitForCurrentReadyPod3(ctx, pod)
	}
}

func deletePod3(ctx context.Context, pod string) {
	cmd := exec.CommandContext(ctx, "kubectl",
		"--kubeconfig", *kubeconfig3,
		"-n", *namespace3,
		"delete", "pod", pod,
		"--force",
		"--grace-period=0",
		"--wait=false",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Fatalf("delete tikv pod %s: %v, output=%s", pod, err, strings.TrimSpace(string(out)))
	}
	log.Printf("delete tikv pod %s ok: %s", pod, strings.TrimSpace(string(out)))
}

func waitForNewReadyPod3(ctx context.Context, pod string, oldUID string) string {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait new tikv pod ready: %v", ctx.Err())
		case <-ticker.C:
		}
		uid, ready, phase, err := podStatus3(ctx, pod)
		if err != nil {
			continue
		}
		log.Printf("tikv pod %s status: uid=%s phase=%s ready=%v", pod, uid, phase, ready)
		if uid != "" && uid != oldUID && ready && phase == "Running" {
			return uid
		}
	}
}

func waitForCurrentReadyPod3(ctx context.Context, pod string) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait current tikv pod ready: %v", ctx.Err())
		case <-ticker.C:
		}
		uid, ready, phase, err := podStatus3(ctx, pod)
		if err != nil {
			continue
		}
		log.Printf("tikv pod %s recovery status: uid=%s phase=%s ready=%v", pod, uid, phase, ready)
		if uid != "" && ready && phase == "Running" {
			return
		}
	}
}

func currentPodUID3(ctx context.Context, pod string) string {
	uid, ready, phase, err := podStatus3(ctx, pod)
	if err != nil {
		log.Fatalf("get current tikv pod uid: %v", err)
	}
	log.Printf("current tikv pod %s: uid=%s phase=%s ready=%v", pod, uid, phase, ready)
	return uid
}

func podStatus3(ctx context.Context, pod string) (uid string, ready bool, phase string, err error) {
	cmd := exec.CommandContext(ctx, "kubectl",
		"--kubeconfig", *kubeconfig3,
		"-n", *namespace3,
		"get", "pod", pod,
		"-o", "jsonpath={.metadata.uid}|{.status.phase}|{.status.containerStatuses[0].ready}",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", false, "", err
	}
	parts := strings.Split(strings.TrimSpace(string(out)), "|")
	if len(parts) != 3 {
		return "", false, "", fmt.Errorf("unexpected pod status output: %s", strings.TrimSpace(string(out)))
	}
	ready, err = strconv.ParseBool(parts[2])
	if err != nil {
		return "", false, "", fmt.Errorf("parse ready from %q: %w", parts[2], err)
	}
	return parts[0], ready, parts[1], nil
}

func runFinalOracles3(ctx context.Context, db *sql.DB, ddlSQL string, jobIDWatermark int64, expectedIndexes []string, st ddlJobState3, ddlErr error) {
	indexes := loadIndexNames3(ctx, db)
	log.Printf("visible indexes after terminal state: %v", indexes)

	allPresent := true
	for _, idx := range expectedIndexes {
		if !containsString3(indexes, idx) {
			allPresent = false
			break
		}
	}

	switch st.State {
	case "done", "synced":
		if !allPresent {
			log.Fatalf("state-schema mismatch: ddl is %s but expected indexes are missing, indexes=%v", st.State, indexes)
		}
		if ddlErr != nil {
			log.Fatalf("state-session mismatch: ddl terminal=%s but session returned error=%v", st.State, ddlErr)
		}
	case "rollback done", "cancelled":
		if allPresent {
			log.Fatalf("state-schema mismatch: ddl is %s but expected indexes are still visible", st.State)
		}
		if ddlErr == nil {
			log.Printf("warning: ddl terminal=%s but session returned success", st.State)
		}
	default:
		log.Fatalf("unexpected terminal state: %s", st.State)
	}

	mustExec3(ctx, db, fmt.Sprintf("admin check table %s.%s", tikvBounceSchemaName, tikvBounceTableName))
	tableCount := queryInt3(ctx, db, fmt.Sprintf("select count(*) from %s.%s use index()", tikvBounceSchemaName, tikvBounceTableName))
	valueCount := queryInt3(ctx, db, fmt.Sprintf("select count(c) from %s.%s", tikvBounceSchemaName, tikvBounceTableName))
	dCount := queryInt3(ctx, db, fmt.Sprintf("select count(d) from %s.%s", tikvBounceSchemaName, tikvBounceTableName))
	log.Printf("final counts: table_count=%d value_count=%d d_count=%d", tableCount, valueCount, dCount)
	if tableCount != *targetRows3 || valueCount != *targetRows3 || dCount != *targetRows3 {
		log.Fatalf("unexpected row count after ddl: table=%d value=%d d=%d target=%d", tableCount, valueCount, dCount, *targetRows3)
	}

	if allPresent {
		for _, idx := range expectedIndexes {
			mustExec3(ctx, db, fmt.Sprintf("admin check index %s.%s %s", tikvBounceSchemaName, tikvBounceTableName, idx))
		}
	}

	showLastDDLJob3(ctx, db, ddlSQL, jobIDWatermark, st.JobID)
}

func loadIndexNames3(ctx context.Context, db *sql.DB) []string {
	rows, err := db.QueryContext(ctx, `
select distinct index_name
from information_schema.statistics
where table_schema = ? and table_name = ?
order by index_name`, tikvBounceSchemaName, tikvBounceTableName)
	if err != nil {
		log.Fatalf("load indexes: %v", err)
	}
	defer rows.Close()

	var indexes []string
	for rows.Next() {
		var idx string
		if err := rows.Scan(&idx); err != nil {
			log.Fatalf("scan index name: %v", err)
		}
		indexes = append(indexes, idx)
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("iterate indexes: %v", err)
	}
	return indexes
}

func containsString3(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func showLastDDLJob3(ctx context.Context, db *sql.DB, ddlSQL string, jobIDWatermark int64, jobID int64) {
	rows, err := db.QueryContext(ctx, "admin show ddl jobs 5")
	if err != nil {
		log.Printf("admin show ddl jobs failed: %v", err)
		return
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		log.Printf("ddl jobs columns failed: %v", err)
		return
	}
	for rows.Next() {
		values := make([]any, len(cols))
		valuePtrs := make([]any, len(cols))
		for i := range values {
			valuePtrs[i] = &values[i]
		}
		if err := rows.Scan(valuePtrs...); err != nil {
			log.Printf("ddl jobs scan failed: %v", err)
			return
		}
		parts := make([]string, 0, len(values))
		for _, v := range values {
			switch x := v.(type) {
			case []byte:
				parts = append(parts, string(x))
			default:
				parts = append(parts, fmt.Sprint(x))
			}
		}
		line := strings.Join(parts, " | ")
		if strings.Contains(line, ddlSQL) || (jobID > jobIDWatermark && strings.Contains(line, fmt.Sprintf("%d", jobID))) {
			log.Printf("matching ddl history: %s", line)
		}
	}
}

func mustExec3(ctx context.Context, db *sql.DB, sqlText string) {
	if _, err := db.ExecContext(ctx, sqlText); err != nil {
		log.Fatalf("exec failed: %s: %v", sqlText, err)
	}
}

func mustQueryDiscard3(ctx context.Context, db *sql.DB, sqlText string) {
	rows, err := db.QueryContext(ctx, sqlText)
	if err != nil {
		log.Fatalf("query failed: %s: %v", sqlText, err)
	}
	defer rows.Close()
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("query iteration failed: %s: %v", sqlText, err)
	}
}

func queryInt3(ctx context.Context, db *sql.DB, sqlText string) int {
	var value int
	if err := db.QueryRowContext(ctx, sqlText).Scan(&value); err != nil {
		log.Fatalf("query int failed: %s: %v", sqlText, err)
	}
	return value
}

func isTerminalState3(state string) bool {
	switch state {
	case "done", "synced", "rollback done", "cancelled":
		return true
	default:
		return false
	}
}

func isTransient3(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "information schema is changed") ||
		strings.Contains(msg, "invalid connection") ||
		strings.Contains(msg, "server closed") ||
		strings.Contains(msg, "context deadline exceeded")
}

func payloadExpr3(id int) string {
	return fmt.Sprintf("concat('c-', lpad(cast(%d as char), 12, '0'), '-', lpad(cast(%d as char), 18, '0'))", id, id*17)
}

func dExpr3(id int) string {
	return fmt.Sprintf("mod(%d * 29, 100003)", id)
}

func padExpr3() string {
	return "repeat('x', 256)"
}
