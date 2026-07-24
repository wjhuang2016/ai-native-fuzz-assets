# ADMIN RECOVER INDEX concurrent-update negative

Status: current-master GREEN boundary on local TiKV-compatible unistore with a deterministic
schedule probe. Temporary source and test hooks were removed.

## Proof obligation

`ADMIN RECOVER INDEX` scans a record snapshot, checks which index keys are absent, and then creates
the missing keys in a retryable optimistic transaction. The candidate asked whether an application
could update the indexed value after that snapshot and make the repair publish the old index key.

The minimized schedule was:

1. create `(id=1, u=10)` and remove only index key `(u=10, id=1)`;
2. pause recover after its table scan and missing-key check;
3. update the row to `u=20`;
4. resume recover;
5. compare primary and forced-index rowsets and run `ADMIN CHECK TABLE`.

## Result

Two independent guards close the candidate:

- with the default transaction assertion level, the application UPDATE is rejected because deleting
  the already-missing old index key violates its `Exist` assertion;
- with `tidb_txn_assertion_level=OFF`, the UPDATE commits, but recover then gets a write conflict on
  the old index key and `RunInNewTxn` retries the whole batch from a fresh snapshot.

The second run finishes with row `(1,20)`, no `u=10` index result, and a green
`ADMIN CHECK TABLE`.

## Method refinement

For `BACKDATED_PHYSICAL_INGEST_WITHOUT_WRITE_FENCE`, screen candidate writers in this order:

1. does normal DML carry assertions that reject the already-inconsistent source state?
2. does the delayed writer include every stale physical owner in its transaction write set?
3. will a conflict retry re-read source values, or only replay cached values?
4. only test a live-target corruption matrix when all three answers leave a gap.

`ADMIN RECOVER INDEX` is safe for this schedule because both the application and repair writers use
the same old index key as a conflict witness. Reopen only if another repair path omits that owner,
suppresses the conflict, or replays cached rows after retry.
