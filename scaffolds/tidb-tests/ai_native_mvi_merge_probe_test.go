package indexmergetest

import (
	"fmt"
	"sync"
	"testing"

	"github.com/pingcap/failpoint"
	"github.com/pingcap/tidb/pkg/ddl/ingest"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/testkit/testfailpoint"
	"github.com/stretchr/testify/require"
)

// This probe encodes the green contract for a multi-valued unique index added
// together with a sibling unique index. A single concurrent UPDATE during the
// temp-index merge window should not make the DDL fail and should not leave the
// final index/query state inconsistent.
func TestAINativeMVIMergeOwnerHomogeneityProbe(t *testing.T) {
	store, _ := testkit.CreateMockStoreAndDomain(t)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("set @@tidb_ddl_reorg_worker_cnt = 1")
	tk.MustExec("set @@tidb_ddl_reorg_batch_size = 16")

	// Force the txn-merge path instead of local ingest so the temp-index merge
	// logic under test is exercised deterministically in the local harness.
	ingest.LitInitialized = false
	testfailpoint.Enable(t, "github.com/pingcap/tidb/pkg/ddl/skipReorgWorkForTempIndex", "return(false)")
	t.Cleanup(func() {
		require.NoError(t, failpoint.Disable("github.com/pingcap/tidb/pkg/ddl/skipReorgWorkForTempIndex"))
	})

	tk.MustExec("create table t(a int primary key, b int, j json)")
	for i := 1; i <= 1024; i++ {
		tk.MustExec(fmt.Sprintf(
			"insert into t values (%d, %d, '[%d,%d]')",
			i, i, i, i+1000000,
		))
	}

	tk1 := testkit.NewTestKit(t, store)
	tk1.MustExec("use test")

	var (
		mergeDMLOnce sync.Once
		concurrentErr error
	)
	testfailpoint.EnableCall(t, "github.com/pingcap/tidb/pkg/ddl/beforeBackfillMerge", func() {
		mergeDMLOnce.Do(func() {
			concurrentErr = tk1.ExecToErr("update t set b = b + 7 where a = 900")
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
	tk.MustQuery("select a from t ignore index(u_mvi) where 900 member of (j) order by a").Check(testkit.Rows("900"))
	tk.MustQuery("select /*+ use_index(t, u_mvi) */ a from t where 900 member of (j) order by a").Check(testkit.Rows("900"))
	tk.MustQuery("select a, b from t where a = 900").Check(testkit.Rows("900 907"))
}
