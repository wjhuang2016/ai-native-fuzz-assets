# CREATE TABLE IF NOT EXISTS can validate unused definitions before target existence

## Status

Confirmed on testbed `8192975`; inserted into remote `found_bug` as id630018.
Remote state after insert: `MAX(id)=630018,COUNT=58`.

## User-visible symptom

A rerunnable migration can fail even though the target table already exists and the statement uses
`IF NOT EXISTS`. TiDB sometimes validates the unused candidate table definition, or resolves the
unused `LIKE` source table, before reaching the target-exists no-op path.

## Minimal repro

```sql
DROP DATABASE IF EXISTS ai_create_if_exists_probe;
CREATE DATABASE ai_create_if_exists_probe;
USE ai_create_if_exists_probe;

CREATE TABLE t(a INT);
CREATE TABLE src(a INT, b VARCHAR(10));

CREATE TABLE IF NOT EXISTS t LIKE missing_src;
```

Actual:

```text
ERROR 1146 (42S02): Table 'ai_create_if_exists_probe.missing_src' doesn't exist
```

The target table still exists and remains unchanged:

```sql
SHOW CREATE TABLE t;
```

```text
CREATE TABLE `t` (
  `a` int DEFAULT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin
```

## Additional red cells

Candidate index column validation also runs before the target-exists no-op:

```sql
CREATE TABLE IF NOT EXISTS t(a INT, INDEX idx_b(b));
```

Actual:

```text
ERROR 1072 (42000): column does not exist: b
```

Candidate partition expression validation also runs before the no-op:

```sql
CREATE TABLE IF NOT EXISTS t(a INT)
PARTITION BY RANGE(b) (PARTITION p0 VALUES LESS THAN (10));
```

Actual:

```text
ERROR 1054 (42S22): Unknown column 'b' in 'partition function'
```

## Controls

Valid duplicate `CREATE TABLE IF NOT EXISTS` behaves as an idempotent no-op:

```sql
CREATE TABLE IF NOT EXISTS t(b BIGINT, c VARCHAR(60));
SHOW WARNINGS;
```

Actual:

```text
Note 1050 Table 'ai_create_if_exists_probe.t' already exists
```

Valid duplicate `CREATE TABLE ... LIKE` also behaves as a no-op:

```sql
CREATE TABLE IF NOT EXISTS t LIKE src;
SHOW WARNINGS;
```

Actual:

```text
Note 1050 Table 'ai_create_if_exists_probe.t' already exists
```

A duplicate-column candidate is also ignored when the target already exists:

```sql
CREATE TABLE IF NOT EXISTS t(a INT, a INT);
SHOW WARNINGS;
```

Actual:

```text
Note 1050 Table 'ai_create_if_exists_probe.t' already exists
```

New-table controls still error, as expected:

```sql
CREATE TABLE IF NOT EXISTS new_t LIKE missing_src;
CREATE TABLE IF NOT EXISTS new_idx(a INT, INDEX idx_b(b));
```

Actual:

```text
ERROR 1146 table missing_src doesn't exist
ERROR 1072 column does not exist: b
```

## Expected

When the target table already exists and `IF NOT EXISTS` is present, the statement should be
classified as an idempotent no-op with Note 1050 before validating candidate metadata that will be
discarded. The existing TiDB sibling behavior already follows this rule for ordinary duplicate
definitions and valid `LIKE` definitions.

## Root cause

`executor.CreateTable` performs candidate/source work before setting `OnExistIgnore` and before
calling `CreateTableWithInfo`:

```text
CreateTable
  if s.ReferTable != nil:
    resolve source schema/table       -- can return ERROR 1146
  BuildTableInfoWithLike/Stmt         -- can return ERROR 1072
  checkTableInfoValidWithStmt         -- can return ERROR 1054
  onExist = OnExistIgnore if IF NOT EXISTS
  CreateTableWithInfo(...)

createTableWithInfoJob
  if target table exists and OnExistIgnore:
    AppendNote(ErrTableExists)
    return nil
```

So only validators that happen to run after `createTableWithInfoJob`'s target-exists check are
ignored. Earlier candidate validators can bypass `IF NOT EXISTS`.

## Source anchors

- `pkg/ddl/executor.go:1015-1024`: `CREATE TABLE ... LIKE` source table is resolved before target
  existence is checked.
- `pkg/ddl/executor.go:1032-1041`: candidate `TableInfo` is built before target existence is
  checked.
- `pkg/ddl/executor.go:1044-1069`: partition/split/FK candidate validations run before
  `OnExistIgnore`.
- `pkg/ddl/executor.go:1072-1077`: `OnExistIgnore` is set only after those validations.
- `pkg/ddl/executor.go:1100-1113`: target-exists no-op path appends Note 1050 inside
  `createTableWithInfoJob`.
- `pkg/ddl/db_integration_test.go:59-84`: existing test covers valid duplicate `CREATE TABLE IF
  NOT EXISTS ... LIKE` and ordinary duplicate `CREATE TABLE IF NOT EXISTS`, but not missing-source
  or invalid-definition prechecks.

## Method value

This extends S15 from helper-before-existence-catch to target-exists-after-candidate-validation:

```text
P_check:  candidate source/metadata validates successfully
Q_claim:  CREATE TABLE may proceed to target creation
D_dim:    with IF NOT EXISTS and an already-existing target, candidate metadata will be discarded
F_effect: candidate validation errors return before the target-exists no-op path
```

The useful matrix is:

```text
target exists + valid candidate      -> Note 1050
target exists + invalid candidate    -> should still Note 1050, but some validators error
target absent + invalid candidate    -> hard error control
```

## Quality

Low severity, medium-to-high method value.

- User-visible rerunnable migration failure.
- Deterministic and minimal.
- No data corruption; existing target table is unchanged.
- Strong sibling oracle from TiDB's own valid duplicate `CREATE TABLE IF NOT EXISTS` behavior.
