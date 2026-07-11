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
	defaultDSN        = "root@tcp(localhost:4000)/"
	defaultKubeconfig = "/Users/bba/pc/kubeconfig.yml"
	defaultNamespace  = "testbed-tps-8220955-1-213"
	defaultPDPod      = "tc-pd-0"

	schemaName = "ai_native_pd_probe"
	tableName  = "rows"
)

var (
	dsn            = flag.String("dsn", defaultDSN, "mysql dsn")
	kubeconfig     = flag.String("kubeconfig", defaultKubeconfig, "kubeconfig path")
	namespace      = flag.String("namespace", defaultNamespace, "kubernetes namespace")
	pdPod          = flag.String("pd-pod", defaultPDPod, "pd pod name")
	faultPods      = flag.String("fault-pods", "", "comma-separated pod sequence to delete; defaults to repeating pd-pod")
	targetRows     = flag.Int("rows", 800000, "rows to prefill before add index")
	splitRegions   = flag.Int("regions", 256, "number of regions to split the table into")
	distTask       = flag.String("dist-task", "on", "set global tidb_enable_dist_task to on/off before running the probe")
	fastReorg      = flag.String("fast-reorg", "on", "set global tidb_ddl_enable_fast_reorg to on/off before running the probe")
	pdBounces      = flag.Int("pd-bounces", 2, "how many times to bounce the pd pod while ddl is running")
	faultMinRows   = flag.Int64("fault-min-row-count", 0, "minimum ddl row_count before the first pd bounce")
	faultMinRun    = flag.Duration("fault-min-running", 0, "minimum time the ddl must stay in the active fault window before the first pd bounce")
	faultSchema    = flag.String("fault-schema-state", "write reorganization", "required schema_state before the first pd bounce; empty disables the check")
	bounceInterval = flag.Duration("bounce-interval", 6*time.Second, "interval between pd bounces after the previous pod becomes ready")
	waitPodReady   = flag.Bool("wait-pod-ready", true, "wait for each deleted pod to become ready before moving to the next fault")
	ddlShape       = flag.String("ddl-shape", "single", "ddl shape: single or multi")
	timeout        = flag.Duration("timeout", 20*time.Minute, "overall probe timeout")
)

func main() {
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	db := mustOpenDB(ctx, *dsn)
	defer db.Close()

	mustSetupWorkload(ctx, db)

	ddlSQL, expectedIndexes := buildDDLShape()
	ddlErrCh := make(chan error, 1)
	var ddlFinished atomic.Bool
	go func() {
		log.Printf("ddl start: %s", ddlSQL)
		start := time.Now()
		_, err := db.ExecContext(ctx, ddlSQL)
		ddlFinished.Store(true)
		if err != nil {
			ddlErrCh <- fmt.Errorf("ddl exec: %w", err)
			return
		}
		log.Printf("ddl finished successfully in %s", time.Since(start))
		ddlErrCh <- nil
	}()

	jobID, jobState, rowCount := waitForDDLJob(ctx, db, ddlSQL, &ddlFinished)
	log.Printf("observed ddl job: job_id=%d state=%s row_count=%d", jobID, jobState, rowCount)

	var touchedPods []string
	if *pdBounces > 0 && jobState == "running" {
		touchedPods = runPDBounceSchedule(ctx)
	} else if *pdBounces > 0 {
		log.Printf("skip pd bounce because ddl already reached state=%s before scheduling", jobState)
	}

	ddlErr := <-ddlErrCh
	if ddlErr != nil {
		log.Fatalf("ddl failed under pd bounce schedule: %v", ddlErr)
	}
	waitForPodsRecovery(ctx, touchedPods)

	runFinalOracles(ctx, db, expectedIndexes)
	log.Printf("probe finished cleanly")
}

func mustOpenDB(ctx context.Context, dsn string) *sql.DB {
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

func mustSetupWorkload(ctx context.Context, db *sql.DB) {
	mustExec(ctx, db, "set global tidb_enable_dist_task = "+normalizeOnOff(*distTask, "dist-task"))
	mustExec(ctx, db, "set global tidb_ddl_enable_fast_reorg = "+normalizeOnOff(*fastReorg, "fast-reorg"))
	mustExec(ctx, db, "set global tidb_ddl_reorg_worker_cnt = 1")
	mustExec(ctx, db, "set global tidb_ddl_reorg_batch_size = 32")

	mustExec(ctx, db, "create database if not exists "+schemaName)
	mustExec(ctx, db, "drop table if exists "+schemaName+"."+tableName)
	mustExec(ctx, db, fmt.Sprintf(`create table %s.%s (
		id bigint not null,
		c varchar(96) not null,
		d bigint not null,
		pad varchar(256) not null,
		primary key (id) clustered
	)`, schemaName, tableName))

	prefillRows(ctx, db, *targetRows)
	log.Printf("prefill done, row_count=%d", queryInt(ctx, db, "select count(*) from "+schemaName+"."+tableName))

	effectiveRegions := *splitRegions
	maxRegions := *targetRows / 1000
	if maxRegions < 1 {
		maxRegions = 1
	}
	if effectiveRegions > maxRegions {
		log.Printf("reducing split regions from %d to %d to satisfy split lower bound", effectiveRegions, maxRegions)
		effectiveRegions = maxRegions
	}
	mustQueryDiscard(ctx, db, fmt.Sprintf("split table %s.%s between (1) and (%d) regions %d",
		schemaName, tableName, *targetRows+1, effectiveRegions))
	log.Printf("split table requested: regions=%d", effectiveRegions)

	runBaselineOracles(ctx, db)
}

func prefillRows(ctx context.Context, db *sql.DB, rows int) {
	if rows < 1 {
		log.Fatalf("rows must be positive")
	}
	mustExec(ctx, db, fmt.Sprintf(`insert into %s.%s values (1, %s, %s, %s)`,
		schemaName, tableName, payloadExpr(1), dExpr(1), padExpr()))

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
			schemaName, tableName,
			count, count, count, count,
			padExpr(),
			schemaName, tableName,
			count, count, rows)
		mustExec(ctx, db, sqlText)
		count += inserted
		if count == rows || count%(rows/8+1) == 0 {
			log.Printf("prefill progress: %d/%d", count, rows)
		}
	}
}

func payloadExpr(id int) string {
	return fmt.Sprintf("concat('c-', lpad(cast(%d as char), 12, '0'), '-', lpad(cast(%d as char), 18, '0'))", id, id*17)
}

func dExpr(id int) string {
	return fmt.Sprintf("mod(%d * 29, 100003)", id)
}

func padExpr() string {
	return "repeat('x', 256)"
}

func buildDDLShape() (string, []string) {
	switch strings.ToLower(*ddlShape) {
	case "single":
		return fmt.Sprintf("alter table %s.%s add index idx_c(c)", schemaName, tableName), []string{"idx_c"}
	case "multi":
		return fmt.Sprintf("alter table %s.%s add unique index uk_c(c), add index idx_d(d)", schemaName, tableName), []string{"uk_c", "idx_d"}
	default:
		log.Fatalf("unsupported ddl shape: %s", *ddlShape)
		return "", nil
	}
}

func normalizeOnOff(value string, flagName string) string {
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

func runBaselineOracles(ctx context.Context, db *sql.DB) {
	log.Printf("baseline admin check start")
	mustExec(ctx, db, fmt.Sprintf("admin check table %s.%s", schemaName, tableName))
	log.Printf("baseline admin check green")
}

func waitForDDLJob(ctx context.Context, db *sql.DB, ddlSQL string, ddlFinished *atomic.Bool) (int64, string, int64) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var activeSince time.Time

	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait ddl running: %v", ctx.Err())
		case <-ticker.C:
		}

		jobID, state, schemaState, rowCount, err := latestDDLJob(ctx, db, ddlSQL)
		if err != nil {
			if isTransient(err) {
				continue
			}
			if ddlFinished.Load() {
				return 0, "synced", 0
			}
			log.Fatalf("query ddl job: %v", err)
		}
		if jobID == 0 {
			if ddlFinished.Load() {
				return 0, "synced", 0
			}
			continue
		}
		log.Printf("ddl job observed: job_id=%d state=%s schema_state=%s row_count=%d", jobID, state, schemaState, rowCount)
		if state == "running" {
			if ddlFinished.Load() {
				log.Printf("skip running observation because ddl session already returned: job_id=%d schema_state=%s row_count=%d", jobID, schemaState, rowCount)
				continue
			}
			if matchesFaultSchemaState(schemaState) {
				if activeSince.IsZero() {
					activeSince = time.Now()
					log.Printf("ddl entered active fault window: job_id=%d schema_state=%s row_count=%d", jobID, schemaState, rowCount)
				}
				activeFor := time.Since(activeSince)
				log.Printf("ddl active fault window: job_id=%d schema_state=%s row_count=%d active_for=%s", jobID, schemaState, rowCount, activeFor.Round(100*time.Millisecond))
				if rowCount >= *faultMinRows && activeFor >= *faultMinRun {
					return jobID, state, rowCount
				}
			} else if !activeSince.IsZero() {
				log.Printf("ddl left active fault window: job_id=%d schema_state=%s", jobID, schemaState)
				activeSince = time.Time{}
			}
		}
		if state == "done" || state == "synced" || state == "rollback done" || state == "cancelled" {
			return jobID, state, rowCount
		}
	}
}

func matchesFaultSchemaState(schemaState string) bool {
	required := strings.TrimSpace(*faultSchema)
	if required == "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(schemaState), required)
}

func latestDDLJob(ctx context.Context, db *sql.DB, ddlSQL string) (int64, string, string, int64, error) {
	rows, err := db.QueryContext(ctx, `
select job_id, state, schema_state, row_count
from information_schema.ddl_jobs
where db_name = ? and table_name = ? and query = ?
order by job_id desc
limit 1`, schemaName, tableName, ddlSQL)
	if err != nil {
		return 0, "", "", 0, err
	}
	defer rows.Close()

	if !rows.Next() {
		return 0, "", "", 0, rows.Err()
	}
	var (
		jobID       int64
		state       string
		schemaState string
		rowCount    int64
	)
	if err := rows.Scan(&jobID, &state, &schemaState, &rowCount); err != nil {
		return 0, "", "", 0, err
	}
	return jobID, state, schemaState, rowCount, rows.Err()
}

func runPDBounceSchedule(ctx context.Context) []string {
	pods := buildFaultPodSequence()
	for i, pod := range pods {
		bounce := i + 1
		prevUID := currentPodUID(ctx, pod)
		log.Printf("pd bounce %d/%d start: pod=%s uid=%s", bounce, len(pods), pod, prevUID)
		deletePDPod(ctx, pod)
		if *waitPodReady {
			nextUID := waitForNewReadyPod(ctx, pod, prevUID)
			log.Printf("pd bounce %d/%d done: pod=%s new_uid=%s", bounce, len(pods), pod, nextUID)
		}
		if bounce < len(pods) {
			select {
			case <-ctx.Done():
				log.Fatalf("pd bounce schedule interrupted: %v", ctx.Err())
			case <-time.After(*bounceInterval):
			}
		}
	}
	return uniquePods(pods)
}

func buildFaultPodSequence() []string {
	if strings.TrimSpace(*faultPods) != "" {
		raw := strings.Split(*faultPods, ",")
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
	pods := make([]string, 0, *pdBounces)
	for i := 0; i < *pdBounces; i++ {
		pods = append(pods, *pdPod)
	}
	return pods
}

func uniquePods(pods []string) []string {
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

func waitForPodsRecovery(ctx context.Context, pods []string) {
	for _, pod := range pods {
		waitForCurrentReadyPod(ctx, pod)
	}
}

func deletePDPod(ctx context.Context, pod string) {
	cmd := exec.CommandContext(ctx, "kubectl",
		"--kubeconfig", *kubeconfig,
		"-n", *namespace,
		"delete", "pod", pod,
		"--force",
		"--grace-period=0",
		"--wait=false",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Fatalf("delete pd pod %s: %v, output=%s", pod, err, strings.TrimSpace(string(out)))
	}
	log.Printf("delete pd pod %s ok: %s", pod, strings.TrimSpace(string(out)))
}

func waitForNewReadyPod(ctx context.Context, pod string, oldUID string) string {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait new pd pod ready: %v", ctx.Err())
		case <-ticker.C:
		}

		uid, ready, phase, err := podStatus(ctx, pod)
		if err != nil {
			continue
		}
		log.Printf("pd pod %s status: uid=%s phase=%s ready=%v", pod, uid, phase, ready)
		if uid != "" && uid != oldUID && ready && phase == "Running" {
			return uid
		}
	}
}

func waitForCurrentReadyPod(ctx context.Context, pod string) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait current pd pod ready: %v", ctx.Err())
		case <-ticker.C:
		}

		uid, ready, phase, err := podStatus(ctx, pod)
		if err != nil {
			continue
		}
		log.Printf("pd pod %s recovery status: uid=%s phase=%s ready=%v", pod, uid, phase, ready)
		if uid != "" && ready && phase == "Running" {
			return
		}
	}
}

func currentPodUID(ctx context.Context, pod string) string {
	uid, ready, phase, err := podStatus(ctx, pod)
	if err != nil {
		log.Fatalf("get current pd pod uid: %v", err)
	}
	log.Printf("current pd pod %s: uid=%s phase=%s ready=%v", pod, uid, phase, ready)
	return uid
}

func podStatus(ctx context.Context, pod string) (uid string, ready bool, phase string, err error) {
	cmd := exec.CommandContext(ctx, "kubectl",
		"--kubeconfig", *kubeconfig,
		"-n", *namespace,
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

func runFinalOracles(ctx context.Context, db *sql.DB, expectedIndexes []string) {
	log.Printf("final oracle: admin check table")
	mustExec(ctx, db, fmt.Sprintf("admin check table %s.%s", schemaName, tableName))
	for _, idx := range expectedIndexes {
		log.Printf("final oracle: admin check index %s", idx)
		mustExec(ctx, db, fmt.Sprintf("admin check index %s.%s %s", schemaName, tableName, idx))
	}

	tableCount := queryInt(ctx, db, fmt.Sprintf("select count(*) from %s.%s use index()", schemaName, tableName))
	valueCount := queryInt(ctx, db, fmt.Sprintf("select count(c) from %s.%s", schemaName, tableName))
	dCount := queryInt(ctx, db, fmt.Sprintf("select count(d) from %s.%s", schemaName, tableName))
	log.Printf("final counts: table_count=%d value_count=%d d_count=%d", tableCount, valueCount, dCount)
	if tableCount != *targetRows || valueCount != *targetRows || dCount != *targetRows {
		log.Fatalf("unexpected row count after ddl: table=%d value=%d d=%d target=%d", tableCount, valueCount, dCount, *targetRows)
	}

	showLastDDLJob(ctx, db)
}

func showLastDDLJob(ctx context.Context, db *sql.DB) {
	rows, err := db.QueryContext(ctx, "admin show ddl jobs 1")
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
	if !rows.Next() {
		return
	}
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
	log.Printf("last ddl job: %s", strings.Join(parts, " | "))
}

func mustExec(ctx context.Context, db *sql.DB, sqlText string) {
	if _, err := db.ExecContext(ctx, sqlText); err != nil {
		log.Fatalf("exec failed: %s: %v", sqlText, err)
	}
}

func mustQueryDiscard(ctx context.Context, db *sql.DB, sqlText string) {
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

func queryInt(ctx context.Context, db *sql.DB, sqlText string) int {
	var value int
	if err := db.QueryRowContext(ctx, sqlText).Scan(&value); err != nil {
		log.Fatalf("query int failed: %s: %v", sqlText, err)
	}
	return value
}

func isTransient(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "information schema is changed") ||
		strings.Contains(msg, "invalid connection") ||
		strings.Contains(msg, "server closed") ||
		strings.Contains(msg, "context deadline exceeded")
}
