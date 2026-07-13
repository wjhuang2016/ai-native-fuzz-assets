# Pessimistic DML retry can change a SETVAR-derived unique key

Status: issue-filed high-severity bug as remote `found_bug id2100003` and upstream
https://github.com/pingcap/tidb/issues/69791.

## User-visible behavior

A pessimistic `UPDATE` can return success and commit a different row image after a lock conflict,
even though re-executing the submitted statement from its original session state must produce a
duplicate-key error.

The testbed result was:

```text
expected: duplicate-key error; rows (1,10),(2,1)
actual:   success; @x=2; rows (1,2),(2,1)
```

This is silent persistent wrong data, not only a surprising final user-variable value.

## SQL-only reproduction

Create the table once:

```sql
DROP TABLE IF EXISTS t;
CREATE TABLE t(id INT PRIMARY KEY, u INT UNIQUE);
INSERT INTO t VALUES (1, 10);
```

Start session A:

```sql
SET tidb_txn_mode = 'pessimistic';
SET tidb_pessimistic_txn_fair_locking = OFF;
SET @x = 0;
BEGIN;
UPDATE t
SET u = SLEEP((@x := @x + 1) + 7) * 0 + @x
WHERE id = 1;
SELECT @x;
COMMIT;
SELECT id, u FROM t ORDER BY id;
```

While session A is evaluating the `UPDATE`, run in session B:

```sql
INSERT INTO t VALUES (2, 1);
```

Session B commits `u=1` after session A has evaluated its first row image but before A finishes the
post-execution pessimistic lock phase. Current TiDB retries A. Because the failed attempt already
changed `@x` from 0 to 1, the second attempt computes `u=2`, succeeds, and hides the duplicate-key
error.

This SQL-only schedule reproduced on testbed 8220955 with real TiKV. The deterministic unit-test
scaffold uses a breakpoint at the same post-evaluation, pre-lock boundary and then performs a real
concurrent insert; it does not inject the terminal duplicate result.

## Root cause

`SETVAR` writes directly to `SessionVars.UserVars` during expression evaluation. Pessimistic DML
then derives its lock keys and calls `txn.LockKeys`. When that returns a retryable write conflict,
`handlePessimisticLockError` rebuilds the executor and calls `StmtRollback`, but statement rollback
only cleans transaction statement state. It does not restore user variables.

The system proves P: the KV statement buffer was rolled back and a new executor was built. It then
assumes Q: every state that can affect the rebuilt executor was restored. The failed attempt's user
variable mutation is the missing state dimension.

## Evidence matrix

| Arm | Conflict altitude | Assignment | Expected | Current result |
| --- | --- | --- | --- | --- |
| No conflict | none | `@x:=@x+1` | `v=1,@x=1` | GREEN |
| Pre-evaluation conflict | before SETVAR | `@x:=@x+1` | `v=1,@x=1` | GREEN |
| Post-evaluation conflict | after SETVAR | `@x:=@x+1` | `v=1,@x=1` | RED: `v=2,@x=2` |
| Post-evaluation conflict | after SETVAR | `@x:=7` | `v=7,@x=7` | GREEN |
| Natural unique-key race | after row image | `@x:=@x+1` | duplicate error | RED: success, `(1,2),(2,1)` |
| Restore attempt state | same two RED arms | same | original semantics | GREEN |

## Fix direction

Automatic retry must restore every attempt-scoped state dimension that can be consumed by the
rebuilt executor. A local full `UserVars` snapshot made both RED arms GREEN and left the controls
GREEN, proving sufficiency. A production implementation should prefer a touched-variable undo log,
or decline transparent retry for statements with non-transactional expression side effects, to
avoid cloning all user variables for every pessimistic DML.

## Distinctness

The retained optimistic-retry boundary concerns deprecated, opt-in whole-transaction retry and a
read-only `SELECT` assignment omitted from statement history. This bug is a different retry owner:
the default/recommended pessimistic statement retry, with `SETVAR` inside the write being replayed.
Post-RED searches found no exact upstream issue.
