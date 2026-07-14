# Async recovery can commit a business write after duplicate-key failure

Status: confirmed critical correctness bug on current TiDB/client-go with real TiKV and MDL ON.
Remote bug DB: `found_bug id2550003`.

## User-visible failure

The application receives a definite duplicate-key error from `COMMIT`, so it is entitled to treat the
transaction as aborted. If cleanup of the failed transaction does not finish, a later request can resolve
the remaining async primary lock as committed. A business write from the failed transaction then becomes
durable after the error was returned.

For the verified example, the client received:

```text
Duplicate entry 'used@example.com'
```

but a fresh session observed the account balance change from `0` to `-100`. The candidate table still
contained only the original `used@example.com` row. An application retry can therefore deduct another 100.

## Concrete production trigger

All of the following are required:

1. `tidb_enable_async_commit=ON` and `tidb_enable_1pc=OFF`. Async commit is not enabled by default in
   current TiDB, so this is an explicit cluster or session setting.
2. The transaction is optimistic and uses lazy uniqueness checking. The current default
   `tidb_constraint_check_in_place=OFF` satisfies this condition.
3. The transaction inserts a row with a unique value and later removes that just-inserted row before
   commit. This occurs in workflows that create a tentative/reservation row and revoke it after later
   business validation. The final row and unique-index tombstones become proof-only `CheckNotExists`
   mutations.
4. The transaction also changes real business data, such as an account, quota, inventory, or ledger key.
   The real mutation and proof mutation are sent in different Region batches. Ordinary table growth and
   Region splitting are enough; no DDL race is involved.
5. The business primary prewrite reaches TiKV before the proof Region reports `AlreadyExist`. Prewrite
   batches run concurrently. The SQL probe reproduced this ordering 3/3 without an injected Region delay.
6. After TiDB returns the duplicate error, its background cleanup does not complete. Concrete causes are
   TiDB process exit/OOM, rolling restart in this short window, loss of the TiKV path, or cleanup RPCs
   exhausting their retry budget.
7. After the lock TTL expires, another TiDB request reads or writes the business key. LockResolver sees an
   async primary with an empty recovery set, chooses a nonzero commit TS, and commits the primary.

One representative transaction is:

```sql
SET tidb_enable_async_commit = ON;
SET tidb_enable_1pc = OFF;

BEGIN OPTIMISTIC;
INSERT INTO candidates VALUES (200, 'used@example.com');
DELETE FROM candidates WHERE id = 200;
UPDATE accounts SET balance = balance - 100 WHERE id = 1;
COMMIT; -- returns Duplicate entry 'used@example.com'
```

The existing `used@example.com` row is committed before this transaction. `accounts` and `candidates`
must reside in different Region batches. MDL remains at its default enabled setting throughout.

## Root cause

`initKeysAndMutations` converts optimistic delete-your-writes carrying
`PresumeKeyNotExists` into `Op_CheckNotExists`. These mutations do not write locks. However:

- `checkAsyncCommit` does not reject a transaction containing proof-only keys;
- `asyncSecondaries` deliberately excludes every `Op_CheckNotExists` key;
- the primary Region can therefore store an async lock with no secondary proof members;
- a separate proof Region can return `AlreadyExist`, making `Commit` return a definite error;
- recovery of the primary has no durable evidence that a required proof failed.

This is not the earlier async age-limit bug. There, every prewrite succeeded before a late guard returned
an error. Here a real prewrite batch fails, but the failed proof was outside the recovery certificate.

## Evidence matrix

| Arm | Region delay | Cleanup | Result |
| --- | --- | --- | --- |
| current client-go, raw real TiKV | 200 ms proof delay | interrupted | RED: definite key-exists error, primary recovered committed |
| current TiDB SQL, real TiKV | none | interrupted | RED 3/3: balance `-100` |
| owner counterfactual | none | interrupted | GREEN: balance `0`, lock rolled back |

The owner counterfactual changes only async admission: `hasNoNeedCommitKeys` makes
`checkAsyncCommit` return false. The same SQL, duplicate, Region layout, and cleanup interruption then
remain atomic.

## Fix direction

The minimal safe counterfactual is to disable async commit for transactions containing
`CheckNotExists`. A more general design can include durable proof markers in the recovery certificate,
but recovery must never decide commit without evidence that every commit prerequisite succeeded.

## Deduplication

Post-RED searches in open and closed TiDB, client-go, and TiKV issues found no exact root. TiDB #65757
concerns a committed async transaction with a stale secondary lock and incompatible minCommitTS; it does
not involve a failed uniqueness proof or recovery committing a transaction after duplicate-key failure.
