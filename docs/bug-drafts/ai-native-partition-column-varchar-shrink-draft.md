# id630003 Draft: Safe VARCHAR Shrink Rejected On Partition Columns

## Summary

`ALTER TABLE ... MODIFY COLUMN` rejects shrinking a `VARCHAR` partition column even when:

- every existing row fits the target length;
- every partition literal/bound fits the target length;
- the same final partitioned schema can be created directly;
- a non-partitioned table can perform the same shrink; and
- partition-column widening succeeds as a checker-aligned control.

Remote `found_bug` row:

```text
id:        630003
title:     MODIFY COLUMN rejects safe VARCHAR shrink on partition columns
severity:  medium
category:  wrong-error
oracle:    O14_TARGET_TYPE_ACCEPTANCE_REFERENCE
method:    S10_DDL_VALIDATION_METRIC_MISMATCH
status:    confirmed
```

## Minimal Repro

```sql
DROP DATABASE IF EXISTS ai_part_mod_s10;
CREATE DATABASE ai_part_mod_s10;
USE ai_part_mod_s10;

CREATE TABLE t_list(a varchar(6), b int)
PARTITION BY LIST COLUMNS(a) (
  PARTITION p0 VALUES IN ('abc'),
  PARTITION p1 VALUES IN ('xyz')
);

INSERT INTO t_list VALUES ('abc',1),('xyz',2);

ALTER TABLE t_list MODIFY COLUMN a varchar(5);
```

Actual:

```text
ERROR 8200 (HY000): Unsupported modify column: can't change the partitioning column,
since it would require reorganize all partitions
```

`SHOW CREATE TABLE t_list` remains at `a varchar(6)`.

The same failure reproduced for:

```sql
PARTITION BY RANGE COLUMNS(a) (
  PARTITION p0 VALUES LESS THAN ('m'),
  PARTITION p1 VALUES LESS THAN (MAXVALUE)
)
```

and:

```sql
PARTITION BY KEY(a) PARTITIONS 3
```

## Reference And Controls

Direct target schema succeeds:

```sql
CREATE TABLE direct_list_v5(a varchar(5), b int)
PARTITION BY LIST COLUMNS(a) (
  PARTITION p0 VALUES IN ('abc'),
  PARTITION p1 VALUES IN ('xyz')
);
INSERT INTO direct_list_v5 VALUES ('abc',1),('xyz',2);
```

Non-partitioned shrink succeeds:

```sql
CREATE TABLE t_nonpart(a varchar(6), b int);
INSERT INTO t_nonpart VALUES ('abc',1),('xyz',2);
ALTER TABLE t_nonpart MODIFY COLUMN a varchar(5);
```

Partition-column widening succeeds:

```sql
CREATE TABLE t_list_ext(a varchar(6), b int)
PARTITION BY LIST COLUMNS(a) (
  PARTITION p0 VALUES IN ('abc'),
  PARTITION p1 VALUES IN ('xyz')
);
INSERT INTO t_list_ext VALUES ('abc',1),('xyz',2);
ALTER TABLE t_list_ext MODIFY COLUMN a varchar(7);
```

KEY partition old/new direct tables had identical sampled partition membership for fitting values:

```text
varchar(6) KEY(a) partitions 4:
p1: ccc:3
p2: bb:2,dddd:4
p3: a:1,中:5,中中:6

varchar(5) KEY(a) partitions 4:
p1: ccc:3
p2: bb:2,dddd:4
p3: a:1,中:5,中中:6
```

This does not prove all KEY partition changes are safe, but it is a useful guard against the
claim that length shrink inherently requires repartitioning for fitting values.

## Source Anchor

`pkg/ddl/modify_column.go`:

- `checkPartitionColumnModifiable` first checks target column type/charset/collation and then calls `checkPartitionColumnTypeChangeAllowlist`.
- `checkPartitionColumnTypeChangeAllowlist` allows `KEY`, `RANGE COLUMNS`, and `LIST COLUMNS` string changes only through `isStringLengthExtension`.
- `isStringLengthExtension` requires `newCol.GetFlen() > col.GetFlen()`, so fitting shrink is rejected before the later target partition-definition validation can run.
- The generic `MODIFY COLUMN` path has a data-fit precheck for shrink; the partition-column validator blocks before using that contract.

Existing test coverage contains a rejection case for `varchar(6)->varchar(5)` where partition literals and rows are length 6. That is a valid reject. The missing cell is a shrink where literals and existing rows fit the target length.

## Fix Direction

For `VARCHAR` partition columns, separate:

- unsafe shrink: at least one partition literal/bound or existing value exceeds the target length;
- safe shrink: target partition definitions are valid and all existing values fit.

A fix should reuse target partition-definition validation plus the generic value-fit check instead
of treating all shrink as requiring repartition. Keep binary string controls separate because
binary lengths are byte-based.

## Quality

Medium.

This is user-visible and reproducible on the testbed, with a strong direct-target oracle, but it is
a wrong-error rather than data loss. Its main methodology value is that the same S10 selector found
a different DDL validator owner after FK length validation was paused.
