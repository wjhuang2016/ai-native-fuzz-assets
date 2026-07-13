# Pessimistic RC retry publishes LAST_INSERT_ID from a rolled-back attempt

Status: issue-filed high severity as remote `found_bug id2190003` and upstream
[TiDB #69796](https://github.com/pingcap/tidb/issues/69796). Current-source local RED/GREEN and
SQL-only real-TiKV RED/control were completed before upstream dedup.

## Summary

`LAST_INSERT_ID(expr)` writes statement/session publication state while a DML row is evaluated. A
later pessimistic lock conflict can make TiDB roll back the KV statement buffer and transparently
retry the DML. `StatementContext.ResetForRetry()` does not reset `LastInsertID` or
`LastInsertIDSet`.

If the rebuilt READ COMMITTED executor sees a concurrent gate and matches zero rows, no successful
attempt overwrites those fields. The UPDATE returns success with zero affected rows but publishes
the ID computed by the rolled-back attempt. A following statement can persist that nonexistent
allocation.

## SQL-only reproduction

Run the setup once:

```sql
DROP DATABASE IF EXISTS ai_lastid_retry;
CREATE DATABASE ai_lastid_retry;
USE ai_lastid_retry;
CREATE TABLE t(id INT PRIMARY KEY, u INT UNIQUE, v BIGINT);
CREATE TABLE gate(id INT PRIMARY KEY);
CREATE TABLE sink(v BIGINT);
INSERT INTO t VALUES (1, 10, 0);
```

Start Session A. The UPDATE opens a 20-second window:

```sql
USE ai_lastid_retry;
SET tidb_txn_mode = 'pessimistic';
SET tx_isolation = 'READ-COMMITTED';
SET tidb_pessimistic_txn_fair_locking = 0;
SELECT LAST_INSERT_ID(7);
BEGIN;

UPDATE t AS x
SET u = 1, v = LAST_INSERT_ID(99 + SLEEP(20))
WHERE id = 1
  AND NOT EXISTS (SELECT 1 FROM gate AS g WHERE g.id = x.id);
```

While Session A is sleeping, run Session B:

```sql
USE ai_lastid_retry;
BEGIN;
INSERT INTO t VALUES (2, 1, 0);
INSERT INTO gate VALUES (1);
COMMIT;
```

After Session A's UPDATE returns, continue there:

```sql
SELECT ROW_COUNT() AS affected, LAST_INSERT_ID() AS published_after_update;
COMMIT;
INSERT INTO sink VALUES (LAST_INSERT_ID());
SELECT id, u, v FROM t ORDER BY id;
SELECT * FROM gate;
SELECT * FROM sink;
```

Current result:

```text
affected  published_after_update
0         99

t:    (1,10,0), (2,1,0)
gate: (1)
sink: (99)
```

With `gate(1)` already present before Session A starts, the same zero-match UPDATE keeps
`LAST_INSERT_ID()=7` and persists `sink(7)`. That same-final-state control proves `99` came only
from the hidden failed attempt.

## Source chain

- `pkg/expression/builtin_info.go:508-520`: `LAST_INSERT_ID(expr)` calls
  `SessionVars.SetLastInsertID` during expression evaluation.
- `pkg/sessionctx/variable/session.go:2745-2750`: the setter writes
  `StmtCtx.LastInsertIDSet=true` and `StmtCtx.LastInsertID`.
- `pkg/executor/adapter.go:1413-1452`: a retry-ready pessimistic lock error rebuilds the executor,
  rolls back statement KV state, and calls `StmtCtx.ResetForRetry()`.
- `pkg/sessionctx/stmtctx/stmtctx.go:1228-1240`: the reset clears counters, row IDs, warnings, and
  task state, but leaves both last-insert-ID fields untouched.

## Expected behavior

Transparent statement retry must be observationally equivalent to one execution from the state
seen by the successful attempt. Since that attempt matches zero rows, it never executes
`LAST_INSERT_ID(99)`; the prior value `7` must remain public.

## Impact

`LAST_INSERT_ID(expr)` is commonly used to return an application-visible value from an UPDATE. A
hidden retry can report an ID that no committed UPDATE allocated. Subsequent statements may use it
as a foreign key, queue ID, counter value, or application reference and commit wrong data.

## Fix direction

Make `LastInsertID` and `LastInsertIDSet` part of the attempt rollback closure. Clearing the current
statement value and flag in `ResetForRetry()` made the exact local conflict matrix return and
persist `7`; a production patch should confirm whether restoring explicit statement-entry values
is required for every retry caller.

## Discovery boundary

The candidate came from current source plus the S45 rollback-closure method. PR review findings,
issues, fixes, and history were closed until after local and testbed RED. Post-RED searches found no
exact issue or PR. This is distinct from `id2100003`: that bug retains `UserVars` and changes the
rebuilt DML row image; this bug retains `StatementContext` publication state when the successful
attempt has no setter at all.
