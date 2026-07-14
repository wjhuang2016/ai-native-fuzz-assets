# Local temporary table plus pessimistic retry: a production-reachable GREEN

## Production workload

A settlement or data-migration service creates a connection-local temporary table containing target
account values. In one explicit pessimistic transaction it applies those values to the persistent
account table and advances the staging row in the same multi-table UPDATE:

```sql
BEGIN PESSIMISTIC;
UPDATE account AS a JOIN tmp_target AS s ON a.id = s.id
SET a.balance = s.target_balance, s.target_balance = s.target_balance + 1;
COMMIT;
```

At the same time, an ordinary payment or status worker updates the same persistent account. A large
join, a hot account, normal TiKV backoff, or storage latency can produce this schedule:

```text
batch begin
  < batch obtains statement forUpdateTS
  < online worker commits the account row
  < batch stages temporary and persistent mutations
  < batch LockKeys sees a write conflict
  < TiDB rolls back the first statement stage
  < TiDB reopens and successfully replays the UPDATE
  < application receives success and executes COMMIT
```

The last edge is important: the client does not commit after an ignored error. TiDB hides the
retry and returns UPDATE success, so the application's existing transaction flow reaches COMMIT.

## Defaults and schedule compression

- Metadata locking stayed ON.
- `REPEATABLE-READ` stayed at the session default. The test did not override
  `tidb_pessimistic_txn_fair_locking`; its resolved global value is not part of this owner proof.
- `BEGIN PESSIMISTIC` is the disclosed workload choice.
- The two existing breakpoints only fixed the interval around the real competing commit. They did
  not inject a write conflict, storage error, or result.

## Proof obligation

`UpdateExec` writes both table mutations into the transaction MemDB before pessimistic DML calls
`KeysNeedToLock`. TiDB filters temporary-table keys from the physical lock set, so the persistent
key supplies the real conflict. Before rebuilding the executor, `handlePessimisticLockError` routes
through `StmtRollback`. Local temporary-table data is copied from the transaction MemDB to the
session owner only at transaction commit.

If first-attempt temporary state escaped that rollback, replay would read the advanced value and
make the final pair `temporary=42, persistent=41`. Correct one-shot execution yields
`temporary=41, persistent=40`.

## Result

The failpoint-enabled local run was GREEN on TiDB `2964713`:

- no-conflict control: temporary/persistent values were `31/30`, then ROLLBACK restored the durable
  table;
- natural-conflict cell: the existing write-conflict path performed one transparent retry;
- after application COMMIT: temporary/persistent values were `41/40`;
- a fresh session observed persistent value `40`, and `ADMIN CHECK TABLE` passed.

This closes the local temporary-table surface under the current statement staging owner. Reopen the
selector only for a side effect published outside staging, or a retry/fallback path that rebuilds an
executor without rolling that owner back.

## Method lesson

A realistic production story is part of the experiment design. It identifies the natural producer,
explains why the client reaches the later COMMIT, and determines the control. The breakpoint becomes
a schedule compressor only after those edges are named. A passing retry test without the first-stage
mutation order or durable feedback oracle would not close this candidate.
