# id630005 Draft: CREATE TABLE LIKE Mutates Source CHECK Constraint Names

## Summary

`CREATE TABLE dst LIKE src` can mutate the SQL-visible CHECK constraint metadata of the source
table. After the LIKE DDL, `SHOW CREATE TABLE src` reports the source constraint with the target
table's generated constraint name.

Remote `found_bug` row:

```text
id:        630005
title:     CREATE TABLE LIKE mutates source CHECK constraint names in SHOW CREATE TABLE
severity:  medium
category:  metadata-corruption
oracle:    O16_SOURCE_TARGET_METADATA_ISOLATION_ORACLE
method:    S13_DDL_SHALLOW_COPY_TARGET_MUTATION
status:    confirmed
```

## Minimal Repro

```sql
SET GLOBAL tidb_enable_check_constraint = 1;

DROP DATABASE IF EXISTS ai_like_check_s13b;
CREATE DATABASE ai_like_check_s13b;
USE ai_like_check_s13b;

CREATE TABLE src_auto(a INT, CHECK (a > 0));
SHOW CREATE TABLE src_auto;

CREATE TABLE dst_auto LIKE src_auto;
SHOW CREATE TABLE src_auto;
SHOW CREATE TABLE dst_auto;
```

Before `CREATE TABLE LIKE`, the source table shows:

```text
CONSTRAINT `src_auto_chk_1` CHECK ((`a` > 0))
```

After `CREATE TABLE dst_auto LIKE src_auto`, both tables show:

```text
CONSTRAINT `dst_auto_chk_1` CHECK ((`a` > 0))
```

The mutation is visible from a new SQL connection as well.

## Behavioral Evidence

The CHECK constraint still enforces the expression, but the source table's user-visible name is
wrong:

```sql
INSERT INTO src_auto VALUES (-1);
```

Actual:

```text
ERROR 3819 (HY000): Check constraint 'dst_auto_chk_1' is violated.
```

`information_schema.check_constraints` still exposes both names:

```text
dst_auto_chk_1  (`a` > 0)
src_auto_chk_1  (`a` > 0)
```

So the metadata surfaces disagree: `SHOW CREATE TABLE src_auto` and the runtime error message use
the target name, while `information_schema.check_constraints` still has the original source name.

## Controls

Direct target creation does not mutate sibling tables:

```sql
CREATE TABLE d1(a INT, CHECK (a > 0));
CREATE TABLE d2(a INT, CHECK (a > 0));
SHOW CREATE TABLE d1; -- d1_chk_1
SHOW CREATE TABLE d2; -- d2_chk_1
```

The red cell is specific to the LIKE reconstruction path.

This is not duplicate `found_bug` id1. id1 is `CHANGE COLUMN` renaming a CHECK-referenced column
and dropping the constraint. id630005 is `CREATE TABLE LIKE` mutating the source table's CHECK
constraint metadata while constructing the target table.

## Source Anchor

`pkg/ddl/create_table.go`:

- `BuildTableInfoWithLike` starts with `tblInfo := *referTblInfo`, a shallow copy.
- It later calls `renameCheckConstraint(&tblInfo)`.
- `renameCheckConstraint` iterates `tblInfo.Constraints` and mutates each `*ConstraintInfo`:
  `cons.Name = ast.NewCIStr("")`, `cons.Table = tblInfo.Name`.
- Because the constraints slice was shallow-copied, those mutations can hit the source table's
  `ConstraintInfo` objects.

`pkg/meta/model/table.go` already has `ConstraintInfo.Clone`, so the fix can reuse an existing
deep-copy primitive.

## Fix Direction

Deep-clone CHECK constraints before target-only renaming:

```text
tblInfo.Constraints = clone each referTblInfo.Constraints[i]
renameCheckConstraint(&tblInfo)
```

or build the LIKE table from `referTblInfo.Clone()` and then reset target-specific IDs, names,
foreign keys, cache state, TTL temporary-table restrictions, and other fields as today.

The invariant is: constructing a target table must never mutate source table metadata, including
metadata that is only held through pointer fields.

