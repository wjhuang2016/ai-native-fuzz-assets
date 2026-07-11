package sessiontxn_test

import (
	"testing"

	"github.com/pingcap/tidb/pkg/config"
	"github.com/pingcap/tidb/pkg/sessiontxn"
	"github.com/pingcap/tidb/pkg/testkit"
)

func runAutocommitRetryRetargetProbe(
	t *testing.T,
	setup func(tk *testkit.TestKit),
	query string,
	conflictSQL string,
	beforeRetry func(tk *testkit.TestKit),
	verify func(tk *testkit.TestKit),
) {
	t.Helper()
	store, _ := setupTxnContextTest(t)

	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("set @@tidb_pessimistic_txn_fair_locking = 0")
	setup(tk)

	tk2 := testkit.NewSteppedTestKit(t, store)
	defer tk2.MustExec("rollback")
	tk2.MustExec("use test")
	tk2.MustExec("set @@tidb_txn_mode = 'pessimistic'")
	tk2.MustExec("set @@tidb_pessimistic_txn_fair_locking = 0")
	tk2.MustExec("set autocommit = 1")
	retryHookCh := make(chan func(), 1)
	tk2.Session().SetValue(sessiontxn.TxnRetryBeforePrepareKey, retryHookCh)
	tk2.SetBreakPoints(
		sessiontxn.BreakPointBeforeExecutorFirstRun,
		sessiontxn.BreakPointOnStmtRetryAfterLockError,
	)

	tk2.SteppedMustExec(query)

	tk2.ExpectStopOnBreakPoint(sessiontxn.BreakPointBeforeExecutorFirstRun)
	tk.MustExec(conflictSQL)

	retryHookCh <- func() {
		beforeRetry(tk)
	}
	tk2.Continue().ExpectStopOnBreakPoint(sessiontxn.BreakPointBeforeExecutorFirstRun)
	tk2.Continue().ExpectIdle()

	verify(tk)
}

func TestAutocommitTransferTargetChangeProbe(t *testing.T) {
	defer config.RestoreFunc()()
	config.UpdateGlobal(func(conf *config.Config) {
		conf.PessimisticTxn.PessimisticAutoCommit.Store(false)
	})

	t.Run("update-join-retarget", func(t *testing.T) {
		runAutocommitRetryRetargetProbe(
			t,
			func(tk *testkit.TestKit) {
				tk.MustExec("drop table if exists src, dst")
				tk.MustExec("create table src(id int primary key, ref int)")
				tk.MustExec("create table dst(id int primary key, v int)")
				tk.MustExec("insert into src values(1, 10)")
				tk.MustExec("insert into dst values(10, 100), (11, 101)")
			},
			`
				with c as (select /*+ MERGE() */ * from src where id = 1)
				update c join dst on c.ref = dst.id
				set dst.v = dst.v + 1
			`,
			"update dst set v = v + 10 where id = 10",
			func(tk *testkit.TestKit) {
				tk.MustExec("update src set ref = 11 where id = 1")
			},
			func(tk *testkit.TestKit) {
				tk.MustQuery("select * from src").Check(testkit.Rows("1 11"))
				tk.MustQuery("select * from dst order by id").Check(testkit.Rows("10 110", "11 102"))
			},
		)
	})

	t.Run("delete-join-retarget", func(t *testing.T) {
		runAutocommitRetryRetargetProbe(
			t,
			func(tk *testkit.TestKit) {
				tk.MustExec("drop table if exists src, dst")
				tk.MustExec("create table src(id int primary key, ref int)")
				tk.MustExec("create table dst(id int primary key, v int)")
				tk.MustExec("insert into src values(1, 10)")
				tk.MustExec("insert into dst values(10, 100), (11, 101)")
			},
			`
				with c as (select /*+ MERGE() */ * from src where id = 1)
				delete dst from c join dst on c.ref = dst.id
			`,
			"update dst set v = v + 10 where id = 10",
			func(tk *testkit.TestKit) {
				tk.MustExec("update src set ref = 11 where id = 1")
			},
			func(tk *testkit.TestKit) {
				tk.MustQuery("select * from src").Check(testkit.Rows("1 11"))
				tk.MustQuery("select * from dst order by id").Check(testkit.Rows("10 110"))
			},
		)
	})

	t.Run("insert-on-dup-retarget", func(t *testing.T) {
		runAutocommitRetryRetargetProbe(
			t,
			func(tk *testkit.TestKit) {
				tk.MustExec("drop table if exists src, dst")
				tk.MustExec("create table src(id int primary key, uk int)")
				tk.MustExec("create table dst(id int primary key, uk int unique, v int)")
				tk.MustExec("insert into src values(1, 10)")
				tk.MustExec("insert into dst values(1, 10, 100), (2, 11, 101)")
			},
			"insert into dst select 3, uk, 999 from src where id = 1 on duplicate key update v = v + 1",
			"update dst set v = v + 10 where uk = 10",
			func(tk *testkit.TestKit) {
				tk.MustExec("update src set uk = 11 where id = 1")
			},
			func(tk *testkit.TestKit) {
				tk.MustQuery("select * from src").Check(testkit.Rows("1 11"))
				tk.MustQuery("select * from dst order by id").Check(testkit.Rows("1 10 110", "2 11 102"))
			},
		)
	})

	t.Run("replace-select-retarget", func(t *testing.T) {
		runAutocommitRetryRetargetProbe(
			t,
			func(tk *testkit.TestKit) {
				tk.MustExec("drop table if exists src, dst")
				tk.MustExec("create table src(id int primary key, uk int)")
				tk.MustExec("create table dst(id int primary key, uk int unique, v int)")
				tk.MustExec("insert into src values(1, 10)")
				tk.MustExec("insert into dst values(1, 10, 100), (2, 11, 101)")
			},
			"replace into dst select 3, uk, 999 from src where id = 1",
			"update dst set v = v + 10 where uk = 10",
			func(tk *testkit.TestKit) {
				tk.MustExec("update src set uk = 11 where id = 1")
			},
			func(tk *testkit.TestKit) {
				tk.MustQuery("select * from src").Check(testkit.Rows("1 11"))
				tk.MustQuery("select * from dst order by id").Check(testkit.Rows("1 10 110", "3 11 999"))
			},
		)
	})

	t.Run("replace-select-double-retarget", func(t *testing.T) {
		store, _ := setupTxnContextTest(t)

		tk := testkit.NewTestKit(t, store)
		tk.MustExec("use test")
		tk.MustExec("set @@tidb_pessimistic_txn_fair_locking = 0")
		tk.MustExec("drop table if exists src, dst")
		tk.MustExec("create table src(id int primary key, uk int)")
		tk.MustExec("create table dst(id int primary key, uk int unique, v int)")
		tk.MustExec("insert into src values(1, 10)")
		tk.MustExec("insert into dst values(1, 10, 100), (2, 11, 101), (4, 12, 102)")

		tk2 := testkit.NewSteppedTestKit(t, store)
		defer tk2.MustExec("rollback")
		tk2.MustExec("use test")
		tk2.MustExec("set @@tidb_txn_mode = 'pessimistic'")
		tk2.MustExec("set @@tidb_pessimistic_txn_fair_locking = 0")
		tk2.MustExec("set autocommit = 1")
		retryHookCh := make(chan func(), 2)
		tk2.Session().SetValue(sessiontxn.TxnRetryBeforePrepareKey, retryHookCh)
		tk2.SetBreakPoints(
			sessiontxn.BreakPointBeforeExecutorFirstRun,
			sessiontxn.BreakPointOnStmtRetryAfterLockError,
		)

		tk2.SteppedMustExec("replace into dst select 3, uk, 999 from src where id = 1")

		tk2.ExpectStopOnBreakPoint(sessiontxn.BreakPointBeforeExecutorFirstRun)
		tk.MustExec("update dst set v = v + 10 where uk = 10")
		retryHookCh <- func() {
			tk.MustExec("update src set uk = 11 where id = 1")
		}

		tk2.Continue().ExpectStopOnBreakPoint(sessiontxn.BreakPointBeforeExecutorFirstRun)
		tk.MustExec("update dst set v = v + 10 where uk = 11")
		retryHookCh <- func() {
			tk.MustExec("update src set uk = 12 where id = 1")
		}

		tk2.Continue().ExpectStopOnBreakPoint(sessiontxn.BreakPointBeforeExecutorFirstRun)
		tk2.Continue().ExpectIdle()

		tk.MustQuery("select * from src").Check(testkit.Rows("1 12"))
		tk.MustQuery("select * from dst order by id").Check(testkit.Rows("1 10 110", "2 11 111", "3 12 999"))
	})

	t.Run("fk-cascade-reparent-on-retry", func(t *testing.T) {
		store, _ := setupTxnContextTest(t)

		tk := testkit.NewTestKit(t, store)
		tk.MustExec("set @@global.tidb_enable_foreign_key = 1")
		defer tk.MustExec("set @@global.tidb_enable_foreign_key = default")
		tk.MustExec("use test")
		tk.MustExec("set @@foreign_key_checks = 1")
		tk.MustExec("set @@tidb_pessimistic_txn_fair_locking = 0")
		tk.MustExec("drop table if exists c, p")
		tk.MustExec("create table p(id int primary key)")
		tk.MustExec("create table c(id int primary key, pid int, v int, index(pid), constraint fk foreign key(pid) references p(id) on delete cascade)")
		tk.MustExec("insert into p values(1), (2)")
		tk.MustExec("insert into c values(10, 1, 0)")

		tk2 := testkit.NewSteppedTestKit(t, store)
		defer tk2.MustExec("rollback")
		tk2.MustExec("use test")
		tk2.MustExec("set @@foreign_key_checks = 1")
		tk2.MustExec("set @@tidb_txn_mode = 'pessimistic'")
		tk2.MustExec("set @@tidb_pessimistic_txn_fair_locking = 0")
		tk2.MustExec("set autocommit = 1")
		tk2.SetBreakPoints(
			sessiontxn.BreakPointBeforeExecutorFirstRun,
			sessiontxn.BreakPointOnStmtRetryAfterLockError,
		)

		tk2.SteppedMustExec("delete from p where id = 1")

		tk2.ExpectStopOnBreakPoint(sessiontxn.BreakPointBeforeExecutorFirstRun)
		tk.MustExec("update c set pid = 2, v = v + 10 where id = 10")

		tk2.Continue().ExpectStopOnBreakPoint(sessiontxn.BreakPointBeforeExecutorFirstRun)
		tk2.Continue().ExpectIdle()

		tk.MustQuery("select * from p order by id").Check(testkit.Rows("2"))
		tk.MustQuery("select * from c order by id").Check(testkit.Rows("10 2 10"))
	})
}
