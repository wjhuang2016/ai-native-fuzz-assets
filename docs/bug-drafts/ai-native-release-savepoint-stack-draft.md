# RELEASE SAVEPOINT Drops Later Savepoints

## Summary

`RELEASE SAVEPOINT` in TiDB removes the named savepoint and every later savepoint in the same
transaction. Under MySQL-compatible savepoint semantics, release should remove only the named
marker; `ROLLBACK TO SAVEPOINT` is the operation that discards later markers.

Remote bug DB entry: id1200002, confirmed, severity medium, root cause
`release-savepoint-drops-later-savepoints`.

## User-Visible Symptom

A user can no longer roll back to a later savepoint after releasing an earlier one:

```sql
DROP DATABASE IF EXISTS ai_txn_savepoint_clean;
CREATE DATABASE ai_txn_savepoint_clean;
USE ai_txn_savepoint_clean;
CREATE TABLE t(id INT PRIMARY KEY, v INT);

BEGIN;
INSERT INTO t VALUES (1,10);
SAVEPOINT sp1;
INSERT INTO t VALUES (2,20);
SAVEPOINT sp2;
INSERT INTO t VALUES (3,30);

RELEASE SAVEPOINT sp1;
ROLLBACK TO SAVEPOINT sp2;
SELECT GROUP_CONCAT(CONCAT(id, ':', v) ORDER BY id) AS rows_in_txn FROM t;
ROLLBACK;
DROP DATABASE ai_txn_savepoint_clean;
```

Observed on testbed `8192975`:

```text
ERROR 1305 (42000): SAVEPOINT sp2 does not exist
rows_in_txn = 1:10,2:20,3:30
```

The write after `sp2` remains in the transaction because the rollback target was deleted by
`RELEASE SAVEPOINT sp1`.

## Expected Contract

MySQL 8.4 documents two different stack effects:

- `RELEASE SAVEPOINT name` removes the named savepoint.
- `ROLLBACK TO SAVEPOINT name` rolls back to the named savepoint and deletes savepoints created
  after it.

Reference: <https://dev.mysql.com/doc/refman/8.4/en/savepoint.html>

## Root Cause

Source anchors:

- `/Users/bba/pc/tidb/pkg/sessionctx/variable/session.go:529-535`
  `ReleaseSavepoint` truncates the stack with `tc.Savepoints = tc.Savepoints[:i]`.
- `/Users/bba/pc/tidb/pkg/sessionctx/variable/session.go:541-548`
  `RollbackToSavepoint` is the operation that should restore state and truncate later savepoints.
- `/Users/bba/pc/tidb/pkg/executor/simple.go:680-685`
  `executeReleaseSavepoint` directly calls `TxnCtx.ReleaseSavepoint`.
- `/Users/bba/pc/tidb/pkg/executor/test/txn/txn_test.go:309-315` and `:443-445`
  existing tests assert the current truncating behavior.

The likely fix direction is to implement release as deleting only the matched record, equivalent
to `slices.Delete(tc.Savepoints, i, i+1)`, while keeping `ROLLBACK TO` as the only stack-truncating
operation.

## Quality

Severity is medium. This is not committed data loss, but it is a user-visible transaction
semantic bug: a valid rollback target disappears, producing a wrong error and preventing the
application from restoring the intended in-transaction state.

## Method Lesson

The efficient txn-module selector is not broad transaction fuzzing. It is:

```text
shared ordered txn state
-> adjacent operations over that state
-> different SQL contracts per operation
-> one implementation reuses the wrong sibling mutation primitive
-> two-marker stack matrix + reference contract oracle
```

This case becomes S21 `txn stack operation semantic split` and O27
`savepoint_stack_semantics_oracle`.

Stop rule: do not enumerate savepoint name case, autocommit modes, or more statement spellings.
Reopen only for another txn state-stack operation split, stronger consequence, or fix validation.
