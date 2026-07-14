# Validated sibling: RC `UPDATE IGNORE` can silently skip a valid unique-key update

Status: local and real-TiKV RED/GREEN complete on current source. This is a new consumer surface of
`STATE_OWNER_SPLIT_IN_WRITE_FASTPATH`, not a new root or bug-count entry. No upstream issue should be
filed separately before the broader RC write-fast-path root is triaged.

## User-visible symptom

In a pessimistic `READ-COMMITTED` transaction with `tidb_rc_write_check_ts=ON`, another transaction
can commit the release of a unique value before a point `UPDATE IGNORE`. TiDB still checks that
unique index at the writer's earlier statement timestamp, treats the deleted index entry as a live
duplicate, and lets `IGNORE` skip the row. The statement returns success with zero affected rows;
after commit, a fresh session sees the old row.

## Production trigger card

```text
workload:
  An account/order reconciliation service uses an explicit pessimistic READ-COMMITTED transaction
  and UPDATE IGNORE to claim a reusable unique business identifier without aborting on real races.
  The update is a point lookup by primary key and changes a UNIQUE column.

producer:
  A normal cleanup or account-retirement transaction deletes the row that currently owns the
  desired unique value and commits. No process pause, network fault, node failure, or failpoint is
  involved.

schedule:
  1. Session A has tidb_rc_write_check_ts=ON, begins a pessimistic RC transaction, and executes an
     earlier statement that observes target row (1,10) and owner row (2,20).
  2. Session B deletes row 2 and commits, releasing unique value 20.
  3. A fresh observer confirms only row (1,10) remains.
  4. Session A runs UPDATE IGNORE ... SET unique_col=20 WHERE id=1 and then commits.
  Required inequality: A.latestOracleTS < B.commitTS < A.UPDATE execution.
  Negative controls: B commits before A begins, or tidb_rc_write_check_ts=OFF.

topology:
  All TiDB, PD, and TiKV components are healthy. MDL is enabled. The schedule needs two ordinary SQL
  sessions and one fresh observer only.

outcome:
  Session A receives success and ROW_COUNT()=0. A fresh session still sees (1,10), although 20 is
  free and the RC update should have persisted (1,20). This is a durable lost update, not a transient
  lock or warning-only discrepancy.
```

The non-default variable is a material reachability limit: `DefTiDBRcWriteCheckTs` is `false` in
current source. The surface is production-reachable only where this TSO optimization is enabled;
MDL remains at its default enabled setting.

## Minimal SQL schedule

```sql
CREATE TABLE account_handle (
  id BIGINT PRIMARY KEY,
  handle_id BIGINT UNIQUE,
  version BIGINT
);
INSERT INTO account_handle VALUES (1,10,100),(2,20,200);

-- Session A
SET transaction_isolation = 'READ-COMMITTED';
SET tidb_txn_mode = 'pessimistic';
SET tidb_rc_write_check_ts = ON;
BEGIN PESSIMISTIC;
SELECT * FROM account_handle WHERE id=1;
SELECT * FROM account_handle WHERE id=2;

-- Session B
DELETE FROM account_handle WHERE id=2;
COMMIT;

-- Session A, after B commits
UPDATE IGNORE account_handle SET handle_id=20, version=101 WHERE id=1;
SELECT ROW_COUNT();
COMMIT;

-- Fresh session
SELECT * FROM account_handle ORDER BY id;
```

Expected: `ROW_COUNT()=1`, final row `(1,20,101)`.

Observed: `ROW_COUNT()=0`, final row `(1,10,100)`.

## Source proof and exact counterfactual

1. `pkg/sessiontxn/isolation/readcommitted.go` accepts a point `Update` for old-TS reuse when
   `tidb_rc_write_check_ts=ON`; the classifier sees the target-row access plan only.
2. `pkg/executor/update.go` selects `DupKeyCheckInPlace` for `UPDATE IGNORE`.
3. `pkg/table/tables/index.go` checks the new unique key through `kv.GetValue(ctx, txn, key)`.
4. The transaction snapshot carries the reused old timestamp, so the deleted unique-index entry is
   returned as `ErrKeyExists`.
5. `UPDATE IGNORE` handles that error by skipping the row. Because no mutation remains, later TiKV
   lock/prewrite conflict handling has no opportunity to retry the statement.

Two counterfactuals made the target GREEN:

- reject `physicalop.Update{IgnoreError:true}` in `planSkipGetTsoFromPD`, forcing a fresh statement
  TSO; this passed on mock storage and real TiKV;
- apply `RCCheckTS` to the transaction-owned helper snapshot; this also fixed the target, but changed
  the stage at which an existing RC conflict test observes conflicts and is therefore too broad as
  a proposed fix.

## Root accounting and quality

The earlier RC `INSERT IGNORE` + FK finding already established the root shape: an outer write fast
path proves freshness for one snapshot owner while a nested semantic checker reads another owner.
This sibling changes the hidden consumer from FK parent existence to unique-index duplicate checking.
It expands blast radius and improves the selector, but must not increment independent-root or bug
counts.

Consequence is severe when reached because a successful statement loses a valid write. Reachability
is narrower than a default-config critical bug because `tidb_rc_write_check_ts` defaults to OFF.
