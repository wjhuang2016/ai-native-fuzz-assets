# New finding: RC `INSERT IGNORE` can silently skip a valid foreign-key row

Status: live RED on authorized testbed `8220955`; new-root deduplication and upstream intake are
still pending. No remote `found_bug` ID is assigned here.

## User-visible symptom

In a pessimistic `READ-COMMITTED` transaction, a parent row committed by another session after the
child transaction began is treated as missing by the foreign-key check. A normal `INSERT` returns a
false `ERROR 1452`; `INSERT IGNORE` turns the same stale check into `Query OK, 0 rows affected` with
only a warning, so the valid child row is silently lost.

This is a high-consequence wrong-result/data-loss candidate: the application asked to insert a row
whose parent is committed in the current read-committed view, but TiDB drops the row without a
statement error.

## Minimal reproduction

```sql
DROP DATABASE IF EXISTS ai_rc_insert_fk;
CREATE DATABASE ai_rc_insert_fk;
USE ai_rc_insert_fk;
SET GLOBAL tidb_enable_foreign_key = ON;

CREATE TABLE parent(id INT PRIMARY KEY);
CREATE TABLE child(
  id INT PRIMARY KEY,
  pid INT,
  CONSTRAINT fk_parent FOREIGN KEY(pid) REFERENCES parent(id)
);

-- Session A: READ-COMMITTED, pessimistic.
SET SESSION transaction_isolation = 'READ-COMMITTED';
SET SESSION tidb_txn_mode = 'pessimistic';
SET SESSION tidb_rc_write_check_ts = OFF; -- ON also reproduces
BEGIN PESSIMISTIC;

-- Session B, after Session A has begun:
INSERT INTO parent VALUES (4);
COMMIT;

-- Session A: the parent is committed in the current RC view.
INSERT IGNORE INTO child VALUES (40, 4);
SHOW WARNINGS;
SELECT * FROM child WHERE id = 40;
COMMIT;
```

Observed on testbed `8220955`:

```text
Query OK, 0 rows affected, 1 warning
Warning 1452 Cannot add or update a child row: a foreign key constraint fails
SELECT * FROM child WHERE id = 40: Empty set
```

The same schedule with `tidb_rc_write_check_ts = ON` produced the same result. A control where the
parent was committed before Session A began inserted the child successfully. With plain `INSERT`
instead of `INSERT IGNORE`, the stale check surfaced as `ERROR 1452`; this is the sibling that makes
the `INSERT IGNORE` result a silent lost write rather than merely a wrong error.

## Source proof

The testbed binary (`5c9198e9484db852b8477ce0014e0422ff9ec6a9`) contains the same path as the local
source:

1. `pkg/sessiontxn/isolation/readcommitted.go:273-295` classifies every physical `INSERT` without
   a SELECT subplan, ON DUPLICATE assignments, or REPLACE as safe to reuse `latestOracleTS`. The
   classification does not check the foreign-key side reads or `INSERT IGNORE`.
2. `readcommitted.go:322-345` marks the write fast path and applies `RCCheckTS` to the statement
   snapshot returned by `GetSnapshotWithStmtForUpdateTS`.
3. `readcommitted.go:180-199` sets the transaction snapshot option to the constant statement TS,
   but the constant-future branch does not update `TxnCtx.ForUpdateTS`. Consequently
   `isolation/base.go:451-469` can return a separate statement snapshot instead of mutating the
   transaction's own snapshot.
4. `pkg/executor/foreign_key.go:557-625` bypasses that returned statement snapshot and calls
   `txn.GetSnapshot()` for the FK scan/check. `foreign_key.go:636-650` uses the same path for
   `INSERT IGNORE`, and `write.go:248-258` converts a failed FK check into an ignored row.

The proof obligation is therefore: every read performed by a write fast path, including FK
validation, must use the current statement timestamp and its consistency-check mode. The current
FK helper consumes the transaction-bound snapshot instead.

## Quality and boundary

- Strong oracle: current-rowset plus affected-row count; no storage fault, failpoint, or timing
  guess is needed beyond the ordinary concurrent commit ordering.
- Direct user surface: `INSERT IGNORE` is a normal compatibility feature used by ingestion and
  idempotent write paths.
- Protective controls: parent-before-BEGIN is GREEN; plain `INSERT` exposes the false rejection;
  parent-delete-after-BEGIN is correctly rejected, so this report does not claim an orphan insert.
- Do not broaden yet to every FK shape. First deduplicate against upstream history and verify the
  same root on the exact testbed/current-master build.

