package addindextest_test

import (
	"testing"

	"github.com/pingcap/tidb/pkg/config/kerneltype"
	"github.com/pingcap/tidb/pkg/ddl/ingest"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/testkit/testfailpoint"
	"github.com/pingcap/tidb/tests/realtikvtest"
)

// This is a local probe harness for the unique-index retry/idempotence path
// around checkpoint watermark advancement.
func TestAINativeUniqueIndexCheckpointRetryProbe(t *testing.T) {
	if kerneltype.IsNextGen() {
		t.Skip("DXF is always enabled on nextgen")
	}

	store := realtikvtest.CreateMockStoreAndSetup(t)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("set global tidb_ddl_enable_fast_reorg=on;")
	tk.MustExec("set @@tidb_ddl_reorg_worker_cnt=1;")
	tk.MustExec("set @@global.tidb_enable_dist_task = 0;")

	tk.MustExec("create table t(id int primary key, b int, k int);")
	tk.MustQuery("split table t by (30000);").Check(testkit.Rows("1 1"))
	tk.MustExec("insert into t values(1, 1, 1);")
	tk.MustExec("insert into t values(100000, 1, 2);")

	oldForceSyncFlag := ingest.ForceSyncFlagForTest.Load()
	ingest.ForceSyncFlagForTest.Store(true)
	t.Cleanup(func() {
		ingest.ForceSyncFlagForTest.Store(oldForceSyncFlag)
	})

	testfailpoint.Enable(t, "github.com/pingcap/tidb/pkg/ddl/ingest/mockAfterImportAllocTSFailed", "2*return")
	tk.MustExec("alter table t add unique index idx(k);")
	tk.MustExec("admin check table t;")
	tk.MustExec("update t set k = k + 10;")
}
