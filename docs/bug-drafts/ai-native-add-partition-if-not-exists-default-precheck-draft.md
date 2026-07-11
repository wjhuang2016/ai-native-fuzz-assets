# ADD PARTITION IF NOT EXISTS can still error on existing partition with LIST DEFAULT

## Status

Confirmed on testbed `8192975`; inserted into remote `found_bug` as id630016.

## User-visible symptom

A rerunnable migration that uses `ADD PARTITION IF NOT EXISTS` can fail even though the requested
partition already exists. The failure appears when the target table is LIST-partitioned and already
contains a DEFAULT partition.

## Minimal repro

```sql
DROP DATABASE IF EXISTS ai_s15_add_part_0703;
CREATE DATABASE ai_s15_add_part_0703;
USE ai_s15_add_part_0703;

CREATE TABLE l_default_dup(a INT) PARTITION BY LIST(a) (
  PARTITION p0 VALUES IN (1),
  PARTITION pdef DEFAULT
);

ALTER TABLE l_default_dup
  ADD PARTITION IF NOT EXISTS (PARTITION p0 VALUES IN (2));
```

Actual:

```text
ERROR 8200 (HY000): Unsupported ADD List partition, already contains DEFAULT partition.
Please use REORGANIZE PARTITION instead
```

Control without DEFAULT:

```sql
CREATE TABLE l_no_default(a INT) PARTITION BY LIST(a) (
  PARTITION p0 VALUES IN (1),
  PARTITION p1 VALUES IN (2)
);

ALTER TABLE l_no_default
  ADD PARTITION IF NOT EXISTS (PARTITION p0 VALUES IN (3));
SHOW WARNINGS;
```

Actual control:

```text
Note 1517 Duplicate partition name p0
```

## Expected

Because `p0` already exists and `IF NOT EXISTS` is present, the statement should be treated as an
idempotent no-op with a duplicate-partition note. A genuinely new partition on a LIST table with
DEFAULT should still fail with ERROR 8200.

## Root cause

`AddTablePartitions` checks whether the current LIST table already has a DEFAULT partition before
it combines old and new partition definitions and before it reaches the duplicate-name catch:

```text
pkg/ddl/executor.go:2300-2304
  if LIST and current table has DEFAULT:
    return ERROR 8200

pkg/ddl/executor.go:2307-2318
  combine old+new definitions
  checkPartitionDefinitionConstraints(...)
  if ErrSameNamePartition && spec.IfNotExists:
    append note and return nil
```

The duplicate-name classifier exists, but the DEFAULT capability gate runs first.

## Fix direction

When `IF NOT EXISTS` is present, classify requested partition names against existing partition
names before the LIST DEFAULT capability gate, or make that gate ignore requests that are already
known to be duplicates. Preserve the current ERROR 8200 behavior for genuinely new partitions on
LIST tables with DEFAULT.

## Method lesson

This is S15 again, but with a sharper selector:

```text
idempotence flag + duplicate/existence catch exists + earlier capability gate returns before
the catch can classify an already-existing requested object
```

