# Partial-index pushdown recheck-elision negative case

## Source proof obligation

Current fast ADD INDEX builds a TiKV `Selection` for a partial-index condition. When the whole
condition can be encoded and the job builds exactly one index, `indexIngestWorker` skips the TiDB
condition checker completely.

```text
P: the whole expression can be pushed to TiKV
Q: TiKV and TiDB evaluate it identically for every admitted value
F: a wrong remote decision becomes a missing or extra durable index key
```

This is distinct from partial-index planner applicability. The owner under test is index
construction, not query path selection.

## Bounded matrix

The current condition grammar admits only `IS [NOT] NULL` and one column-to-literal comparison.
The matrix therefore covered its semantic boundaries rather than arbitrary SQL syntax:

- unsigned values versus a negative integer literal;
- ENUM and SET numeric/string equality;
- binary and case-insensitive collation, including PAD SPACE;
- decimal precision and floating negative zero;
- DATETIME/TIMESTAMP DST boundaries, negative TIME, BIT, and YEAR.

The candidate query used an ordinary predicate and plan evidence showed `Selection` in
`cop[tikv]`. The reference used
`IF(predicate, LAST_INSERT_ID(id), NULL) IS NOT NULL`; its row-level side effect kept the entire
predicate in a root `Selection` while preserving `predicate IS TRUE` semantics.

## Result

All 15 rowset pairs matched. The target is retired before ADD INDEX execution because there is no
semantic RED to lift into a physical-index oracle.

The first reference used `CONNECTION_ID()`. TiDB folded it into a constant and pushed the reference
back to TiKV, making that attempt INVALID. This is useful methodology evidence: a differential is
not real until plan evidence proves the two sides have different semantic owners.

## Reusable rule

`can push` proves capability, not equivalence. Whenever that boolean disables a local checker:

1. identify the remote and local semantic owners;
2. construct an equivalent reference that cannot be partially pushed;
3. verify both owners in the plans;
4. lift only a semantic RED to the durable or user-visible consumer;
5. retire after the complete admitted expression domain is GREEN.

Do not reopen this target by adding literals. Reopen only when the product admits a new expression
class or independent source evidence exposes a new TiKV/TiDB semantic ownership split.
