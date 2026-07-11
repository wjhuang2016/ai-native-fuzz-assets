# id30006 Method Case: Hypo index session metadata stale after DDL

## Why This Target Was Selected

After resource-group `SWITCH_GROUP` turned out to be an unvalidated name parameter, the selector tightened:

```text
only enter the rewrite/block matrix when create/alter validates the referenced object
and the metadata has a public surface after later DDL.
```

Hypo indexes satisfy that bar:

- `ALTER TABLE t ADD INDEX idx(a) USING HYPO` validates table and column names;
- the metadata is stored outside `TableInfo`, in `SessionVars.HypoIndexes`;
- `SHOW CREATE TABLE` merges that session-local metadata back into a DDL-looking table definition;
- normal column/table/database DDL does not update the session map.

## Probe Result

Probe:

```bash
python3 /Users/bba/pc/ai_native_ddl_hypo_index_reference_probe.py
```

Result:

```text
SUMMARY total=7 findings=6 skipped=0
```

The green control proves the hypo index is visible while the original column exists. The red cells show stale or resurrected metadata after:

- `RENAME COLUMN`
- `CHANGE COLUMN`
- `DROP COLUMN`
- `DROP TABLE` + recreate
- `RENAME TABLE` + recreate old name
- `DROP DATABASE` + recreate

## Why The Method Worked

The important move was not broad fuzzing. It was comparing two near-neighbor index owners:

- columnar/vector index looked promising, but current runtime lacked TiFlash/failpoint support for live SQL probing;
- hypo index was the same broad family, but with a cheaper oracle and a clearer side-metadata split.

The proof obligation became:

```text
if a session-local index is shown as part of table DDL,
then DDL that changes/removes the table or indexed column
must invalidate, rewrite, or block that session-local index.
```

That gives high target density: one owner, one side map, seven DDL paths, one `SHOW CREATE` oracle.

## Selector Improvement

Add a new positive selector:

```text
session-local or cache-local side metadata
+ created by DDL syntax after validating object names
+ merged into public DDL/API output
+ keyed by schema/table/column names instead of object IDs
= high-value invalidation/rekey target
```

And keep the negative rule from resource-group:

```text
if create/alter does not validate target existence,
do not treat the name field as a maintained DDL reference edge.
```

## What This Improves

This hit improved the search in three concrete ways:

1. **Target density**: instead of scanning all index features, it picked the one index owner whose metadata lives outside `TableInfo` but is later printed as table DDL.
2. **Proof-obligation precision**: the invariant was narrow: after DDL changes/removes a referenced table or column, a session-local DDL artifact must be rewritten, removed, or blocked.
3. **Oracle sensitivity**: `SHOW CREATE TABLE` gave a cheap DDL metadata oracle, and replayability of the emitted key definition proved the stale output is not just cosmetic.

The key was the contrast with negative samples:

```text
 resource-group SWITCH_GROUP: unvalidated name parameter -> not a maintained reference
 view text: validated at create time, but stored as name-bound SQL text -> invalid view is expected semantics
 region split policy: stored inside the object metadata that DDL moves/drops -> low side-owner risk
 hypo index: validated DDL artifact + external session map + public DDL output -> high invalidation risk
```

## Next Target Card

Next search should stay on the same proof obligation, but use the refined selector instead of expanding hypo-index variants:

```text
 DDL syntax creates or mutates an auxiliary object
 auxiliary metadata is stored in SessionVars, an in-memory cache, or a side table
 metadata is keyed by schema/table/column/index names or old object IDs
 public SQL surface later merges it into SHOW/INFORMATION_SCHEMA/DDL-like output
 normal table/column/container DDL does not obviously call a cleanup/rekey helper
 = build a 3-7 cell DDL invalidation matrix
```

Candidate families should be screened in this order:

- session-local DDL artifacts first, because stale state can be proven without waiting for background workers;
- cache-local or sys-table owners with immediate public display next;
- async job history, recommendations, and runtime learning artifacts last, because they are often historical records rather than DDL-owned references.

The next positive result should answer one question:

```text
Can the selector predict another DDL stale-reference bug before SQL probing,
or does the next owner contain a cleanup/rekey helper that should become a new negative rule?
```

## Stop Rule

Do not expand hypo-index variants now. The current red cells already share one root cause: no DDL invalidation/rekey for `SessionVars.HypoIndexes`. The useful next step is owner discussion or a fix-semantics decision, not more case enumeration.
