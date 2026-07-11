# id630006 Method Case: Recovery Namespace Validation Bypass

## Selector

```text
S14_DDL_RECOVERY_NAMESPACE_VALIDATION_BYPASS
```

This selector applies when a recover/flashback/import path re-materializes stored metadata into a
current schema and validates only the container/object identity, while sibling create/add paths
validate additional schema-level namespaces or references.

```text
P_check:  recovered table name and table ID are free
Q_claim:  recovered TableInfo can be safely published in the current schema
effect:   publish restored metadata without running create/add namespace validators
D_dim:    CHECK constraint names are schema-level, not table-local
```

## Matrix

| Cell | Initial state | Operation | Oracle | Result |
| --- | --- | --- | --- | --- |
| Normal duplicate check | existing `base_chk_1` in schema | `CREATE TABLE ... CONSTRAINT base_chk_1` | duplicate name must be rejected | GREEN, error 3822 |
| Dropped + recreated name | dropped `f(a CHECK a>0)`, current `f(a CHECK a>1)` | `FLASHBACK TABLE f TO f_old` | schema must not contain duplicate CHECK names | RED, succeeds |
| Metadata surface | same as red cell | `SHOW CREATE f`, `SHOW CREATE f_old`, `I_S.CHECK_CONSTRAINTS` | no duplicate public CHECK name in schema | RED, two `f_chk_1` rows |
| Runtime behavior | same as red cell | violating inserts into both tables | error names should identify a unique constraint | RED, both report `f_chk_1` |
| Sibling target reconstruction | current `f(a CHECK a>1)` | `CREATE TABLE like_copy LIKE f` | target reconstruction can create independent names | GREEN, `like_copy_chk_1` |

## Oracle

```text
O17_SCHEMA_CHECK_CONSTRAINT_NAMESPACE_ORACLE
```

The oracle is schema-level: after any DDL publishes a table, `CHECK_CONSTRAINTS` must not contain
duplicate CHECK constraint names within the same schema. Normal create/add duplicate-name failures
are the control path.

## Why The Method Worked

id630005 showed that CHECK constraints are an especially good nested metadata owner because their
names are SQL-visible and schema-scoped. Instead of enumerating CHECK expressions, the next useful
question was:

```text
Which sibling DDL paths publish TableInfo with CHECK metadata but skip the normal namespace gate?
```

`CREATE TABLE` and `ADD CHECK` both call `checkConstraintNamesNotExists`. `RecoverTable` checks only
the table name and table ID before recreating the table. That narrowed the test to one tiny
FLASHBACK matrix.

## Quality

Medium.

- It violates a documented schema-level metadata invariant.
- It is user-visible in `SHOW CREATE TABLE`, `information_schema.check_constraints`, and CHECK
  violation errors.
- It does not corrupt row data; enforcement still applies per table.
- The method value is high because it generalizes beyond CHECK constraints: recovery paths must
  re-prove the same namespace/reference invariants as create/add paths.

## Pause Gate

Do not enumerate all flashback/recover fields. Reopen S14 only for:

- another schema-level namespace or reference validator that create/add runs but recover bypasses;
- a behavioral consequence stronger than duplicate metadata names;
- fix validation for CHECK names, FK references, placement refs, table cache, and other recovered
  side metadata.
