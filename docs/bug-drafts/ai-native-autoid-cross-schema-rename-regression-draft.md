# Cross-database RENAME can reuse AUTO_INCREMENT IDs after a cold InfoSchema load

Status: confirmed high severity / critical persistent-data-loss consequence on current nightly and
real TiKV. Remote bug DB: `found_bug id2910003`, `known-regression`.

## Summary

For a table with `AUTO_ID_CACHE=1`, TiDB persists the original auto-ID schema owner in
`TableInfo.AutoIDSchemaID` when the table is renamed to another database.

A TiDB that starts after the rename reconstructs the allocator with the current schema ID instead
of the persisted owner. Its visible auto-ID high-water mark falls from 2 to 0, and its first
generated value is 2.

Three current-nightly consequences were observed:

- a normal `INSERT` into a primary-key table fails with duplicate key 2;
- a table whose auto-ID column has only a nonunique index accepts a second row with ID 2;
- `REPLACE INTO` reports success, generates ID 2, and silently overwrites the existing row 2.

The last case is persistent application-data loss. `ADMIN CHECK TABLE` remains green because the
resulting table is physically consistent.

## Production trigger

The schedule uses ordinary cluster behavior:

1. An application uses an `AUTO_INCREMENT` table with `AUTO_ID_CACHE=1`.
2. The table already contains generated IDs.
3. An operator or migration tool runs a cross-database `RENAME TABLE`.
4. A healthy TiDB that did not own the warm allocator consumes the schema change, or a new/restarted
   TiDB performs a full InfoSchema load.
5. That TiDB runs an `INSERT`, `REPLACE`, `INSERT IGNORE`, or another statement that generates an ID.

No concurrency, failpoint, node failure, unusual isolation level, disabled MDL, or long-running
transaction is required. Multi-TiDB deployments and rolling restarts naturally supply the cold
allocator consumer.

## Reproduction

Run phase 1 through an existing TiDB:

```sql
CREATE DATABASE ai_auto_reload_src;
CREATE DATABASE ai_auto_reload_dst;

CREATE TABLE ai_auto_reload_src.t (
  id BIGINT AUTO_INCREMENT PRIMARY KEY,
  v VARCHAR(32)
) AUTO_ID_CACHE=1;

INSERT INTO ai_auto_reload_src.t(v) VALUES ('order-1'), ('order-2');
RENAME TABLE ai_auto_reload_src.t TO ai_auto_reload_dst.t;
SELECT * FROM ai_auto_reload_dst.t ORDER BY id;
```

Start another unmodified TiDB against the same PD/TiKV after the rename. Through that new TiDB:

```sql
SELECT TABLE_NAME, AUTO_INCREMENT
FROM information_schema.tables
WHERE TABLE_SCHEMA='ai_auto_reload_dst' AND TABLE_NAME='t';

REPLACE INTO ai_auto_reload_dst.t(v) VALUES ('replacement');
SELECT ROW_COUNT() AS affected_rows, LAST_INSERT_ID() AS generated_id;
SELECT * FROM ai_auto_reload_dst.t ORDER BY id;
ADMIN CHECK TABLE ai_auto_reload_dst.t;
```

The reusable three-cell script is
[`ai_native_autoid_cross_schema_rename_reload.sql`](../../scaffolds/tidb-tests/ai_native_autoid_cross_schema_rename_reload.sql).

## Actual result

The cold TiDB first reports:

```text
TABLE_NAME  AUTO_INCREMENT
t           0
```

`REPLACE` then succeeds:

```text
affected_rows  generated_id
2              2

id  v
1   order-1
2   replacement
```

The original `id=2, v='order-2'` row is gone. A normal `INSERT` variant returns:

```text
ERROR 1062: Duplicate entry '2' for key 't.PRIMARY'
```

With a nonunique index on `id`, the statement succeeds and leaves:

```text
id  cnt
2   2
```

## Expected result

Every TiDB must reconstruct the same durable auto-ID owner and high-water mark. The next generated
ID must be 3. `REPLACE` must insert one new row and preserve both existing rows.

## Root cause

The producer and consumers disagree:

- `pkg/ddl/table.go:checkAndRenameTables` sets `AutoIDSchemaID` to the original schema ID and
  explicitly documents that auto ID remains owned by `(original schema ID, table ID)`.
- `pkg/meta/autoid/autoid.go:NewAllocatorsFromTblInfo` ignores `AutoIDSchemaID`.
- incremental InfoSchema construction, full InfoSchema loading, and InfoSchema v2 all call that
  constructor with the table's current database ID.
- the cold allocator therefore reads or publishes the wrong metadata owner.

`AUTO_ID_CACHE=1` uses the separated single-point auto-increment allocator. Its transfer path rebases
from process-local `lastAllocated`, which is 0 on a cold TiDB and cannot recover the old owner's
high-water mark.

## Counterfactual

The following temporary current-master change was built as a full TiDB server:

```go
func NewAllocatorsFromTblInfo(r Requirement, dbID int64, tblInfo *model.TableInfo) Allocators {
    if tblInfo.AutoIDSchemaID != 0 {
        dbID = tblInfo.AutoIDSchemaID
    }
    // Existing constructor body.
}
```

Against the same real PD/TiKV and the same schedule, the patched cold TiDB produced:

```text
affected_rows  generated_id
1              3

id  v
1   order-1
2   order-2
3   replacement
```

Only the persisted owner identity changed. The source patch was then removed and the worktree was
confirmed clean.

## History and deduplication

Post-RED history lookup found closed issue
[#55846](https://github.com/pingcap/tidb/issues/55846) and merged fix
[#55847](https://github.com/pingcap/tidb/pull/55847). The historical report showed duplicate-key
failure after restart. Current nightly has regressed that root.

This record does not claim a new bug family. It records a current-master regression and a stronger
current consumer: successful `REPLACE` can silently delete an existing row.

## Scope

Verified with:

- TiDB nightly `ed2376acc6e0feeff9f3e2c38db489727933aa80`;
- current source `231dad5225f0d3c9cf38d4ab7ebc03a5326785c7` for the counterfactual build;
- one PD, one real TiKV, and a warm plus cold TiDB process;
- Classic kernel, `tidb_enable_metadata_lock=ON`;
- no source instrumentation in RED and no fault injection.

## Fix direction

Use `AutoIDSchemaID` consistently whenever an allocator is reconstructed, including incremental,
full-load, and InfoSchema v2 paths. If ownership is migrated to the new schema instead, copy and
verify every allocator high-water mark atomically before publishing the table. Add a cold-peer and
full-reload oracle, not only a same-domain concurrent-rename test.
