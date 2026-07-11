# id630015 Draft: DROP PARTITION IF EXISTS Can Error Before Checking Missing Names

Remote `found_bug` row:

```text
id:        630015
title:     DROP PARTITION IF EXISTS can still error when missing names look like all partitions
severity:  low
category:  wrong-error
oracle:    O18_IDEMPOTENT_DDL_FLAG_ORACLE
method:    S15_DDL_IDEMPOTENCE_PRECHECK_ORDERING
status:    confirmed; inserted into remote found_bug (`MAX(id)=630015,COUNT=50`)
```

## Summary

`ALTER TABLE ... DROP PARTITION IF EXISTS ...` normally converts a missing partition name into a
note and leaves the table unchanged. But if the number of requested partition names is greater than
or equal to the number of current partitions, TiDB checks "would this remove all partitions" before
it checks whether those names actually exist. A missing-name idempotent DDL can therefore fail with
`ERROR 1508 Cannot remove all partitions`.

The proof obligation is:

```text
P_check:  len(requested partition names) < len(current partitions)
Q_claim:  the DDL does not remove all existing partitions
D_dim:    only existing requested names count as partitions to remove; missing names under
          IF EXISTS should not contribute to the removal count
F_effect: CheckDropTablePartition returns ErrDropLastPartition before the IF EXISTS missing-name
          handler can convert ErrDropPartitionNonExistent into a note
```

## Minimal Repro

Confirmed on testbed `8192975` / `fp-tidb`:

```sql
DROP DATABASE IF EXISTS ai_drop_part_one_0703;
CREATE DATABASE ai_drop_part_one_0703;
USE ai_drop_part_one_0703;

CREATE TABLE onep(id INT PRIMARY KEY)
PARTITION BY RANGE(id) (
  PARTITION p0 VALUES LESS THAN (10)
);

ALTER TABLE onep DROP PARTITION IF EXISTS px;
```

Actual:

```text
ERROR 1508 (HY000): Cannot remove all partitions, use DROP TABLE instead
```

Expected:

```text
Note 1507: Error in list of partitions to DROP
```

and `onep` should remain unchanged, matching the two-partition missing-name control below.

## Controls

Missing single name on a two-partition table already follows idempotent semantics:

```sql
DROP DATABASE IF EXISTS ai_drop_part_two_0703;
CREATE DATABASE ai_drop_part_two_0703;
USE ai_drop_part_two_0703;

CREATE TABLE twop(id INT PRIMARY KEY)
PARTITION BY RANGE(id) (
  PARTITION p0 VALUES LESS THAN (10),
  PARTITION p1 VALUES LESS THAN (20)
);

ALTER TABLE twop DROP PARTITION IF EXISTS px;
SHOW WARNINGS;
SELECT partition_name
FROM information_schema.partitions
WHERE table_schema=DATABASE() AND table_name='twop'
ORDER BY partition_ordinal_position;
```

Observed:

```text
Note 1507 Error in list of partitions to DROP
p0
p1
```

Dropping a real partition from a two-partition table is still valid:

```sql
CREATE TABLE twop(id INT PRIMARY KEY)
PARTITION BY RANGE(id) (
  PARTITION p0 VALUES LESS THAN (10),
  PARTITION p1 VALUES LESS THAN (20)
);

ALTER TABLE twop DROP PARTITION IF EXISTS p0;
```

Observed final partitions:

```text
p1
```

Dropping the only real partition should still fail:

```sql
CREATE TABLE onep(id INT PRIMARY KEY)
PARTITION BY RANGE(id) (
  PARTITION p0 VALUES LESS THAN (10)
);

ALTER TABLE onep DROP PARTITION IF EXISTS p0;
```

Observed:

```text
ERROR 1508 (HY000): Cannot remove all partitions, use DROP TABLE instead
```

This is the green control for the `ErrDropLastPartition` guard itself.

## Blast Shape

The same root shows up whenever requested-name count reaches current partition count before
missing-name filtering:

```sql
CREATE TABLE twop(id INT PRIMARY KEY)
PARTITION BY RANGE(id) (
  PARTITION p0 VALUES LESS THAN (10),
  PARTITION p1 VALUES LESS THAN (20)
);

ALTER TABLE twop DROP PARTITION IF EXISTS px, py;
-- ERROR 1508

ALTER TABLE twop DROP PARTITION IF EXISTS p0, px;
-- ERROR 1508
```

By contrast, on a three-partition table the mixed request does not trigger the count precheck:

```sql
CREATE TABLE threep(id INT PRIMARY KEY)
PARTITION BY RANGE(id) (
  PARTITION p0 VALUES LESS THAN (10),
  PARTITION p1 VALUES LESS THAN (20),
  PARTITION p2 VALUES LESS THAN (30)
);

ALTER TABLE threep DROP PARTITION IF EXISTS p0, px;
SHOW WARNINGS;
```

Observed:

```text
Note 1507 Error in list of partitions to DROP
```

and the table remains unchanged. This is useful calibration: current TiDB treats any missing name
in the list as a no-op note before job submission, but the red cells bypass that missing-name path.

## Source Anchors

- `/Users/bba/pc/tidb/pkg/parser/parser.y`: parser accepts
  `DROP PARTITION IfExists PartitionNameList` and stores `spec.IfExists`.
- `/Users/bba/pc/tidb/pkg/ddl/executor.go:2956-2961`: `DropTablePartition` calls
  `CheckDropTablePartition`, then converts only `ErrDropPartitionNonExistent` into a note when
  `spec.IfExists` is true.
- `/Users/bba/pc/tidb/pkg/ddl/partition.go:2027-2043`: `CheckDropTablePartition` checks
  `len(oldDefs) <= len(partLowerNames)` and returns `ErrDropLastPartition` before checking whether
  each requested name exists.
- `/Users/bba/pc/tidb/pkg/ddl/db_change_test.go:1508-1528`: existing IF EXISTS DDL coverage tests
  a three-partition table dropping existing `p1`, but not missing names or the count boundary.

## User-Visible Symptom

An idempotent migration such as `ALTER TABLE t DROP PARTITION IF EXISTS old_partition` can fail on
a table with one partition if the partition has already been removed or never existed. The same
missing-name DDL succeeds as a note on a two-partition table, so behavior depends on current
partition count rather than on the requested operation's real effect.

## Fix Direction

For `IF EXISTS`, compute the set of existing requested partitions before applying the "cannot remove
all partitions" guard. Missing names should not contribute to the removal count. A conservative
implementation can keep ordinary non-`IF EXISTS` error ordering unchanged, while the flagged path
filters or classifies missing names first.
