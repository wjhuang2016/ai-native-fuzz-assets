package rule

import (
	"strings"
	"testing"

	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/testkit/testdata"
	"github.com/stretchr/testify/require"
)

func TestCorrelateAlternativePreservesAccessPathIdentity(t *testing.T) {
	store := testkit.CreateMockStore(t)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("create table o(id int primary key, a int not null)")
	tk.MustExec("create table i(a int not null, b int not null, key ia(a))")
	tk.MustExec("insert into o values (1,1),(2,2),(3,3)")
	tk.MustExec("insert into i values (1,10),(1,11),(2,20),(3,30),(5,50)")
	tk.MustExec("analyze table o, i")

	sql := "select id from o where id <= 3 and a in " +
		"(select max(a) from i group by b) order by id"

	tk.MustExec("set tidb_opt_enable_alternative_logical_plans=off")
	off := testdata.ConvertRowsToStrings(tk.MustQuery(sql).Rows())
	require.Equal(t, []string{"1", "2", "3"}, off)

	tk.MustExec("set tidb_opt_enable_alternative_logical_plans=on")
	tk.MustExec("set tidb_opt_hash_join_cost_factor=1")
	tk.MustExec("set tidb_opt_merge_join_cost_factor=1")
	plan := testdata.ConvertRowsToStrings(tk.MustQuery("explain format='brief' " + sql).Rows())
	on := testdata.ConvertRowsToStrings(tk.MustQuery(sql).Rows())
	t.Logf("alternative plan:\n%s\noff=%v on=%v", strings.Join(plan, "\n"), off, on)
	require.Equal(t, off, on)
}
