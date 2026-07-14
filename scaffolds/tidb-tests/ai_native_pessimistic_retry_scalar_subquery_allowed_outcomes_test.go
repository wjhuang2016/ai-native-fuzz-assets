package txntest

import (
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/tests/realtikvtest"
	"github.com/stretchr/testify/require"
)

func TestAINativePessimisticRetryScalarSubqueryAllowedOutcomes(t *testing.T) {
	store := realtikvtest.CreateMockStoreAndSetup(t)
	setup := testkit.NewTestKit(t, store)
	setup.MustExec("use test")
	setup.MustQuery("select @@tidb_enable_metadata_lock, @@transaction_isolation, @@tidb_pessimistic_txn_fair_locking").
		Check(testkit.Rows("1 REPEATABLE-READ 1"))

	setup.MustExec("create table retry_src(id int primary key, next_u int not null, payload int not null)")
	setup.MustExec("create table retry_dst(id int primary key, u int not null, payload int not null, unique key uk_u(u))")
	setup.MustExec("insert into retry_src values (1, 1, 10)")
	setup.MustExec("insert into retry_dst values (1, 10, 0)")

	retryWorker := testkit.NewTestKit(t, store)
	retryWorker.MustExec("use test")
	retryWorker.MustExec("begin pessimistic")
	done := make(chan error, 1)
	go func() {
		done <- retryWorker.ExecToErr(`update retry_dst d join retry_src s on s.id = 1
			set d.u = s.next_u + sleep(2) * 0,
				d.payload = (select payload from retry_src where id = 1)
			where d.id = 1`)
	}()
	time.Sleep(300 * time.Millisecond)
	setup.MustExec("begin pessimistic")
	setup.MustExec("update retry_src set next_u = 2, payload = 20 where id = 1")
	setup.MustExec("insert into retry_dst values (2, 1, 99)")
	setup.MustExec("commit")
	require.NoError(t, <-done)
	require.Greater(t, retryWorker.Session().GetSessionVars().StmtCtx.ExecRetryCount, uint64(0))
	retryWorker.MustExec("commit")
	retryRows := setup.MustQuery("select * from retry_dst order by id").Rows()

	setup.MustExec("create table one_src like retry_src")
	setup.MustExec("create table one_dst like retry_dst")
	setup.MustExec("insert into one_src values (1, 1, 10)")
	setup.MustExec("insert into one_dst values (1, 10, 0)")
	oneWorker := testkit.NewTestKit(t, store)
	oneWorker.MustExec("use test")
	oneWorker.MustExec("begin pessimistic")
	oneWorker.MustQuery("select payload from one_src where id = 1").Check(testkit.Rows("10"))
	setup.MustExec("begin pessimistic")
	setup.MustExec("update one_src set next_u = 2, payload = 20 where id = 1")
	setup.MustExec("insert into one_dst values (2, 1, 99)")
	setup.MustExec("commit")
	oneWorker.MustExec(`update one_dst d join one_src s on s.id = 1
		set d.u = s.next_u,
			d.payload = (select payload from one_src where id = 1)
		where d.id = 1`)
	require.Equal(t, uint64(0), oneWorker.Session().GetSessionVars().StmtCtx.ExecRetryCount)
	oneWorker.MustExec("commit")
	oneAttemptRows := setup.MustQuery("select * from one_dst order by id").Rows()

	require.Equal(t, oneAttemptRows, retryRows)
	setup.MustExec("admin check table retry_dst")
	setup.MustExec("admin check table one_dst")
}
