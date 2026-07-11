# DROP DATABASE leaves mysql.table_cache_meta rows for cached tables

## Issue-Ready Summary

Suggested title:

```text
DROP DATABASE leaves mysql.table_cache_meta rows for cached tables
```

## Minimal Reproduction

Environment used for this probe:

- failpoint-enabled local TiDB on `127.0.0.1:14000`
- TiKV store
- DDL owner: `fp-tidb`

```sql
DROP DATABASE IF EXISTS ai_native_cache_drop_schema;
CREATE DATABASE ai_native_cache_drop_schema;
USE ai_native_cache_drop_schema;

CREATE TABLE t(id INT PRIMARY KEY, v INT);
ALTER TABLE t CACHE;

SET @tid := (
  SELECT tidb_table_id
  FROM information_schema.tables
  WHERE table_schema = 'ai_native_cache_drop_schema'
    AND table_name = 't'
);

SHOW CREATE TABLE t;
SELECT tid, lock_type, lease, oldReadLease
FROM mysql.table_cache_meta
WHERE tid = @tid;

DROP DATABASE ai_native_cache_drop_schema;

SELECT tid, lock_type, lease, oldReadLease
FROM mysql.table_cache_meta
WHERE tid = @tid;

SELECT table_schema, table_name
FROM information_schema.tables
WHERE tidb_table_id = @tid;

-- cleanup after reproducing
DELETE FROM mysql.table_cache_meta WHERE tid = @tid;
```

## Expected Behavior

`DROP DATABASE` should be consistent with other DDL on cached tables:

- either reject the operation with the same cache-table error family used by `DROP TABLE`, `RENAME TABLE`, `TRUNCATE TABLE`, index DDL, and partition DDL;
- or complete the drop and remove `mysql.table_cache_meta` rows for all dropped tables.

In either case, after the database no longer exists, `mysql.table_cache_meta` should not contain a row for the dropped table ID.

## Actual Behavior

`DROP DATABASE` succeeds, the table disappears from `information_schema.tables`, but `mysql.table_cache_meta` still contains the old table ID:

```text
DROP DATABASE succeeded but left mysql.table_cache_meta row for dropped table_id=1415: ['1415\tNONE\t0\t0']
```

The probe output:

```text
OK       cache_nocache_metadata_lifecycle       CACHE creates table-id row and NOCACHE removes it
OK       cached_table_blocks_direct_table_and_index_ddl       cached table blocked rename/drop/truncate/index/partition DDL and preserved side metadata
FINDING  drop_database_with_cached_table_cleans_or_blocks_cache_meta       DROP DATABASE succeeded but left mysql.table_cache_meta row for dropped table_id=1415: ['1415\tNONE\t0\t0']
SUMMARY total=3 findings=1 skipped=0
```

Probe:

```text
/Users/bba/pc/ai_native_ddl_table_cache_reference_probe.py
```

## Why This Looks Like A DDL Reference Bug

`mysql.table_cache_meta` is keyed by table ID:

- `pkg/meta/metadef/system_tables_def.go:360` defines `mysql.table_cache_meta(tid, lock_type, lease, oldReadLease)` with `PRIMARY KEY (tid)`.
- `pkg/ddl/executor.go:6935` writes `replace into mysql.table_cache_meta values (... t.Meta().ID ...)` before submitting `ActionAlterCacheTable`.
- `pkg/ddl/job_worker.go:428` / `:431` delete the row when `ActionAlterNoCacheTable` finishes.

Most DDL paths explicitly block cached tables:

- `pkg/ddl/executor.go:4310` blocks `DROP TABLE`.
- `pkg/ddl/executor.go:4438` blocks `TRUNCATE TABLE`.
- `pkg/ddl/executor.go:4517` blocks `RENAME TABLE`.
- `pkg/ddl/index.go:681`, `:1173`, `:2080` block rename/create/drop index on cached tables.

`DROP DATABASE` appears to miss both choices:

- `pkg/ddl/executor.go:763` builds an `ActionDropSchema` job after FK checks, but has no cached-table scan.
- `pkg/ddl/schema.go:158` handles `onDropSchema`; in the final state it cleans affinity groups and masking policies, then drops the schema and records dropped table IDs, but does not clean `mysql.table_cache_meta`.
- `pkg/ddl/delete_range.go:284` consumes the dropped table IDs for delete-range registration, not for table-cache metadata cleanup.

## Fix-Direction Validation

The existing code leans toward "block before dropping" as the most consistent first fix direction:

- `ALTER TABLE` rejects any cached-table operation other than `CACHE`/`NOCACHE`.
- `DROP TABLE`, `TRUNCATE TABLE`, `RENAME TABLE`, multi-table rename, index DDL, columnar index DDL, and partition DDL all reject cached tables with `ErrOptOnCacheTable`.
- `ALTER TABLE NOCACHE` is the only path that intentionally turns cache state off, and its finish hook deletes `mysql.table_cache_meta`.
- `DROP DATABASE` currently does FK validation before submitting `ActionDropSchema`, but does not scan the tables in the schema for cache state.

So the lowest-surprise behavior is probably:

```text
DROP DATABASE db_with_cached_table
=> ErrOptOnCacheTable("Drop Database")
```

The alternative is to allow `DROP DATABASE` and clean `mysql.table_cache_meta` for every dropped table ID in the final `onDropSchema` state. That would also be semantically valid, but it would diverge from the sibling DDL policy that cached tables must first be taken out of cache mode before object-changing DDL proceeds.

## Methodology Note

This was found by the refined DDL side-metadata selector:

```text
object-identity side metadata
+ DDL has explicit block/cleanup controls on sibling paths
+ one broader object-removal path bypasses those controls
= stale metadata after DDL
```

The important part was not scanning more SQL syntax. The selector first distinguished this owner from name-bound policy metadata such as privilege grants, then asked whether all object-removal paths had the same block/cleanup ownership.
