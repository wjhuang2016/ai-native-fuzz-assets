# ROLLBACK TO SAVEPOINT leaves local temporary table size accounting stale

Status: confirmed moderate severity as remote `found_bug id2220003`. Reproduced on current source
and SQL-only on testbed 8220955 at the exact TiDB commit `5c9198e9484d`.

## Summary

`ROLLBACK TO SAVEPOINT` restores the transaction MemDB, so rows written after the savepoint
disappear. It does not restore the transaction-local dirty-size counter stored inside each local
temporary table object.

After enough data is written and rolled back, the table can be visibly empty while the next tiny
write fails with `ERROR 1114: The table is full`.

## Reproduction

```sql
USE test;
SET SESSION tidb_tmp_table_max_size = 1048576;
CREATE TEMPORARY TABLE ai_savepoint_tmp (
  id INT PRIMARY KEY,
  v LONGBLOB
);

BEGIN;
SAVEPOINT sp;
INSERT INTO ai_savepoint_tmp VALUES (1, REPEAT('x', 600000));
INSERT INTO ai_savepoint_tmp VALUES (2, REPEAT('y', 600000));
SELECT COUNT(*), SUM(LENGTH(v)) FROM ai_savepoint_tmp;

ROLLBACK TO SAVEPOINT sp;
SELECT COUNT(*) FROM ai_savepoint_tmp;
INSERT INTO ai_savepoint_tmp VALUES (3, 'z');
```

Current result:

```text
before rollback: 2 rows, 1200000 payload bytes
after rollback:  0 rows
final INSERT:    ERROR 1114 (HY000): The table 'ai_savepoint_tmp' is full
```

## Expected

The final INSERT succeeds. The visible table and its transaction-local dirty-size owner should
both reflect the savepoint state.

## Source chain

- `pkg/executor/simple.go`: `ROLLBACK TO SAVEPOINT` restores `TxnCtxNeedToRestore`, then calls
  `RollbackMemDBToCheckpoint`.
- `pkg/sessionctx/variable/session.go`: `TemporaryTables` is stored in `TxnCtxNoNeedToRestore` and
  is absent from `GetCurrentSavepoint` and `RestoreBySavepoint`.
- `pkg/table/tables/tables.go`: each temporary-table write calls `handleTempTableSize`, which adds
  the transaction size delta to the mutable `TemporaryTable.size` value.
- `pkg/table/tables/tables.go`: `checkTempTableSize` consumes committed size plus that dirty size
  before the next write.

The top-level map classification is misleading: membership may safely survive, but the mutable
value contains savepoint-scoped accounting state.

## Counterfactual

A local patch snapshotting `TemporaryTable.GetSize()` per table at savepoint creation and restoring
only those values during rollback made the exact failing test pass. No SQL, MemDB, or limit-check
logic changed.

## Impact

Valid writes to a local temporary table can be rejected for the rest of the transaction after a
large rolled-back segment. Ending the transaction is a practical recovery. No wrong durable data,
cross-session corruption, or memory-limit bypass was demonstrated, so this is moderate rather than
high severity.

## Discovery boundary

The candidate came from current-source owner analysis: compare the savepoint snapshot with every
state consumed after rollback, then traverse mutable values behind maps and interfaces. PR review
findings, issues, fixes, and history were not used to select or validate it.
