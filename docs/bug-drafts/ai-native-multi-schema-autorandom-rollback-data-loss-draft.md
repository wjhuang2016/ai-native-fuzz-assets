# Failed multi-schema AUTO_RANDOM conversion can make a cold TiDB overwrite existing rows

Status: confirmed on current master and packaged nightly with real TiKV and MDL enabled. No exact
upstream issue or internal root was found.

## Summary

A multi-schema `ALTER TABLE` can return `ERROR 1062` and finish as `rollback done`, while an earlier
`AUTO_INCREMENT -> AUTO_RANDOM` subjob has already migrated allocator state and deleted the old
RowID owner. The restored `TableInfo` is a hybrid: the column remains `AUTO_INCREMENT`, while the
table also advertises `AUTO_RANDOM`.

The TiDB that populated the table can continue from its cached high-water mark. A new or restarted
TiDB reconstructs both allocators, chooses the reset RowID owner for generated `AUTO_INCREMENT`
writes, and starts at existing primary keys. Plain `INSERT` returns a duplicate-key error.
`REPLACE` returns success with `affected_rows=2` and permanently overwrites an existing row.
`ADMIN CHECK TABLE` remains green.

## Production trigger

1. A populated table has a clustered `BIGINT AUTO_INCREMENT` primary key. The default auto-ID
   cache is sufficient.
2. A schema migration enables the supported conversion guard:
   `SET SESSION tidb_allow_remove_auto_inc=1`.
3. One composite statement converts the key to `AUTO_RANDOM` and adds a unique index.
4. Existing duplicate business values make the index subjob fail with 1062.
5. Traffic later reaches a new TiDB after scale-out, restart, rolling upgrade, or failover.
6. The application uses generated IDs. A normal insert reports a duplicate; a MySQL-compatible
   `REPLACE` upsert silently removes the preexisting row.

The trigger needs no failpoint, network fault, process crash during DDL, disabled MDL, unusual
transaction isolation, `AUTO_ID_CACHE=1`, or race timing. It does require an intentional
`AUTO_INCREMENT -> AUTO_RANDOM` migration and a later cold TiDB consumer.

## Current-master reproduction

Environment:

```text
TiDB: 05b396fb6636f73b3bc06b09107cf43f2c725c35
topology: two TiDB processes, one PD, one real TiKV
MDL: ON
table option: default auto-ID cache
fault injection: none
```

Create 64 rows with IDs `1..64` and duplicate `v` values. Before DDL:

```text
rows=64, min(id)=1, max(id)=64
NEXT_GLOBAL_ROW_ID=30001, ID_TYPE=_TIDB_ROWID
```

Run:

```sql
SET SESSION tidb_allow_remove_auto_inc=1;
ALTER TABLE t
  MODIFY COLUMN id BIGINT AUTO_RANDOM(1),
  ADD UNIQUE INDEX ux_v(v);
```

The client receives:

```text
ERROR 1062 (23000): Duplicate entry '0' for key 't.ux_v'
```

DDL history says the parent is `rollback done`, the modify-column subjob is `cancelled`, and the
add-index subjob is `rollback done`. However, `SHOW CREATE TABLE` contains:

```sql
`id` bigint NOT NULL AUTO_INCREMENT /*T![auto_rand] AUTO_RANDOM(1) */
```

The warm TiDB reports the RowID next value as `1`. A newly started TiDB reports:

```text
id  1      _TIDB_ROWID
id  30002  AUTO_RANDOM
```

On the new TiDB:

```sql
INSERT INTO t(v,payload) VALUES (9001,'cold-insert');
-- ERROR 1062: Duplicate entry '1' for key 't.PRIMARY'

SELECT id,payload FROM t WHERE id=2;
-- 2, original-14

REPLACE INTO t(v,payload) VALUES (9002,'cold-replace');
SELECT LAST_INSERT_ID(), ROW_COUNT();
-- 2, 2
```

A fresh read through the original TiDB returns `id=2, payload='cold-replace'`;
`original-14` is absent, the row count remains 64, and `ADMIN CHECK TABLE t` succeeds.

## Matched controls

| Shape | Cold-node result |
| --- | --- |
| only failing `ADD UNIQUE INDEX` | generated ID `30001`, `affected_rows=1` |
| only successful `AUTO_INCREMENT -> AUTO_RANDOM` | sharded ID `4611686018427417907`, `affected_rows=1` |
| conversion plus later index failure | first insert collides at `1`; `REPLACE` overwrites `2` |

The selector is the failed composite transition, not duplicate-index rollback, successful allocator
migration, generated IDs, or `REPLACE` alone.

## Root cause

`onMultiSchemaChange` first advances every subjob to its last revertible point, saves `TableInfo`,
then asks all subjobs to cross the non-revertible boundary together.

`onModifyColumn` calls `checkAndApplyAutoRandomBits` before
`doModifyColumnNoCheck` marks its proxy subjob non-revertible. The apply step:

1. sets `tblInfo.AutoRandomBits`;
2. rebases the AutoRandom allocator from the old RowID high-water mark;
3. deletes the RowID accessor.

These effects happen while the parent still treats the subjob as revertible. The saved `TableInfo`
therefore already contains `AutoRandomBits`, while the old column still has the `AUTO_INCREMENT`
flag. If the later add-index final transition fails, restoring that snapshot and subjob state cannot
undo the separately committed allocator migration.

The resulting hybrid metadata makes warm and cold allocator reconstruction disagree. Warm memory
can mask the corruption until topology changes.

## Counterfactual

A minimal experimental guard rejected an `AUTO_RANDOM` conversion whenever
`job.MultiSchemaInfo != nil`, before `checkAndApplyAutoRandomBits`.

With the same SQL and data:

```text
DDL: ERROR 1105 AUTO_RANDOM conversion is not supported in multi-schema change
SHOW CREATE: pure AUTO_INCREMENT
NEXT_GLOBAL_ROW_ID: 30001 _TIDB_ROWID
cold INSERT: generated_id=30001, affected_rows=1
original payloads: 8/8 preserved
ADMIN CHECK: green
```

The guard was removed after validation and the worktree was returned clean.

## Expected behavior

An `ALTER TABLE` that reports rollback must preserve a self-consistent pre-DDL schema and every
allocator owner used by that schema. A cold TiDB must allocate an ID disjoint from all durable rows.

## Fix direction

The safe short-term fix is to reject `AUTO_INCREMENT -> AUTO_RANDOM` inside multi-schema changes.
A complete fix should stage allocator migration until every sibling can cross the parent commit
boundary, or make the migration part of the same atomic metadata transaction with an exact rollback
compensator. Restoring only `TableInfo` is insufficient.

## Impact and severity

This is successful persistent data loss under a normal write after a failed DDL and topology
refresh. The asset catalog records it as `high` with critical-class consequence. Trigger frequency
is narrower than a default DML path because the operator must request the guarded conversion, but
the default allocator cache and default MDL configuration are affected.

