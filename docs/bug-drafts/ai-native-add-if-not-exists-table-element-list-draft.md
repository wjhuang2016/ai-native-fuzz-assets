# id630010 Draft: ADD IF NOT EXISTS Table-Element List Drops Constraint Flags

## Summary

`ALTER TABLE ... ADD IF NOT EXISTS (...)` accepts a table-element list containing indexes and
CHECK constraints. The first execution succeeds, but retrying the same statement still takes the
hard duplicate-error path for those constraints.

Remote `found_bug` row:

```text
id:        630010
title:     ADD IF NOT EXISTS table-element list still errors on existing indexes/check constraints
severity:  low
category:  wrong-error
oracle:    O18_IDEMPOTENT_DDL_FLAG_ORACLE
method:    S15_DDL_IDEMPOTENCE_FLAG_DROPPED
status:    confirmed
```

## Minimal Repro

```sql
DROP DATABASE IF EXISTS ai_s15_clean;
CREATE DATABASE ai_s15_clean;
USE ai_s15_clean;

CREATE TABLE idx_outer(a INT);

ALTER TABLE idx_outer ADD IF NOT EXISTS (KEY idx_a(a));

-- Should be idempotent under the accepted outer IF NOT EXISTS.
ALTER TABLE idx_outer ADD IF NOT EXISTS (KEY idx_a(a));
```

Actual:

```text
ERROR 1061 (42000): Duplicate key name 'idx_a'
```

Same shape for CHECK constraints:

```sql
CREATE TABLE ck_outer(a INT);

ALTER TABLE ck_outer ADD IF NOT EXISTS (CONSTRAINT ck_a CHECK (a > 0));

-- Should be idempotent or the syntax should have been rejected up front.
ALTER TABLE ck_outer ADD IF NOT EXISTS (CONSTRAINT ck_a CHECK (a > 0));
```

Actual:

```text
ERROR 3822 (HY000): Duplicate check constraint name 'ck_a'.
```

## Controls

The same outer flag works for columns split from the same table-element-list grammar:

```sql
CREATE TABLE col_outer(a INT);
ALTER TABLE col_outer ADD IF NOT EXISTS (b INT);
ALTER TABLE col_outer ADD IF NOT EXISTS (b INT);
SHOW WARNINGS;
```

```text
Note 1060 Duplicate column name 'b'
```

Index idempotence itself also works when the inner constraint-level flag is present:

```sql
CREATE TABLE idx_inner(a INT);
ALTER TABLE idx_inner ADD IF NOT EXISTS (KEY IF NOT EXISTS idx_a(a));
ALTER TABLE idx_inner ADD IF NOT EXISTS (KEY IF NOT EXISTS idx_a(a));
SHOW WARNINGS;
```

```text
Note 1061 Duplicate key name 'idx_a'
```

The schema-count guard shows this is wrong-error rather than duplicate metadata insertion:

```sql
SELECT COUNT(*)
FROM information_schema.tidb_indexes
WHERE table_schema='ai_s15_clean'
  AND table_name='idx_outer'
  AND key_name='idx_a';

SELECT COUNT(*)
FROM information_schema.check_constraints
WHERE constraint_schema='ai_s15_clean'
  AND constraint_name='ck_a';
```

Both counts are `1`.

## Source Anchor

`pkg/parser/parser.y` accepts `ADD ColumnKeywordOpt IfNotExists '(' TableElementList ')'` and
stores the flag on the parent `AlterTableSpec`:

```text
parser.y:2401-2418
  IfNotExists:    $3.(bool),
  NewColumns:     columnDefs,
  NewConstraints: constraints,
```

`pkg/ddl/executor.go` splits that parent spec into individual column and constraint specs:

```text
executor.go:1637-1652
  t := *spec
  ...
  t.Constraint = con
  t.Tp = ast.AlterTableAddConstraint
```

The column path honors `spec.IfNotExists`:

```text
add_column.go:160-166
  if col != nil {
    err = infoschema.ErrColumnExists...
    if spec.IfNotExists { AppendNote; return nil }
  }
```

The constraint path does not merge the parent flag into the constraint-level flag:

```text
executor.go:1809-1812
  createIndex(..., constr.IfNotExists)

executor.go:1821-1825
  CreateCheckConstraint(..., spec.Constraint)
```

So `ADD IF NOT EXISTS (KEY idx_a(a))` loses the outer flag unless the user writes a second inner
`KEY IF NOT EXISTS`.

## Fix Direction

When splitting `ADD IF NOT EXISTS (...)`, either:

- propagate or merge the parent `spec.IfNotExists` into supported constraint-level idempotence
  checks, such as `spec.IfNotExists || constr.IfNotExists` for ordinary indexes; or
- reject unsupported flagged constraint forms during parsing/resolution instead of accepting them
  and later taking a hard duplicate-error path.

CHECK constraints need an explicit product decision: either duplicate CHECK under the accepted
outer flag becomes note/no-op, or the syntax is rejected as unsupported.

## Quality

Low to medium.

- User-visible migration-idempotence failure.
- No data corruption; index and CHECK counts stay at one.
- Method value is high: S15 should audit flag ownership across parser/spec splitting and AST
  rewrites, not only direct executor sibling branches.
