# DROP INDEX IF EXISTS `PRIMARY` still errors when no primary key exists

## Status

Confirmed on testbed `8192975`; inserted into remote `found_bug` as id630017.
Remote state after insert: `MAX(id)=630017,COUNT=57`.

## User-visible symptom

A rerunnable DDL migration can still fail when it asks TiDB to drop a missing
`PRIMARY` index with `IF EXISTS`. Ordinary missing index names are correctly converted to a
note/no-op, but the special `PRIMARY` name takes a different error path.

## Minimal repro

```sql
DROP DATABASE IF EXISTS ai_native_pk_if_exists2;
CREATE DATABASE ai_native_pk_if_exists2;
USE ai_native_pk_if_exists2;

CREATE TABLE no_pk(a INT, KEY ka(a));

ALTER TABLE no_pk DROP INDEX IF EXISTS missing_i;
SHOW WARNINGS;

ALTER TABLE no_pk DROP INDEX IF EXISTS `PRIMARY`;
```

Actual for the ordinary missing index:

```text
Note 1091 index missing_i doesn't exist
```

Actual for the missing `PRIMARY` index:

```text
ERROR 1091 (42000): Can't DROP 'PRIMARY'; check that column/key exists
```

The top-level form has the same symptom:

```sql
DROP INDEX IF EXISTS `PRIMARY` ON no_pk;
```

Actual:

```text
ERROR 1091 (42000): Can't DROP 'PRIMARY'; check that column/key exists
```

## Controls

Existing `PRIMARY` is still dropped normally:

```sql
CREATE TABLE pk_nc(a INT PRIMARY KEY NONCLUSTERED, b INT);
ALTER TABLE pk_nc DROP INDEX IF EXISTS `PRIMARY`;
SHOW WARNINGS;
SHOW INDEX FROM pk_nc;
```

Actual:

```text
Query OK
SHOW WARNINGS -> Empty set
SHOW INDEX FROM pk_nc -> Empty set
```

Missing ordinary index without the flag still errors, as expected:

```sql
ALTER TABLE no_pk DROP INDEX missing_i;
```

Actual:

```text
ERROR 1091 (42000): index missing_i doesn't exist
```

## Expected

Because the requested `PRIMARY` index/key does not exist on `no_pk`, `IF EXISTS` should make the
statement an idempotent no-op with a missing-index note. This should match ordinary missing index
behavior.

## Root cause

`dropIndex` computes `indexInfo := t.Meta().FindIndexByName(indexName.L)`, then calls
`CheckIsDropPrimaryKey(indexName, indexInfo, t)` before the generic missing-index handler.

For `indexName = PRIMARY` and `indexInfo == nil` on a table without a primary key,
`CheckIsDropPrimaryKey` returns `ErrCantDropFieldOrKey` immediately:

```text
dropIndex
  indexInfo := FindIndexByName("primary") // nil
  CheckIsDropPrimaryKey("primary", nil, table)
    -> ErrCantDropFieldOrKey("PRIMARY")
  return err

generic IF EXISTS handler is below this point:
  if indexInfo == nil && ifExist { AppendNote; return nil }
```

So the `IF EXISTS` safe path is bypassed only for the special `PRIMARY` name.

## Source anchors

- `pkg/parser/parser.y:2832-2838`: `ALTER TABLE ... DROP INDEX IF EXISTS Identifier` stores
  `spec.IfExists`.
- `pkg/parser/parser.y:5718-5729`: top-level `DROP INDEX IF EXISTS Identifier ON table` stores
  `stmt.IfExists`.
- `pkg/ddl/executor.go:5518-5521`: `dropIndex` returns errors from
  `CheckIsDropPrimaryKey` before checking `indexInfo == nil`.
- `pkg/ddl/executor.go:5527-5533`: generic missing-index handler converts the error to a note
  when `ifExist` is set, but this block is unreachable for missing `PRIMARY`.
- `pkg/ddl/executor.go:5571-5582`: `CheckIsDropPrimaryKey` treats missing `PRIMARY` as a
  primary-key drop and returns `ErrCantDropFieldOrKey`.

## Method value

This extends S15 from partition raw-count/capability ordering to a special-name classifier:

```text
P_check:  indexName == PRIMARY and indexInfo is nil
Q_claim:  this must be handled as a primary-key drop error
D_dim:    under IF EXISTS, the requested object is missing and should be classified as a no-op
F_effect: CheckIsDropPrimaryKey returns before the generic missing-index IF EXISTS handler
```

The useful selector was:

```text
does any helper before the existence/missing-object catch classify a special object name
and return an error that would be irrelevant if the object is absent?
```

## Quality

Low severity, high method value.

- User-visible rerunnable migration failure.
- Deterministic and minimal.
- No data corruption.
- Strong sibling oracle: ordinary missing index is already a note/no-op under `IF EXISTS`.
- Strong control: existing `PRIMARY` drop still works.
