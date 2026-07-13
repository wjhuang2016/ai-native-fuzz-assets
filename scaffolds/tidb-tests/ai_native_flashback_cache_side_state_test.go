// AI-native proof harness. Tests are the validation instrument, not the deliverable.
package ddl_test

import (
	"testing"

	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/stretchr/testify/require"
)

func TestCachedTableMissingSideRowBlocksWrites(t *testing.T) {
	store, dom := testkit.CreateMockStoreAndDomain(t)
	dom.SetStatsUpdating(true)

	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("create table cache_flashback_t (id int primary key)")
	tk.MustExec("insert into cache_flashback_t values (1)")
	tk.MustExec("alter table cache_flashback_t cache")

	tableID := tk.MustQuery(`select tidb_table_id from information_schema.tables
		where table_schema = 'test' and table_name = 'cache_flashback_t'`).Rows()[0][0].(string)
	tk.MustQuery("select tid from mysql.table_cache_meta where tid = ?", tableID).Check(testkit.Rows(tableID))
	tk.MustExec("delete from mysql.table_cache_meta where tid = ?", tableID)

	fresh := testkit.NewTestKit(t, store)
	fresh.MustExec("use test")
	fresh.MustQuery("select * from cache_flashback_t").Check(testkit.Rows("1"))

	_, err := fresh.Exec("insert into cache_flashback_t values (2)")
	require.ErrorContains(t, err, "table_cache_meta tid not exist")
	fresh.MustQuery("select * from cache_flashback_t order by id").Check(testkit.Rows("1"))

	tk.MustExec("replace into mysql.table_cache_meta values (?, 'NONE', 0, 0)", tableID)
	fresh.MustExec("insert into cache_flashback_t values (2)")
	fresh.MustQuery("select * from cache_flashback_t order by id").Check(testkit.Rows("1", "2"))
}
