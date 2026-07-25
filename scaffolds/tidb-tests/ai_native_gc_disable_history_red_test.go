package sessiontest

import (
	"context"
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/kv"
	"github.com/pingcap/tidb/pkg/store/gcworker"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/testkit/testfailpoint"
	"github.com/pingcap/tidb/tests/realtikvtest"
	"github.com/stretchr/testify/require"
	"github.com/tikv/client-go/v2/oracle"
)

func writeGCVersion(t *testing.T, store kv.Storage, key, value []byte) uint64 {
	txn, err := store.Begin()
	require.NoError(t, err)
	require.NoError(t, txn.Set(key, value))
	require.NoError(t, txn.Commit(context.Background()))
	return txn.CommitTS()
}

func readGCVersion(t *testing.T, store kv.Storage, key []byte, ts uint64) string {
	value, err := store.GetSnapshot(kv.Version{Ver: ts}).Get(context.Background(), key)
	require.NoError(t, err)
	return string(value.Value)
}

func TestAINativeGCDisableAfterEnableCheckStillCollectsHistory(t *testing.T) {
	require.True(t, *realtikvtest.WithRealTiKV)
	store, dom := realtikvtest.CreateMockStoreAndDomainAndSetup(t)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("SET GLOBAL tidb_gc_enable = OFF")
	tk.MustExec("DELETE FROM mysql.tidb WHERE variable_name = 'tikv_gc_last_run_time'")

	worker, err := gcworker.NewWorkerForAINativeTest(store, dom.GetPDClient())
	require.NoError(t, err)
	gcCtx := kv.WithInternalSourceType(context.Background(), kv.InternalTxnGC)

	controlKey := []byte("ai-native-gc-disable-control")
	controlV1TS := writeGCVersion(t, store, controlKey, []byte("v1"))
	writeGCVersion(t, store, controlKey, []byte("v2"))
	ok, safePoint, err := worker.PrepareForAINativeTest(gcCtx)
	require.NoError(t, err)
	require.False(t, ok)
	require.Zero(t, safePoint)
	require.Equal(t, "v1", readGCVersion(t, store, controlKey, controlV1TS))

	raceKey := []byte("ai-native-gc-disable-red")
	savedSafePoint, err := worker.SavedSafePointForAINativeTest()
	require.NoError(t, err)
	require.NotNil(t, savedSafePoint)
	historyBaseTS := oracle.GoTimeToTS(savedSafePoint.Add(time.Millisecond))
	raceV1TS := historyBaseTS + 2
	raceV2TS := historyBaseTS + 4
	require.NoError(t, worker.WriteVersionAtForAINativeTest(
		gcCtx, raceKey, []byte("v1"), historyBaseTS+1, raceV1TS,
	))
	require.NoError(t, worker.WriteVersionAtForAINativeTest(
		gcCtx, raceKey, []byte("v2"), historyBaseTS+3, raceV2TS,
	))
	require.Equal(t, "v1", readGCVersion(t, store, raceKey, raceV1TS))
	tk.Session().Close()
	raceTK := testkit.NewTestKit(t, store)

	reached := make(chan struct{})
	release := make(chan struct{})
	testfailpoint.EnableCall(t,
		"github.com/pingcap/tidb/pkg/store/gcworker/afterCheckGCEnable",
		func() {
			close(reached)
			<-release
		})
	testfailpoint.EnableCall(t,
		"github.com/pingcap/tidb/pkg/store/gcworker/overrideGCPrepareTarget",
		func(target *uint64) {
			*target = raceV2TS
		})

	raceTK.MustExec("SET GLOBAL tidb_gc_enable = ON")
	type prepareResult struct {
		ok        bool
		safePoint uint64
		err       error
	}
	resultCh := make(chan prepareResult, 1)
	go func() {
		ok, safePoint, err := worker.PrepareForAINativeTest(gcCtx)
		resultCh <- prepareResult{ok: ok, safePoint: safePoint, err: err}
	}()

	<-reached
	raceTK.MustExec("SET GLOBAL tidb_gc_enable = OFF")
	raceTK.MustQuery("SELECT @@GLOBAL.tidb_gc_enable").Check(testkit.Rows("0"))
	close(release)

	result := <-resultCh
	require.NoError(t, result.err)
	require.True(t, result.ok)
	require.Greater(t, result.safePoint, raceV1TS)
	require.LessOrEqual(t, result.safePoint, raceV2TS)
	require.NoError(t, worker.RunJobForAINativeTest(gcCtx, result.safePoint))

	_, err = store.GetSnapshot(kv.Version{Ver: raceV1TS}).Get(context.Background(), raceKey)
	require.Error(t, err)
	require.Equal(t, "v2", readGCVersion(t, store, raceKey, raceV2TS))
	raceTK.MustQuery("SELECT @@GLOBAL.tidb_gc_enable").Check(testkit.Rows("0"))
}
