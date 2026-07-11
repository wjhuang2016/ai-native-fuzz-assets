# id630007 Draft: Metadata-Only MODIFY Rejected On Expression-Index Dependencies

## Summary

`ALTER TABLE ... MODIFY COLUMN` rejects metadata-only changes on a column that is referenced by an
expression index. The same final schema can be created directly and the expression index remains
valid.

Remote `found_bug` row:

```text
id:        630007
title:     MODIFY COLUMN rejects metadata-only changes on columns used by expression indexes
severity:  medium
category:  wrong-error
oracle:    O14_TARGET_TYPE_ACCEPTANCE_REFERENCE
method:    S11_DDL_DEPENDENCY_GATE_OVERBROAD
status:    confirmed
```

This is a companion/blast-radius case for id630004. It uses the same common MODIFY dependency gate,
but it affects a distinct user-facing owner: expression indexes, represented internally by hidden
generated columns.

## Minimal Repro

```sql
DROP DATABASE IF EXISTS ai_funcidx_s11;
CREATE DATABASE ai_funcidx_s11;
USE ai_funcidx_s11;

CREATE TABLE t_comment(
  a INT,
  INDEX idx_expr ((a + 1))
);

ALTER TABLE t_comment MODIFY COLUMN a INT COMMENT 'new-comment';
```

Actual:

```text
ERROR 3106 (HY000): '[ddl:3837]Column 'a' has an expression index dependency and cannot be
dropped or renamed' is not supported for generated columns.
```

The DDL is neither a drop nor a rename.

## References And Controls

Direct target schema succeeds:

```sql
CREATE TABLE direct_comment(
  a INT COMMENT 'new-comment',
  INDEX idx_expr ((a + 1))
);
INSERT INTO direct_comment VALUES (1),(2);
SELECT a FROM direct_comment WHERE a + 1 = 2; -- 1
ADMIN CHECK TABLE direct_comment;
```

Default-only target succeeds directly:

```sql
CREATE TABLE direct_default(
  a INT DEFAULT 5,
  INDEX idx_expr ((a + 1))
);
INSERT INTO direct_default VALUES ();
SELECT a, a + 1 AS expr FROM direct_default; -- 5,6
ADMIN CHECK TABLE direct_default;
```

But the ALTER path rejects the same default-only transition:

```sql
CREATE TABLE t_default(a INT, INDEX idx_expr ((a + 1)));
ALTER TABLE t_default MODIFY COLUMN a INT DEFAULT 5;
-- ERROR 3106 wrapping ddl:3837
```

Green controls:

```sql
-- Non-dependent column comment changes are allowed.
CREATE TABLE t_non_dep(a INT, b INT, INDEX idx_expr ((a + 1)));
ALTER TABLE t_non_dep MODIFY COLUMN b INT COMMENT 'b-comment';

-- Once the expression index is dropped, the base-column metadata change succeeds.
CREATE TABLE t_dropidx(a INT, INDEX idx_expr ((a + 1)));
ALTER TABLE t_dropidx DROP INDEX idx_expr;
ALTER TABLE t_dropidx MODIFY COLUMN a INT COMMENT 'ok-after-drop-index';

-- True semantic type change remains rejected while the expression index depends on the column.
CREATE TABLE t_type(a INT, INDEX idx_expr ((a + 1)));
ALTER TABLE t_type MODIFY COLUMN a BIGINT;
```

## Source Anchor

`pkg/ddl/modify_column.go`:

- `checkModifyColumnWithGeneratedColumnsConstraint` scans generated expressions.
- If the dependent generated column is hidden, it returns `ErrDependentByFunctionalIndex`.
- `GetModifiableColumnJob` uses that error precisely when the column name changes.
- Later, after `checkModifyTypes` and option processing, the same `errG` is used to reject every
  `MODIFY COLUMN`, including COMMENT and DEFAULT changes.

The nearby comment says type changes involving generated columns are prohibited; the actual gate
also catches metadata-only changes for expression-index base columns.

## Fix Direction

Split dependency checks by operation semantics:

- Keep rejecting base-column rename while hidden generated expression indexes still refer to the
  old column name.
- Keep rejecting unsafe type/domain changes unless the expression index can be rebuilt safely.
- Allow metadata-only COMMENT/DEFAULT changes after the ordinary type/no-reorg checks prove the
  column value domain and expression dependency are unchanged.

Fix validation should include expression-index direct targets, ALTER COMMENT, ALTER DEFAULT,
non-dependent column changes, DROP INDEX then ALTER, and true type/rename rejects.

## Quality

Medium.

- User-visible wrong-error with a strong direct-target oracle.
- No data corruption; it blocks valid DDL.
- Method value is useful but should be counted honestly: this is not a new root-cause family. It is
  a second dependency owner validating S11 and exposing the blast radius of id630004's gate.
