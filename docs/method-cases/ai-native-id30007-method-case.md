# Method Case id30007: sibling DDL path asymmetry

## Result

`ALTER TABLE ... REORGANIZE PARTITION` can leave a replacement global index incomplete. The DDL succeeds, `SHOW CREATE TABLE` reports a valid global index, but `USE INDEX` misses rows from a later non-touched partition and `ADMIN CHECK TABLE` reports `8223`.

Artifacts:

- Probe: `/Users/bba/pc/ai_native_ddl_reorg_global_index_reference_probe.py`
- Draft: `/Users/bba/pc/ai-native-reorg-global-index-reference-draft.md`

## Why This Worked

The earlier object-reference matrix had already made ordinary global-index partition paths green:

```text
DROP PARTITION
TRUNCATE PARTITION
REMOVE PARTITIONING
PARTITION BY ... UPDATE INDEXES
```

The next useful move was not to expand every global-index case. It was to find the sibling DDL path whose implementation shape is different:

```text
REORGANIZE PARTITION
  -> copy rows from dropping partitions
  -> build indexes on adding partitions
  -> backfill replacement global indexes from non-touched partitions
```

That third phase is a separate iterator and has a precise proof obligation:

```text
new global index entries
= rows from adding partitions
+ rows from every still-live non-touched partition
```

The oracle is unusually strong and cheap:

```text
USE INDEX(global_idx) rowset == IGNORE INDEX(global_idx) rowset
AND ADMIN CHECK TABLE passes
```

## Selector Upgrade

The id30006 selector was:

```text
DDL-created side metadata
+ current-schema public surface
+ no obvious cleanup/rekey
= stale metadata target
```

id30007 adds another selector:

```text
same owner has green coverage on common DDL paths
+ a sibling DDL path uses a different multi-stage iterator
+ comments/source state an "all remaining objects" proof obligation
+ rowset or ADMIN CHECK oracle is cheap
= high-value small matrix target
```

## Why It Is Not Drift

This stays inside the DDL lane:

- target DDL: `REORGANIZE PARTITION`
- owner: replacement global index metadata/data built by DDL
- public current-schema surface: `SHOW CREATE TABLE` still exposes `GLOBAL`
- consequence oracle: index rowset and `ADMIN CHECK TABLE`

The executor is only used to observe the DDL aftermath.

## Next Improvement

When a matrix finds a green owner, do not immediately leave that owner. First ask:

```text
Which sibling DDL paths use a different prepare/iterate/finalize path?
Which path says "all partitions/indexes/refs" but implements "next one"?
Can a 2-row table put one row before and one row after the changed range?
```

That is the pattern that made this hit small.

## Pause Gate

This candidate has reached the "pause before expansion" gate. The useful work now is to agree on the repair contract, not to add more random `REORGANIZE PARTITION` cases.

Checklist:

- Minimal repro: one `REORGANIZE PARTITION p1` and two rows, one inside the reorganized range and one in a later non-touched partition.
- Oracle: `USE INDEX(global_idx)` rowset must equal `IGNORE INDEX(global_idx)`, and `ADMIN CHECK TABLE` must pass.
- Green neighbor: partition placement refs rewrite correctly in the same DDL shape, so the failure is specific to replacement global-index backfill.
- Root model: the non-touched phase can re-enter `AddingDefinitions` iteration and finish before visiting later non-touched partitions.
- Repair contract: replacement global-index backfill must visit exactly `pi.Definitions - pi.AddingDefinitions - pi.DroppingDefinitions` once the adding-partition phase is complete.
- Stop rule: do not expand to hash/list/multiple-index variants until the repair contract is accepted or rejected.

## Fix Validation Contract

After a code fix exists, validation should prove the iterator contract rather than only replay the current repro:

| Shape | Purpose |
|---|---|
| Reorganize a middle range with live rows in a later non-touched partition | Current red case; proves no tail partition is skipped |
| Reorganize a middle range with live rows in both earlier and later non-touched partitions | Proves the non-touched iterator does not stop after entering/finishing `AddingDefinitions` |
| Reorganize the first range with later non-touched partitions | Proves the fix does not rely on a preceding non-touched partition |
| Reorganize the last range with earlier non-touched partitions | Proves ordinary earlier non-touched backfill still works |
| Reorganize all partitions so no non-touched partition remains | Proves the empty set is handled without accidental extra work |

For every shape, the same oracle should be enough:

```text
global-index rowset == table rowset
AND ADMIN CHECK TABLE passes
```

This is a fix-validation matrix, not the next bug-hunting direction. The next hunting direction should reuse the selector on a different DDL owner/path pair.
