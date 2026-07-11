package addindextest_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/pingcap/failpoint"
	"github.com/pingcap/tidb/pkg/config/kerneltype"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/testkit/testfailpoint"
	"github.com/pingcap/tidb/tests/realtikvtest"
	"github.com/stretchr/testify/require"
)

func TestAINativeRealTiKVMVIMergeOwnerHomogeneityProbe(t *testing.T) {
	if kerneltype.IsNextGen() {
		t.Skip("probe targets classic txn-merge add-index path")
	}

	store := realtikvtest.CreateMockStoreAndSetup(t)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("set @@global.tidb_ddl_enable_fast_reorg = off")
	tk.MustExec("set @@global.tidb_enable_dist_task = off")
	tk.MustExec("set @@global.tidb_ddl_reorg_worker_cnt = 1")
	tk.MustExec("set @@global.tidb_ddl_reorg_batch_size = 16")
	tk.MustExec("create table t(a int primary key, b int, j json)")

	for start := 1; start <= 2048; start += 128 {
		end := start + 128
		if end > 2049 {
			end = 2049
		}
		vals := make([]string, 0, end-start)
		for i := start; i < end; i++ {
			vals = append(vals, fmt.Sprintf("(%d,%d,'[%d,%d]')", i, i, i, i+1000000))
		}
		tk.MustExec("insert into t values " + strings.Join(vals, ","))
	}

	tk1 := testkit.NewTestKit(t, store)
	tk1.MustExec("use test")

	require.NoError(t, failpoint.Enable("github.com/pingcap/tidb/pkg/ddl/mockBackfillSlow", "return"))
	t.Cleanup(func() {
		require.NoError(t, failpoint.Disable("github.com/pingcap/tidb/pkg/ddl/mockBackfillSlow"))
	})

	var (
		mergeDMLOnce  sync.Once
		concurrentErr error
	)
	testfailpoint.EnableCall(t, "github.com/pingcap/tidb/pkg/ddl/beforeBackfillMerge", func() {
		mergeDMLOnce.Do(func() {
			concurrentErr = tk1.ExecToErr("update t set b = b + 7 where a = 1900")
		})
	})

	ddlErr := tk.ExecToErr(
		"alter table t " +
			"add unique index u_mvi((cast(j as signed array))), " +
			"add unique index u_ab(a, b)",
	)

	require.NoError(t, concurrentErr)
	require.NoError(t, ddlErr)
	require.NoError(t, tk.ExecToErr("admin check table t"))
	tk.MustQuery("select a from t ignore index(u_mvi) where 1900 member of (j) order by a").Check(testkit.Rows("1900"))
	tk.MustQuery("select /*+ use_index(t, u_mvi) */ a from t where 1900 member of (j) order by a").Check(testkit.Rows("1900"))
	tk.MustQuery("select a, b from t where a = 1900").Check(testkit.Rows("1900 1907"))
}
