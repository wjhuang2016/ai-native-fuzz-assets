# id630023 Method Case: Partition Column NULL To NOT NULL False Rejection

## What was found

`ALTER TABLE ... MODIFY COLUMN` rejects adding `NOT NULL` to a partition column even when the
table has no NULL values.

Remote bug DB: `found_bug` id630023, confirmed, medium severity, wrong-error.

## Why this was fast

This was not found by enumerating partition syntaxes. It came from continuing S10 after a negative
enum/set branch:

```text
transition validator has a compact allowlist
but direct target schema and generic data-fit validation may accept more cases
```

The static clue was compact:

```text
checkPartitionColumnModifiable:
  if eval type / flags / charset / collation differ -> reject

isAllowedPartitionColumnFlagChange:
  allow NOT NULL -> NULL
  reject NULL -> NOT NULL
```

That made the proof obligation obvious:

```text
P_check:  partition-column flag allowlist says NULL -> NOT NULL is unsafe
Q_claim:  the transition would require repartition or cannot be proven safe
D_dim:    nullability is a row-data invariant, not a partition-placement invariant
F_effect: reject before the generic NULL data check can prove safety
```

## Matrix

```text
direct target partition schema
  CREATE TABLE direct_range(a INT NOT NULL, b INT) PARTITION BY RANGE(a) ...
  INSERT non-NULL rows
  classification: GREEN

non-partition transition, no NULL rows
  CREATE TABLE nonpart(a INT NULL, b INT); INSERT (1),(11);
  ALTER TABLE nonpart MODIFY a INT NOT NULL
  classification: GREEN

partition transition, no NULL rows
  CREATE TABLE part_range(a INT NULL, b INT) PARTITION BY RANGE(a) ...
  INSERT (1),(11);
  ALTER TABLE part_range MODIFY a INT NOT NULL
  actual: ERROR 8200
  classification: RED

non-partition transition, NULL row present
  ALTER TABLE nonpart_null MODIFY a INT NOT NULL
  actual: ERROR 1265
  classification: GREEN unsafe-data control
```

The red reproduced for `RANGE(a)`, `LIST COLUMNS(a)`, `KEY(a)`, and `RANGE(TO_DAYS(datetime_col))`.

## Method Lesson

S10 is not only about string length metrics.

The improved version is:

```text
For each transition DDL validator, list the dimension it checks.
Then ask whether that dimension is truly part of the target structural contract, or whether a
later safe path already has a more precise data/reference validator.
```

Here the partition validator treated a flag change as a partition-placement change. Direct target
schemas and the non-partitioned data check show a better proof: `NULL -> NOT NULL` is safe exactly
when existing rows contain no NULLs.

## Boundary Samples From The Same Pass

- `LIST COLUMNS` with enum/set was not a red cell: direct target `LIST COLUMNS(enum_col)` is not
  accepted, so ALTER rejection does not exceed target-state rules.
- Column-level `REFERENCES` was not an S18 red cell: direct `CREATE TABLE ... pid INT REFERENCES p(id)`
  also publishes no FK metadata in this build.
- Unique-index conversion collision was a useful S17 green: `DECIMAL(10,2) UNIQUE` values
  `0.40` and `0.49` cannot be modified to `INT`; ALTER rejects with duplicate key and keeps the
  original schema.

## Pause Rule

Do not enumerate partition-column flags and type variants. Reopen S10 only for:

- another validation dimension that has a stronger direct target or data-fit reference;
- silent wrong-acceptance; or
- fix validation for partition-column nullability and safe varchar shrink.
