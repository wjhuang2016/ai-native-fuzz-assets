# Held-out calibration: parameter keys must dominate stateful grouping

This case is not counted as a new bug. PR #66217's AI review had already identified that the
IndexJoin inner-aggregation guard was too permissive for `GROUP BY` expressions. The value here is
that the testing loop independently derived an executable, deterministic oracle for that review
finding.

## Proof obligation

```text
P: the IndexJoin inner key column appears somewhere inside the GROUP BY expression.
Q: parameterizing the inner plan by that key cannot split a semantic aggregation group.
F: column membership is not functional dominance. GROUP BY a+b can put different a values in the
   same group, while IndexJoin evaluates different a lookup tasks independently.
```

Source anchor: `checkIndexJoinInnerTaskWithAgg` uses
`ExtractColumnsMapFromExpressions(GroupByItems...)` and checks only that every datasource inner key
is in that column set. For `GROUP BY a+b`, key `a` passes even though `a` does not determine `a+b`.

## Strong oracle

Use two inner rows `(a,b,v)=(1,1025,10)` and `(1025,1,20)`, which share group key `a+b=1026`.
Place lookup keys 1 and 1025 in an outer clustered-PK table containing 1..1025, and set
`tidb_index_join_batch_size=1`.

- Forced HashJoin computes the derived table once and returns `(1,1026,30)`.
- Forced IndexJoin crosses execution tasks and returns two partial groups:
  `(1,1026,10)` and `(1025,1026,20)`.
- `EXPLAIN ANALYZE` proves `IndexJoin task:33`; the one-task two-row control returns the same row as
  HashJoin and explains why a tiny test misses the bug.

This upgrades the oracle from "different join algorithms return different rows" to a triggered
schedule oracle: both plan identities must be proven, and the RED requires more than one
parameterized inner task.

## Method improvement

When a stateful operator such as aggregation, windowing, deduplication, limit, or top-N is moved
under a parameterized executor, do not ask only whether the parameter key is mentioned in the
operator expression. Require one of:

1. exact grouping-key inclusion;
2. a proven functional dependency from the parameter key to the state partition key; or
3. execution that preserves one global state domain across all parameter tasks.

The minimal matrix is `one task GREEN / cross-task RED / non-parameterized reference`. This is a
better selector than broadly fuzzing join hints or GROUP BY expressions.

## Dedup and stop rule

Dedup: known PR review finding in merged PR #66217, not a new root and not a new upstream issue.
Stop after this held-out RED. Do not enumerate arithmetic expressions, join types, or batch sizes.
Reopen only for another stateful operator or a distinct missing dominance proof.
