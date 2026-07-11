# id630019 Method Case: CREATE SEQUENCE IF NOT EXISTS Prevalidation

## What was found

`CREATE SEQUENCE IF NOT EXISTS seq ...` can still fail when `seq` already exists if the unused
candidate sequence options are invalid.

Remote bug DB: `found_bug` id630019, confirmed, low severity, wrong-error.

## Why this was fast

This was not found by enumerating sequence syntax. It came from migrating id630018's selector:

```text
target-exists no-op exists,
but candidate validation runs before target-exists classification.
```

The static clue was compact:

```text
CreateSequence:
  buildSequenceInfo(stmt, ident)  // validates candidate
  onExist = OnExistIgnore
  CreateTableWithInfo(...)

CreateTableWithInfo:
  createTableWithInfoJob(...)
    if target exists and OnExistIgnore -> Note 1050
```

So the target-exists classifier was downstream of candidate sequence validation.

## Matrix

```text
target exists + valid duplicate sequence options
  CREATE SEQUENCE IF NOT EXISTS seq START WITH 20 INCREMENT BY 5 MAXVALUE 20000
  actual: Note 1050; existing seq remains start 10 / increment 2 / maxvalue 10000
  classification: GREEN

target exists + zero increment
  CREATE SEQUENCE IF NOT EXISTS seq INCREMENT 0
  actual: ERROR 4136 values are conflicting
  classification: RED

target exists + invalid max/start
  CREATE SEQUENCE IF NOT EXISTS seq MAXVALUE 1 START WITH 2
  actual: ERROR 4136 values are conflicting
  classification: RED

target exists + unsupported table option
  CREATE SEQUENCE IF NOT EXISTS seq CHARSET=utf8
  actual: ERROR 8227 unsupported sequence table-option
  classification: RED

target absent + the same invalid candidates
  actual: same hard errors
  classification: GREEN hard-error controls
```

## Method lesson

S15's create-like sub-shape is now cross-owner:

```text
id630018: CREATE TABLE target-exists classifier delayed behind candidate table/source validation
id630019: CREATE SEQUENCE target-exists classifier delayed behind candidate sequence validation
```

The new selector wording:

```text
For any CREATE-like IF NOT EXISTS owner, locate the first target-exists classifier.
All candidate-source / candidate-metadata validation before that point is suspect, because the
candidate object is discarded when the target already exists.
```

This is better than syntax enumeration because it names the owner boundary:

- target object classifier;
- candidate object builder;
- candidate validators;
- shared create job wrapper.

## Pause rule

Do not enumerate every sequence option. Reopen only for:

- another create-like owner with a distinct candidate builder before target-exists;
- silent wrong-acceptance or duplicate-write;
- fix validation that must preserve valid duplicate sequence no-op and target-absent invalid
  sequence errors.
