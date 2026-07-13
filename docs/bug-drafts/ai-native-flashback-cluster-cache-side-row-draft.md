# FLASHBACK CLUSTER can make a cached table unwritable

Status: confirmed high severity on current source and authorized testbed `8220955`; remote bug DB
target `id1980003`.

## Impact

A successful `FLASHBACK CLUSTER` can restore a table as `CACHED ON` without restoring the matching
row in `mysql.table_cache_meta`. Reads fall back to TiKV, but every `INSERT`, `UPDATE`, or `DELETE`
must acquire the cached-table write lock before commit and fails with:

```text
ERROR 1105 (HY000): table_cache_meta tid not exist <table-id>
```

The table remains unwritable until an operator repairs the missing row or disables table cache.

## Reproduction

1. Create a non-partition table, insert a row, and run `ALTER TABLE t CACHE`.
2. Start a transaction and record `@@tidb_current_ts`; roll back that read-only transaction.
3. Run `ALTER TABLE t NOCACHE` and confirm the table's row is absent from
   `mysql.table_cache_meta`.
4. Wait for resolved-ts to pass the recorded TSO, then run `FLASHBACK CLUSTER TO TSO <tso>`.
5. Confirm the Flashback job is `synced/public` and `SHOW CREATE TABLE t` contains `CACHED ON`.
6. Confirm `mysql.table_cache_meta` still has no row for the table ID.
7. `SELECT` the table, then try an `INSERT` from a fresh session.

On testbed `8220955`, Flashback job `5432` restored table ID `5428` as cached. The side-row count
remained zero; `SELECT` returned `(1,10)`, while `INSERT (2,20)` failed with the error above.
Replacing only row `(5428,'NONE',0,0)` immediately restored write progress.

## Root Cause

`getFlashbackKeyRanges` restores user schema metadata and user table data, but excludes the `mysql`
schema. `ALTER TABLE ... NOCACHE` deletes `mysql.table_cache_meta` outside the user table metadata.
The Flashback compatibility guard allows CACHE/NOCACHE DDL, so historical `TableInfo` can be
restored with `TableCacheStatusEnable` while the current system-table row remains deleted.

The read path logs lock acquisition failure and falls back to TiKV. The DML commit path calls
`WriteLockAndKeepAlive`, and `stateRemoteHandle.loadRow` treats the absent row as terminal, so the
write is rejected before commit.

## Fix Direction

Either reject Flashback windows containing CACHE/NOCACHE DDL, include/reconcile the required cache
side state, or rebuild `mysql.table_cache_meta` from the restored `TableInfo` set before publishing
the recovered schema. The fix must cover both enable and disable directions and avoid reviving stale
leases.
