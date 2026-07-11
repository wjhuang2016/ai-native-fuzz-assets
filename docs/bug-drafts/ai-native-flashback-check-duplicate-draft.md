# id630006 Draft: FLASHBACK TABLE Restores Duplicate CHECK Constraint Names

## Summary

`FLASHBACK TABLE ... TO new_name` can publish a recovered table whose CHECK constraint name
duplicates an existing CHECK constraint in the same schema. This breaks TiDB/MySQL's schema-level
CHECK constraint namespace invariant and makes runtime errors ambiguous.

Remote `found_bug` row:

```text
id:        630006
title:     FLASHBACK TABLE can restore duplicate CHECK constraint names in one schema
severity:  medium
category:  metadata-corruption
oracle:    O17_SCHEMA_CHECK_CONSTRAINT_NAMESPACE_ORACLE
method:    S14_DDL_RECOVERY_NAMESPACE_VALIDATION_BYPASS
status:    confirmed
```

## Minimal Repro

```sql
SET GLOBAL tidb_enable_check_constraint = 1;

DROP DATABASE IF EXISTS ai_flash_dup_s14;
CREATE DATABASE ai_flash_dup_s14;
USE ai_flash_dup_s14;

CREATE TABLE f(a INT, CHECK (a > 0));
SHOW CREATE TABLE f; -- CONSTRAINT `f_chk_1` CHECK ((`a` > 0))

DROP TABLE f;

CREATE TABLE f(a INT, CHECK (a > 1));
SHOW CREATE TABLE f; -- CONSTRAINT `f_chk_1` CHECK ((`a` > 1))

FLASHBACK TABLE f TO f_old;

SHOW CREATE TABLE f;
SHOW CREATE TABLE f_old;
SELECT constraint_name, check_clause
  FROM information_schema.check_constraints
 WHERE constraint_schema = 'ai_flash_dup_s14'
 ORDER BY constraint_name, check_clause;
```

Actual after flashback:

```text
f      -> CONSTRAINT `f_chk_1` CHECK ((`a` > 1))
f_old  -> CONSTRAINT `f_chk_1` CHECK ((`a` > 0))

information_schema.check_constraints:
f_chk_1  (`a` > 0)
f_chk_1  (`a` > 1)
```

Runtime errors are also ambiguous:

```sql
INSERT INTO f VALUES (1);
-- ERROR 3819 (HY000): Check constraint 'f_chk_1' is violated.

INSERT INTO f_old VALUES (0);
-- ERROR 3819 (HY000): Check constraint 'f_chk_1' is violated.
```

## Controls

Normal DDL paths enforce the schema-level namespace:

```sql
CREATE TABLE base(a INT, CHECK (a > 0));
CREATE TABLE dup_explicit(a INT, CONSTRAINT base_chk_1 CHECK (a > 1));
-- ERROR 3822 (HY000): Duplicate check constraint name 'base_chk_1'.
```

`CREATE TABLE LIKE` also proves that target reconstruction can produce independent target names:

```sql
CREATE TABLE like_copy LIKE f;
SHOW CREATE TABLE like_copy;
-- CONSTRAINT `like_copy_chk_1` CHECK ((`a` > 1))
```

The red cell is therefore not "CHECK names are allowed to duplicate"; it is specific to the
recovery/flashback path re-materializing old metadata without validating it against the current
schema namespace.

## Source Anchor

- `/Users/bba/pc/tidb/pkg/executor/ddl.go:605`: `executeFlashbackTable`.
- `/Users/bba/pc/tidb/pkg/executor/ddl.go:610`: `FLASHBACK TABLE ... TO new_name` changes only
  `tblInfo.Name`.
- `/Users/bba/pc/tidb/pkg/ddl/table.go:183`: `onRecoverTable` checks that the target table name
  does not exist.
- `/Users/bba/pc/tidb/pkg/ddl/table.go:191`: `onRecoverTable` checks that the target table ID does
  not exist.
- `/Users/bba/pc/tidb/pkg/ddl/table.go:1311`: `checkConstraintNamesNotExists` is the schema-level
  CHECK-name uniqueness helper.
- `/Users/bba/pc/tidb/pkg/ddl/create_table.go:73`: `CREATE TABLE` calls the helper.
- `/Users/bba/pc/tidb/pkg/ddl/constraint.go:153`: `ALTER TABLE ADD CHECK` calls the helper.

`onRecoverTable` does not call the same helper before publishing recovered table metadata.

## Fix Direction

At minimum, `RecoverTable` should run the same schema-level CHECK constraint name validation used
by `CREATE TABLE` and `ALTER TABLE ADD CHECK`.

If product semantics require `FLASHBACK TABLE ... TO new_name` to regenerate anonymous CHECK names,
the fix needs a safe way to distinguish target-owned generated names from user-authored names.
Without that distinction, rejecting conflicting restored names is safer than publishing duplicate
public CHECK metadata.

## Method Lesson

The fast path was:

```text
normal create/add validates schema-level CHECK names
recovery path trusts restored metadata after only table-name/table-ID checks
```

This is a sibling-path proof gap. Once a DDL path re-materializes metadata into a current schema,
it must re-prove every schema-level namespace invariant that normal create/add paths prove.
