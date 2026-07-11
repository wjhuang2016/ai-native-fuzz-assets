# id30004 Method Case: table-cache side metadata and DROP DATABASE

## Goal

Use the table-cache `DROP DATABASE` red cell as a complete AI-native DDL bug-discovery case.

This case is not about adding more table-cache tests. It is about validating and improving this selector:

```text
object-identity side metadata
+ sibling DDL paths already have explicit block/cleanup controls
+ broader container DDL removes the object through a different path
= stale side metadata after DDL
```

## Why AI Could Mark This Cell Before Running It

The high-signal facts were visible from the code and behavior matrix:

1. `mysql.table_cache_meta` is keyed by `tid`, so it is object-identity metadata, not a name-bound policy.
2. `ALTER TABLE ... CACHE` creates a side row and `ALTER TABLE ... NOCACHE` has a finish hook that deletes it.
3. Direct sibling DDL paths already know cached tables are special: drop table, truncate table, rename table, index DDL, columnar index DDL, and partition DDL all reject cached tables.
4. `DROP DATABASE` removes tables through `ActionDropSchema`, which is a broader container path and can bypass single-table guards.

So the predicted red cell was:

```text
cached table
+ DROP DATABASE containing that table
+ no schema-level cache scan or drop-schema cache cleanup
= dropped table ID remains in mysql.table_cache_meta
```

## Oracle

This is a DDL metadata ownership oracle, not an executor oracle:

```text
after DDL removes the object:
  information_schema.tables has no table with the old table ID
  mysql.table_cache_meta has no row for the old table ID
```

The green controls matter:

- `CACHE` creates a table-ID side row.
- `NOCACHE` removes that row.
- direct cached-table DDL paths block and preserve the row.

Those controls prove the oracle is looking at the right owner, not at random sys-table noise.

## Result

Probe:

```text
/Users/bba/pc/ai_native_ddl_table_cache_reference_probe.py
```

Observed result:

```text
SUMMARY total=3 findings=1 skipped=0
```

The finding is one root family:

```text
DROP DATABASE succeeds for a schema containing a cached table,
the table disappears from information_schema.tables,
but mysql.table_cache_meta still contains the dropped table ID.
```

Issue/repro draft:

```text
/Users/bba/pc/ai-native-table-cache-drop-database-draft.md
```

## Fix Semantics

Narrow source validation favors blocking:

```text
DROP DATABASE db_with_cached_table
=> ErrOptOnCacheTable("Drop Database")
```

Reason:

- most object-changing DDL on cached tables already blocks with `ErrOptOnCacheTable`;
- `ALTER TABLE NOCACHE` is the intentional path for turning cache state off;
- allowing `DROP DATABASE` to silently remove cached tables would diverge from the sibling DDL policy.

The alternative valid fix is cleanup:

```text
DROP DATABASE succeeds
+ ActionDropSchema final state deletes mysql.table_cache_meta rows for all dropped table IDs
```

This is viable, but it should be an explicit owner decision because it changes the current "cached table must first be made non-cached before object-changing DDL" policy.

## Method Lesson

This case improves the DDL reference methodology in three ways.

First, it adds a prefilter:

```text
sys table has db/table/column strings
!= object-identity reference
```

Privilege grants are the negative example: they are name-bound policy, so rename/drop not rewriting them is not automatically a DDL bug.

Second, it raises container DDL priority:

```text
if single-object DDL has explicit owner checks,
then container DDL must either call an equivalent check
or clean all owned side metadata for contained objects.
```

Third, it clarifies the pause rule:

```text
after one red cell:
  stop expanding variants
  minimize
  decide block vs cleanup semantics
  update the selector
  only then pick the next owner
```

## Next Owner Search

Use id30004 to search for the next DDL-only owner, not for more table-cache cases.

Prioritize owners with all of these:

1. side metadata keyed by object ID or mixed ID/name state;
2. public `SHOW`, information_schema, or system table surface that exposes stale references;
3. direct table/index/partition DDL has owner-specific block, rewrite, or cleanup;
4. a broader entrypoint can bypass that helper: `DROP DATABASE`, multi-table DDL, partition reorg, truncate with ID replacement, recover/flashback, or batch cleanup;
5. a low-noise oracle can verify old reference removed and new reference protected.

Downweight owners when:

- metadata is name-bound policy by design;
- existing green probes already cover the basic happy path;
- the only remaining oracle depends on slow background GC or manual timing;
- the target would pull the work into pure optimizer/executor behavior.

## Stop Rule

Do not continue table-cache fuzzing unless one of these happens:

1. owner feedback asks for a specific variant;
2. a fix is proposed and needs validation;
3. source changes create a new table-cache DDL entrypoint.

Otherwise, the next move is a new DDL side-metadata owner selected by the refined selector above.
