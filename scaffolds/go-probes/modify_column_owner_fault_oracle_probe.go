package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	defaultModifyDSN        = "root@tcp(10.2.12.57:32334)/"
	defaultModifyKubeconfig = "/Users/bba/pc/kubeconfig.yml"
	defaultModifyNamespace  = "testbed-tps-8220955-1-213"
	defaultModifySchema     = "ai_native_modify_owner_probe"
	defaultModifyTable      = "rows"
	defaultModifyJobCheck   = 200 * time.Millisecond
	defaultModifyPodCheck   = 2 * time.Second
	paddingSize             = 256
	defaultDMLWorkers       = 8
	defaultDMLBatch         = 4
	defaultShardWidth       = 4000
	prefillRowsCount        = 600000
)

var (
	dsn2             = flag.String("dsn", defaultModifyDSN, "mysql dsn")
	kubeconfig2      = flag.String("kubeconfig", defaultModifyKubeconfig, "kubeconfig path")
	namespace2       = flag.String("namespace", defaultModifyNamespace, "kubernetes namespace")
	targetRows2      = flag.Int("rows", prefillRowsCount, "rows to prefill before modify column")
	dmlWorkers2      = flag.Int("dml-workers", defaultDMLWorkers, "number of concurrent delete/reinsert workers")
	dmlBatch2        = flag.Int("dml-batch", defaultDMLBatch, "max rows per delete/reinsert batch")
	dmlShardWidth2   = flag.Int("dml-shard-width", defaultShardWidth, "per-worker key range width")
	dmlSleep2        = flag.Duration("dml-sleep", 80*time.Millisecond, "pause between delete and reinsert")
	faultMode2       = flag.String("fault-mode", "none", "fault mode: none, delete-owner-pod, delete-pod, or network-partition")
	faultPod2        = flag.String("fault-pod", "", "single pod to delete when fault-mode=delete-pod")
	faultPods2       = flag.String("fault-pods", "", "comma-separated pod sequence to delete when fault-mode=delete-pod")
	faultStartDelay2 = flag.Duration("fault-start-delay", 4*time.Second, "extra wait after the active fault window before first fault")
	faultMinRows2    = flag.Int64("fault-min-row-count", 0, "minimum ddl row_count before the first fault")
	faultMinRun2     = flag.Duration("fault-min-running", 0, "minimum time the ddl must stay in the active fault window before the first fault")
	faultSchema2     = flag.String("fault-schema-state", "write reorganization", "required schema_state before the first fault; empty disables the check")
	waitPodReady2    = flag.Bool("wait-pod-ready", true, "wait for each deleted pod to become ready before moving to the next fault")
	bounceInterval2  = flag.Duration("bounce-interval", 6*time.Second, "interval between successive delete-pod faults")
	networkDuration2 = flag.Duration("network-duration", 12*time.Second, "duration of network-partition fault")
	networkDir2      = flag.String("network-direction", "both", "direction for network-partition fault: to, from, or both")
	networkTarget2   = flag.String("network-target-component", "tikv", "target component for network-partition fault: tikv, tidb, or pd")
	controlMode2     = flag.String("control-mode", "none", "control mode: none, pause-resume, cancel, or pause-resume-cancel")
	controlDelay2    = flag.Duration("control-delay", 0, "extra delay before control action after active window is observed")
	controlPause2    = flag.Duration("control-pause", 5*time.Second, "how long to keep the ddl paused before resume")
	controlMinRows2  = flag.Int64("control-min-row-count", 0, "minimum row_count before applying control action")
	timeout2         = flag.Duration("timeout", 25*time.Minute, "overall timeout")
)

var activeNetworkChaosName2 string

type ddlState2 struct {
	JobID       int64
	State       string
	SchemaState string
	RowCount    int64
}

func main() {
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout2)
	defer cancel()

	db := mustOpenDB2(ctx)
	defer db.Close()

	mustSetupDatabase2(ctx, db)

	probeIDs := buildProbeIDs2()

	dmlCtx, cancelDML := context.WithCancel(ctx)
	defer cancelDML()

	stopDML := make(chan struct{})
	startDML := make(chan struct{})
	var dmlWG sync.WaitGroup
	startDMLWorkers2(dmlCtx, startDML, stopDML, &dmlWG)
	startLiveReadOracle2(dmlCtx, startDML, stopDML, probeIDs)

	ddlSQL := fmt.Sprintf("alter table %s.%s change column x a varchar(16) not null", defaultModifySchema, defaultModifyTable)
	jobIDWatermark := latestMatchingJobID2(ctx, db, ddlSQL)
	go runDDL2(ctx, ddlSQL)

	runningState := waitForRunning2(ctx, db, ddlSQL, jobIDWatermark)
	close(startDML)
	var touchedPods []string
	if *faultMode2 != "none" {
		faultState := waitForFaultWindow2(ctx, db, ddlSQL, jobIDWatermark, runningState.JobID)
		log.Printf("fault window sees: job_id=%d state=%s schema_state=%s row_count=%d", faultState.JobID, faultState.State, faultState.SchemaState, faultState.RowCount)
		if *faultStartDelay2 > 0 {
			sleepOrDie2(ctx, *faultStartDelay2)
		}
		touchedPods = injectFault2(ctx, db)
	}
	if *controlMode2 != "none" {
		applyControl2(ctx, ddlSQL, jobIDWatermark, runningState.JobID)
	}

	state := waitForTerminal2(ctx, db, ddlSQL, jobIDWatermark)
	log.Printf("terminal ddl state: job_id=%d state=%s schema_state=%s row_count=%d", state.JobID, state.State, state.SchemaState, state.RowCount)
	if activeNetworkChaosName2 != "" {
		deleteNetworkChaos2(ctx, activeNetworkChaosName2)
		sleepWithContext2(ctx, 2*time.Second)
	}
	if len(touchedPods) > 0 {
		waitForPodsRecovery2(ctx, touchedPods)
	}

	close(stopDML)
	waitForDMLDrain2(cancelDML, &dmlWG)

	finalDB := mustOpenDB2(ctx)
	defer finalDB.Close()
	runFinalOracle2(ctx, finalDB, state, probeIDs)
	log.Printf("probe finished cleanly")
}

func mustOpenDB2(ctx context.Context) *sql.DB {
	db, err := sql.Open("mysql", *dsn2)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(*dmlWorkers2 + 12)
	db.SetMaxIdleConns(*dmlWorkers2 + 12)
	db.SetConnMaxLifetime(0)
	for {
		if err := db.PingContext(ctx); err == nil {
			return db
		}
		if ctx.Err() != nil {
			log.Fatalf("ping db: %v", err)
		}
		time.Sleep(2 * time.Second)
	}
}

func mustSetupDatabase2(ctx context.Context, db *sql.DB) {
	mustExec2(ctx, db, "create database if not exists "+defaultModifySchema)
	mustExec2(ctx, db, "drop table if exists "+defaultModifySchema+"."+defaultModifyTable)
	mustExec2(ctx, db, fmt.Sprintf(`create table %s.%s (
		id int not null,
		x int not null,
		y int not null,
		padding varchar(%d) not null default '',
		primary key (id) clustered,
		key idx_x(x)
	)`, defaultModifySchema, defaultModifyTable, paddingSize))
	mustExec2(ctx, db, "set global tidb_ddl_reorg_worker_cnt = 1")
	mustExec2(ctx, db, "set global tidb_ddl_reorg_batch_size = 4")

	const batchSize = 1000
	paddingBuffer := make([]byte, paddingSize/2)
	for start := 1; start <= *targetRows2; start += batchSize {
		end := min(start+batchSize-1, *targetRows2)
		valueStrings := make([]string, 0, end-start+1)
		args := make([]any, 0, (end-start+1)*4)
		for id := start; id <= end; id++ {
			rand.Read(paddingBuffer)
			valueStrings = append(valueStrings, "(?, ?, ?, ?)")
			args = append(args, id, id%10000, (id%10000)*2, hex.EncodeToString(paddingBuffer))
		}
		sqlText := fmt.Sprintf("insert into %s.%s values %s", defaultModifySchema, defaultModifyTable, strings.Join(valueStrings, ","))
		mustExecArgs2(ctx, db, sqlText, args...)
	}
	log.Printf("prefill done, row_count=%d", queryInt2(ctx, db, "select count(*) from "+defaultModifySchema+"."+defaultModifyTable))
}

func buildProbeIDs2() []int {
	ids := make([]int, 0, *dmlWorkers2)
	for workerID := 0; workerID < *dmlWorkers2; workerID++ {
		id := workerID**dmlShardWidth2 + 1
		if id > *targetRows2 {
			break
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		ids = append(ids, 1)
	}
	return ids
}

func startDMLWorkers2(opCtx context.Context, startCh <-chan struct{}, stopCh <-chan struct{}, wg *sync.WaitGroup) {
	for workerID := 0; workerID < *dmlWorkers2; workerID++ {
		db := mustOpenDB2(opCtx)
		wg.Add(1)
		go func(id int, workerDB *sql.DB) {
			defer wg.Done()
			defer workerDB.Close()
			dmlWorker2(opCtx, startCh, stopCh, workerDB, id)
		}(workerID, db)
	}
}

func startLiveReadOracle2(opCtx context.Context, startCh <-chan struct{}, stopCh <-chan struct{}, probeIDs []int) {
	go func() {
		select {
		case <-opCtx.Done():
			return
		case <-startCh:
		}

		db := mustOpenDB2(opCtx)
		defer db.Close()

		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-opCtx.Done():
				return
			case <-stopCh:
				return
			case <-ticker.C:
			}

			cols, ok := tryCurrentValueColumns2(opCtx, db)
			if !ok {
				continue
			}
			bad := formulaMismatchCount2(opCtx, db, cols[0], cols[1])
			if bad != 0 {
				log.Fatalf("live read oracle mismatch during active ddl: bad_count=%d cols=%v", bad, cols)
			}
			probePointRowsLive2(opCtx, db, cols[0], cols[1], probeIDs)
		}
	}()
}

func waitForDMLDrain2(cancel func(), wg *sync.WaitGroup) {
	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()

	select {
	case <-doneCh:
		return
	case <-time.After(3 * time.Second):
		log.Printf("dml drain exceeded grace period, cancelling remaining workers")
		cancel()
		<-doneCh
	}
}

func dmlWorker2(opCtx context.Context, startCh <-chan struct{}, stopCh <-chan struct{}, db *sql.DB, workerID int) {
	select {
	case <-opCtx.Done():
		return
	case <-startCh:
	}

	localRand := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerID)*29))
	paddingBuffer := make([]byte, paddingSize/2)
	shardStart := workerID**dmlShardWidth2 + 1
	shardEnd := min(shardStart+*dmlShardWidth2-1, *targetRows2)
	if shardStart > shardEnd {
		return
	}
	for {
		select {
		case <-stopCh:
			return
		default:
		}

		rowCount := localRand.Intn(max(1, *dmlBatch2)) + 1
		if rowCount > shardEnd-shardStart+1 {
			rowCount = shardEnd - shardStart + 1
		}
		offsetCap := shardEnd - shardStart - rowCount + 2
		base := shardStart
		if offsetCap > 1 {
			base = shardStart + localRand.Intn(offsetCap)
		}

		valueStrings := make([]string, 0, rowCount)
		args := make([]any, 0, rowCount*4)
		ids := make([]int, 0, rowCount)
		for i := 0; i < rowCount; i++ {
			id := base + i
			rand.Read(paddingBuffer)
			valueStrings = append(valueStrings, "(?, ?, ?, ?)")
			args = append(args, id, id%10000, (id%10000)*2, hex.EncodeToString(paddingBuffer))
			ids = append(ids, id)
		}

		deletePlaceholders := strings.Repeat("?,", len(ids))
		deleteSQL := fmt.Sprintf("delete from %s.%s where id in (%s)", defaultModifySchema, defaultModifyTable, strings.TrimRight(deletePlaceholders, ","))
		deleteArgs := make([]any, len(ids))
		for i, id := range ids {
			deleteArgs[i] = id
		}
		if err := execRetry2(opCtx, db, deleteSQL, false, deleteArgs...); err != nil {
			log.Fatalf("worker %d delete failed: %v", workerID, err)
		}
		sleepWithContext2(opCtx, *dmlSleep2)

		insertSQL := fmt.Sprintf("insert into %s.%s values %s", defaultModifySchema, defaultModifyTable, strings.Join(valueStrings, ","))
		if err := execRetry2(opCtx, db, insertSQL, true, args...); err != nil {
			log.Fatalf("worker %d insert failed: %v", workerID, err)
		}
		sleepWithContext2(opCtx, *dmlSleep2)
	}
}

func runDDL2(ctx context.Context, ddlSQL string) {
	db := mustOpenDB2(ctx)
	defer db.Close()
	start := time.Now()
	log.Printf("ddl start: %s", ddlSQL)
	if _, err := db.ExecContext(ctx, ddlSQL); err != nil {
		log.Printf("ddl session returned error after %s: %v", time.Since(start), err)
		return
	}
	log.Printf("ddl session returned success after %s", time.Since(start))
}

func waitForRunning2(ctx context.Context, db *sql.DB, ddlSQL string, jobIDWatermark int64) ddlState2 {
	ticker := time.NewTicker(defaultModifyJobCheck)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait running ddl: %v", ctx.Err())
		case <-ticker.C:
		}
		st, ok := lookupDDLState2(ctx, db, ddlSQL, jobIDWatermark)
		if !ok {
			continue
		}
		log.Printf("ddl state observed: job_id=%d state=%s schema_state=%s row_count=%d", st.JobID, st.State, st.SchemaState, st.RowCount)
		if st.State == "running" {
			return st
		}
		if isTerminalState2(st.State) {
			log.Fatalf("ddl became terminal before concurrent window: state=%s schema_state=%s", st.State, st.SchemaState)
		}
	}
}

func injectFault2(ctx context.Context, db *sql.DB) []string {
	switch *faultMode2 {
	case "delete-owner-pod":
		deleteCurrentOwner2(ctx, db)
		return nil
	case "delete-pod":
		return deletePodSequence2(ctx)
	case "network-partition":
		applyNetworkPartition2(ctx, db)
		return nil
	default:
		log.Fatalf("unknown fault mode: %s", *faultMode2)
		return nil
	}
}

func waitForFaultWindow2(ctx context.Context, db *sql.DB, ddlSQL string, jobIDWatermark int64, expectedJobID int64) ddlState2 {
	ticker := time.NewTicker(defaultModifyJobCheck)
	defer ticker.Stop()

	var activeStart time.Time
	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait fault window: %v", ctx.Err())
		case <-ticker.C:
		}

		st, ok := lookupDDLState2(ctx, db, ddlSQL, jobIDWatermark)
		if !ok || st.JobID != expectedJobID {
			continue
		}
		if isTerminalState2(st.State) {
			log.Fatalf("ddl became terminal before fault action: job_id=%d state=%s schema_state=%s", st.JobID, st.State, st.SchemaState)
		}

		match := st.State == "running" &&
			matchesSchemaState2(st.SchemaState, *faultSchema2) &&
			st.RowCount >= *faultMinRows2
		if !match {
			activeStart = time.Time{}
			continue
		}

		if activeStart.IsZero() {
			activeStart = time.Now()
		}
		activeFor := time.Since(activeStart)
		log.Printf("ddl active fault window: job_id=%d schema_state=%s row_count=%d active_for=%s", st.JobID, st.SchemaState, st.RowCount, activeFor.Truncate(100*time.Millisecond))
		if activeFor >= *faultMinRun2 {
			return st
		}
	}
}

func applyControl2(ctx context.Context, ddlSQL string, jobIDWatermark int64, jobID int64) {
	controlDB := mustOpenDB2(ctx)
	defer controlDB.Close()

	st := waitForControlWindow2(ctx, controlDB, ddlSQL, jobIDWatermark, jobID)
	log.Printf("control window sees: job_id=%d state=%s schema_state=%s row_count=%d", st.JobID, st.State, st.SchemaState, st.RowCount)
	if *controlDelay2 > 0 {
		sleepOrDie2(ctx, *controlDelay2)
	}

	switch *controlMode2 {
	case "pause-resume":
		mustExec2(ctx, controlDB, fmt.Sprintf("admin pause ddl jobs %d", st.JobID))
		waitForExactState2(ctx, controlDB, ddlSQL, jobIDWatermark, "paused")
		log.Printf("ddl job paused: job_id=%d", st.JobID)
		sleepOrDie2(ctx, *controlPause2)
		mustExec2(ctx, controlDB, fmt.Sprintf("admin resume ddl jobs %d", st.JobID))
		waitForOneOfStates2(ctx, controlDB, ddlSQL, jobIDWatermark, "running", "queueing")
		log.Printf("ddl job resumed: job_id=%d", st.JobID)
	case "cancel":
		mustExec2(ctx, controlDB, fmt.Sprintf("admin cancel ddl jobs %d", st.JobID))
		log.Printf("ddl job cancel requested: job_id=%d", st.JobID)
	case "pause-resume-cancel":
		mustExec2(ctx, controlDB, fmt.Sprintf("admin pause ddl jobs %d", st.JobID))
		waitForExactState2(ctx, controlDB, ddlSQL, jobIDWatermark, "paused")
		log.Printf("ddl job paused: job_id=%d", st.JobID)
		sleepOrDie2(ctx, *controlPause2)
		mustExec2(ctx, controlDB, fmt.Sprintf("admin resume ddl jobs %d", st.JobID))
		waitForOneOfStates2(ctx, controlDB, ddlSQL, jobIDWatermark, "running", "queueing")
		log.Printf("ddl job resumed: job_id=%d", st.JobID)
		st = waitForControlWindow2(ctx, controlDB, ddlSQL, jobIDWatermark, st.JobID)
		log.Printf("post-resume control window sees: job_id=%d state=%s schema_state=%s row_count=%d", st.JobID, st.State, st.SchemaState, st.RowCount)
		mustExec2(ctx, controlDB, fmt.Sprintf("admin cancel ddl jobs %d", st.JobID))
		log.Printf("ddl job cancel requested after pause-resume: job_id=%d", st.JobID)
	default:
		log.Fatalf("unknown control mode: %s", *controlMode2)
	}
}

func waitForControlWindow2(ctx context.Context, db *sql.DB, ddlSQL string, jobIDWatermark int64, expectedJobID int64) ddlState2 {
	ticker := time.NewTicker(defaultModifyJobCheck)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait control window: %v", ctx.Err())
		case <-ticker.C:
		}
		st, ok := lookupDDLState2(ctx, db, ddlSQL, jobIDWatermark)
		if !ok {
			continue
		}
		if st.JobID != expectedJobID {
			continue
		}
		if isTerminalState2(st.State) {
			log.Fatalf("ddl became terminal before control action: job_id=%d state=%s schema_state=%s", st.JobID, st.State, st.SchemaState)
		}
		if st.State != "running" {
			continue
		}
		if st.RowCount < *controlMinRows2 {
			continue
		}
		return st
	}
}

func waitForExactState2(ctx context.Context, db *sql.DB, ddlSQL string, jobIDWatermark int64, want string) {
	ticker := time.NewTicker(defaultModifyJobCheck)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait exact state %s: %v", want, ctx.Err())
		case <-ticker.C:
		}
		st, ok := lookupDDLState2(ctx, db, ddlSQL, jobIDWatermark)
		if !ok {
			continue
		}
		if st.State == want {
			return
		}
	}
}

func waitForOneOfStates2(ctx context.Context, db *sql.DB, ddlSQL string, jobIDWatermark int64, wants ...string) {
	ticker := time.NewTicker(defaultModifyJobCheck)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait one-of states %v: %v", wants, ctx.Err())
		case <-ticker.C:
		}
		st, ok := lookupDDLState2(ctx, db, ddlSQL, jobIDWatermark)
		if !ok {
			continue
		}
		for _, want := range wants {
			if st.State == want {
				return
			}
		}
	}
}

func deleteCurrentOwner2(ctx context.Context, db *sql.DB) {
	ownerPod := currentDDLOwnerPod2(ctx, db)
	oldUID := currentPodUID2(ctx, ownerPod)
	log.Printf("delete current owner pod %s uid=%s", ownerPod, oldUID)
	deleteNamedPod2(ctx, ownerPod)
	waitForOwnerAfterDeletion2(ctx, db, ownerPod, oldUID)
}

func currentDDLOwnerPod2(ctx context.Context, db *sql.DB) string {
	for {
		rows, err := db.QueryContext(ctx, "admin show ddl")
		if err != nil {
			if ctx.Err() != nil {
				log.Fatalf("admin show ddl: %v", err)
			}
			time.Sleep(2 * time.Second)
			continue
		}
		if !rows.Next() {
			_ = rows.Close()
			time.Sleep(2 * time.Second)
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
		if err := rows.Scan(&schemaVer, &ownerID, &ownerAddress, &runningJobs, &selfID, &query); err != nil {
			_ = rows.Close()
			time.Sleep(2 * time.Second)
			continue
		}
		_ = rows.Close()
		host := strings.Split(ownerAddress, ":")[0]
		pod := strings.Split(host, ".")[0]
		log.Printf("current ddl owner: %s", pod)
		return pod
	}
}

func currentPodUID2(ctx context.Context, pod string) string {
	uid, ready, phase := podStatus2(ctx, pod)
	log.Printf("pod %s uid=%s phase=%s ready=%v", pod, uid, phase, ready)
	return uid
}

func deletePodSequence2(ctx context.Context) []string {
	pods := buildFaultPodSequence2(ctx, nil, false)
	for i, pod := range pods {
		bounce := i + 1
		prevUID := currentPodUID2(ctx, pod)
		log.Printf("delete-pod fault %d/%d start: pod=%s uid=%s", bounce, len(pods), pod, prevUID)
		deleteNamedPod2(ctx, pod)
		if *waitPodReady2 {
			nextUID := waitForNewReadyPod2(ctx, pod, prevUID)
			log.Printf("delete-pod fault %d/%d done: pod=%s new_uid=%s", bounce, len(pods), pod, nextUID)
		}
		if bounce < len(pods) {
			select {
			case <-ctx.Done():
				log.Fatalf("delete-pod fault schedule interrupted: %v", ctx.Err())
			case <-time.After(*bounceInterval2):
			}
		}
	}
	return uniquePods2(pods)
}

func buildFaultPodSequence2(ctx context.Context, db *sql.DB, allowOwnerDefault bool) []string {
	if strings.TrimSpace(*faultPods2) != "" {
		raw := strings.Split(*faultPods2, ",")
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
	if pod := strings.TrimSpace(*faultPod2); pod != "" {
		return []string{pod}
	}
	if allowOwnerDefault {
		return []string{currentDDLOwnerPod2(ctx, db)}
	}
	log.Fatalf("fault mode %s requires -fault-pod or -fault-pods", *faultMode2)
	return nil
}

func applyNetworkPartition2(ctx context.Context, db *sql.DB) {
	sourcePods := uniquePods2(buildFaultPodSequence2(ctx, db, true))
	targetComponent := normalizedNetworkTarget2()
	name := fmt.Sprintf("ai-native-modify-netpart-%d", time.Now().UnixNano())
	yaml := renderNetworkPartition2(name, sourcePods, targetComponent)
	cmd := exec.CommandContext(ctx, "kubectl",
		"--kubeconfig", *kubeconfig2,
		"apply",
		"-f",
		"-",
	)
	cmd.Stdin = strings.NewReader(yaml)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Fatalf("apply network chaos %s: %v output=%s", name, err, strings.TrimSpace(string(out)))
	}
	activeNetworkChaosName2 = name
	log.Printf("network-partition fault applied: name=%s duration=%s direction=%s source_pods=%v target_component=%s", name, networkDuration2.String(), *networkDir2, sourcePods, targetComponent)
}

func renderNetworkPartition2(name string, sourcePods []string, targetComponent string) string {
	var source bytes.Buffer
	for _, pod := range sourcePods {
		source.WriteString("        - ")
		source.WriteString(pod)
		source.WriteByte('\n')
	}
	return fmt.Sprintf(`apiVersion: chaos-mesh.org/v1alpha1
kind: NetworkChaos
metadata:
  name: %s
  namespace: %s
spec:
  action: partition
  mode: all
  selector:
    pods:
      %s:
%s  direction: %s
  target:
    mode: all
    selector:
      namespaces:
      - %s
      labelSelectors:
        app.kubernetes.io/component: %s
        app.kubernetes.io/instance: tc
  duration: %s
`, name, *namespace2, *namespace2, source.String(), *networkDir2, *namespace2, targetComponent, networkDuration2.String())
}

func normalizedNetworkTarget2() string {
	component := strings.ToLower(strings.TrimSpace(*networkTarget2))
	switch component {
	case "tidb", "tikv", "pd":
		return component
	default:
		log.Fatalf("unsupported -network-target-component: %s", *networkTarget2)
		return ""
	}
}

func deleteNetworkChaos2(ctx context.Context, name string) {
	cmd := exec.CommandContext(ctx, "kubectl",
		"--kubeconfig", *kubeconfig2,
		"-n", *namespace2,
		"delete", "networkchaos", name,
		"--ignore-not-found=true",
		"--wait=false",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Fatalf("delete network chaos %s: %v output=%s", name, err, strings.TrimSpace(string(out)))
	}
	log.Printf("network-partition fault deleted: name=%s", name)
	activeNetworkChaosName2 = ""
}

func uniquePods2(pods []string) []string {
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

func deleteNamedPod2(ctx context.Context, pod string) {
	cmd := exec.CommandContext(ctx, "kubectl",
		"--kubeconfig", *kubeconfig2,
		"-n", *namespace2,
		"delete", "pod", pod,
		"--force",
		"--grace-period=0",
		"--wait=false",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Fatalf("delete pod %s: %v output=%s", pod, err, strings.TrimSpace(string(out)))
	}
}

func podStatus2(ctx context.Context, pod string) (string, bool, string) {
	cmd := exec.CommandContext(ctx, "kubectl",
		"--kubeconfig", *kubeconfig2,
		"-n", *namespace2,
		"get", "pod", pod,
		"-o", "jsonpath={.metadata.uid}|{.status.phase}|{.status.containerStatuses[0].ready}",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", false, ""
	}
	parts := strings.Split(strings.TrimSpace(string(out)), "|")
	if len(parts) != 3 {
		return "", false, ""
	}
	ready, _ := strconv.ParseBool(parts[2])
	return parts[0], ready, parts[1]
}

func waitForNewReadyPod2(ctx context.Context, pod string, oldUID string) string {
	ticker := time.NewTicker(defaultModifyPodCheck)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait new pod ready: %v", ctx.Err())
		case <-ticker.C:
		}
		uid, ready, phase := podStatus2(ctx, pod)
		log.Printf("fault pod %s status: uid=%s phase=%s ready=%v", pod, uid, phase, ready)
		if uid != "" && uid != oldUID && ready && phase == "Running" {
			return uid
		}
	}
}

func waitForCurrentReadyPod2(ctx context.Context, pod string) {
	ticker := time.NewTicker(defaultModifyPodCheck)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait current pod ready: %v", ctx.Err())
		case <-ticker.C:
		}
		uid, ready, phase := podStatus2(ctx, pod)
		log.Printf("fault pod %s recovery status: uid=%s phase=%s ready=%v", pod, uid, phase, ready)
		if uid != "" && ready && phase == "Running" {
			return
		}
	}
}

func waitForPodsRecovery2(ctx context.Context, pods []string) {
	for _, pod := range pods {
		waitForCurrentReadyPod2(ctx, pod)
	}
}

func waitForOwnerAfterDeletion2(ctx context.Context, db *sql.DB, deletedOwnerPod string, deletedUID string) {
	ticker := time.NewTicker(defaultModifyPodCheck)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait owner after deletion: %v", ctx.Err())
		case <-ticker.C:
		}
		if owner, ok := tryCurrentOwner2(ctx, db); ok && owner != deletedOwnerPod {
			log.Printf("owner handoff observed: old=%s new=%s", deletedOwnerPod, owner)
			return
		}
		uid, ready, phase := podStatus2(ctx, deletedOwnerPod)
		log.Printf("deleted owner pod %s status: uid=%s phase=%s ready=%v", deletedOwnerPod, uid, phase, ready)
		if uid != "" && uid != deletedUID && ready {
			log.Printf("deleted owner pod restarted; continue")
			return
		}
	}
}

func tryCurrentOwner2(ctx context.Context, db *sql.DB) (string, bool) {
	rows, err := db.QueryContext(ctx, "admin show ddl")
	if err != nil {
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
		return "", false
	}
	host := strings.Split(ownerAddress, ":")[0]
	return strings.Split(host, ".")[0], true
}

func waitForTerminal2(ctx context.Context, db *sql.DB, ddlSQL string, jobIDWatermark int64) ddlState2 {
	ticker := time.NewTicker(defaultModifyJobCheck)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Fatalf("wait terminal ddl: %v", ctx.Err())
		case <-ticker.C:
		}
		st, ok := lookupDDLState2(ctx, db, ddlSQL, jobIDWatermark)
		if !ok {
			continue
		}
		log.Printf("ddl terminal wait sees: job_id=%d state=%s schema_state=%s row_count=%d", st.JobID, st.State, st.SchemaState, st.RowCount)
		if isTerminalState2(st.State) {
			return st
		}
	}
}

func latestMatchingJobID2(ctx context.Context, db *sql.DB, ddlSQL string) int64 {
	var jobID sql.NullInt64
	err := db.QueryRowContext(ctx, `
select max(job_id)
from information_schema.ddl_jobs
where db_name = ? and table_name = ? and query = ?`,
		defaultModifySchema, defaultModifyTable, ddlSQL).Scan(&jobID)
	if err != nil {
		log.Fatalf("lookup latest matching job id: %v", err)
	}
	if !jobID.Valid {
		return 0
	}
	return jobID.Int64
}

func lookupDDLState2(ctx context.Context, db *sql.DB, ddlSQL string, jobIDWatermark int64) (ddlState2, bool) {
	rows, err := db.QueryContext(ctx, `
select job_id, state, schema_state, row_count
from information_schema.ddl_jobs
where db_name = ? and table_name = ? and query = ?
  and job_id > ?
order by job_id desc
limit 1`, defaultModifySchema, defaultModifyTable, ddlSQL, jobIDWatermark)
	if err != nil {
		return ddlState2{}, false
	}
	defer rows.Close()
	if !rows.Next() {
		return ddlState2{}, false
	}
	var st ddlState2
	if err := rows.Scan(&st.JobID, &st.State, &st.SchemaState, &st.RowCount); err != nil {
		return ddlState2{}, false
	}
	return st, true
}

func isTerminalState2(state string) bool {
	switch state {
	case "done", "synced", "rollback done", "cancelled":
		return true
	default:
		return false
	}
}

func matchesSchemaState2(schemaState, required string) bool {
	required = strings.TrimSpace(required)
	if required == "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(schemaState), required)
}

func runFinalOracle2(ctx context.Context, db *sql.DB, state ddlState2, probeIDs []int) {
	successPath := state.State == "done" || state.State == "synced"
	rollbackPath := state.State == "rollback done" || state.State == "cancelled"
	if !successPath && !rollbackPath {
		log.Fatalf("unexpected terminal state for modify-column probe: %s", state.State)
	}
	cols := currentValueColumns2(ctx, db)
	if successPath {
		if cols[0] != "a" || cols[1] != "y" {
			log.Fatalf("unexpected success-path columns: got=%v want=[a y]", cols)
		}
	}
	if rollbackPath {
		if cols[0] != "x" || cols[1] != "y" {
			log.Fatalf("unexpected rollback-path columns: got=%v want=[x y]", cols)
		}
	}
	mustExec2(ctx, db, "admin check table "+defaultModifySchema+"."+defaultModifyTable)
	totalRows := queryInt2(ctx, db, "select count(*) from "+defaultModifySchema+"."+defaultModifyTable)
	if totalRows != *targetRows2 {
		log.Fatalf("row count drift after modify-column probe: got=%d want=%d", totalRows, *targetRows2)
	}
	bad := formulaMismatchCount2(ctx, db, cols[0], cols[1])
	if bad != 0 {
		log.Fatalf("formula mismatch after owner fault: bad_count=%d cols=%v", bad, cols)
	}
	probePointRows2(ctx, db, cols[0], cols[1], probeIDs)
	probeIndexPath2(ctx, db, cols[0], probeIDs)
	probeDeleteRollback2(ctx, db, probeIDs)
	log.Printf("final oracle green, cols=%v probe_ids=%v", cols, probeIDs)
}

func currentValueColumns2(ctx context.Context, db *sql.DB) [2]string {
	cols, ok := tryCurrentValueColumns2(ctx, db)
	if !ok {
		log.Fatalf("current columns unavailable without transient error")
	}
	return cols
}

func tryCurrentValueColumns2(ctx context.Context, db *sql.DB) ([2]string, bool) {
	rows, err := db.QueryContext(ctx, `
select column_name
from information_schema.columns
where table_schema = ? and table_name = ? and ordinal_position in (2,3)
order by ordinal_position`, defaultModifySchema, defaultModifyTable)
	if err != nil {
		if isTransientSQLErr2(err) {
			return [2]string{}, false
		}
		log.Fatalf("current columns: %v", err)
	}
	defer rows.Close()
	var cols [2]string
	i := 0
	for rows.Next() {
		if err := rows.Scan(&cols[i]); err != nil {
			if isTransientSQLErr2(err) {
				return [2]string{}, false
			}
			log.Fatalf("scan current columns: %v", err)
		}
		i++
	}
	if i != 2 {
		return [2]string{}, false
	}
	return cols, true
}

func formulaMismatchCount2(ctx context.Context, db *sql.DB, col1, col2 string) int {
	sqlText := fmt.Sprintf(`
select count(*)
from %s.%s
where %s is null
   or cast(%s as char) != cast(mod(id,10000) as char)
   or %s != mod(id,10000) * 2`,
		defaultModifySchema, defaultModifyTable, col1, col1, col2)
	return queryInt2(ctx, db, sqlText)
}

func probePointRows2(ctx context.Context, db *sql.DB, col1, col2 string, ids []int) {
	if err := probePointRowsCore2(ctx, db, col1, col2, ids, false); err != nil {
		log.Fatalf("point-row oracle failed: %v", err)
	}
}

func probePointRowsLive2(ctx context.Context, db *sql.DB, col1, col2 string, ids []int) {
	if err := probePointRowsCore2(ctx, db, col1, col2, ids, true); err != nil {
		if isTransientSQLErr2(err) {
			return
		}
		log.Fatalf("live point-row oracle failed: %v", err)
	}
}

func probePointRowsCore2(ctx context.Context, db *sql.DB, col1, col2 string, ids []int, tolerateTransient bool) error {
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	sqlText := fmt.Sprintf(`
select id, cast(%s as char), %s
from %s.%s
where id in (%s)
order by id`, col1, col2, defaultModifySchema, defaultModifyTable, placeholders)
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		if tolerateTransient && isTransientSQLErr2(err) {
			return err
		}
		return err
	}
	defer rows.Close()

	seen := make([]int, 0, len(ids))
	for rows.Next() {
		var (
			id int
			v1 string
			v2 int
		)
		if err := rows.Scan(&id, &v1, &v2); err != nil {
			if tolerateTransient && isTransientSQLErr2(err) {
				return err
			}
			return err
		}
		if v1 != strconv.Itoa(id%10000) || v2 != (id%10000)*2 {
			return fmt.Errorf("point-row oracle mismatch: id=%d got=(%s,%d)", id, v1, v2)
		}
		seen = append(seen, id)
	}
	if len(seen) != len(ids) {
		return fmt.Errorf("point-row oracle missing rows: seen=%v want=%v", seen, ids)
	}
	return nil
}

func probeIndexPath2(ctx context.Context, db *sql.DB, changedCol string, ids []int) {
	for _, id := range ids {
		var (
			indexCnt int
			tableCnt int
		)
		var (
			indexSQL string
			tableSQL string
			arg      any
		)
		switch changedCol {
		case "a":
			indexSQL = fmt.Sprintf("select count(*) from %s.%s use index(idx_x) where %s = ?",
				defaultModifySchema, defaultModifyTable, changedCol)
			tableSQL = fmt.Sprintf("select count(*) from %s.%s ignore index(idx_x) where %s = ?",
				defaultModifySchema, defaultModifyTable, changedCol)
			arg = strconv.Itoa(id % 10000)
		case "x":
			indexSQL = fmt.Sprintf("select count(*) from %s.%s use index(idx_x) where %s = ?",
				defaultModifySchema, defaultModifyTable, changedCol)
			tableSQL = fmt.Sprintf("select count(*) from %s.%s ignore index(idx_x) where %s = ?",
				defaultModifySchema, defaultModifyTable, changedCol)
			arg = id % 10000
		default:
			log.Fatalf("unexpected first value column for index-path oracle: %s", changedCol)
		}
		if err := db.QueryRowContext(ctx, indexSQL, arg).Scan(&indexCnt); err != nil {
			log.Fatalf("index-path oracle failed for id=%d: %v", id, err)
		}
		if err := db.QueryRowContext(ctx, tableSQL, arg).Scan(&tableCnt); err != nil {
			log.Fatalf("table-path oracle failed for id=%d: %v", id, err)
		}
		if indexCnt != tableCnt || indexCnt == 0 {
			log.Fatalf("index-path oracle mismatch for id=%d: index_cnt=%d table_cnt=%d", id, indexCnt, tableCnt)
		}
	}
}

func probeDeleteRollback2(ctx context.Context, db *sql.DB, ids []int) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Fatalf("begin probe delete tx: %v", err)
	}
	defer tx.Rollback()

	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	sqlText := fmt.Sprintf("delete from %s.%s where id in (%s)", defaultModifySchema, defaultModifyTable, placeholders)
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	res, err := tx.ExecContext(ctx, sqlText, args...)
	if err != nil {
		log.Fatalf("delete rollback oracle failed: %v", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		log.Fatalf("delete rollback rows affected: %v", err)
	}
	if affected != int64(len(ids)) {
		log.Fatalf("delete rollback oracle affected unexpected rows: got=%d want=%d", affected, len(ids))
	}
	if err := tx.Rollback(); err != nil {
		log.Fatalf("delete rollback final rollback failed: %v", err)
	}
}

func execRetry2(ctx context.Context, db *sql.DB, sqlText string, duplicateMeansSuccess bool, args ...any) error {
	for {
		_, err := db.ExecContext(ctx, sqlText, args...)
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
			time.Sleep(2 * time.Second)
			continue
		}
		if isTransientSQLErr2(err) {
			log.Printf("dml exec retry after transient error: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		return err
	}
}

func mustExec2(ctx context.Context, db *sql.DB, sqlText string) {
	mustExecArgs2(ctx, db, sqlText)
}

func mustExecArgs2(ctx context.Context, db *sql.DB, sqlText string, args ...any) {
	if _, err := db.ExecContext(ctx, sqlText, args...); err != nil {
		log.Fatalf("exec failed: %s: %v", sqlText, err)
	}
}

func queryInt2(ctx context.Context, db *sql.DB, sqlText string) int {
	for {
		var v int
		err := db.QueryRowContext(ctx, sqlText).Scan(&v)
		if err == nil {
			return v
		}
		if ctx.Err() != nil {
			log.Fatalf("query int failed: %s: %v", sqlText, err)
		}
		if isTransientSQLErr2(err) {
			log.Printf("query int retry after transient error: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		log.Fatalf("query int failed permanently: %s: %v", sqlText, err)
	}
}

func isTransientSQLErr2(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unknown column") ||
		strings.Contains(msg, "information schema is changed") ||
		strings.Contains(msg, "has changed") ||
		strings.Contains(msg, "invalid connection") ||
		strings.Contains(msg, "region is unavailable") ||
		strings.Contains(msg, "region unavailable") ||
		strings.Contains(msg, "not leader") ||
		strings.Contains(msg, "epoch not match") ||
		strings.Contains(msg, "server is busy") ||
		strings.Contains(msg, "tikv server timeout")
}

func sleepWithContext2(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func sleepOrDie2(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
		log.Fatalf("sleep interrupted: %v", ctx.Err())
	case <-time.After(d):
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
