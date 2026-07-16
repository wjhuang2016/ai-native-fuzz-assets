# Fast ADD INDEX x PessimisticLockNotFound Mechanism Proof

## Result

- Status: mechanism RED, production reachability still open.
- Error reproduced twice on fresh stores:
  `PessimisticLockNotFound { reason: NonLockKeyConflict }`.
- Topology: one TiDB/domain and one real TiKV, TiDB v8.5.1.
- Configuration: MDL ON, fast ADD INDEX ON, distributed reorg OFF, pessimistic transaction.
- Conclusion: multiple TiDB instances are not required for this error mechanism.

This result deliberately does not claim a pure-SQL reproduction. It uses two narrow
counterfactuals: DDL merge omits the row `Op_Lock`, and the target prewrite is marked
as an RPC retry. Those are now the only unresolved production-reachability obligations.

## Proof Obligation

P: a pessimistic DML has an old `forUpdateTS` and does not pessimistically lock a
touched non-unique secondary-index key.

Q: the row lock is sufficient protection, so prewrite may skip the secondary-index
conflict check.

Counterexample schedule:

1. Fast ADD INDEX scans the row with `k=0`.
2. Before Lightning import completes, DML changes `k=0 -> 1`, creating temp-index
   delete and insert operations.
3. The target pessimistic DML starts in `BackfillStateMerging`, obtains its old
   `forUpdateTS`, and pauses before sending its lock request.
4. DDL merge writes the normal non-unique index key for `k=1` without a row guard.
5. The target resumes, changes `k=1 -> 2`, and sends a retried prewrite.
6. TiKV sees a committed secondary-index version newer than `forUpdateTS` and returns
   `PessimisticLockNotFound/NonLockKeyConflict`.

## Strong Evidence

Both fresh runs had the same closed timestamp relation:

```text
run 1: startTS=467721309854892054
       lockForUpdateTS=467721309854892054
       prewriteForUpdateTS=467721309854892054
run 2: startTS=467721327431909389
       lockForUpdateTS=467721327431909389
       prewriteForUpdateTS=467721327431909389
```

In both runs, merge reported `scan count=2` and `added count=2`, the target returned
the exact error, the test passed, and `ADMIN CHECK TABLE` succeeded after DDL finished.

## Controls And Retired Selectors

- Seeding at `BackfillStateReadyToMerge` produced `scan count=0`: too late to create
  a temp-index record.
- Seeding from a `job.RowCount > 0` observation was not a valid import boundary.
- `MockDMLExecutionStateBeforeImport` produced the intended temp-index closure.
- With the normal DDL row `Op_Lock`, TiKV raised the target prewrite `forUpdateTS`
  after merge, and the target committed. This control is GREEN and proves the row
  guard is the decisive protection in the one-row schedule.
- Generic concurrent DML, TiKV restarts, and guessed timing windows were retired;
  they did not establish either the missing row guard or retry semantics.

## Reusable Assets

- TiDB test: `scaffolds/tidb-tests/ai_native_pessimistic_lock_not_found_fast_add_index_test.go`
- TiDB support patch: `scaffolds/tidb-tests/ai_native_pessimistic_lock_not_found_fast_add_index_support.patch`
- client-go instrumentation:
  `scaffolds/client-go-tests/ai_native_pessimistic_lock_not_found_instrumentation.patch`

The test should run with exactly one TiDB/domain against real TiKV. The two hook
callbacks are filtered by the target transaction `startTS` so internal DDL traffic
cannot satisfy the oracle accidentally.

## Next Selector

Search only for natural owners of the two remaining conditions:

1. Row protection missing, delayed, or not observed by the DML lock request while
   DDL merge has already made the normal secondary-index version durable.
2. A real prewrite RPC retry after that version becomes durable, such as a precise
   region error, response loss, or request reroute that sets `IsRetryRequest`.

The next experiment must preserve the exact timestamp and key oracle above. A mere
transaction error, lock wait, or successful retry is not a hit.
