# id630009 Draft: Partial-Index Condition Columns Reject Metadata-Only MODIFY

## Summary

`ALTER TABLE ... MODIFY COLUMN` rejects metadata-only changes on a column referenced by a partial
index condition, even when the final target schema can be created directly and passes
`ADMIN CHECK TABLE`.

Local status:

```text
id:        630009
title:     MODIFY COLUMN rejects metadata-only changes on columns used by partial-index conditions
severity:  low
category:  wrong-error
oracle:    O14_TARGET_TYPE_ACCEPTANCE_REFERENCE
method:    S11_DDL_DEPENDENCY_GATE_OVERBROAD
status:    confirmed
```

## Minimal Repro

```sql
DROP DATABASE IF EXISTS ai_partial_meta;
CREATE DATABASE ai_partial_meta;
USE ai_partial_meta;

CREATE TABLE t_comment(
  a INT,
  b INT,
  c INT,
  INDEX idx_a(a) WHERE b > 0
);

ALTER TABLE t_comment MODIFY COLUMN b INT COMMENT 'new-comment';
```

Actual:

```text
ERROR 8272 (HY000): Cannot drop, change or modify column 'b': it is referenced in partial index 'idx_a'
```

The same wrong-error happens for a safe default-only change:

```sql
CREATE TABLE t_default(
  a INT,
  b INT,
  c INT,
  INDEX idx_a(a) WHERE b > 0
);

ALTER TABLE t_default MODIFY COLUMN b INT DEFAULT 5;
-- ERROR 8272
```

## Strong Reference

The direct target schemas are valid:

```sql
CREATE TABLE direct_comment(
  a INT,
  b INT COMMENT 'new-comment',
  c INT,
  INDEX idx_a(a) WHERE b > 0
);
INSERT INTO direct_comment VALUES (1,1,10),(2,-1,20),(3,2,30);
ADMIN CHECK TABLE direct_comment;

CREATE TABLE direct_default(
  a INT,
  b INT DEFAULT 5,
  c INT,
  INDEX idx_a(a) WHERE b > 0
);
INSERT INTO direct_default(a,c) VALUES (1,10);
ADMIN CHECK TABLE direct_default;
```

Both `CREATE TABLE` paths succeed. `direct_comment` returns two rows for
`WHERE b > 0 AND a >= 1`, and `direct_default` inserts `b=5` by default.

## Controls

- Modifying a non-condition column succeeds:
  `ALTER TABLE t_control MODIFY COLUMN c INT COMMENT 'new-comment'`.
- Dropping the partial index first releases the dependency:
  `DROP INDEX idx_a ON t_control; ALTER TABLE t_control MODIFY COLUMN b INT COMMENT 'after-drop'`.
- `ADMIN CHECK TABLE` passes before and after the rejected attempts; the schema stays unchanged.

## Source Anchor

The partial-index condition validator builds `IndexInfo.AffectColumn` from the condition columns:

- `pkg/ddl/index.go`: `checkIndexCondition` validates that the partial-index condition column
  exists and has a compatible type.
- `pkg/ddl/index.go`: `buildAffectColumn` stores columns referenced by the partial-index
  condition.

The MODIFY path then treats mere dependency existence as a blanket rejection:

- `pkg/ddl/executor.go`: `checkColumnReferencedByPartialCondition` returns
  `ErrModifyColumnReferencedByPartialCondition` whenever the column appears in `idx.AffectColumn`.
- `pkg/ddl/modify_column.go`: both the precheck path and the common `GetModifiableColumnJob`
  path call that checker before distinguishing metadata-only changes from semantic changes.

## Proof Obligation

```text
P_check:  column b is referenced by partial index idx_a's WHERE condition
Q_claim:  any MODIFY COLUMN on b can invalidate the partial-index condition
D_dim:    COMMENT and DEFAULT metadata do not change the condition expression, column name, type,
          collation, nullability, or existing row membership
F_effect: common MODIFY path rejects before comparing the requested target schema
O_oracle: direct target schema with the same partial index, plus ADMIN CHECK and behavior query
```

## Fix Direction

Split dependency checks by operation semantics:

- keep rejecting `DROP COLUMN`, `RENAME/CHANGE COLUMN`, and type/collation/nullability changes that
  can alter partial-index condition evaluation;
- allow metadata-only `COMMENT` and safe `DEFAULT` changes when the direct target schema is valid;
- validate the final target partial-index condition after the column definition is built, rather
  than treating dependency existence as sufficient proof of danger.

## Quality

Low-to-medium wrong-error.

The bug does not corrupt data, but it blocks routine schema evolution on tables using partial
indexes. Method value is high because it confirms a sharper S11 rule: after generated columns and
expression indexes, a different dependency gate (`checkColumnReferencedByPartialCondition`) shows
the same proof mistake through a distinct owner.
