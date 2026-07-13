## Bug Report

### 1. Minimal reproduce step

At current master, add a test seam around the TSO allocation used by
`generatePlanForPhysicalTable`:

```diff
diff --git a/pkg/ddl/backfilling_dist_scheduler.go b/pkg/ddl/backfilling_dist_scheduler.go
--- a/pkg/ddl/backfilling_dist_scheduler.go
+++ b/pkg/ddl/backfilling_dist_scheduler.go
@@
-            importTS, err := allocNewTS(ctx, store.(kv.StorageWithPD))
+            importTS, err := allocNewTSForBackfill(ctx, store.(kv.StorageWithPD))
@@
+var allocNewTSForBackfill = allocNewTS
+
 func allocNewTS(ctx context.Context, store kv.StorageWithPD) (uint64, error) {
```

Add `pkg/ddl/ai_native_export_test.go`:

```go
package ddl

import (
	"context"

	"github.com/pingcap/tidb/pkg/kv"
)

func SetAllocNewTSForBackfillForTest(
	fn func(context.Context, kv.StorageWithPD) (uint64, error),
) func() {
	old := allocNewTSForBackfill
	allocNewTSForBackfill = fn
	return func() {
		allocNewTSForBackfill = old
	}
}
```

Then add this test to `pkg/ddl/backfilling_dist_scheduler_test.go` and add the imports
`fmt`, `pkg/tablecodec`, and `tikv/client-go/v2/tikv`:

```go
func TestReadIndexPlanRejectsPartialMetasOnTSOFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	store, dom := testkit.CreateMockStoreAndDomain(t)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("create table ai_partial_plan (id bigint primary key, v int)")
	tk.MustExec("insert into ai_partial_plan values (1, 1), (1999, 1999)")
	tbl, err := dom.InfoSchema().TableByName(
		context.Background(), ast.NewCIStr("test"), ast.NewCIStr("ai_partial_plan"))
	require.NoError(t, err)
	tableID := tbl.Meta().ID
	splitKey := tablecodec.EncodeRowKeyWithHandle(tableID, kv.IntHandle(1000))
	regionCache := store.(interface{ GetRegionCache() *tikv.RegionCache }).GetRegionCache()
	loc, err := regionCache.LocateKey(tikv.NewBackoffer(context.Background(), 5000), splitKey)
	require.NoError(t, err)
	_, err = store.(kv.SplittableStore).SplitRegions(
		context.Background(), [][]byte{splitKey}, false, &tableID)
	require.NoError(t, err)
	regionCache.InvalidateCachedRegion(loc.Region)

	sch, err := ddl.NewBackfillingSchedulerForTest(dom.DDL())
	require.NoError(t, err)
	litSch := sch.(*ddl.LitBackfillScheduler)
	litSch.GlobalSort = true
	litSch.BaseScheduler = &scheduler.BaseScheduler{
		Param: backfillingSchedulerParamForTest(ctrl, store, dom.SysSessionPool()),
	}
	task, server := createAddIndexTask(t, dom, "test", "ai_partial_plan", proto.Backfill, false)
	require.Nil(t, server)
	task.Step = sch.GetNextStep(&task.TaskBase)
	ctx := util.WithInternalSourceType(context.Background(), "backfill")
	execIDs := []string{":4000", ":4001"}

	controlMetas, err := sch.OnNextSubtasksBatch(ctx, nil, task, execIDs, task.Step)
	require.NoError(t, err)
	require.Len(t, controlMetas, 2, "control must prove the source range has two batches")

	allocCalls := 0
	restoreAlloc := ddl.SetAllocNewTSForBackfillForTest(
		func(context.Context, kv.StorageWithPD) (uint64, error) {
			allocCalls++
			if allocCalls == 2 {
				return 0, fmt.Errorf("injected TSO failure")
			}
			return uint64(allocCalls), nil
		})
	t.Cleanup(restoreAlloc)

	metas, err := sch.OnNextSubtasksBatch(ctx, nil, task, execIDs, task.Step)
	t.Logf("partial-plan oracle: err=%v metas=%d control=%d", err, len(metas), len(controlMetas))
	if err != nil {
		require.Empty(t, metas, "failed planning must not publish partial range metas")
		return
	}
	require.Len(t, metas, len(controlMetas),
		"a recovered retry must rebuild the complete plan")
}
```

Run:

```bash
go test -tags=intest ./pkg/ddl \
  -run '^TestReadIndexPlanRejectsPartialMetasOnTSOFailure$' -count=1 -v
```

The control returns two metas. The fault arm returns `err=nil, metas=1`, so the final assertion
fails.

Changing only the existing error branch from `return true, nil` to `return true, err` does not fix
the invariant. `RunWithRetry` retries, but the second attempt appends to the first attempt's retained
prefix and returns three metas. Resetting `subTaskMetas` at the beginning of every retry attempt in
addition to propagating the error makes the same test pass with exactly two metas.

This was also reproduced with real PD, TiKV, and two TiDB distributed-task executors. A nonpartition
table was split into 101 regions, which made the planner use two batches (100 and 1). Failing only the
second per-plan TSO allocation produced:

```text
DDL history: synced, max_node_count=2
table scan ids:       1, 100999
FORCE INDEX ids:      1
table count v=100999: 1
index count v=100999: 0
ADMIN CHECK TABLE:    ERROR 8223 for handle 100999
```

The same setup without the fault returned both rows through the table and index and passed
`ADMIN CHECK TABLE`.

### 2. What did you expect to see?

Planning should either fail without publishing metas, or recover by rebuilding a plan that covers
the complete source range exactly once. A successful `ADD INDEX` must not publish a partial index.

### 3. What did you see instead

`generatePlanForPhysicalTable` declares `subTaskMetas` outside `handle.RunWithRetry`. It appends one
meta per region batch, but a later `allocNewTS` error returns `(retryable=true, err=nil)`. The retry
helper therefore treats a nonempty prefix as success, and the caller checks only that the slice is
nonempty rather than proving complete source-range coverage.

The distributed ADD INDEX job can consequently finish `synced` while the new index silently omits
committed rows. Error propagation alone leaves another correctness problem because failed-attempt
metas survive into the next attempt.

### 4. What is your TiDB version?

Current master at `13282a8bd06bd33324a4dbfd3c1c03685f3cd9aa`.
