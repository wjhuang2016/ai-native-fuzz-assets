// Install under tests/realtikvtest/pessimistictest with package pessimistictest.
package pessimistictest

import (
	"testing"

	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/tests/realtikvtest"
)

func TestAINativeRCUpdateIgnoreAfterUniqueKeyIsFreed(t *testing.T) {
	store := realtikvtest.CreateMockStoreAndSetup(t)
	writer := testkit.NewTestKit(t, store)
	competitor := testkit.NewTestKit(t, store)
	observer := testkit.NewTestKit(t, store)

	for _, tk := range []*testkit.TestKit{writer, competitor, observer} {
		tk.MustExec("use test")
	}
	writer.MustQuery("select @@tidb_enable_metadata_lock").Check(testkit.Rows("1"))
	writer.MustExec("create table ai_rc_update_ignore_t (id int primary key, u int unique, v int)")
	writer.MustExec("insert into ai_rc_update_ignore_t values (1,10,100),(2,20,200)")

	writer.MustExec("set transaction_isolation = 'READ-COMMITTED'")
	writer.MustExec("set tidb_txn_mode = 'pessimistic'")
	writer.MustExec("set tidb_rc_write_check_ts = on")
	writer.MustExec("begin pessimistic")
	writer.MustQuery("select * from ai_rc_update_ignore_t where id=1").Check(testkit.Rows("1 10 100"))
	writer.MustQuery("select * from ai_rc_update_ignore_t where id=2").Check(testkit.Rows("2 20 200"))

	competitor.MustExec("delete from ai_rc_update_ignore_t where id=2")
	observer.MustQuery("select * from ai_rc_update_ignore_t").Check(testkit.Rows("1 10 100"))

	writer.MustExec("update ignore ai_rc_update_ignore_t set u=20,v=101 where id=1")
	writer.MustQuery("select row_count()").Check(testkit.Rows("1"))
	writer.MustExec("commit")
	observer.MustQuery("select * from ai_rc_update_ignore_t").Check(testkit.Rows("1 20 101"))
}
