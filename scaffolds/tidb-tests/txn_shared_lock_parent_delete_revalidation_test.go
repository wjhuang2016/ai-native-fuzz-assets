package txntest

import (
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/tests/realtikvtest"
	"github.com/stretchr/testify/require"
)

func prepareSharedLockParentDeleteTables(tk *testkit.TestKit) {
	tk.MustExec("drop table if exists child, parent")
	tk.MustExec("create table parent (id int primary key)")
	tk.MustExec("create table child (id int primary key, pid int, foreign key (pid) references parent(id))")
	tk.MustExec("insert into parent values (1)")
}

func TestSharedLockReleaseOneHolderDoesNotAllowParentDelete(t *testing.T) {
	if !*realtikvtest.WithRealTiKV {
		t.Skip("requires real TiKV")
	}

	store := realtikvtest.CreateMockStoreAndSetup(t)
	for _, secondHolderCommits := range []bool{false, true} {
		name := "second_holder_rolls_back"
		if secondHolderCommits {
			name = "second_holder_commits"
		}
		t.Run(name, func(t *testing.T) {
			holder1 := testkit.NewTestKit(t, store)
			holder2 := testkit.NewTestKit(t, store)
			deleter := testkit.NewTestKit(t, store)
			observer := testkit.NewTestKit(t, store)
			for _, tk := range []*testkit.TestKit{holder1, holder2, deleter, observer} {
				tk.MustExec("use test")
				tk.MustExec("set @@tidb_foreign_key_check_in_shared_lock = ON")
				tk.MustExec("set @@tidb_pessimistic_txn_fair_locking = 0")
			}

			prepareSharedLockParentDeleteTables(holder1)
			holder1.MustExec("begin pessimistic")
			holder2.MustExec("begin pessimistic")
			holder1.MustExec("insert into child values (1, 1)")
			holder2.MustExec("insert into child values (2, 1)")

			deleter.MustExec("begin pessimistic")
			deleteDone := make(chan error, 1)
			go func() {
				_, err := deleter.Exec("delete from parent where id = 1")
				deleteDone <- err
			}()

			select {
			case err := <-deleteDone:
				require.FailNow(t, "delete should wait for both shared-lock holders", "err: %v", err)
			case <-time.After(500 * time.Millisecond):
			}

			holder1.MustExec("rollback")
			select {
			case err := <-deleteDone:
				require.FailNow(t, "delete should remain blocked after only one holder exits", "err: %v", err)
			case <-time.After(500 * time.Millisecond):
			}

			if secondHolderCommits {
				holder2.MustExec("commit")
				require.Error(t, <-deleteDone)
				deleter.MustExec("rollback")
				observer.MustQuery("select id from parent where id = 1").Check(testkit.Rows("1"))
				observer.MustQuery("select * from child order by id").Check(testkit.Rows("2 1"))
			} else {
				holder2.MustExec("rollback")
				require.NoError(t, <-deleteDone)
				deleter.MustExec("commit")
				observer.MustQuery("select id from parent where id = 1").Check(testkit.Rows())
				observer.MustQuery("select * from child order by id").Check(testkit.Rows())
			}
			observer.MustExec("admin check table parent")
			observer.MustExec("admin check table child")
		})
	}
}
