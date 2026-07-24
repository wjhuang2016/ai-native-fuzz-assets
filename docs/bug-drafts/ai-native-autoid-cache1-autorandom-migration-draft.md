# AUTO_ID_CACHE=1 to AUTO_RANDOM conversion can silently replace existing rows

Status: confirmed high severity / critical persistent-data-loss consequence on current master and
real TiKV.

## Summary

TiDB supports converting an `AUTO_INCREMENT` primary key to `AUTO_RANDOM` after the session enables
`tidb_allow_remove_auto_inc`.

For a table with `AUTO_ID_CACHE=1`, the conversion transfers the high-water mark from the wrong
allocator. The new `AUTO_RANDOM` incremental component starts near zero even when the table already
owns IDs 1 through 64.

A generated `INSERT` can fail with a duplicate primary key. A generated `REPLACE` is more severe:
when its random shard bit is zero, it reuses an existing primary key, reports success with
`affected_rows=2`, and permanently removes the old row. `ADMIN CHECK TABLE` remains green because
the resulting table is structurally valid.

## Production trigger

The trigger is a normal schema migration:

1. A table uses `AUTO_ID_CACHE=1` and an `AUTO_INCREMENT` clustered primary key.
2. The table contains generated IDs.
3. An operator enables the guarded conversion with `SET SESSION tidb_allow_remove_auto_inc=1`.
4. The operator converts the column to `AUTO_RANDOM`.
5. The application resumes generated inserts, upserts, or replaces.

The RED needs no concurrency, retry, failpoint, node failure, restart, disabled MDL, or unusual
isolation level. `AUTO_ID_CACHE=1` and the explicit conversion guard are required.

## Minimal reproduction

```sql
DROP DATABASE IF EXISTS ai_auto_random_migration;
CREATE DATABASE ai_auto_random_migration;

CREATE TABLE ai_auto_random_migration.t (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  v VARCHAR(64) NOT NULL
) AUTO_ID_CACHE=1;

INSERT INTO ai_auto_random_migration.t(v) VALUES
  ('order-001'), ('order-002'), ('order-003'), ('order-004'),
  ('order-005'), ('order-006'), ('order-007'), ('order-008');

SET SESSION tidb_allow_remove_auto_inc=1;
ALTER TABLE ai_auto_random_migration.t
  MODIFY COLUMN id BIGINT AUTO_RANDOM(1);

REPLACE INTO ai_auto_random_migration.t(v) VALUES ('replacement-1');
SELECT ROW_COUNT() AS affected_rows, LAST_INSERT_ID() AS generated_id;
SELECT * FROM ai_auto_random_migration.t ORDER BY id;
ADMIN CHECK TABLE ai_auto_random_migration.t;
```

Because one shard bit is random, repeat the generated `REPLACE` enough times to make the probe
deterministic. The reusable script runs 64 attempts and a matched default-cache control:
[`ai_native_autoid_cache1_autorandom_migration.sh`](../../scaffolds/top-level/ai_native_autoid_cache1_autorandom_migration.sh).

## Actual result

The strongest unmodified current-master run used 64 original rows and 24 generated `REPLACE`
statements:

```text
DDL owner: 127.0.0.1:4002
TiDB:      8.0.11-TiDB-231dad5225

final_count      76
original_rows    52
replacement_rows 24

overwritten IDs:
1,5,9,10,12,14,15,16,19,20,21,22
```

All 12 collisions returned `affected_rows=2`. A fresh read proved that the corresponding original
payloads were gone. `ADMIN CHECK TABLE` succeeded.

On the same cluster, the default-cache control preserved all 64 original rows. Generated IDs began
above the existing high-water mark or in another shard, and every `REPLACE` returned
`affected_rows=1`.

## Expected result

The migration must preserve the old allocator owner's high-water mark. Every generated
`AUTO_RANDOM` incremental component must be disjoint from existing generated identities, and all
pre-migration rows must remain after successful application writes.

## Root cause

The validation and application phases disagree in `pkg/ddl/column.go`:

- `checkNewAutoRandomBits` selects `IncrementID` when the conversion comes from `AUTO_INCREMENT`
  and `TableInfo.SepAutoInc()` is true.
- `applyNewAutoRandomBits` unconditionally selects `RowID()`, reads its value, rebases
  `AUTO_RANDOM`, and deletes that accessor.
- `TableInfo.SepAutoInc()` is true for table-info version 5 with `AUTO_ID_CACHE=1`.

The old high-water mark lives in `IncrementID`, while `RowID` is unrelated and can be zero. The
conversion therefore initializes the new allocator from the wrong owner.

## Causal counterfactual

A temporary current-master build changed only the accessor selected by
`applyNewAutoRandomBits`:

```go
idAccessors := jobCtx.metaMut.GetAutoIDAccessors(dbInfo.ID, tblInfo.ID)
idAcc := idAccessors.RowID()
if tblInfo.SepAutoInc() {
    idAcc = idAccessors.IncrementID(tblInfo.Version)
}
```

After explicitly transferring DDL ownership to this patched TiDB, the identical 64-row/24-write
matrix preserved all original rows and every `REPLACE` returned `affected_rows=1`.

An earlier counterfactual attempt was invalid because an unpatched TiDB still owned DDL. This
became a harness requirement: multi-TiDB DDL experiments must record and verify the actual owner
binary before classifying RED or GREEN.

## Deduplication

Post-RED searches for `AUTO_ID_CACHE AUTO_RANDOM`, `AUTO_RANDOM migration auto_increment`, and
`AUTO_RANDOM duplicate` found no matching TiDB issue. Remote bug-asset search also found no same
root.

This is distinct from `id2910003`. That regression reconstructs an allocator from the wrong schema
owner after cross-database rename and a cold load. This root transfers state between two allocator
types during one column migration and reads the wrong old accessor.

## Fix direction

Choose the old allocator using the same `SepAutoInc` rule in both validation and application.
Rebase the new allocator from that owner, delete exactly that owner, and fail closed if the
high-water mark cannot be transferred. Cover:

- `AUTO_ID_CACHE=1` and default-cache controls;
- nonzero existing high-water marks;
- generated `INSERT` and successful `REPLACE` consumers;
- explicit DDL-owner identity in multi-TiDB test environments.
