# id630004 Draft: Metadata-Only MODIFY Rejected On Generated-Column Dependencies

## Summary

`ALTER TABLE ... MODIFY COLUMN` rejects metadata-only changes on a base column that is referenced
by a generated column. The same target schema can be created directly, and the generated column
expression remains unchanged.

Remote `found_bug` row:

```text
id:        630004
title:     MODIFY COLUMN rejects metadata-only changes on columns used by generated columns
severity:  medium
category:  wrong-error
oracle:    O14_TARGET_TYPE_ACCEPTANCE_REFERENCE
method:    S11_DDL_DEPENDENCY_GATE_OVERBROAD
status:    confirmed
```

## Minimal Repro

```sql
DROP DATABASE IF EXISTS ai_gen_mod_s10;
CREATE DATABASE ai_gen_mod_s10;
USE ai_gen_mod_s10;

CREATE TABLE t_comment(
  a int,
  b int GENERATED ALWAYS AS (a + 1) STORED
);

INSERT INTO t_comment(a) VALUES (1);

ALTER TABLE t_comment MODIFY COLUMN a int COMMENT 'new-comment';
```

Actual:

```text
ERROR 3106 (HY000): '[ddl:3108]Column 'a' has a generated column dependency.'
is not supported for generated columns.
```

`SHOW CREATE TABLE t_comment` remains unchanged.

## References And Controls

Direct target schema succeeds:

```sql
CREATE TABLE direct_comment(
  a int COMMENT 'new-comment',
  b int GENERATED ALWAYS AS (a + 1) STORED
);
INSERT INTO direct_comment(a) VALUES (1);
SELECT a,b FROM direct_comment; -- 1,2
```

Default-only target also succeeds directly:

```sql
CREATE TABLE direct_default(
  a int DEFAULT 5,
  b int GENERATED ALWAYS AS (a + 1) STORED
);
INSERT INTO direct_default() VALUES ();
SELECT a,b FROM direct_default; -- 5,6
```

But the ALTER path rejects the same default-only transition:

```sql
CREATE TABLE t_default(
  a int,
  b int GENERATED ALWAYS AS (a + 1) STORED
);
ALTER TABLE t_default MODIFY COLUMN a int DEFAULT 5;
```

Green controls:

```sql
-- Non-dependent column comment changes are allowed.
CREATE TABLE t_non_dep(a int, c int, b int GENERATED ALWAYS AS (a + 1) STORED);
ALTER TABLE t_non_dep MODIFY COLUMN c int COMMENT 'ok';

-- Generated column's own metadata-only comment change is allowed.
CREATE TABLE t_gcol(a int, b int GENERATED ALWAYS AS (a + 1) STORED);
ALTER TABLE t_gcol MODIFY COLUMN b int GENERATED ALWAYS AS (a + 1) STORED COMMENT 'gcol-comment';

-- True type change of the dependent base column is still rejected.
CREATE TABLE t_type(a int, b int GENERATED ALWAYS AS (a + 1) STORED);
ALTER TABLE t_type MODIFY COLUMN a bigint;
```

## Source Anchor

`pkg/ddl/modify_column.go`:

- `checkModifyColumnWithGeneratedColumnsConstraint` returns an error when any generated column
  expression references the target base column.
- `GetModifiableColumnJob` uses that error precisely for rename checks.
- Later, after `checkModifyTypes` and column option processing, the same dependency error is used
  unconditionally to reject the job.
- The nearby comment says type changes involving generated columns are prohibited, but the gate
  also catches metadata-only changes such as `COMMENT` and safe `DEFAULT`.

Existing tests cover true type changes:

```text
ALTER TABLE t2 MODIFY COLUMN a mediumint
```

where `a` is used by generated columns. They do not cover metadata-only changes.

## Fix Direction

Keep rejecting:

- base-column rename when generated expressions still name the old column;
- base-column type changes that require re-validating or rewriting generated column values;
- functional-index dependency cases only when the requested operation changes name/type/expression
  semantics; metadata-only expression-index cases are covered by id630007.

Allow, after ordinary column validation succeeds:

- comment-only changes;
- safe default changes;
- other metadata-only changes that do not change the generated expression dependency, the base
  column evaluation type, or stored generated values.

The gate should be tied to an actual semantic-change predicate, not to dependency existence alone.

## Quality

Medium.

This is a user-visible wrong-error with a strong direct-target oracle, but it is not data loss. Its
methodology value is high because it adds a new selector: a dependency guard that proves too much
from the fact that a dependency exists.
