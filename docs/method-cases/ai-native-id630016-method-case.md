# Method Case: id630016 ADD PARTITION IF NOT EXISTS DEFAULT precheck

## One-line result

`ALTER TABLE ... ADD PARTITION IF NOT EXISTS (PARTITION p0 ...)` returns `ERROR 8200`
on a LIST-partitioned table that already has a DEFAULT partition, even when `p0`
already exists and the same duplicate-name statement is a note/no-op on a LIST table
without DEFAULT.

## P/Q/D/F/O card

```text
P_check:
  AddTablePartitions checks whether the current LIST table already has a DEFAULT partition.

Q_claim:
  If the table has DEFAULT, every ADD LIST PARTITION request must fail and use REORGANIZE
  PARTITION instead.

D_dims:
  IF NOT EXISTS changes the object-existence dimension. A requested partition name that already
  exists should be classified as an idempotent duplicate before capability checks for genuinely
  new partitions decide whether ADD is supported.

F_effect:
  The executor returns ERROR 8200 before combining old/new definitions and reaching the
  ErrSameNamePartition + IfNotExists note path.

O_oracle:
  O18 idempotent DDL flag oracle:
  compare duplicate-name IF NOT EXISTS behavior with and without DEFAULT, and include a
  DEFAULT+new-partition control that should still error.
```

## Matrix

```text
LIST table without DEFAULT, duplicate name:
  ALTER TABLE l_no_default ADD PARTITION IF NOT EXISTS (PARTITION p0 VALUES IN (3))
  actual: Note 1517 Duplicate partition name p0; p0,p1 remain
  classification: GREEN reference for duplicate-name idempotence

LIST table with DEFAULT, duplicate name:
  ALTER TABLE l_default_dup ADD PARTITION IF NOT EXISTS (PARTITION p0 VALUES IN (2))
  actual: ERROR 8200 Unsupported ADD List partition, already contains DEFAULT partition
  classification: RED

LIST table with DEFAULT, new name:
  ALTER TABLE l_default_new ADD PARTITION IF NOT EXISTS (PARTITION p1 VALUES IN (2))
  actual: ERROR 8200
  classification: GREEN capability-control

LIST table without DEFAULT, new name:
  ALTER TABLE l_no_default_new ADD PARTITION IF NOT EXISTS (PARTITION p1 VALUES IN (2))
  actual: p1 is added
  classification: GREEN ordinary-add control
```

## Why this was fast

This was found by continuing the id630015 question instead of widening syntax:

```text
For an idempotent DDL flag, can any earlier precheck return a different error before the
requested object is classified as existing/missing?
```

`ADD PARTITION IF NOT EXISTS` matched the source shape:

1. Parser stores `spec.IfNotExists`.
2. Executor has a duplicate-partition catch only inside `checkPartitionDefinitionConstraints`.
3. A LIST DEFAULT capability gate runs before that combined-definition duplicate check.
4. The oracle needs only four cells: duplicate vs new, DEFAULT vs no DEFAULT.

The negative samples from the same session were useful too. `DROP COLUMN IF EXISTS` in
multi-schema ALTER was green because the count check used submitted subjobs after missing-column
filtering, not the raw request list. TTL rename/drop was also green because source and tests have
explicit repair/block helpers. Those screens sharpened the target to "raw request or capability
gate before existence classification."

## Quality

Low severity, medium method value.

- User-visible rerunnable migration failure.
- Deterministic and minimal.
- No data corruption and no metadata duplication.
- The expected semantics are supported by TiDB's own sibling behavior: the same duplicate
  `ADD PARTITION IF NOT EXISTS` is a note/no-op when no DEFAULT gate intervenes.

## Selector lesson

This adds a fourth S15 sub-shape:

```text
idempotence flag + duplicate/existence catch exists + earlier capability gate decides on the
whole operation before classifying whether the requested object is already present
```

So the audit is no longer only:

```text
Did the flag survive dispatch?
Did a raw count precheck run before missing-name filtering?
```

It is now:

```text
List every precheck before the existence/duplicate catch.
For each precheck, ask whether it is meaningful for an already-existing requested object.
```

## Stop rule

Do not enumerate partition syntax. Reopen only for:

- another idempotent DDL where a capability/default gate runs before duplicate/missing
  classification;
- fix validation for `ADD PARTITION IF NOT EXISTS` on LIST DEFAULT tables;
- a stronger consequence than deterministic wrong-error.

