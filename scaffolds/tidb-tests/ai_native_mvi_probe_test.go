package indexmergetest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pingcap/failpoint"
	"github.com/pingcap/tidb/pkg/ddl"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/stretchr/testify/require"
)

type aiNativeMVIProbeCase struct {
	name          string
	alterSQL      string
	concurrentSQL string
	referenceSQL  string
	refShouldErr  bool
	probeValues   []int
}

func TestAINativeMVIConcurrentAddIndexProbe(t *testing.T) {
	cases := []aiNativeMVIProbeCase{
		{
			name:          "unique_mvi_only_update_j_dup",
			alterSQL:      "alter table t add unique index u_mvi((cast(j as signed array)))",
			concurrentSQL: "update t set j = '[1]' where id = 240",
			referenceSQL:  "update ref set j = '[1]' where id = 240",
			refShouldErr:  true,
		},
		{
			name:          "unique_mvi_plus_idx_bc_update_j_dup",
			alterSQL:      "alter table t add unique index u_mvi((cast(j as signed array))), add index idx_bc(b, c)",
			concurrentSQL: "update t set j = '[1]' where id = 240",
			referenceSQL:  "update ref set j = '[1]' where id = 240",
			refShouldErr:  true,
		},
		{
			name:          "unique_mvi_plus_idx_bc_update_bc_only",
			alterSQL:      "alter table t add unique index u_mvi((cast(j as signed array))), add index idx_bc(b, c)",
			concurrentSQL: "update t set b = b + 1000, c = c + 1000 where id = 240",
		},
		{
			name:          "unique_mvi_plus_idx_bc_update_all",
			alterSQL:      "alter table t add unique index u_mvi((cast(j as signed array))), add index idx_bc(b, c)",
			concurrentSQL: "update t set b = b + 1000, c = c + 1000, j = '[1]' where id = 240",
			referenceSQL:  "update ref set b = b + 1000, c = c + 1000, j = '[1]' where id = 240",
			refShouldErr:  true,
		},
		{
			name:          "unique_mvi_plus_idx_bc_update_j_multivalue",
			alterSQL:      "alter table t add unique index u_mvi((cast(j as signed array))), add index idx_bc(b, c)",
			concurrentSQL: "update t set j = '[9, 1009]' where id = 9",
			referenceSQL:  "update ref set j = '[9, 1009]' where id = 9",
			probeValues:   []int{9, 10, 1009},
		},
	}
	for _, ca := range cases {
		t.Run(ca.name, func(t *testing.T) {
			runAINativeMVIProbeCase(t, ca)
		})
	}
}

func runAINativeMVIProbeCase(t *testing.T, ca aiNativeMVIProbeCase) {
	store := testkit.CreateMockStore(t)

	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("set @@tidb_ddl_reorg_worker_cnt = 1")
	tk.MustExec("set @@tidb_ddl_reorg_batch_size = 16")
	tk.MustExec("create table t(id int primary key, b int, c int, j json)")
	if ca.referenceSQL != "" {
		tk.MustExec("create table ref(id int primary key, b int, c int, j json)")
	}
	for i := 1; i <= 256; i++ {
		val := fmt.Sprintf("[%d]", i)
		tk.MustExec(fmt.Sprintf("insert into t values (%d, %d, %d, '%s')", i, i, i, val))
		if ca.referenceSQL != "" {
			tk.MustExec(fmt.Sprintf("insert into ref values (%d, %d, %d, '%s')", i, i, i, val))
		}
	}
	if ca.referenceSQL != "" {
		tk.MustExec(ca.referenceSQL)
		refErr := tk.ExecToErr(strings.Replace(ca.alterSQL, "alter table t", "alter table ref", 1))
		if ca.refShouldErr {
			require.Error(t, refErr)
		} else {
			require.NoError(t, refErr)
		}
		t.Logf("reference add unique mvi err: %v", refErr)
	}

	tk1 := testkit.NewTestKit(t, store)
	tk1.MustExec("use test")
	var concurrentErr error
	ddl.MockDMLExecution = func() {
		concurrentErr = tk1.ExecToErr(ca.concurrentSQL)
	}
	t.Cleanup(func() {
		ddl.MockDMLExecution = nil
	})
	require.NoError(t, failpoint.Enable("github.com/pingcap/tidb/pkg/ddl/mockDMLExecution", "1*return(true)->return(false)"))
	t.Cleanup(func() {
		require.NoError(t, failpoint.Disable("github.com/pingcap/tidb/pkg/ddl/mockDMLExecution"))
	})

	ddlErr := tk.ExecToErr(ca.alterSQL)
	t.Logf("ddl err: %v", ddlErr)
	t.Logf("concurrent dml err: %v", concurrentErr)
	if ddlErr == nil {
		t.Logf("show index: %#v", tk.MustQuery("show index from t").Rows())
		probeValues := ca.probeValues
		if len(probeValues) == 0 {
			probeValues = []int{17}
		}
		for _, v := range probeValues {
			fullScanSQL := fmt.Sprintf("select id from t ignore index(u_mvi) where %d member of (j) order by id", v)
			forcedIndexSQL := fmt.Sprintf("select /*+ use_index(t, u_mvi) */ id from t where %d member of (j) order by id", v)
			t.Logf("full scan explain for %d: %#v", v, tk.MustQuery("explain format = 'brief' "+fullScanSQL).Rows())
			t.Logf("forced index explain for %d: %#v", v, tk.MustQuery("explain format = 'brief' "+forcedIndexSQL).Rows())
			t.Logf("full scan result for %d: %#v", v, tk.MustQuery(fullScanSQL).Rows())
			t.Logf("forced index result for %d: %#v", v, tk.MustQuery(forcedIndexSQL).Rows())
		}
	}
	adminErr := tk.ExecToErr("admin check table t")
	t.Logf("admin check err: %v", adminErr)
	rows := tk.MustQuery("select id, b, c, json_unquote(cast(j as char)) from t where id in (1, 17, 240) order by id").Rows()
	t.Logf("final rows: %#v", rows)
}
