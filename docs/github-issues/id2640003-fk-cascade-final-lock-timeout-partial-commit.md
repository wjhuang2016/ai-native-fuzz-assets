# [txn] Failed pessimistic FK cascade update can be committed after lock wait timeout

## Bug Report

### 1. Minimal reproduce step (Required)

#### Concrete production scenario

One supported production shape is a SaaS control-plane, payment service, or data-repair worker that
migrates a tenant, merchant, or account identifier. The identifier is a parent primary key, and
dependent subscription, settlement, or routing rows use `ON UPDATE CASCADE`.

The service also has a singleton `migration_guard` row. A worker includes
`guard.version = guard.version` in the same multi-table UPDATE to acquire that database mutex without
changing its value or making another SQL round trip:

```sql
BEGIN PESSIMISTIC;
UPDATE account AS a
JOIN migration_guard AS g ON g.id = 1
SET a.id = 2, g.version = g.version
WHERE a.id = 1;
COMMIT;
```

An older migration or reconciliation worker can naturally hold the guard lock for more than the
default 50-second lock timeout while it processes a large batch. A hot Region, TiKV server-busy
backoff, storage pressure, or a long-running batch is sufficient; no crash or injected error is
required.

The racing worker receives error 1205 and treats it as a retryable migration conflict. In an explicit
transaction, a lock wait timeout rejects the statement but does not automatically end the transaction.
Raw MySQL clients and services that intentionally preserve earlier audit/progress work can therefore
issue `COMMIT` before scheduling the migration for retry. Applications that always issue `ROLLBACK`
after any 1205 do not expose this surface, but statement atomicity must hold in either case.

The exact production conditions are:

1. Foreign-key enforcement is enabled, as it is by default on current TiDB, and a parent primary-key
   update has an `ON UPDATE CASCADE` child.
2. The application uses an explicit pessimistic transaction and a multi-table UPDATE that also makes
   a no-op assignment to a guard/mutex row.
3. Another pessimistic transaction already owns that guard-row lock for longer than
   `innodb_lock_wait_timeout`. The reproducer keeps the default value of 50 seconds.
4. The parent update and FK cascade finish before TiDB's final `LockKeys` phase reaches the unchanged
   guard row.
5. The application catches error 1205 and later commits the still-open explicit transaction.

The required ordering is:

```text
T(B locks migration_guard)
  < T(A writes the parent in its statement stage)
  < T(A executes ON UPDATE CASCADE)
  < T(A intermediate StmtCommit publishes parent and child mutations)
  < T(A final LockKeys waits on migration_guard)
  < T(A receives error 1205)
  < T(A COMMIT)
```

One TiDB, one TiKV, and two SQL sessions are sufficient. The rows do not need to be in different
Regions. MDL remains enabled, and no DDL, failpoint, user SAVEPOINT, async commit, or 1PC is involved.

Add the following file as `pkg/executor/test/fktest/ai_native_fk_timeout_test.go`:

```go
package fk_test

import (
	"testing"

	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/tests/realtikvtest"
	"github.com/stretchr/testify/require"
)

func TestAIFKCascadeFinalLockTimeoutStatementAtomicity(t *testing.T) {
	store := realtikvtest.CreateMockStoreAndSetup(t)
	setup := testkit.NewTestKit(t, store)
	setup.MustExec("use test")
	setup.MustExec("set global tidb_enable_foreign_key = on")
	setup.MustExec("create table ai_parent (id int primary key)")
	setup.MustExec("create table ai_child (id int primary key, pid int, " +
		"constraint ai_fk foreign key (pid) references ai_parent(id) on update cascade)")
	setup.MustExec("create table ai_guard (id int primary key, version int not null)")
	setup.MustExec("insert into ai_parent values (1)")
	setup.MustExec("insert into ai_child values (10, 1)")
	setup.MustExec("insert into ai_guard values (1, 0)")

	holder := testkit.NewTestKit(t, store)
	holder.MustExec("use test")
	holder.MustExec("begin pessimistic")
	holder.MustExec("update ai_guard set version = version + 1 where id = 1")
	defer holder.MustExec("rollback")

	writer := testkit.NewTestKit(t, store)
	writer.MustExec("use test")
	writer.MustQuery("select @@tidb_enable_metadata_lock, @@innodb_lock_wait_timeout, " +
		"@@tidb_constraint_check_in_place_pessimistic, @@global.tidb_enable_foreign_key").
		Check(testkit.Rows("1 50 1 1"))
	writer.MustExec("begin pessimistic")
	err := writer.ExecToErr("update ai_parent as p join ai_guard as g on g.id = 1 " +
		"set p.id = 2, g.version = g.version where p.id = 1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "Lock wait timeout")
	writer.MustExec("commit")

	fresh := testkit.NewTestKit(t, store)
	fresh.MustExec("use test")
	fresh.MustQuery("select (select id from ai_parent), (select pid from ai_child where id = 10)").
		Check(testkit.Rows("1 1"))
}
```

Start a one-TiKV playground and run:

```bash
tiup playground nightly --db=0 --kv=1 --tiflash=0 --without-monitor
go test -tags=intest ./pkg/executor/test/fktest \
  -run '^TestAIFKCascadeFinalLockTimeoutStatementAtomicity$' -count=1 -v
```

This test deliberately waits for the default 50-second timeout. Setting the session timeout to one
second produces the same result and is only a faster local equivalent.

### 2. What did you expect to see? (Required)

The UPDATE returned a definite statement error. Committing the still-open transaction must not publish
any mutation from that failed statement. A fresh session should read:

```text
parent.id = 1
child.pid = 1
```

### 3. What did you see instead (Required)

The final assertion failed after the real TiKV lock waited for 50 seconds:

```text
error:    [tikv:1205] Lock wait timeout exceeded; try restarting transaction
expected: [1 1]
actual:   [2 2]
```

The client received a definite failure for the UPDATE, but `COMMIT` succeeded and made both its parent
mutation and the generated FK cascade durable. A retry can therefore run against data that the service
was told had not been changed.

`prepareFKCascadeContext` records an internal transaction savepoint before the statement. During FK
processing, `handleStmtForeignKeyTrigger` and nested cascade execution call `StmtCommit` so later
cascades can see intermediate parent/child mutations. After the cascade succeeds,
`handleStmtForeignKeyTrigger` releases that savepoint.

The outer pessimistic statement is not finished at that point. `handlePessimisticDML` still calls final
`LockKeys`, which waits on the guard row and returns 1205. Generic `StmtRollback` only cleans the current
statement stage; the earlier parent and child mutations were already published, and the FK savepoint
that could restore the transaction checkpoint has been released.

The exact owner counterfactual is to retain the FK savepoint until final pessimistic locking succeeds,
roll back to it on every terminal post-trigger error, and release it only after the complete user
statement has succeeded. With that change, the same UPDATE still returns 1205, but a later COMMIT leaves
the fresh state at `[1 1]` on both mock and real TiKV.

This is related to, but distinct from, #69828. That issue is a lost final lock-owner path where two
transactions both succeed and leave an FK orphan. This issue is a definite statement error followed by
durable mutation because an internal rollback checkpoint ends before the statement's fallibility
horizon.

### 4. What is your TiDB version? (Required)

Reproduced on TiDB master:

```text
2964713e267eac6eab92c4be53e9ad0641df2e9f
```

The real-TiKV run asserted these current defaults:

```text
@@tidb_enable_metadata_lock = 1
@@innodb_lock_wait_timeout = 50
@@tidb_constraint_check_in_place_pessimistic = 1
@@global.tidb_enable_foreign_key = 1
```
