package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	defaultDSN             = "root@tcp(10.2.12.57:32334)/"
	defaultKubeconfig      = "/Users/bba/pc/kubeconfig.yml"
	defaultNamespace       = "testbed-tps-8220955-1-213"
	defaultTiDBPod         = "tc-tidb-0"
	defaultStatusURL       = "http://10.2.12.57:31188"
	defaultSchemaName      = "ai_native_owner_probe"
	defaultTableName       = "rows"
	defaultJobCheckEvery   = 1 * time.Second
	defaultPodCheckEvery   = 2 * time.Second
	defaultDDLWaitTimeout  = 12 * time.Minute
	defaultMySQLRetryDelay = 2 * time.Second
)

var (
	dsn              = flag.String("dsn", defaultDSN, "mysql dsn")
	kubeconfig       = flag.String("kubeconfig", defaultKubeconfig, "kubeconfig path")
	namespace        = flag.String("namespace", defaultNamespace, "kubernetes namespace")
	tidbPod          = flag.String("tidb-pod", defaultTiDBPod, "tidb pod name")
	statusURL        = flag.String("status-url", defaultStatusURL, "tidb status base url")
	targetRows       = flag.Int("rows", 600000, "rows to prefill before add index")
	splitRegions     = flag.Int("regions", 256, "number of regions to split table into")
	ddlShape         = flag.String("ddl-shape", "single", "ddl shape: single or multi")
	distTask         = flag.Bool("dist-task", true, "whether to enable tidb_enable_dist_task")
	fastReorg        = flag.Bool("fast-reorg", true, "whether to enable tidb_ddl_enable_fast_reorg")
	faultMinRowCount = flag.Int64("fault-min-row-count", 0, "minimum ddl row_count before injecting first fault")
	faultMinRun      = flag.Duration("fault-min-running", 0, "minimum time the ddl must stay in the active fault window before the first owner fault")
	faultSchema      = flag.String("fault-schema-state", "write reorganization", "required schema_state before the first owner fault; empty disables the check")
	faultMode        = flag.String("fault-mode", "delete-pod", "fault mode: delete-pod, delete-owner-pod, or resign-owner")
	faultCount       = flag.Int("fault-count", 1, "number of owner faults to inject while ddl is running")
	faultStartDelay  = flag.Duration("fault-start-delay", 0, "wait after observing running state before first fault")
	faultInterval    = flag.Duration("fault-interval", 6*time.Second, "wait between faults after service recovers")
	controlMode      = flag.String("control-mode", "none", "control mode: none, pause-resume, cancel, or pause-resume-cancel")
	controlDelay     = flag.Duration("control-delay", 0, "extra delay before control action after the control window is observed")
	controlPause     = flag.Duration("control-pause", 5*time.Second, "how long to keep the ddl paused before resuming")
	controlMinRows   = flag.Int64("control-min-row-count", 0, "minimum ddl row_count before applying control action")
	dmlWorkers       = flag.Int("dml-workers", 0, "number of concurrent delete-reinsert workers")
	dmlSlots         = flag.Int("dml-slots", 16, "number of hot-key slots reused by concurrent dml workers")
	dmlSleep         = flag.Duration("dml-sleep", 40*time.Millisecond, "sleep between delete and reinsert steps")
	ddlTimeout       = flag.Duration("ddl-timeout", defaultDDLWaitTimeout, "timeout waiting for ddl terminal state")
	totalTimeout     = flag.Duration("timeout", 20*time.Minute, "overall probe timeout")
)

func main() {
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *totalTimeout)
	defer cancel()

	db := mustOpenDB(ctx, *dsn)
	defer db.Close()

	ddlSQL, expectedIndexes := buildDDLShape()
	jobIDWatermark := currentJobIDWatermark(ctx, db)
	hotSlots := mustSetupWorkload(ctx, db)

	var dmlWG sync.WaitGroup
	var stopDML chan struct{}
	if len(hotSlots) > 0 {
		stopDML = make(chan struct{})
		startHotDMLWorkers(ctx, &dmlWG, stopDML, hotSlots)
	}

	go submitDDLAndIgnoreConnBreak(ctx, ddlSQL)

	st := waitForRunningJob(ctx, db, ddlSQL, jobIDWatermark)
	log.Printf("observed running ddl job: job_id=%d row_count=%d", st.JobID, st.RowCount)

	injectOwnerFaults(ctx, db, ddlSQL, jobIDWatermark)
	if *controlMode != "none" {
		applyControl(ctx, ddlSQL, jobIDWatermark, st.JobID)
	}

	state := waitForTerminalState(ctx, db, ddlSQL, jobIDWatermark)
	log.Printf("observed terminal ddl state: %s", state.State)

	if stopDML != nil {
		close(stopDML)
		dmlWG.Wait()
	}

	runTerminalOracles(ctx, db, ddlSQL, expectedIndexes, hotSlots, state, jobIDWatermark)
	log.Printf("probe finished cleanly")
}

type ddlState struct {
	JobID       int64
	State       string
	SchemaState string
	RowCount    int64
}

type hotSlot struct {
	Name    string
	DValue  int64
	Current int64
	NextID  int64
}

func mustOpenDB(ctx context.Context, dsn string) *sql.DB {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(16)
	db.SetConnMaxLifetime(0)
	mustPing(ctx, db)
	return db
}

func mustPing(ctx context.Context, db *sql.DB) {
	for {
		err := db.PingContext(ctx)
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			log.Fatalf("ping db: %v", err)
		}
		log.Printf("ping retry after error: %v", err)
		time.Sleep(defaultMySQLRetryDelay)
	}
}

func buildDDLShape() (string, []string) {
	switch strings.ToLower(*ddlShape) {
	case "single":
		return fmt.Sprintf("alter table %s.%s add index idx_c(c)", defaultSchemaName, defaultTableName), []string{"idx_c"}
	case "unique":
		return fmt.Sprintf("alter table %s.%s add unique index uk_c(c)", defaultSchemaName, defaultTableName), []string{"uk_c"}
	case "multi":
		return fmt.Sprintf("alter table %s.%s add unique index uk_c(c), add index idx_d(d)", defaultSchemaName, defaultTableName), []string{"uk_c", "idx_d"}
	default:
		log.Fatalf("unsupported ddl shape: %s", *ddlShape)
		return "", nil
	}
}

func mustSetupWorkload(ctx context.Context, db *sql.DB) []*hotSlot {
	if *distTask {
		mustExec(ctx, db, "set global tidb_enable_dist_task = on")
	} else {
		mustExec(ctx, db, "set global tidb_enable_dist_task = off")
	}
	if *fastReorg {
		mustExec(ctx, db, "set global tidb_ddl_enable_fast_reorg = on")
	} else {
		mustExec(ctx, db, "set global tidb_ddl_enable_fast_reorg = off")
	}
	mustExec(ctx, db, "set global tidb_ddl_reorg_worker_cnt = 1")
	mustExec(ctx, db, "set global tidb_ddl_reorg_batch_size = 32")

	mustExec(ctx, db, "create database if not exists "+defaultSchemaName)
	mustExec(ctx, db, "drop table if exists "+defaultSchemaName+"."+defaultTableName)
	mustExec(ctx, db, fmt.Sprintf(`create table %s.%s (
		id bigint not null,
		c varchar(96) not null,
		d bigint not null,
		pad varchar(256) not null,
		primary key (id) clustered
	)`, defaultSchemaName, defaultTableName))

	prefillRows(ctx, db, *targetRows)
	rowCount := queryInt(ctx, db, "select count(*) from "+defaultSchemaName+"."+defaultTableName)
	log.Printf("prefill done, row_count=%d", rowCount)

	effectiveRegions := min(*splitRegions, max(1, *targetRows/1000))
	if effectiveRegions != *splitRegions {
		log.Printf("reducing split regions from %d to %d to satisfy lower bound", *splitRegions, effectiveRegions)
	}
	mustQueryDiscard(ctx, db, fmt.Sprintf("split table %s.%s between (1) and (%d) regions %d",
		defaultSchemaName, defaultTableName, *targetRows+1, effectiveRegions))
	log.Printf("split table requested: regions=%d", effectiveRegions)

	mustExec(ctx, db, fmt.Sprintf("admin check table %s.%s", defaultSchemaName, defaultTableName))
	return prepareHotSlots(ctx, db)
}

func prefillRows(ctx context.Context, db *sql.DB, rows int) {
	if rows < 1 {
		log.Fatalf("rows must be positive")
	}
	mustExec(ctx, db, fmt.Sprintf(`insert into %s.%s values (1, %s, %s, %s)`,
		defaultSchemaName, defaultTableName, payloadExpr(1), dExpr(1), padExpr()))

	count := 1
	for count < rows {
		inserted := min(count, rows-count)
		sqlText := fmt.Sprintf(`insert into %s.%s
select
	id + %d,
	concat('c-', lpad(cast(id + %d as char), 12, '0'), '-', lpad(cast((id + %d) * 17 as char), 18, '0')),
	mod((id + %d) * 29, 100003),
	%s
from %s.%s
where id <= %d and id + %d <= %d`,
			defaultSchemaName, defaultTableName,
			count, count, count, count,
			padExpr(),
			defaultSchemaName, defaultTableName,
			count, count, rows)
		mustExec(ctx, db, sqlText)
		count += inserted
		if count == rows || count%(rows/8+1) == 0 {
			log.Printf("prefill progress: %d/%d", count, rows)
		}
	}
}

func prepareHotSlots(ctx context.Context, db *sql.DB) []*hotSlot {
	if *dmlWorkers <= 0 || *dmlSlots <= 0 {
		return nil
	}
	if *dmlSlots >= *targetRows {
		log.Fatalf("dml-slots must be smaller than target rows")
	}

	slots := make([]*hotSlot, 0, *dmlSlots)
	nextBase := int64(*targetRows + 100000)
	for i := 0; i < *dmlSlots; i++ {
		id := int64(i + 1)
		name := fmt.Sprintf("hot-%04d", i)
		dValue := int64(i + 1)
		updateSQL := fmt.Sprintf("update %s.%s set c = %s, d = %d where id = %d",
			defaultSchemaName, defaultTableName, wrapString(name), dValue, id)
		mustExec(ctx, db, updateSQL)
		slots = append(slots, &hotSlot{
			Name:    name,
			DValue:  dValue,
			Current: id,
			NextID:  nextBase + int64(i),
		})
	}
	log.Printf("prepared %d hot-key slots for concurrent dml", len(slots))
	return slots
}

func startHotDMLWorkers(opCtx context.Context, wg *sync.WaitGroup, stopCh <-chan struct{}, slots []*hotSlot) {
	workerCount := min(*dmlWorkers, len(slots))
	if workerCount <= 0 {
		return
	}
	for workerID := 0; workerID < workerCount; workerID++ {
		db := mustOpenDB(opCtx, *dsn)
		assigned := make([]*hotSlot, 0)
		for i := workerID; i < len(slots); i += workerCount {
			assigned = append(assigned, slots[i])
		}
		wg.Add(1)
		go func(id int, workerDB *sql.DB, workerSlots []*hotSlot) {
			defer wg.Done()
			defer workerDB.Close()
			runHotDMLWorker(opCtx, stopCh, workerDB, id, workerSlots)
		}(workerID, db, assigned)
	}
}

func runHotDMLWorker(opCtx context.Context, stopCh <-chan struct{}, db *sql.DB, workerID int, slots []*hotSlot) {
	for {
		select {
		case <-stopCh:
			return
		default:
		}

		for _, slot := range slots {
			deleteSQL := fmt.Sprintf("delete from %s.%s where id = %d", defaultSchemaName, defaultTableName, slot.Current)
			if err := execRetry(opCtx, db, deleteSQL, false); err != nil {
				log.Printf("worker %d delete slot %s stop on error: %v", workerID, slot.Name, err)
				return
			}
			time.Sleep(*dmlSleep)

			insertSQL := fmt.Sprintf("insert into %s.%s values (%d, %s, %d, %s)",
				defaultSchemaName, defaultTableName,
				slot.NextID, wrapString(slot.Name), slot.DValue, padExpr())
			if err := execRetry(opCtx, db, insertSQL, true); err != nil {
				log.Printf("worker %d insert slot %s stop on error: %v", workerID, slot.Name, err)
				return
			}
			slot.Current = slot.NextID
			slot.NextID += int64(len(slots) + *dmlWorkers + 97)
			select {
			case <-stopCh:
				return
			default:
			}
			time.Sleep(*dmlSleep)
		}
	}
}

func submitDDLAndIgnoreConnBreak(ctx context.Context, ddlSQL string) {
	db := mustOpenDB(ctx, *dsn)
	defer db.Close()

	log.Printf("ddl start: %s", ddlSQL)
	start := time.Now()
	_, err := db.ExecContext(ctx, ddlSQL)
	if err != nil {
		log.Printf("ddl session returned error after %s: %v", time.Since(start), err)
		return
	}
	log.Printf("ddl session returned success after %s", time.Since(start))
}

func currentJobIDWatermark(ctx context.Context, db *sql.DB) int64 {
	rows, err := db.QueryContext(ctx, `
select ifnull(max(job_id), 0)
from information_schema.ddl_jobs
where db_name = ? and table_name = ?`, defaultSchemaName, defaultTableName)
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

func waitForRunningJob(ctx context.Context, db *sql.DB, ddlSQL string, jobIDWatermark int64) ddlState {
	ticker := time.NewTicker(defaultJobCheckEvery)
	defer ticker.Stop()
	var activeSince time.Time

	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait running ddl job: %v", ctx.Err())
		case <-ticker.C:
		}

		st, ok, err := lookupDDLState(ctx, db, ddlSQL, jobIDWatermark)
		if err != nil {
			log.Printf("lookup running ddl retry after error: %v", err)
			continue
		}
		if !ok {
			continue
		}
		log.Printf("ddl state observed: job_id=%d state=%s schema_state=%s row_count=%d", st.JobID, st.State, st.SchemaState, st.RowCount)
		if isActiveState(st.State) {
			if matchesFaultSchemaState(st.SchemaState) {
				if activeSince.IsZero() {
					activeSince = time.Now()
					log.Printf("ddl entered active fault window: job_id=%d schema_state=%s row_count=%d", st.JobID, st.SchemaState, st.RowCount)
				}
				activeFor := time.Since(activeSince)
				log.Printf("ddl active fault window: job_id=%d schema_state=%s row_count=%d active_for=%s", st.JobID, st.SchemaState, st.RowCount, activeFor.Round(100*time.Millisecond))
				if st.RowCount >= *faultMinRowCount && activeFor >= *faultMinRun {
					return st
				}
			} else if !activeSince.IsZero() {
				log.Printf("ddl left active fault window: job_id=%d schema_state=%s", st.JobID, st.SchemaState)
				activeSince = time.Time{}
			}
		}
		if isTerminalState(st.State) {
			log.Fatalf("ddl became terminal before fault injection: state=%s job_id=%d", st.State, st.JobID)
		}
	}
}

func injectOwnerFaults(ctx context.Context, db *sql.DB, ddlSQL string, jobIDWatermark int64) {
	if *faultCount <= 0 {
		return
	}
	if *faultStartDelay > 0 {
		log.Printf("waiting %s before first owner fault", *faultStartDelay)
		sleepOrDie(ctx, *faultStartDelay)
	}

	switch strings.ToLower(*faultMode) {
	case "delete-pod":
		for i := 1; i <= *faultCount; i++ {
			if _, ok := waitForActiveFaultWindow(ctx, db, ddlSQL, jobIDWatermark); !ok {
				log.Printf("skip remaining owner faults because ddl is already terminal")
				return
			}
			prevUID := currentPodUID(ctx)
			log.Printf("owner fault %d/%d: delete tidb pod %s uid=%s", i, *faultCount, *tidbPod, prevUID)
			deleteTiDBPod(ctx)
			_ = waitForNewReadyPod(ctx, prevUID)
			waitForServiceRecovery(ctx)
			if i < *faultCount {
				sleepOrDie(ctx, *faultInterval)
			}
		}
	case "delete-owner-pod":
		for i := 1; i <= *faultCount; i++ {
			if _, ok := waitForActiveFaultWindow(ctx, db, ddlSQL, jobIDWatermark); !ok {
				log.Printf("skip remaining owner faults because ddl is already terminal")
				return
			}
			ownerPod := currentDDLOwnerPod(ctx, db)
			prevUID := currentPodUIDByName(ctx, ownerPod)
			log.Printf("owner fault %d/%d: delete current ddl owner pod %s uid=%s", i, *faultCount, ownerPod, prevUID)
			deleteTiDBPodByName(ctx, ownerPod)
			newOwner := waitForOwnerAfterDeletion(ctx, db, ownerPod, prevUID)
			log.Printf("owner fault %d/%d handoff observed: old_owner=%s new_owner=%s", i, *faultCount, ownerPod, newOwner)
			if i < *faultCount {
				sleepOrDie(ctx, *faultInterval)
			}
		}
	case "resign-owner":
		for i := 1; i <= *faultCount; i++ {
			if _, ok := waitForActiveFaultWindow(ctx, db, ddlSQL, jobIDWatermark); !ok {
				log.Printf("skip remaining owner faults because ddl is already terminal")
				return
			}
			log.Printf("owner fault %d/%d: resign ddl owner via status api", i, *faultCount)
			postResignOwner(ctx)
			sleepOrDie(ctx, *faultInterval)
		}
	default:
		log.Fatalf("unsupported fault mode: %s", *faultMode)
	}
}

func applyControl(ctx context.Context, ddlSQL string, jobIDWatermark int64, jobID int64) {
	controlDB := mustOpenDB(ctx, *dsn)
	defer controlDB.Close()

	st := waitForControlWindow(ctx, controlDB, ddlSQL, jobIDWatermark, jobID)
	log.Printf("control window sees: job_id=%d state=%s schema_state=%s row_count=%d", st.JobID, st.State, st.SchemaState, st.RowCount)
	if *controlDelay > 0 {
		sleepOrDie(ctx, *controlDelay)
	}

	switch *controlMode {
	case "pause-resume":
		mustExec(ctx, controlDB, fmt.Sprintf("admin pause ddl jobs %d", st.JobID))
		waitForExactState(ctx, controlDB, ddlSQL, jobIDWatermark, st.JobID, "paused")
		log.Printf("ddl job paused: job_id=%d", st.JobID)
		sleepOrDie(ctx, *controlPause)
		mustExec(ctx, controlDB, fmt.Sprintf("admin resume ddl jobs %d", st.JobID))
		waitForOneOfStates(ctx, controlDB, ddlSQL, jobIDWatermark, st.JobID, "running", "queueing")
		log.Printf("ddl job resumed: job_id=%d", st.JobID)
	case "cancel":
		mustExec(ctx, controlDB, fmt.Sprintf("admin cancel ddl jobs %d", st.JobID))
		log.Printf("ddl job cancel requested: job_id=%d", st.JobID)
	case "pause-resume-cancel":
		mustExec(ctx, controlDB, fmt.Sprintf("admin pause ddl jobs %d", st.JobID))
		waitForExactState(ctx, controlDB, ddlSQL, jobIDWatermark, st.JobID, "paused")
		log.Printf("ddl job paused: job_id=%d", st.JobID)
		sleepOrDie(ctx, *controlPause)
		mustExec(ctx, controlDB, fmt.Sprintf("admin resume ddl jobs %d", st.JobID))
		waitForOneOfStates(ctx, controlDB, ddlSQL, jobIDWatermark, st.JobID, "running", "queueing")
		log.Printf("ddl job resumed: job_id=%d", st.JobID)
		st = waitForControlWindow(ctx, controlDB, ddlSQL, jobIDWatermark, st.JobID)
		log.Printf("post-resume control window sees: job_id=%d state=%s schema_state=%s row_count=%d", st.JobID, st.State, st.SchemaState, st.RowCount)
		mustExec(ctx, controlDB, fmt.Sprintf("admin cancel ddl jobs %d", st.JobID))
		log.Printf("ddl job cancel requested after pause-resume: job_id=%d", st.JobID)
	default:
		log.Fatalf("unsupported control mode: %s", *controlMode)
	}
}

func deleteTiDBPod(ctx context.Context) {
	deleteTiDBPodByName(ctx, *tidbPod)
}

func deleteTiDBPodByName(ctx context.Context, podName string) {
	cmd := exec.CommandContext(ctx, "kubectl",
		"--kubeconfig", *kubeconfig,
		"-n", *namespace,
		"delete", "pod", podName,
		"--force",
		"--grace-period=0",
		"--wait=false",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Fatalf("delete tidb pod: %v, output=%s", err, strings.TrimSpace(string(out)))
	}
	log.Printf("delete tidb pod ok: %s", strings.TrimSpace(string(out)))
}

func currentPodUID(ctx context.Context) string {
	return currentPodUIDByName(ctx, *tidbPod)
}

func currentPodUIDByName(ctx context.Context, podName string) string {
	uid, ready, phase, err := podStatusByName(ctx, podName)
	if err != nil {
		log.Fatalf("get current tidb pod uid: %v", err)
	}
	log.Printf("current tidb pod %s: uid=%s phase=%s ready=%v", podName, uid, phase, ready)
	return uid
}

func waitForNewReadyPod(ctx context.Context, oldUID string) string {
	return waitForNewReadyPodByName(ctx, *tidbPod, oldUID)
}

func waitForNewReadyPodByName(ctx context.Context, podName string, oldUID string) string {
	ticker := time.NewTicker(defaultPodCheckEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait new tidb pod ready: %v", ctx.Err())
		case <-ticker.C:
		}

		uid, ready, phase, err := podStatusByName(ctx, podName)
		if err != nil {
			log.Printf("pod status retry after error: %v", err)
			continue
		}
		log.Printf("tidb pod %s status: uid=%s phase=%s ready=%v", podName, uid, phase, ready)
		if uid != "" && uid != oldUID && phase == "Running" && ready {
			return uid
		}
	}
}

func podStatus(ctx context.Context) (uid string, ready bool, phase string, err error) {
	return podStatusByName(ctx, *tidbPod)
}

func podStatusByName(ctx context.Context, podName string) (uid string, ready bool, phase string, err error) {
	cmd := exec.CommandContext(ctx, "kubectl",
		"--kubeconfig", *kubeconfig,
		"-n", *namespace,
		"get", "pod", podName,
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
		return "", false, "", err
	}
	return parts[0], ready, parts[1], nil
}

func currentDDLOwnerPod(ctx context.Context, db *sql.DB) string {
	for {
		rows, err := db.QueryContext(ctx, "admin show ddl")
		if err != nil {
			if ctx.Err() != nil {
				log.Fatalf("query admin show ddl: %v", err)
			}
			log.Printf("admin show ddl retry after error: %v", err)
			time.Sleep(defaultMySQLRetryDelay)
			continue
		}
		if !rows.Next() {
			_ = rows.Close()
			if ctx.Err() != nil {
				log.Fatalf("admin show ddl returned no rows")
			}
			time.Sleep(defaultMySQLRetryDelay)
			continue
		}
		var (
			schemaVer    any
			ownerID      string
			ownerAddress string
			runningJobs  any
			selfID       any
			query        any
		)
		err = rows.Scan(&schemaVer, &ownerID, &ownerAddress, &runningJobs, &selfID, &query)
		_ = rows.Close()
		if err != nil {
			if ctx.Err() != nil {
				log.Fatalf("scan admin show ddl: %v", err)
			}
			log.Printf("scan admin show ddl retry after error: %v", err)
			time.Sleep(defaultMySQLRetryDelay)
			continue
		}
		host := strings.Split(ownerAddress, ":")[0]
		podName := strings.Split(host, ".")[0]
		if podName == "" {
			log.Fatalf("cannot parse owner pod from address %q", ownerAddress)
		}
		log.Printf("current ddl owner: id=%s address=%s pod=%s", ownerID, ownerAddress, podName)
		return podName
	}
}

func waitForOwnerAfterDeletion(ctx context.Context, db *sql.DB, deletedOwnerPod string, deletedUID string) string {
	ticker := time.NewTicker(defaultPodCheckEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait owner after deletion: %v", ctx.Err())
		case <-ticker.C:
		}

		ownerPod, ownerOK := tryCurrentDDLOwnerPod(ctx, db)
		if ownerOK && ownerPod != deletedOwnerPod {
			return ownerPod
		}

		uid, ready, phase, err := podStatusByName(ctx, deletedOwnerPod)
		if err == nil {
			log.Printf("deleted owner pod %s status: uid=%s phase=%s ready=%v", deletedOwnerPod, uid, phase, ready)
			if uid != "" && uid != deletedUID && phase == "Running" && ready && ownerOK {
				return ownerPod
			}
		}
	}
}

func tryCurrentDDLOwnerPod(ctx context.Context, db *sql.DB) (string, bool) {
	rows, err := db.QueryContext(ctx, "admin show ddl")
	if err != nil {
		log.Printf("admin show ddl retry after error: %v", err)
		return "", false
	}
	defer rows.Close()
	if !rows.Next() {
		return "", false
	}
	var (
		schemaVer    any
		ownerID      string
		ownerAddress string
		runningJobs  any
		selfID       any
		query        any
	)
	if err := rows.Scan(&schemaVer, &ownerID, &ownerAddress, &runningJobs, &selfID, &query); err != nil {
		log.Printf("scan admin show ddl retry after error: %v", err)
		return "", false
	}
	host := strings.Split(ownerAddress, ":")[0]
	podName := strings.Split(host, ".")[0]
	if podName == "" {
		log.Printf("cannot parse owner pod from address %q", ownerAddress)
		return "", false
	}
	log.Printf("current ddl owner: id=%s address=%s pod=%s", ownerID, ownerAddress, podName)
	return podName, true
}

func waitForServiceRecovery(ctx context.Context) {
	db := mustOpenDB(ctx, *dsn)
	defer db.Close()
	log.Printf("mysql service recovered")
}

func postResignOwner(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(*statusURL, "/")+"/ddl/owner/resign", nil)
	if err != nil {
		log.Fatalf("build resign request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("post resign owner: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		log.Fatalf("resign owner returned status %s", resp.Status)
	}
	log.Printf("resign owner request returned %s", resp.Status)
}

func waitForActiveFaultWindow(ctx context.Context, db *sql.DB, ddlSQL string, jobIDWatermark int64) (ddlState, bool) {
	ticker := time.NewTicker(defaultJobCheckEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait active fault window: %v", ctx.Err())
		case <-ticker.C:
		}

		st, ok, err := lookupDDLState(ctx, db, ddlSQL, jobIDWatermark)
		if err != nil {
			log.Printf("lookup active fault window retry after error: %v", err)
			continue
		}
		if !ok {
			continue
		}
		log.Printf("fault window sees: job_id=%d state=%s schema_state=%s row_count=%d", st.JobID, st.State, st.SchemaState, st.RowCount)
		if isTerminalState(st.State) {
			return st, false
		}
		if isActiveState(st.State) && matchesFaultSchemaState(st.SchemaState) {
			return st, true
		}
	}
}

func waitForControlWindow(ctx context.Context, db *sql.DB, ddlSQL string, jobIDWatermark int64, expectedJobID int64) ddlState {
	ticker := time.NewTicker(defaultJobCheckEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait control window: %v", ctx.Err())
		case <-ticker.C:
		}

		st, ok, err := lookupDDLState(ctx, db, ddlSQL, jobIDWatermark)
		if err != nil {
			log.Printf("lookup control window retry after error: %v", err)
			continue
		}
		if !ok || st.JobID != expectedJobID {
			continue
		}
		if isTerminalState(st.State) {
			log.Fatalf("ddl became terminal before control action: job_id=%d state=%s schema_state=%s", st.JobID, st.State, st.SchemaState)
		}
		if st.State != "running" {
			continue
		}
		if st.RowCount < *controlMinRows {
			continue
		}
		return st
	}
}

func waitForExactState(ctx context.Context, db *sql.DB, ddlSQL string, jobIDWatermark int64, expectedJobID int64, want string) {
	ticker := time.NewTicker(defaultJobCheckEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait exact state %s: %v", want, ctx.Err())
		case <-ticker.C:
		}

		matched, err := jobHasAnyState(ctx, db, ddlSQL, jobIDWatermark, expectedJobID, want)
		if err != nil {
			log.Printf("lookup exact state retry after error: %v", err)
			continue
		}
		if matched {
			return
		}
	}
}

func waitForOneOfStates(ctx context.Context, db *sql.DB, ddlSQL string, jobIDWatermark int64, expectedJobID int64, wants ...string) {
	ticker := time.NewTicker(defaultJobCheckEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait one-of states %v: %v", wants, ctx.Err())
		case <-ticker.C:
		}

		matched, err := jobHasAnyState(ctx, db, ddlSQL, jobIDWatermark, expectedJobID, wants...)
		if err != nil {
			log.Printf("lookup one-of states retry after error: %v", err)
			continue
		}
		if matched {
			return
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

func waitForTerminalState(ctx context.Context, db *sql.DB, ddlSQL string, jobIDWatermark int64) ddlState {
	timeoutCtx, cancel := context.WithTimeout(ctx, *ddlTimeout)
	defer cancel()
	ticker := time.NewTicker(defaultJobCheckEvery)
	defer ticker.Stop()

	for {
		select {
		case <-timeoutCtx.Done():
			st, ok, err := lookupDDLState(ctx, db, ddlSQL, jobIDWatermark)
			if err != nil {
				log.Fatalf("ddl wait timeout and final lookup failed: %v", err)
			}
			if !ok {
				log.Fatalf("ddl wait timeout and job disappeared")
			}
			log.Fatalf("ddl stuck beyond timeout: job_id=%d state=%s schema_state=%s", st.JobID, st.State, st.SchemaState)
		case <-ticker.C:
		}

		st, ok, err := lookupDDLState(ctx, db, ddlSQL, jobIDWatermark)
		if err != nil {
			log.Printf("lookup terminal ddl retry after error: %v", err)
			continue
		}
		if !ok {
			continue
		}
		log.Printf("ddl terminal wait sees: job_id=%d state=%s schema_state=%s", st.JobID, st.State, st.SchemaState)
		if isTerminalState(st.State) {
			return st
		}
	}
}

func lookupDDLState(ctx context.Context, db *sql.DB, ddlSQL string, jobIDWatermark int64) (ddlState, bool, error) {
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
limit 1`, defaultSchemaName, defaultTableName, ddlSQL, jobIDWatermark)
	if err != nil {
		return ddlState{}, false, err
	}
	defer rows.Close()

	if !rows.Next() {
		return ddlState{}, false, rows.Err()
	}
	var st ddlState
	if err := rows.Scan(&st.JobID, &st.State, &st.SchemaState, &st.RowCount); err != nil {
		return ddlState{}, false, err
	}
	return st, true, rows.Err()
}

func jobHasAnyState(ctx context.Context, db *sql.DB, ddlSQL string, jobIDWatermark int64, expectedJobID int64, wants ...string) (bool, error) {
	if len(wants) == 0 {
		return false, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(wants)), ",")
	args := make([]any, 0, 4+len(wants))
	args = append(args, defaultSchemaName, defaultTableName, ddlSQL, jobIDWatermark, expectedJobID)
	for _, want := range wants {
		args = append(args, want)
	}
	sqlText := fmt.Sprintf(`
select count(*)
from information_schema.ddl_jobs
where db_name = ? and table_name = ? and query = ?
  and job_id > ?
  and job_id = ?
  and state in (%s)`, placeholders)
	var cnt int
	if err := db.QueryRowContext(ctx, sqlText, args...).Scan(&cnt); err != nil {
		return false, err
	}
	return cnt > 0, nil
}

func isActiveState(state string) bool {
	switch state {
	case "running", "queueing":
		return true
	default:
		return false
	}
}

func isTerminalState(state string) bool {
	switch state {
	case "done", "synced", "rollback done", "cancelled":
		return true
	default:
		return false
	}
}

func isSuccessState(state string) bool {
	switch state {
	case "done", "synced":
		return true
	default:
		return false
	}
}

func runTerminalOracles(ctx context.Context, db *sql.DB, ddlSQL string, expectedIndexes []string, hotSlots []*hotSlot, st ddlState, jobIDWatermark int64) {
	existingIndexes := loadIndexNames(ctx, db)
	log.Printf("visible indexes after terminal state: %v", existingIndexes)

	allPresent := true
	for _, idx := range expectedIndexes {
		if !slices.Contains(existingIndexes, idx) {
			allPresent = false
			break
		}
	}

	switch st.State {
	case "done", "synced":
		if !allPresent {
			log.Fatalf("state-schema mismatch: ddl is %s but expected indexes are missing, indexes=%v", st.State, existingIndexes)
		}
	case "rollback done", "cancelled":
		if allPresent {
			for _, idx := range expectedIndexes {
				count := queryInt(ctx, db, fmt.Sprintf("select count(*) from %s.%s use index(%s)", defaultSchemaName, defaultTableName, idx))
				log.Printf("index count after terminal %s via %s: %d", st.State, idx, count)
			}
			log.Fatalf("state-schema mismatch: ddl is %s but expected indexes are still visible and usable", st.State)
		}
	default:
		log.Fatalf("unexpected terminal state: %s", st.State)
	}

	mustExec(ctx, db, fmt.Sprintf("admin check table %s.%s", defaultSchemaName, defaultTableName))
	tableCount := queryInt(ctx, db, fmt.Sprintf("select count(*) from %s.%s use index()", defaultSchemaName, defaultTableName))
	cCount := queryInt(ctx, db, fmt.Sprintf("select count(c) from %s.%s", defaultSchemaName, defaultTableName))
	dCount := queryInt(ctx, db, fmt.Sprintf("select count(d) from %s.%s", defaultSchemaName, defaultTableName))
	log.Printf("terminal counts: table=%d c=%d d=%d", tableCount, cCount, dCount)
	if tableCount != *targetRows || cCount != *targetRows || dCount != *targetRows {
		log.Fatalf("row count mismatch after terminal state %s: table=%d c=%d d=%d target=%d", st.State, tableCount, cCount, dCount, *targetRows)
	}

	if allPresent {
		for _, idx := range expectedIndexes {
			mustExec(ctx, db, fmt.Sprintf("admin check index %s.%s %s", defaultSchemaName, defaultTableName, idx))
			count := queryInt(ctx, db, fmt.Sprintf("select count(*) from %s.%s use index(%s)", defaultSchemaName, defaultTableName, idx))
			log.Printf("index count via %s: %d", idx, count)
			if count != *targetRows {
				log.Fatalf("index count mismatch for %s: got %d target %d", idx, count, *targetRows)
			}
		}
	}

	verifyHotSlotOracles(ctx, db, expectedIndexes, hotSlots, allPresent)

	showLastDDLJob(ctx, db, ddlSQL, jobIDWatermark)
}

func loadIndexNames(ctx context.Context, db *sql.DB) []string {
	rows, err := db.QueryContext(ctx, `
select distinct index_name
from information_schema.statistics
where table_schema = ? and table_name = ?
order by index_name`, defaultSchemaName, defaultTableName)
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

func verifyHotSlotOracles(ctx context.Context, db *sql.DB, expectedIndexes []string, hotSlots []*hotSlot, allPresent bool) {
	if len(hotSlots) == 0 {
		return
	}
	cIndex := ""
	if allPresent && strings.EqualFold(*ddlShape, "multi") {
		cIndex = "uk_c"
	} else if allPresent && strings.EqualFold(*ddlShape, "single") {
		cIndex = "idx_c"
	}

	ignoreHint := ""
	if allPresent && len(expectedIndexes) > 0 {
		ignoreHint = " ignore index(" + strings.Join(expectedIndexes, ",") + ") "
	}

	for _, slot := range hotSlots {
		scanID, scanD := queryHotSlot(ctx, db, "", ignoreHint, slot.Name)
		if scanID == 0 {
			log.Fatalf("hot slot %s disappeared: scan_id=%d", slot.Name, scanID)
		}
		if cIndex == "" {
			if scanD != slot.DValue {
				log.Fatalf("hot slot mismatch for %s without index: scan=(id=%d,d=%d) expected_d=%d",
					slot.Name, scanID, scanD, slot.DValue)
			}
			continue
		}
		forcedID, forcedD := queryHotSlot(ctx, db, cIndex, "", slot.Name)
		if forcedID == 0 {
			log.Fatalf("hot slot %s disappeared on forced index path: force_id=%d", slot.Name, forcedID)
		}
		if forcedID != scanID || forcedD != scanD || forcedD != slot.DValue {
			log.Fatalf("hot slot mismatch for %s: force=(id=%d,d=%d) scan=(id=%d,d=%d) expected_d=%d",
				slot.Name, forcedID, forcedD, scanID, scanD, slot.DValue)
		}
	}
	log.Printf("hot slot oracle green for %d slots", len(hotSlots))
}

func queryHotSlot(ctx context.Context, db *sql.DB, useIndex string, extraHint string, key string) (int64, int64) {
	hint := ""
	if useIndex != "" {
		hint = fmt.Sprintf(" use index(%s) ", useIndex)
	} else {
		hint = extraHint
	}
	sqlText := fmt.Sprintf("select id, d from %s.%s%s where c = %s",
		defaultSchemaName, defaultTableName, hint, wrapString(key))
	for {
		var (
			id int64
			d  int64
		)
		err := db.QueryRowContext(ctx, sqlText).Scan(&id, &d)
		if err == nil {
			return id, d
		}
		if err == sql.ErrNoRows {
			return 0, 0
		}
		if ctx.Err() != nil {
			log.Fatalf("query hot slot failed: %s: %v", sqlText, err)
		}
		log.Printf("query hot slot retry after error: %s: %v", sqlText, err)
		time.Sleep(defaultMySQLRetryDelay)
	}
}

func showLastDDLJob(ctx context.Context, db *sql.DB, ddlSQL string, jobIDWatermark int64) {
	rows, err := db.QueryContext(ctx, `
select job_id, job_type, state, schema_state
from information_schema.ddl_jobs
where db_name = ? and table_name = ? and query = ?
  and job_id > ?
order by job_id desc
limit 1`, defaultSchemaName, defaultTableName, ddlSQL, jobIDWatermark)
	if err != nil {
		log.Printf("show ddl job failed: %v", err)
		return
	}
	defer rows.Close()
	if !rows.Next() {
		return
	}
	var (
		jobID       int64
		jobType     string
		state       string
		schemaState string
	)
	if err := rows.Scan(&jobID, &jobType, &state, &schemaState); err != nil {
		log.Printf("scan ddl job failed: %v", err)
		return
	}
	log.Printf("final ddl job summary: job_id=%d type=%s state=%s schema_state=%s", jobID, jobType, state, schemaState)
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

func wrapString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func mustExec(ctx context.Context, db *sql.DB, sqlText string) {
	for {
		_, err := db.ExecContext(ctx, sqlText)
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			log.Fatalf("exec failed: %s: %v", sqlText, err)
		}
		log.Printf("exec retry after error: %s: %v", sqlText, err)
		time.Sleep(defaultMySQLRetryDelay)
	}
}

func execRetry(ctx context.Context, db *sql.DB, sqlText string, duplicateMeansSuccess bool) error {
	for {
		_, err := db.ExecContext(ctx, sqlText)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "duplicate entry") {
			if duplicateMeansSuccess {
				return nil
			}
			time.Sleep(defaultMySQLRetryDelay)
			continue
		}
		if strings.Contains(msg, "data too long") ||
			strings.Contains(msg, "out of range") {
			return err
		}
		log.Printf("exec retry after error: %s: %v", sqlText, err)
		time.Sleep(defaultMySQLRetryDelay)
	}
}

func mustQueryDiscard(ctx context.Context, db *sql.DB, sqlText string) {
	for {
		rows, err := db.QueryContext(ctx, sqlText)
		if err != nil {
			if ctx.Err() != nil {
				log.Fatalf("query failed: %s: %v", sqlText, err)
			}
			log.Printf("query retry after error: %s: %v", sqlText, err)
			time.Sleep(defaultMySQLRetryDelay)
			continue
		}
		for rows.Next() {
		}
		err = rows.Err()
		_ = rows.Close()
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			log.Fatalf("query iteration failed: %s: %v", sqlText, err)
		}
		log.Printf("query iteration retry after error: %s: %v", sqlText, err)
		time.Sleep(defaultMySQLRetryDelay)
	}
}

func queryInt(ctx context.Context, db *sql.DB, sqlText string) int {
	for {
		var value int
		err := db.QueryRowContext(ctx, sqlText).Scan(&value)
		if err == nil {
			return value
		}
		if ctx.Err() != nil {
			log.Fatalf("query int failed: %s: %v", sqlText, err)
		}
		log.Printf("query int retry after error: %s: %v", sqlText, err)
		time.Sleep(defaultMySQLRetryDelay)
	}
}

func sleepOrDie(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
		log.Fatalf("sleep interrupted: %v", ctx.Err())
	case <-time.After(d):
	}
}
