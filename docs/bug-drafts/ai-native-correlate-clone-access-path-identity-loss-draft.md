# Alternative logical plan can return no rows for a nonempty aggregate IN subquery

Status: issue-filed high severity as remote `found_bug id2070003` and upstream
https://github.com/pingcap/tidb/issues/69790.

## User-visible behavior

With `tidb_opt_enable_alternative_logical_plans=ON`, a valid aggregate `IN` subquery over a
nonempty table can be replaced by `TableDual(rows:0)`. The query returns no rows without an error.
The same SQL with the feature disabled returns the expected ids `1,2,3`.

The issue reproduces at the default hash/merge join cost factors. It needs no fault injection,
concurrency, topology change, or unusual data. The feature is currently disabled by default, which
limits reachability but does not weaken the wrong-result consequence when enabled.

## Root cause

`cloneDataSource` correctly tries to isolate mutable access paths between the Join and Apply
alternatives. However, it deep-clones `AllPossibleAccessPaths` and `PossibleAccessPaths`
independently. Those slices are two views of the same path objects, not two independent object
graphs.

Stats derivation fills ranges through `AllPossibleAccessPaths`. Physical planning later consumes
`PossibleAccessPaths`. In the clone, the active path objects therefore retain nil ranges. For a
plain correlated `IN`, `resetStatsForCorrelatedDS` rebuilds both slices and masks the defect. With
`MAX(a) ... GROUP BY b`, the correlated equality stays above the aggregation, so the leaf
DataSource is not rebuilt. `find_best_task` interprets the empty active ranges as an empty scan and
creates `TableDual`.

## Evidence matrix

| Arm | Aggregate IN | Plain IN | Inner access | Result |
| --- | --- | --- | --- | --- |
| Feature OFF | `1,2,3` | `1,2,3` | real scan | GREEN |
| Feature ON, current source | empty | `1,2,3` | aggregate uses `TableDual` | RED |
| Feature ON, alias-preserving clone | `1,2,3` | `1,2,3` | real scan | GREEN |

A nine-cell local matrix changed only the aggregate-IN cell. The exact alias-preserving
counterfactual kept the Apply alternative selected while replacing `TableDual` with a real scan and
made all nine cells GREEN. Testbed 8220955 reproduced the minimal SQL with real TiKV.

The portable regression scaffold was also replayed from the stored files: current source failed
with `off=[1 2 3] on=[]`, while applying only the stored alias-mapping patch passed with
`off=[1 2 3] on=[1 2 3]` and a real inner scan. Evidence is preserved in
`assets/store/logs/correlate-access-path-scaffold-current-red.log` and
`assets/store/logs/correlate-access-path-scaffold-fix-green.log`.

## Fix direction

Clone every canonical access path once, build an original-to-clone map, and make each cloned active
path reference the corresponding canonical clone. This preserves both required invariants:

1. alternatives do not share mutable AccessPath objects;
2. canonical and active views inside one alternative do share the same path object.

## Method result

This promotes `CLONED_CANONICAL_ACTIVE_VIEW_IDENTITY`: when code deep-clones a structure with
multiple indexes or filtered views over shared mutable objects, field equality is insufficient.
The clone must preserve the original alias graph inside its ownership boundary. The fastest matrix
changes whether a downstream repair path touches the leaf, then compares fast-path and bypass
rowsets plus the physical scan altitude.
