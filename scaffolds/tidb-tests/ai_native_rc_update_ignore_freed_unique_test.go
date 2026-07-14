// Install under pkg/executor/test/txn with package txn_test.
package txn_test

import (
	"testing"

	"github.com/pingcap/tidb/pkg/testkit"
)

func TestAINativeRCUpdateIgnoreAfterUniqueKeyIsFreed(t *testing.T) {
	store := testkit.CreateMockStore(t)
	writer := testkit.NewTestKit(t, store)
	competitor := testkit.NewTestKit(t, store)
	observer := testkit.NewTestKit(t, store)

	for _, tk := range []*testkit.TestKit{writer, competitor, observer} {
		tk.MustExec("use test")
	}
	writer.MustQuery("select @@tidb_enable_metadata_lock").Check(testkit.Rows("1"))
	writer.MustExec("create table rc_update_ignore_t (id int primary key, u int unique, v int)")
	writer.MustExec("insert into rc_update_ignore_t values (1,10,100),(2,20,200)")

	writer.MustExec("set transaction_isolation = 'READ-COMMITTED'")
	writer.MustExec("set tidb_txn_mode = 'pessimistic'")
	writer.MustExec("set tidb_rc_write_check_ts = on")
	writer.MustExec("begin pessimistic")
	writer.MustQuery("select * from rc_update_ignore_t where id=1").Check(testkit.Rows("1 10 100"))
	writer.MustQuery("select * from rc_update_ignore_t where id=2").Check(testkit.Rows("2 20 200"))

	competitor.MustExec("delete from rc_update_ignore_t where id=2")
	observer.MustQuery("select * from rc_update_ignore_t").Check(testkit.Rows("1 10 100"))

	writer.MustExec("update ignore rc_update_ignore_t set u=20,v=101 where id=1")
	writer.MustQuery("select row_count()").Check(testkit.Rows("1"))
	writer.MustExec("commit")
	observer.MustQuery("select * from rc_update_ignore_t").Check(testkit.Rows("1 20 101"))
}
