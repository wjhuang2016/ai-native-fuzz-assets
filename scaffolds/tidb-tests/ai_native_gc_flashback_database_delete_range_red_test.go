package sessiontest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/kv"
	"github.com/pingcap/tidb/pkg/store/gcworker"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/testkit/testfailpoint"
	"github.com/pingcap/tidb/tests/realtikvtest"
	"github.com/stretchr/testify/require"
)

// Requires InjectCall hooks after GC reads tidb_gc_enable and after recover-schema
// validates its snapshot, plus overrideGCPrepareTarget in calcNewTxnSafePoint.
func TestAINativeFlashbackDatabaseCanPublishAfterGCDeletesItsRanges(t *testing.T) {
	require.True(t, *realtikvtest.WithRealTiKV)
	store, dom := realtikvtest.CreateMockStoreAndDomainAndSetup(t)
	tk := testkit.NewTestKit(t, store)

	tk.MustExec("SET GLOBAL tidb_gc_enable = OFF")
	tk.MustExec(`
		UPDATE mysql.tidb
		SET variable_value = '20000101-00:00:00.000 +0800'
		WHERE variable_name = 'tikv_gc_last_run_time'
	`)
	tk.MustExec("DROP DATABASE IF EXISTS ai_gc_flashback")
	tk.MustExec("CREATE DATABASE ai_gc_flashback")
	tk.MustExec("CREATE TABLE ai_gc_flashback.t (id BIGINT PRIMARY KEY, u BIGINT UNIQUE)")
	for i := 1; i <= 64; i++ {
		tk.MustExec("INSERT INTO ai_gc_flashback.t VALUES (?, ?)", i, i+1000)
	}
	tk.MustQuery("SELECT COUNT(*), SUM(id), SUM(u) FROM ai_gc_flashback.t").
		Check(testkit.Rows("64 2080 66080"))
	tk.MustExec("DROP DATABASE ai_gc_flashback")
	dropJob := tk.MustQuery("ADMIN SHOW DDL JOBS 1").Rows()
	require.Len(t, dropJob, 1)
	require.Equal(t, "drop schema", dropJob[0][3])
	dropJobID := dropJob[0][0]
	require.Eventually(t, func() bool {
		rows := tk.MustQuery(
			"SELECT COUNT(*) FROM mysql.gc_delete_range WHERE job_id = ?",
			dropJobID,
		).Rows()
		return rows[0][0].(string) != "0"
	}, 10*time.Second, 50*time.Millisecond)

	targetVersion, err := store.CurrentVersion(kv.GlobalTxnScope)
	require.NoError(t, err)
	targetTS := targetVersion.Ver

	worker, err := gcworker.NewWorkerForAINativeTest(store, dom.GetPDClient())
	require.NoError(t, err)
	gcCtx := kv.WithInternalSourceType(context.Background(), kv.InternalTxnGC)

	gcReached := make(chan struct{})
	gcRelease := make(chan struct{})
	var gcOnce, gcReleaseOnce sync.Once
	releaseGC := func() {
		gcReleaseOnce.Do(func() { close(gcRelease) })
	}
	t.Cleanup(releaseGC)
	testfailpoint.EnableCall(t,
		"github.com/pingcap/tidb/pkg/store/gcworker/afterCheckGCEnable",
		func() {
			gcOnce.Do(func() { close(gcReached) })
			<-gcRelease
		})
	testfailpoint.EnableCall(t,
		"github.com/pingcap/tidb/pkg/store/gcworker/overrideGCPrepareTarget",
		func(target *uint64) {
			*target = targetTS
		})

	recoverReached := make(chan struct{})
	recoverRelease := make(chan struct{})
	var recoverOnce, recoverReleaseOnce sync.Once
	releaseRecover := func() {
		recoverReleaseOnce.Do(func() { close(recoverRelease) })
	}
	t.Cleanup(releaseRecover)
	testfailpoint.EnableCall(t,
		"github.com/pingcap/tidb/pkg/ddl/afterRecoverSchemaCheckSafePoint",
		func() {
			recoverOnce.Do(func() { close(recoverReached) })
			<-recoverRelease
		})

	tk.MustExec("SET GLOBAL tidb_gc_enable = ON")
	type prepareResult struct {
		ok        bool
		safePoint uint64
		err       error
	}
	prepareCh := make(chan prepareResult, 1)
	go func() {
		ok, safePoint, err := worker.PrepareForAINativeTest(gcCtx)
		prepareCh <- prepareResult{ok: ok, safePoint: safePoint, err: err}
	}()
	select {
	case <-gcReached:
	case <-time.After(10 * time.Second):
		t.Fatal("GC prepare did not reach the post-enable barrier")
	}

	recoverTK := testkit.NewTestKit(t, store)
	recoverCh := make(chan error, 1)
	go func() {
		_, err := recoverTK.Exec("FLASHBACK DATABASE ai_gc_flashback")
		recoverCh <- err
	}()
	select {
	case <-recoverReached:
	case err := <-recoverCh:
		t.Fatalf("flashback returned before the safe-point barrier: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("flashback did not pass the old safe-point check")
	}
	tk.MustQuery("SELECT @@GLOBAL.tidb_gc_enable").Check(testkit.Rows("0"))

	releaseGC()
	prepare := <-prepareCh
	require.NoError(t, prepare.err)
	require.True(t, prepare.ok)
	require.Equal(t, targetTS, prepare.safePoint)

	require.NoError(t, worker.RunJobForAINativeTest(gcCtx, prepare.safePoint))
	tk.MustQuery(
		"SELECT COUNT(*) FROM mysql.gc_delete_range WHERE job_id = ?",
		dropJobID,
	).Check(testkit.Rows("0"))

	releaseRecover()
	select {
	case err := <-recoverCh:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("flashback did not finish after GC deleted the ranges")
	}

	freshTK := testkit.NewTestKit(t, store)
	freshTK.MustQuery(
		"SELECT COUNT(*), COALESCE(SUM(id), 0), COALESCE(SUM(u), 0) FROM ai_gc_flashback.t",
	).Check(testkit.Rows("0 0 0"))
	freshTK.MustExec("ADMIN CHECK TABLE ai_gc_flashback.t")
	require.Eventually(t, func() bool {
		rows := freshTK.MustQuery(
			"SELECT COUNT(*) FROM mysql.gc_delete_range_done WHERE job_id = ?",
			dropJobID,
		).Rows()
		return rows[0][0].(string) != "0"
	}, 10*time.Second, 50*time.Millisecond)
}
