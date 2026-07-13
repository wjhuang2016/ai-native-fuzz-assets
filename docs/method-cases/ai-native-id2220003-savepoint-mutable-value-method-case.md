# id2220003: savepoint mutable-value owner closure

Remote bug DB: `found_bug id2220003`, confirmed moderate severity.

## Why the selector worked

The starting proof obligation was simple:

```text
After ROLLBACK TO SAVEPOINT, every later correctness or admission decision must observe state that
is equivalent to the savepoint, unless the state is intentionally monotonic.
```

The first source comparison looked safe. `TxnCtxNeedToRestore` explicitly snapshots table deltas,
pessimistic lock cache, cached-table membership, and TTL counters. `TemporaryTables` was explicitly
placed in `TxnCtxNoNeedToRestore`.

The useful move was to stop treating that type declaration as proof and traverse the object graph:

```text
TemporaryTables map[int64]TempTable
  -> TemporaryTable value
     -> size, modified, stats, allocator
        -> checkTempTableSize consumes size after rollback
```

`size` is neither immutable nor transaction-monotonic. It is updated by writes after the savepoint,
but only MemDB is rolled back. This produced a direct P/Q/F:

- **P:** the row buffer was restored to the savepoint.
- **Q:** temporary-table admission state is therefore also at the savepoint.
- **F:** `checkTempTableSize` rejects a later write using the stale mutable value.

## Efficient matrix

Only three observations were needed:

| Cell | State after rollback | Next write | Result |
| --- | --- | --- | --- |
| Control | empty, no large rolled-back segment | 1 byte | succeeds |
| RED | empty after 1.2 MB rolled back | 1 byte | table full |
| Counterfactual | same RED, restore only size | 1 byte | succeeds |

The row-count oracle prevented an ambiguous interpretation that data might still exist. The
one-variable counterfactual proved owner attribution.

## Method improvement

Extend reference-reset differential from fields to reachable mutable state:

```text
N = state graph at normal/savepoint snapshot
R = state graph restored at rollback/retry
C = state graph consumed afterward
debt = (mutable(N) intersect C) minus R
```

For every map, slice, interface, pointer, cache, or handle in N/R:

1. classify container membership separately from value state;
2. identify fields mutated inside the rollback window;
3. follow each field to its highest consumer;
4. admit only reachable consumers with a user-visible oracle;
5. use an exact field-level restore as the counterfactual.

This refinement also explains the useful negatives from the same pass: cached-table values are
shared immutable handles for this purpose; pessimistic primary membership survives through lock
flags; explicit optimistic whole-transaction replay is product-blocked; pipelined DML cannot enter
an explicit savepoint transaction.

## Quality assessment

The discovery is strong method evidence because it is current-source-derived, naturally reachable,
SQL-only, exact-commit reproducible, and counterfactual-closed. The bug itself is moderate because
the highest consumer is transaction-local availability rather than durable correctness.

The reusable asset pack is complete (`open_gaps=[]`): selector, obligation, oracle, natural fault
boundary, scenario, schedule, local RED, exact GREEN, and testbed RED are all linked from the same
obligation. A later savepoint pass can start from this pack and search only for a new mutable owner
or a higher consumer.
