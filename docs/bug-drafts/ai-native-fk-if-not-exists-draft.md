# id630008 Draft: ADD FOREIGN KEY IF NOT EXISTS Ignores Idempotence Flag

## Summary

`ALTER TABLE ... ADD CONSTRAINT ... FOREIGN KEY IF NOT EXISTS` accepts the syntax and succeeds the
first time, but a second execution with the same foreign key name still fails with duplicate-FK
error instead of behaving as an idempotent DDL.

Remote `found_bug` row:

```text
id:        630008
title:     ADD FOREIGN KEY IF NOT EXISTS still errors on existing foreign key
severity:  low
category:  wrong-error
oracle:    O18_IDEMPOTENT_DDL_FLAG_ORACLE
method:    S15_DDL_IDEMPOTENCE_FLAG_DROPPED
status:    confirmed
```

## Minimal Repro

```sql
DROP DATABASE IF EXISTS ai_fk_ifne;
CREATE DATABASE ai_fk_ifne;
USE ai_fk_ifne;
SET foreign_key_checks=1;

CREATE TABLE p(id INT PRIMARY KEY);
CREATE TABLE c(id INT PRIMARY KEY, pid INT, INDEX(pid));

ALTER TABLE c ADD CONSTRAINT fk_pid FOREIGN KEY IF NOT EXISTS (pid) REFERENCES p(id);

-- Should be idempotent because fk_pid already exists.
ALTER TABLE c ADD CONSTRAINT fk_pid FOREIGN KEY IF NOT EXISTS (pid) REFERENCES p(id);
```

Actual:

```text
ERROR 1826 (HY000): Duplicate foreign key constraint name 'fk_pid'
```

The first execution leaves exactly one FK:

```sql
SELECT constraint_name, table_name, referenced_table_name
FROM information_schema.referential_constraints
WHERE constraint_schema='ai_fk_ifne';
```

```text
fk_pid  c  p
```

## Controls

Plain duplicate ADD FOREIGN KEY still fails, as expected:

```sql
ALTER TABLE c ADD CONSTRAINT fk_pid FOREIGN KEY (pid) REFERENCES p(id);
-- ERROR 1826
```

Sibling idempotent DDL works:

```sql
CREATE TABLE idx_t(a INT, KEY idx_a(a));
ALTER TABLE idx_t ADD INDEX IF NOT EXISTS idx_a(a);
SHOW WARNINGS;
```

```text
Note 1061 Duplicate key name 'idx_a'
```

`DROP FOREIGN KEY IF EXISTS` is not accepted by this parser, so it is not part of this finding.

## Source Anchor

`pkg/ddl/executor.go` handles `ALTER TABLE ADD CONSTRAINT` by constraint kind:

- `ConstraintKey` / `ConstraintIndex` pass `constr.IfNotExists` into `createIndex`.
- `ConstraintColumnar` also passes `constr.IfNotExists`.
- `ConstraintForeignKey` has a source comment saying FK already-exists handling is not implemented,
  and calls `CreateForeignKey` without passing `IfNotExists`.

`CreateForeignKey` eventually reaches `checkAddForeignKeyValidInOwner`, whose first check is
`checkFKDupName`; duplicate name therefore returns `ErrFkDupName` even when the parsed AST carried
`IF NOT EXISTS`.

## Fix Direction

Propagate idempotence semantics for foreign-key ADD:

- If the FK name already exists and `IF NOT EXISTS` is present, return success with a note/warning,
  matching the index sibling behavior.
- Keep hard errors for plain duplicate ADD FOREIGN KEY.
- Keep validating missing parent/index/column/type errors when the FK does not already exist.

Fix validation should include: first ADD succeeds, duplicate ADD without `IF NOT EXISTS` fails,
duplicate ADD with `IF NOT EXISTS` succeeds without creating a second FK row, and non-duplicate
invalid FK definitions still error.

## Quality

Low to medium.

- User-visible wrong-error for an accepted DDL syntax.
- No data corruption and no wrong result; schema remains unchanged after the failed duplicate.
- Method value is higher than severity: it introduces a compact selector for DDL idempotence flags
  that are handled in one sibling branch but dropped in another.
