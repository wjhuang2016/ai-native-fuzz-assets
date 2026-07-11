# id630002: MODIFY COLUMN rejects FK VARCHAR length changes that target schema accepts

> 2026-07-03. Confirmed on testbed `8192975` / `fp-tidb`. Inserted into remote `found_bug` as id630002 (`MAX(id)=630002`, `COUNT(*)=34`).

## Summary

TiDB allows foreign-key columns with different `VARCHAR` lengths when the type, charset, and collation match. For example, a child `varchar(20)` can reference a parent `varchar(10)`.

But later `ALTER TABLE ... MODIFY COLUMN` rejects some target schemas that TiDB can create directly and that preserve existing data:

- child `varchar(20) -> varchar(10)` referencing parent `varchar(10)` fails with `ERROR 1832`;
- child `varchar(20) -> varchar(15)` referencing parent `varchar(10)` fails with `ERROR 1832`;
- parent `varchar(10) -> varchar(15)` referenced by child `varchar(20)` fails with `ERROR 1833`.

The target schemas themselves are valid via direct `CREATE TABLE`.

## Minimal Repro

```sql
DROP DATABASE IF EXISTS ai_fk_mod_len;
CREATE DATABASE ai_fk_mod_len DEFAULT CHARSET utf8mb4 COLLATE utf8mb4_bin;
USE ai_fk_mod_len;
SET SESSION sql_mode='STRICT_TRANS_TABLES';
SET SESSION foreign_key_checks=1;

-- Direct target reference: parent varchar(10), child varchar(10) is valid.
CREATE TABLE ref_p10 (
  a varchar(10) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin,
  INDEX(a)
);
CREATE TABLE ref_c10 (
  b varchar(10) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin,
  INDEX(b),
  CONSTRAINT fk_ref FOREIGN KEY (b) REFERENCES ref_p10(a)
);

-- DDL arm: equivalent target, reached by modifying child column.
CREATE TABLE p (
  a varchar(10) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin,
  INDEX(a)
);
CREATE TABLE c (
  b varchar(20) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin,
  INDEX(b),
  CONSTRAINT fk_child FOREIGN KEY (b) REFERENCES p(a)
);

INSERT INTO p VALUES ('abcdefghij'), ('abc');
INSERT INTO c VALUES ('abcdefghij'), ('abc'), (NULL);

SELECT MAX(CHAR_LENGTH(b)) FROM c; -- 10

ALTER TABLE c
  MODIFY COLUMN b varchar(10) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;
-- ERROR 1832 (HY000): Cannot change column 'b': used in a foreign key constraint 'fk_child'
```

Sibling parent case:

```sql
-- Direct target reference: parent varchar(15), child varchar(20) is valid.
CREATE TABLE ref_p15 (
  a varchar(15) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin,
  INDEX(a)
);
CREATE TABLE ref_c20 (
  b varchar(20) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin,
  INDEX(b),
  CONSTRAINT fk_ref_parent FOREIGN KEY (b) REFERENCES ref_p15(a)
);

-- DDL arm: equivalent target, reached by modifying parent column.
CREATE TABLE p2 (
  a varchar(10) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin,
  INDEX(a)
);
CREATE TABLE c2 (
  b varchar(20) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin,
  INDEX(b),
  CONSTRAINT fk_parent FOREIGN KEY (b) REFERENCES p2(a)
);
INSERT INTO p2 VALUES ('abc');
INSERT INTO c2 VALUES ('abc');

ALTER TABLE p2
  MODIFY COLUMN a varchar(15) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;
-- ERROR 1833 (HY000): Cannot change column 'a': used in a foreign key constraint 'fk_parent' of table 'ai_fk_mod_len.c2'
```

## Guard Cells

| Cell | Setup | Expected | Observed |
| --- | --- | --- | --- |
| Direct target | parent `varchar(10)`, child `varchar(10)` | create succeeds | GREEN |
| Direct target | parent `varchar(10)`, child `varchar(15)` | create succeeds | GREEN |
| Direct target | parent `varchar(15)`, child `varchar(20)` | create succeeds | GREEN |
| Child shrink to parent length | child `varchar(20) -> varchar(10)`, parent `varchar(10)` | succeeds, data max length 10 | RED: 1832 |
| Child shrink above parent length | child `varchar(20) -> varchar(15)`, parent `varchar(10)` | succeeds | RED: 1832 |
| Parent widen below child length | parent `varchar(10) -> varchar(15)`, child `varchar(20)` | succeeds | RED: 1833 |
| Child widen | child `varchar(20) -> varchar(25)` | succeeds | GREEN |
| Parent widen to child length | parent `varchar(10) -> varchar(20)` | succeeds | GREEN |

## Source Chain

- `/Users/bba/pc/tidb/pkg/ddl/foreign_key.go:280`: `checkTableForeignKey` validates FK creation by type, unsigned flag, charset, and collation. It does not require equal string length.
- `/Users/bba/pc/tidb/pkg/ddl/tests/fk/foreign_key_test.go:718`: test coverage explicitly treats parent `varchar(10)` / child `varchar(20)` as a passing FK case.
- `/Users/bba/pc/tidb/pkg/ddl/modify_column.go:1912`: `buildNewColumnAndCheck` calls `checkModifyColumnWithForeignKeyConstraint` before normal modify-column data checks.
- `/Users/bba/pc/tidb/pkg/ddl/foreign_key.go:301`: `checkModifyColumnWithForeignKeyConstraint` checks the column being modified against owned and referred FKs.
- `/Users/bba/pc/tidb/pkg/ddl/foreign_key.go:356`: `isAcceptableForeignKeyColumnChange` rejects when `newCol.GetFlen() < relatedCol.GetFlen()` or `newCol.GetFlen() < originalCol.GetFlen()`.

## Root Cause

The FK modify-column validator treats these implications as true:

```text
new string length < original string length
=> unsafe FK column change

new string length < related FK column length
=> unsafe FK column change
```

Those implications are stricter than TiDB's own FK creation contract. The direct target schema can be valid even when parent and child `VARCHAR` lengths differ.

For the child shrink case, existing non-NULL child values are already constrained to match parent values. If the parent column is `varchar(10)`, a child value longer than 10 cannot have a matching parent value. The testbed red cell also projected `MAX(CHAR_LENGTH(b)) = 10`.

For the parent widen-to-15 case, the target FK pair `parent varchar(15), child varchar(20)` is directly accepted by TiDB. Widening the parent does not invalidate existing child rows, and future child rows can store parent values up to length 15 because the child length is 20.

## Expected Behavior

`MODIFY COLUMN` should be allowed when the target parent/child column pair is valid by TiDB's FK compatibility rules and existing data fits the target column definition.

## Fix Direction

Align modify-column FK validation with FK creation compatibility plus the normal data-fit check:

- keep checks for type, unsigned flag, charset, and collation compatibility;
- do not require `newFlen >= originalFlen` for character string FK columns when shrinking is data-safe;
- do not require `newFlen >= relatedFlen` when the resulting FK pair is accepted by `CREATE TABLE` / `ADD FOREIGN KEY`;
- add regression coverage for child shrink to parent length, child shrink above parent length, parent widen below child length, and the existing widening controls.

## Method Lesson

This is a second S10 hit, but a different sub-shape from id630001:

```text
code checks P: newFlen >= originalFlen and newFlen >= relatedFlen
system believes Q: the target FK column pair is valid only under those inequalities
fast path / safe path skipped: reject MODIFY COLUMN before data-fit and target-state validation
missing D: TiDB's real FK compatibility contract permits unequal VARCHAR lengths
oracle: direct target-schema acceptance plus existing-data fit
```

The efficient move was to compare the modify validator against a sibling validator for the same target state. `CREATE TABLE` already defines the product's accepted FK type pair; `MODIFY COLUMN` should not impose a stricter hidden length metric unless there is a separate data-safety reason.
