# Hypo index metadata can survive column/table/database DDL and become stale

## Summary

`ALTER TABLE ... ADD INDEX ... USING HYPO` creates a session-local hypothetical index after validating the target table and column. Later DDL on the referenced column/table/database does not rewrite or remove that session-local metadata.

As a result, `SHOW CREATE TABLE` can expose a hypo index that references a dropped or renamed column, and old hypo indexes can reappear on a newly-created table with the same schema/table name after `DROP TABLE`, `RENAME TABLE`, or `DROP DATABASE`.

## Minimal Repro: Column Rename

Run the following in one session:

```sql
DROP DATABASE IF EXISTS ai_native_hypo_min;
CREATE DATABASE ai_native_hypo_min;
USE ai_native_hypo_min;

CREATE TABLE t(a INT, b INT);
ALTER TABLE t ADD INDEX idx_a(a) USING HYPO;

ALTER TABLE t RENAME COLUMN a TO aa;
SHOW CREATE TABLE t;
```

Actual `SHOW CREATE TABLE` includes a hypo index on the old column:

```sql
CREATE TABLE `t` (
  `aa` int DEFAULT NULL,
  `b` int DEFAULT NULL,
  KEY `idx_a` (`a`) /* HYPO INDEX */
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin
```

The emitted definition is not replayable:

```sql
CREATE TABLE replay(
  aa INT DEFAULT NULL,
  b INT DEFAULT NULL,
  KEY idx_a (a)
);
```

returns:

```text
ERROR 1072 (42000): column does not exist: a
```

## Other Confirmed Shapes

The probe covers 7 cells and finds 6 stale-reference cases:

```bash
python3 /Users/bba/pc/ai_native_ddl_hypo_index_reference_probe.py
```

Result:

```text
SUMMARY total=7 findings=6 skipped=0
```

Finding cells:

| DDL | Actual stale behavior |
|---|---|
| `ALTER TABLE t RENAME COLUMN a TO aa` | `SHOW CREATE TABLE` still prints `KEY idx_a (a) /* HYPO INDEX */` |
| `ALTER TABLE t CHANGE COLUMN a aa INT` | same stale old-column reference |
| `ALTER TABLE t DROP COLUMN a` | `SHOW CREATE TABLE` prints a key on a dropped column |
| `DROP TABLE t; CREATE TABLE t(...)` | old session-local hypo index attaches to the new table |
| `RENAME TABLE t TO t2; CREATE TABLE t(...)` | renamed table has no hypo index, but recreating old name gets the old hypo index |
| `DROP DATABASE db; CREATE DATABASE db; CREATE TABLE t(...)` | old hypo index attaches to the new table in the recreated schema |

Green control:

```sql
CREATE TABLE t(a INT, b INT);
ALTER TABLE t ADD INDEX idx_a(a) USING HYPO;
SHOW CREATE TABLE t;
```

prints the expected hypo index while the original column still exists.

## Expected Behavior

DDL that changes/removes the referenced object should keep session-local hypo index metadata consistent. Reasonable fix semantics:

- column rename/change: either rewrite the hypo index column reference or remove the hypo index;
- column drop: remove the hypo index or block the column drop;
- table rename: either rekey hypo indexes to the new table name or remove them;
- table/database drop: remove hypo indexes for dropped objects;
- if a table/database is later recreated with the same name, it must not inherit stale hypo indexes from the old object.

## Source Notes

The source shape is a classic side-metadata invalidation gap:

- `pkg/ddl/executor.go:5043-5062` stores hypo indexes in `SessionVars.HypoIndexes[schema][table][index]`.
- `pkg/ddl/executor.go:5121-5127` creates the hypo `IndexInfo` after normal index validation and stores it in the session map instead of table metadata.
- `pkg/executor/show.go:1207-1220` merges session-local hypo indexes into `SHOW CREATE TABLE` by current schema/table name.
- `pkg/executor/show.go:1277-1278` prints `/* HYPO INDEX */`.
- `pkg/ddl/executor.go:5480-5493` can drop a hypo index from the session map, but `dropIndex` first resolves the current real table at `pkg/ddl/executor.go:5498-5513`, so table rename/drop can leave old-name entries unreachable until a same-name object is recreated.

The missing piece is DDL invalidation/rewrite of `SessionVars.HypoIndexes` on column/table/database DDL.

## Fix Direction / Insertion Points

The primary fix should update the session-local map when the DDL succeeds. Filtering only in `SHOW CREATE TABLE` would hide one symptom, but it would not fix stale entries that later attach to a recreated schema/table name.

Recommended semantics:

| DDL path | Preferred behavior | Source insertion point |
|---|---|---|
| `RENAME COLUMN` | Drop or rewrite hypo indexes that reference the old column. Dropping is simpler and safer because hypo indexes may also carry expressions or partial conditions. | after successful `doDDLJob2` in `pkg/ddl/executor.go:3463-3530` |
| `CHANGE COLUMN old new ...` | Same as rename when the column name changes; pure type/position changes can keep hypo indexes if the referenced column identity is still valid. | after successful `DoDDLJobWrapper` in `pkg/ddl/executor.go:3390-3429` |
| `DROP COLUMN` | Drop hypo indexes that reference the dropped column. Blocking the DDL is heavier than needed because hypo indexes are advisory session metadata. | after successful `doDDLJob2` in `pkg/ddl/executor.go:3216-3255` |
| `DROP TABLE` | Remove `HypoIndexes[schema][table]`. | after successful `doDDLJob2` in `pkg/ddl/executor.go:4228-4374` |
| `DROP DATABASE` | Remove `HypoIndexes[schema]`. | after successful `doDDLJob2` in `pkg/ddl/executor.go:763-807` |
| single `RENAME TABLE` / `ALTER TABLE ... RENAME TO` | Either rekey `old_schema.old_table` to `new_schema.new_table`, or drop it. Rekey preserves user intent; drop is simpler and still safe. | after successful `doDDLJob2` in `pkg/ddl/executor.go:4504-4550` |
| multi-table `RENAME TABLE` | Apply the same rekey/drop rule for every pair. If rekeying, handle swaps/cycles through a temporary copy instead of mutating the map in-place. | after successful `doDDLJob2` in `pkg/ddl/executor.go:4553-4614` |

There is also a useful defensive layer in `pkg/executor/show.go:1207-1220`: before appending session-local hypo indexes to `publicIndices`, validate each hypo index against the current `TableInfo`. At minimum, every index column should still resolve to the same current column name/offset. If a hypo index has expression or partial-condition metadata, a conservative filter should drop it from the output when the referenced columns cannot be proven current.

That defensive filter prevents non-replayable `SHOW CREATE TABLE` output if another invalidation path is missed. It should be treated as a belt-and-suspenders check, not the root fix, because stale entries in `SessionVars.HypoIndexes` are what cause resurrection after `DROP TABLE`, `RENAME TABLE`, and `DROP DATABASE`.

## Method Classification

This is not a query-planner-only bug. The finding is visible through DDL metadata:

```text
session-local side metadata
+ create path validates table/column
+ SHOW CREATE merges side metadata into table DDL
+ column/table/database DDL does not invalidate or rekey the side metadata
= stale or resurrected object reference after DDL
```
