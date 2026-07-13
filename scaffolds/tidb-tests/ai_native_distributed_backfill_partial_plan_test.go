package ddl_test

// This focused scaffold is intended to live beside backfilling_dist_scheduler_test.go, where it
// reuses createAddIndexTask and backfillingSchedulerParamForTest. The production call to allocNewTS
// is exposed through a test-only setter so the second planner allocation can fail deterministically.

import (
	"context"
	"fmt"
	"testing"

	"github.com/pingcap/tidb/pkg/ddl"
	"github.com/pingcap/tidb/pkg/dxf/framework/proto"
	"github.com/pingcap/tidb/pkg/dxf/framework/scheduler"
	"github.com/pingcap/tidb/pkg/kv"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/tablecodec"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/stretchr/testify/require"
	"github.com/tikv/client-go/v2/tikv"
	"github.com/tikv/client-go/v2/util"
	"go.uber.org/mock/gomock"
)

func TestAINativeReadIndexPlanRejectsPartialMetasOnTSOFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store, dom := testkit.CreateMockStoreAndDomain(t)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("create table ai_partial_plan (id bigint primary key, v int)")
	tk.MustExec("insert into ai_partial_plan values (1, 1), (1999, 1999)")
	tbl, err := dom.InfoSchema().TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("ai_partial_plan"))
	require.NoError(t, err)
	tableID := tbl.Meta().ID
	splitKey := tablecodec.EncodeRowKeyWithHandle(tableID, kv.IntHandle(1000))
	cache := store.(interface{ GetRegionCache() *tikv.RegionCache }).GetRegionCache()
	loc, err := cache.LocateKey(tikv.NewBackoffer(context.Background(), 5000), splitKey)
	require.NoError(t, err)
	_, err = store.(kv.SplittableStore).SplitRegions(context.Background(), [][]byte{splitKey}, false, &tableID)
	require.NoError(t, err)
	cache.InvalidateCachedRegion(loc.Region)

	ext, err := ddl.NewBackfillingSchedulerForTest(dom.DDL())
	require.NoError(t, err)
	lit := ext.(*ddl.LitBackfillScheduler)
	lit.GlobalSort = true
	lit.BaseScheduler = &scheduler.BaseScheduler{Param: backfillingSchedulerParamForTest(ctrl, store, dom.SysSessionPool())}
	task, _ := createAddIndexTask(t, dom, "test", "ai_partial_plan", proto.Backfill, false)
	task.Step = ext.GetNextStep(&task.TaskBase)
	ctx := util.WithInternalSourceType(context.Background(), "backfill")
	execIDs := []string{":4000", ":4001"}
	control, err := ext.OnNextSubtasksBatch(ctx, nil, task, execIDs, task.Step)
	require.NoError(t, err)
	require.Len(t, control, 2)

	calls := 0
	restore := ddl.SetAllocNewTSForBackfillForTest(func(context.Context, kv.StorageWithPD) (uint64, error) {
		calls++
		if calls == 2 {
			return 0, fmt.Errorf("injected TSO failure")
		}
		return uint64(calls), nil
	})
	t.Cleanup(restore)
	metas, err := ext.OnNextSubtasksBatch(ctx, nil, task, execIDs, task.Step)
	if err != nil {
		require.Empty(t, metas)
		return
	}
	require.Len(t, metas, len(control), "a recovered retry must rebuild the complete plan")
}
