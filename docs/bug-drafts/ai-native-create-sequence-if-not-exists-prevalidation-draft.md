# CREATE SEQUENCE IF NOT EXISTS can validate unused sequence options before target existence

## Status

Confirmed on testbed `8192975`; inserted into remote `found_bug` as id630019.
Remote state after insert: `MAX(id)=630019,COUNT=59`.

## User-visible symptom

A rerunnable `CREATE SEQUENCE IF NOT EXISTS` migration can fail even though the target sequence
already exists. TiDB validates the candidate sequence options before reaching the target-exists
no-op path.

## Minimal repro

```sql
DROP DATABASE IF EXISTS ai_seq_if_not_exists_probe;
CREATE DATABASE ai_seq_if_not_exists_probe;
USE ai_seq_if_not_exists_probe;

CREATE SEQUENCE seq START WITH 10 INCREMENT BY 2 MAXVALUE 10000;

CREATE SEQUENCE IF NOT EXISTS seq INCREMENT 0;
```

Actual:

```text
ERROR 4136 (HY000): Sequence 'ai_seq_if_not_exists_probe.seq' values are conflicting
```

The existing sequence is unchanged:

```sql
SHOW CREATE SEQUENCE seq;
```

```text
CREATE SEQUENCE `seq` start with 10 minvalue 1 maxvalue 10000 increment by 2 cache 1000 nocycle ENGINE=InnoDB
```

## Additional red cells

```sql
CREATE SEQUENCE IF NOT EXISTS seq MAXVALUE 1 START WITH 2;
```

Actual:

```text
ERROR 4136 (HY000): Sequence 'ai_seq_if_not_exists_probe.seq' values are conflicting
```

```sql
CREATE SEQUENCE IF NOT EXISTS seq CHARSET=utf8;
```

Actual:

```text
ERROR 8227 (HY000): Unsupported sequence table-option utf8
```

## Controls

Valid duplicate sequence creation is an idempotent no-op:

```sql
CREATE SEQUENCE IF NOT EXISTS seq START WITH 20 INCREMENT BY 5 MAXVALUE 20000;
SHOW WARNINGS;
SHOW CREATE SEQUENCE seq;
```

Actual:

```text
Note 1050 Table 'ai_seq_if_not_exists_probe.seq' already exists
SHOW CREATE SEQUENCE seq -> still start with 10, increment by 2, maxvalue 10000
```

New-sequence invalid controls still error, as expected:

```sql
CREATE SEQUENCE IF NOT EXISTS new_seq_bad INCREMENT 0;
CREATE SEQUENCE IF NOT EXISTS new_seq_bad2 MAXVALUE 1 START WITH 2;
CREATE SEQUENCE IF NOT EXISTS new_seq_bad3 CHARSET=utf8;
```

Actual:

```text
ERROR 4136 values are conflicting
ERROR 4136 values are conflicting
ERROR 8227 Unsupported sequence table-option utf8
```

After the failed duplicate attempts, `new_seq_bad`, `new_seq_bad2`, and `new_seq_bad3` were absent.

## Expected

When the target sequence already exists and `IF NOT EXISTS` is present, the statement should be
classified as an idempotent no-op with Note 1050 before validating candidate sequence options that
will be discarded. This matches TiDB's own valid duplicate sequence behavior.

## Root cause

`executor.CreateSequence` builds and validates candidate sequence metadata before the generic
target-exists handler:

```text
CreateSequence
  buildSequenceInfo(stmt, ident)
    validate sequence table options
    validate sequence options
  BuildTableInfo(...)
  onExist = OnExistIgnore if IF NOT EXISTS
  CreateTableWithInfo(...)

createTableWithInfoJob
  if target exists and OnExistIgnore:
    AppendNote(ErrTableExists)
    return nil
```

So invalid candidate options return `ErrSequenceInvalidData` or `ErrSequenceUnsupportedTableOption`
before `IF NOT EXISTS` can no-op the statement.

## Source anchors

- `pkg/ddl/executor.go:6067-6071`: `CreateSequence` calls `buildSequenceInfo` and returns its
  errors before `OnExistIgnore`.
- `pkg/ddl/executor.go:6080-6085`: `OnExistIgnore` is set only after candidate sequence info is
  built.
- `pkg/ddl/executor.go:1100-1113`: the target-exists no-op path is inside
  `createTableWithInfoJob`.
- `pkg/ddl/sequence.go:142-160`: sequence option validation rejects zero increment, invalid
  ranges, and overflow-prone cache/increment combinations.
- `pkg/ddl/sequence.go:171-184`: unsupported table options and invalid sequence options return
  before target existence is checked.
- `pkg/ddl/sequence_test.go:32-61`: existing tests cover invalid new sequence definitions, but
  not target-exists plus invalid candidate options.

## Method value

This validates id630018's create-like selector across a second owner:

```text
P_check:  candidate sequence options validate successfully
Q_claim:  CREATE SEQUENCE may proceed to target creation
D_dim:    with IF NOT EXISTS and an already-existing target sequence, candidate options are unused
F_effect: candidate option validation errors return before the target-exists no-op path
```

The efficient matrix is:

```text
target exists + valid candidate options    -> Note 1050
target exists + invalid candidate options  -> RED hard error
target absent + invalid candidate options  -> hard-error control
```

## Quality

Low severity, medium method value.

- User-visible rerunnable migration failure.
- Deterministic and minimal.
- No data corruption; existing sequence remains unchanged.
- Strong sibling oracle from valid duplicate `CREATE SEQUENCE IF NOT EXISTS`.
