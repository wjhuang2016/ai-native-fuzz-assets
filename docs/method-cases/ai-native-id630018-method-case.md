# id630018 Method Case: CREATE TABLE IF NOT EXISTS Prevalidation

## What was found

`CREATE TABLE IF NOT EXISTS t ...` can still fail when `t` already exists if an unused candidate
definition is invalid, or if an unused `LIKE` source table is missing.

Remote bug DB: `found_bug` id630018, confirmed, low severity, wrong-error.

## Why this was fast

id630017 made the selector sharper:

```text
safe path exists, but an earlier helper/classifier can return before the requested object is
classified as already-present or missing.
```

Applying that to the create-table owner exposed a different ordering version:

```text
candidate source/metadata validation
  runs before
target-exists IF NOT EXISTS no-op
```

The static clue was visible in `executor.CreateTable`: source resolution and `BuildTableInfo` run
before `onExist = OnExistIgnore` is set and before `CreateTableWithInfo` checks whether the target
table already exists.

## Matrix

```text
target exists + valid ordinary definition
  CREATE TABLE IF NOT EXISTS t(b BIGINT, c VARCHAR(60))
  actual: Note 1050, t unchanged
  classification: GREEN

target exists + valid LIKE source
  CREATE TABLE IF NOT EXISTS t LIKE src
  actual: Note 1050, t unchanged
  classification: GREEN

target exists + missing LIKE source
  CREATE TABLE IF NOT EXISTS t LIKE missing_src
  actual: ERROR 1146
  classification: RED

target exists + invalid index column
  CREATE TABLE IF NOT EXISTS t(a INT, INDEX idx_b(b))
  actual: ERROR 1072
  classification: RED

target exists + invalid partition expression
  CREATE TABLE IF NOT EXISTS t(a INT) PARTITION BY RANGE(b) (...)
  actual: ERROR 1054
  classification: RED

target absent + missing LIKE source / invalid index column
  actual: same hard errors
  classification: GREEN hard-error control

target exists + duplicate column candidate
  CREATE TABLE IF NOT EXISTS t(a INT, a INT)
  actual: Note 1050
  classification: GREEN calibration; not every definition issue runs before target-exists
```

## Method lesson

S15 now has a sixth sub-shape:

```text
idempotence flag + target-exists no-op exists
+ candidate object/source validation runs before target existence classification
+ candidate validation returns a hard error even though the candidate will be discarded
```

This is why the fastest search loop is:

1. Draw the exact validation order.
2. Mark the first existence/duplicate/missing classification point.
3. Audit every earlier check with the question: "would this check matter if the requested DDL is a
   no-op?"
4. Keep the matrix tiny: target exists + valid candidate, target exists + invalid candidate,
   target absent + invalid candidate.

## Pause rule

Do not enumerate all `CREATE TABLE` options. Reopen only for:

- another create-like owner where target-exists is delayed behind a different candidate owner;
- silent wrong-acceptance or duplicate-write;
- fix validation for missing LIKE source, invalid index column, invalid partition expression, and
  existing valid duplicate behavior.
