# ALTER ADD COLUMN Inline CHECK Loss Draft

Remote `found_bug` row:

```text
id:        30032
title:     ALTER TABLE ADD COLUMN silently drops inline CHECK constraints
severity:  medium
status:    confirmed
oracle:    O23_TARGET_SCHEMA_CONSTRAINT_REFERENCE
method:    S18_EMBEDDED_CONSTRAINT_OWNER_LOSS
```

## Minimal Repro

Confirmed on testbed `8192975` / `fp-tidb`:

```sql
DROP DATABASE IF EXISTS ai_add_col_check_0703;
CREATE DATABASE ai_add_col_check_0703;
USE ai_add_col_check_0703;

CREATE TABLE t_inline_loss_0703(a INT);
INSERT INTO t_inline_loss_0703 VALUES (1),(2);
ALTER TABLE t_inline_loss_0703 ADD COLUMN b INT DEFAULT 1 CHECK (b > 0);
SELECT @@warning_count;
SHOW CREATE TABLE t_inline_loss_0703;
SELECT COUNT(*) AS check_constraint_count
FROM information_schema.check_constraints
WHERE constraint_schema=DATABASE()
  AND constraint_name LIKE 't_inline_loss_0703%';
INSERT INTO t_inline_loss_0703(a,b) VALUES (3,0);
SELECT GROUP_CONCAT(CONCAT(a,':',b,':',IF(b>0,1,0)) ORDER BY a) AS rows_seen
FROM t_inline_loss_0703;
```

Observed:

```text
@@warning_count = 0
SHOW CREATE TABLE only has `a` and `b int DEFAULT '1'`
check_constraint_count = 0
rows_seen = 1:1:1,2:1:1,3:0:0
```

Named inline column constraints behave the same:

```sql
ALTER TABLE t_inline_named_0703
  ADD COLUMN b INT DEFAULT 1 CONSTRAINT ck_inline_named_0703 CHECK (b > 0);
```

The statement succeeds with `@@warning_count=0`, publishes no CHECK constraint, and accepts
`b=0`.

## Reference Controls

Direct `CREATE TABLE` preserves and enforces the same inline CHECK:

```sql
CREATE TABLE t_create_inline(a INT, b INT DEFAULT 1 CHECK (b > 0));
SHOW CREATE TABLE t_create_inline;
INSERT INTO t_create_inline(a,b) VALUES (1,0);
```

Observed:

```text
SHOW CREATE TABLE includes:
  CONSTRAINT `t_create_inline_chk_1` CHECK ((`b` > 0))
INSERT b=0 fails:
  ERROR 3819 Check constraint 't_create_inline_chk_1' is violated.
```

Sequential ALTER also preserves and enforces the CHECK:

```sql
CREATE TABLE t_seq_ref_0703(a INT);
INSERT INTO t_seq_ref_0703 VALUES (1),(2);
ALTER TABLE t_seq_ref_0703 ADD COLUMN b INT DEFAULT 1;
ALTER TABLE t_seq_ref_0703 ADD CONSTRAINT ck_seq_ref_b_pos_0703 CHECK (b > 0);
SHOW CREATE TABLE t_seq_ref_0703;
INSERT INTO t_seq_ref_0703(a,b) VALUES (3,0);
```

Observed:

```text
SHOW CREATE TABLE includes:
  CONSTRAINT `ck_seq_ref_b_pos_0703` CHECK ((`b` > 0))
INSERT b=0 fails:
  ERROR 3819 Check constraint 'ck_seq_ref_b_pos_0703' is violated.
```

## Source Anchors

- `/Users/bba/pc/tidb/pkg/ddl/add_column.go:279`: `CreateNewColumn` calls
  `buildColumnAndConstraint(...)` as `col, _, err := ...`, discarding the column-level
  constraints extracted from `ColumnOptionCheck`.
- `/Users/bba/pc/tidb/pkg/ddl/add_column.go:577-592`: `buildColumnAndConstraint` does extract
  column-level CHECK into an `ast.Constraint`.
- `/Users/bba/pc/tidb/pkg/ddl/executor.go:2203-2257`: `AddColumn` submits only
  `ActionAddColumn` with `TableColumnArgs`.
- `/Users/bba/pc/tidb/pkg/ddl/create_table.go:1456-1516`: `CREATE TABLE` consumes
  `ast.ConstraintCheck` and appends the CHECK constraint to table metadata.
- `/Users/bba/pc/tidb/pkg/ddl/executor.go:7016-7089`: table-level `ADD CHECK` is a separate
  `ActionAddCheckConstraint` path.
- `/Users/bba/pc/tidb/pkg/ddl/constraint.go:354-388`: ADD CHECK validates remaining rows before
  publishing the constraint.

## User-Visible Symptom

The user writes a normal schema-evolution statement that appears to request an enforced CHECK. The
DDL succeeds and emits no warning, but the final schema silently lacks the constraint. Later writes
that should be rejected are accepted. This is not just display metadata: `b=0` is stored.

## Relation to id1

This is distinct from `found_bug id1`. id1 is `CHANGE/MODIFY COLUMN` rename/rebuild losing an
existing CHECK constraint because the modify-column path misses dependency handling. id30032 is
`ADD COLUMN` accepting a new column definition with inline CHECK and never publishing that new
constraint at all.

## Fix Direction

Either:

- carry column-level CHECK constraints returned by `buildColumnAndConstraint` into an
  `ActionAddCheckConstraint` subjob after the new column is public, including existing-row/default
  validation; or
- reject inline column CHECK in `ALTER TABLE ADD COLUMN` explicitly instead of accepting and
  dropping it.

The sibling table-level form `ALTER TABLE ... ADD COLUMN b ..., ADD CONSTRAINT ... CHECK(b > 0)`
currently fails with "unknown column b", which points at the same target-schema handoff area but is
less severe than the silent inline loss.
