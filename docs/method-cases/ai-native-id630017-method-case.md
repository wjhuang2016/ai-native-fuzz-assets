# id630017 Method Case: `DROP INDEX IF EXISTS PRIMARY`

## What was found

`ALTER TABLE t DROP INDEX IF EXISTS \`PRIMARY\`` still returns a hard 1091 error when `t` has no
primary key. The same table and the same flag work for an ordinary missing index name.

Remote bug DB: `found_bug` id630017, confirmed, low severity, wrong-error.

## Why this was fast

The key was not to enumerate more DDL syntax. The previous S15 bugs changed the audit question:

```text
Before the duplicate/missing-object catch runs,
can any helper return an error that is only relevant for a real object?
```

That made `dropIndex` stand out:

```text
indexInfo := FindIndexByName(indexName)
isPK, err := CheckIsDropPrimaryKey(indexName, indexInfo, t)
if err != nil { return err }

if indexInfo == nil {
  if ifExist { AppendNote; return nil }
  return missing-index error
}
```

`CheckIsDropPrimaryKey` is a special-name classifier. It answers "is this the PRIMARY key path?"
before the code answers "does the requested index exist?".

## Matrix

```text
ordinary missing index, no flag
  ALTER TABLE no_pk DROP INDEX missing_i
  actual: ERROR 1091
  classification: GREEN hard-error control

ordinary missing index, IF EXISTS
  ALTER TABLE no_pk DROP INDEX IF EXISTS missing_i
  actual: Note 1091, no-op
  classification: GREEN safe-path control

missing PRIMARY, IF EXISTS
  ALTER TABLE no_pk DROP INDEX IF EXISTS `PRIMARY`
  actual: ERROR 1091 Can't DROP 'PRIMARY'
  classification: RED

top-level missing PRIMARY, IF EXISTS
  DROP INDEX IF EXISTS `PRIMARY` ON no_pk
  actual: ERROR 1091 Can't DROP 'PRIMARY'
  classification: RED companion entrypoint

existing PRIMARY, IF EXISTS
  CREATE TABLE pk_nc(a INT PRIMARY KEY NONCLUSTERED, b INT)
  ALTER TABLE pk_nc DROP INDEX IF EXISTS `PRIMARY`
  actual: success, index removed
  classification: GREEN real-object control
```

## Method lesson

S15 now has a fifth useful sub-shape:

```text
idempotence flag + generic missing-object catch exists
+ special-name / special-object classifier runs earlier
+ classifier returns a hard error when the object is absent
```

This is different from id630015 and id630016:

- id630015: raw requested-name count before existence filtering.
- id630016: capability gate before duplicate classification.
- id630017: special-name classifier before missing-object classification.

The efficient way to find this was:

1. Start from existing green/negative samples. `DROP COLUMN IF EXISTS` was green because it
   classifies missing columns before richer validators.
2. Search for DDL owners where `IF EXISTS` is handled after helper calls.
3. Prefer helpers that use a coarse symbol such as `PRIMARY`, `DEFAULT`, raw name count, or global
   capability state.
4. Build a three-cell oracle: ordinary missing object, special missing object, real special object.

## Pause rule

Do not enumerate every spelling of `DROP INDEX`. Reopen S15 here only for:

- another special-name classifier before an existence catch;
- silent duplicate write or wrong-acceptance;
- fix validation that must preserve ordinary missing-index notes and real PRIMARY drops.
