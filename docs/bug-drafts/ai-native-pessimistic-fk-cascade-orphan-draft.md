## Bug Report

### 1. Minimal reproduce step (Required)

This test pauses a pessimistic parent-key update after the main DML and its
`ON UPDATE CASCADE` have executed, but before the statement computes and acquires
its final pessimistic lock set. A second pessimistic transaction then inserts a
child referencing the still-visible old parent key and commits.

Add this test-only breakpoint immediately before `KeysNeedToLock()` in
`ExecStmt.handlePessimisticDML` (`pkg/executor/adapter.go`):

```diff
@@
 		if err != nil {
 			return err
 		}
+		if _, fpErr := failpoint.Eval("github.com/pingcap/tidb/pkg/util/breakpoint/afterPessimisticDMLExecutionBeforeLock"); fpErr == nil {
+			if notify, ok := a.Ctx.Value(breakpoint.NotifyBreakPointFuncKey).(func(string)); ok {
+				notify("afterPessimisticDMLExecutionBeforeLock")
+			}
+		}
 
 		keys, err1 := txn.(pessimisticTxn).KeysNeedToLock()
```

The file already imports `failpoint` and `pkg/util/breakpoint`, so no import
change is needed.

Add `pkg/executor/test/writetest/pessimistic_fk_cascade_orphan_test.go`:

```go
// Copyright 2026 PingCAP, Inc.

package writetest

import (
	"testing"
	"time"

	"github.com/pingcap/failpoint"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/util/breakpoint"
	"github.com/stretchr/testify/require"
)

func TestPessimisticFKCascadeDoesNotCommitOrphan(t *testing.T) {
	store := testkit.CreateMockStore(t)
	owner := testkit.NewTestKit(t, store)
	competitor := testkit.NewTestKit(t, store)
	owner.MustExec("use test")
	competitor.MustExec("use test")
	owner.MustQuery("show global variables like 'tidb_enable_metadata_lock'").
		Check(testkit.Rows("tidb_enable_metadata_lock ON"))

	owner.MustExec("create table parent (id int primary key, u int)")
	owner.MustExec(`create table child (
		id int primary key,
		pid int,
		constraint child_parent foreign key (pid) references parent(id) on update cascade
	)`)
	owner.MustExec("create table src (id int primary key, next_id int)")
	owner.MustExec("insert into parent values (1, 10)")
	owner.MustExec("insert into child values (100, 1)")
	owner.MustExec("insert into src values (1, 2)")
	owner.MustExec("set transaction_isolation = 'READ-COMMITTED'")
	competitor.MustExec("set transaction_isolation = 'READ-COMMITTED'")

	const name = "afterPessimisticDMLExecutionBeforeLock"
	const path = "github.com/pingcap/tidb/pkg/util/breakpoint/" + name
	require.NoError(t, failpoint.Enable(path, "return(true)"))
	t.Cleanup(func() { _ = failpoint.Disable(path) })

	stopped := make(chan string, 1)
	resume := make(chan struct{})
	owner.Session().SetValue(breakpoint.NotifyBreakPointFuncKey, func(got string) {
		stopped <- got
		<-resume
	})

	updateSQL := `update parent p
		join src s on s.id = p.id
		set p.id = s.next_id
		where p.id = 1`

	owner.MustExec("begin pessimistic")
	ownerErrCh := make(chan error, 1)
	go func() {
		_, err := owner.Exec(updateSQL)
		ownerErrCh <- err
	}()

	select {
	case got := <-stopped:
		require.Equal(t, name, got)
	case <-time.After(10 * time.Second):
		require.FailNow(t, "parent update did not reach the final-lock boundary")
	}

	competitor.MustExec("begin pessimistic")
	competitor.MustExec("insert into child values (200, 1)")
	competitor.MustExec("commit")
	close(resume)

	select {
	case err := <-ownerErrCh:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		require.FailNow(t, "parent update did not finish")
	}
	require.NoError(t, failpoint.Disable(path))
	owner.MustExec("commit")

	observer := testkit.NewTestKit(t, store)
	observer.MustExec("use test")
	observer.MustQuery("select * from parent order by id").
		Check(testkit.Rows("2 10"))
	observer.MustQuery("select * from child where id = 100").
		Check(testkit.Rows("100 2"))
	require.NoError(t, observer.ExecToErr("admin check table parent"))
	require.NoError(t, observer.ExecToErr("admin check table child"))

	orphans := observer.MustQuery(`select c.id, c.pid
		from child c left join parent p on p.id = c.pid
		where p.id is null order by c.id`).Rows()
	require.Empty(t, orphans,
		"a committed ON UPDATE CASCADE must not leave a child on the old parent key")
}
```

Run it from the TiDB repository:

```bash
go test ./pkg/executor/test/writetest \
  -run TestPessimisticFKCascadeDoesNotCommitOrphan \
  -tags=intest -count=1 -v
```

The join in the parent update is important: it selects an execution path where
the old parent key is not point-locked before the FK cascade is handled.

### 2. What did you expect to see? (Required)

The two successful transactions must be serializable with respect to the foreign
key constraint. In either valid order:

- the child insert observes parent `1`, commits first, and the later cascade moves
  every child from `1` to `2`; or
- the parent update commits first and the child insert fails with error 1452
  because parent `1` no longer exists.

There must not be a history where both transactions commit but child `200` still
references the removed parent key `1`.

### 3. What did you see instead (Required)

Both transactions returned success and committed. The pre-existing child was
cascaded, but the concurrently inserted child was left behind on the old key:

```text
parent=[[2 10]]
child=[[100 2] [200 1]]
orphans=[[200 1]]
parentCheckErr=<nil> childCheckErr=<nil>
```

The test fails at the final `require.Empty(orphans)` assertion. `ADMIN CHECK
TABLE` succeeds for both tables, so the violation is silent to the physical table
checker and remains visible to later queries.

The ownership gap appears to be:

1. `handleStmtForeignKeyTrigger` calls `StmtCommit` so the nested cascade can see
   the main DML's mem-buffer changes.
2. `StmtCommit` releases the current mem-buffer staging area; statement cleanup
   then initializes a fresh stage.
3. After the cascade returns, `handlePessimisticDML` calls `KeysNeedToLock`, which
   inspects only the current stage.
4. The released mutation of old parent key `1` is therefore absent from the final
   pessimistic lock set. The concurrent child insert validates against the still
   committed parent `1` and commits before the owner publishes the parent-key
   change.

As an ownership counterfactual, acquiring the current `KeysNeedToLock()` set
immediately before the first FK `StmtCommit` makes the exact schedule safe: the
child insert blocks until the parent update commits, then returns error 1452; the
anti-join is empty. This is evidence for the lost lock-ownership boundary, not a
proposed production fix.

This can cause durable, user-visible relational inconsistency even though both
transactions and both `ADMIN CHECK TABLE` commands report success.

### 4. What is your TiDB version? (Required)

Current master commit:

```text
b8d04e17a2ca61eee1220c5ce2d641a376f75e9b
```

The test uses pessimistic transactions at `READ-COMMITTED` with
`tidb_enable_metadata_lock=ON` (the default).
