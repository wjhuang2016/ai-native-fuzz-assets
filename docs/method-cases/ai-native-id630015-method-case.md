# Method Case: id630015 DROP PARTITION IF EXISTS count precheck

## One-line result

`ALTER TABLE ... DROP PARTITION IF EXISTS px` returns `ERROR 1508 Cannot remove all partitions` on a
single-partition table even though `px` does not exist. The same missing-name statement on a
two-partition table produces a note and leaves the table unchanged.

## P/Q/D/F/O card

```text
P_check:
  CheckDropTablePartition first checks whether len(current partitions) <= len(requested names).

Q_claim:
  If that count check is true, the DDL would remove every existing partition.

D_dims:
  Requested names are not all existing names. Under IF EXISTS, missing names should be ignored or
  classified as notes before they are counted as removals.

F_effect:
  The function returns ErrDropLastPartition before the executor's IF EXISTS handler can turn a
  missing-name error into a note.

O_oracle:
  O18 idempotent DDL flag oracle:
  compare missing-name IF EXISTS behavior across partition counts, plus real-last-partition and
  real-existing-partition controls.
```

## Matrix

```text
one partition, missing name:
  ALTER TABLE onep DROP PARTITION IF EXISTS px
  actual: ERROR 1508 Cannot remove all partitions
  classification: RED

two partitions, one missing name:
  ALTER TABLE twop DROP PARTITION IF EXISTS px
  actual: Note 1507; p0,p1 remain
  classification: GREEN reference for missing-name idempotence

two partitions, one existing name:
  ALTER TABLE twop DROP PARTITION IF EXISTS p0
  actual: p0 is dropped, p1 remains
  classification: GREEN control

one partition, existing name:
  ALTER TABLE onep DROP PARTITION IF EXISTS p0
  actual: ERROR 1508
  classification: GREEN control for the real last-partition guard

two partitions, two missing names:
  ALTER TABLE twop DROP PARTITION IF EXISTS px, py
  actual: ERROR 1508
  classification: RED sibling, same root

two partitions, one existing plus one missing:
  ALTER TABLE twop DROP PARTITION IF EXISTS p0, px
  actual: ERROR 1508
  classification: RED sibling, same root
```

## Why this was fast

This did not come from enumerating `IF EXISTS` syntax. The source question was:

```text
Which DDL idempotence flag is implemented by catching a later existence error, while an earlier
precheck can return a different error before existence is known?
```

`DROP PARTITION IF EXISTS` matched that shape:

1. Parser stores `spec.IfExists`.
2. Executor catches `ErrDropPartitionNonExistent` only after `CheckDropTablePartition`.
3. The shared checker proves "all partitions would be removed" using requested-name count.
4. The missing-name oracle is already available from the two-partition control.

The matrix collapsed to a count boundary instead of a broad partition-DDL fuzz space.

## Quality

Low-to-medium wrong-error bug:

- user-visible idempotent DDL can fail in migrations;
- trigger is deterministic and minimal;
- source root cause is narrow;
- no data corruption and no wrong query result;
- product semantics for mixed existing/missing partition lists may need owner ruling, but the
  single missing-name inconsistency is strong because TiDB already implements note/no-op for the
  same missing-name statement on a larger table.

## Selector lesson

This sharpens S15. The flag can be present and still be ineffective if it only catches one error
class after a precheck. For idempotent DDL, audit the whole validation order:

```text
does any precheck use requested object count/name list
before proving which requested objects actually exist?
```

The reusable selector is not "try more IF EXISTS". It is:

```text
idempotence flag + later existence-error catch + earlier aggregate precheck over raw requested names
```

## Stop rule

Do not enumerate every partition syntax. Reopen only for:

- another idempotence flag whose earlier precheck uses raw requested names/counts;
- fix validation for `DROP PARTITION IF EXISTS`;
- a stronger consequence than deterministic wrong-error.

