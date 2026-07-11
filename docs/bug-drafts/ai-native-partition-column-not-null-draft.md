# id630023 Draft: NULL To NOT NULL Rejected On Partition Columns

## Summary

`ALTER TABLE ... MODIFY COLUMN` rejects changing a partition column from nullable to
`NOT NULL` even when:

- every existing row is non-NULL;
- the same final partitioned schema can be created directly;
- a non-partitioned table can perform the same `NULL -> NOT NULL` change; and
- the non-partitioned path correctly rejects the same change when NULL rows exist.

Remote `found_bug` row:

```text
id:        630023
title:     MODIFY COLUMN rejects adding NOT NULL to partition columns with no NULL rows
severity:  medium
category:  wrong-error
oracle:    O14_TARGET_TYPE_ACCEPTANCE_REFERENCE
method:    S10_DDL_VALIDATION_METRIC_MISMATCH
status:    confirmed
```

## Minimal Repro

```sql
DROP DATABASE IF EXISTS ai_s10_part_notnull;
CREATE DATABASE ai_s10_part_notnull;
USE ai_s10_part_notnull;

CREATE TABLE part_range(a INT NULL, b INT)
PARTITION BY RANGE(a) (
  PARTITION p0 VALUES LESS THAN (10),
  PARTITION p1 VALUES LESS THAN (MAXVALUE)
);

INSERT INTO part_range VALUES (1,1),(11,2);

ALTER TABLE part_range MODIFY COLUMN a INT NOT NULL;
```

Actual:

```text
ERROR 8200 (HY000): Unsupported modify column: can't change the partitioning column,
since it would require reorganize all partitions
```

`SHOW CREATE TABLE part_range` remains nullable.

The same failure reproduced for:

```sql
PARTITION BY LIST COLUMNS(a) (
  PARTITION p0 VALUES IN (1),
  PARTITION p1 VALUES IN (11)
)
```

and:

```sql
PARTITION BY KEY(a) PARTITIONS 2
```

and expression partitioning:

```sql
PARTITION BY RANGE (TO_DAYS(a)) (
  PARTITION p0 VALUES LESS THAN (TO_DAYS('2024-01-10')),
  PARTITION p1 VALUES LESS THAN (MAXVALUE)
)
```

## Reference And Controls

Direct target schemas succeed:

```sql
CREATE TABLE direct_range(a INT NOT NULL, b INT)
PARTITION BY RANGE(a) (
  PARTITION p0 VALUES LESS THAN (10),
  PARTITION p1 VALUES LESS THAN (MAXVALUE)
);
INSERT INTO direct_range VALUES (1,1),(11,2);
```

```sql
CREATE TABLE direct_expr(a DATETIME NOT NULL, b INT)
PARTITION BY RANGE (TO_DAYS(a)) (
  PARTITION p0 VALUES LESS THAN (TO_DAYS('2024-01-10')),
  PARTITION p1 VALUES LESS THAN (MAXVALUE)
);
INSERT INTO direct_expr VALUES ('2024-01-01 00:00:00',1),('2024-02-01 00:00:00',2);
```

The non-partitioned transition succeeds when there are no NULL rows:

```sql
CREATE TABLE nonpart(a INT NULL, b INT);
INSERT INTO nonpart VALUES (1,1),(11,2);
ALTER TABLE nonpart MODIFY COLUMN a INT NOT NULL;
SHOW CREATE TABLE nonpart; -- a int NOT NULL
```

The same non-partitioned transition rejects when NULL rows exist:

```sql
CREATE TABLE nonpart_null(a INT NULL, b INT);
INSERT INTO nonpart_null VALUES (NULL,1),(11,2);
ALTER TABLE nonpart_null MODIFY COLUMN a INT NOT NULL;
-- ERROR 1265 Data truncated for column 'a' at row 1
```

This proves the safe path already has the necessary data-fit oracle. The partition-column
validator rejects before reaching it.

## Source Anchor

`pkg/ddl/modify_column.go`:

- `GetModifiableColumnJob` builds the target `newCol`, applies options, then calls
  `preCheckPartitionModifiableColumn`.
- `checkPartitionColumnModifiable` compares eval type, flags, charset, and collation before
  target partition-definition validation and before the generic `checkForNullValue` path.
- `isAllowedPartitionColumnFlagChange` allows `NOT NULL -> NULL`, but rejects
  `NULL -> NOT NULL`.

Current tests in `pkg/ddl/tests/partition/modify_column_test.go` explicitly expect
`NULL -> NOT NULL` rejection for partition columns. The missing oracle in those tests is the
target-state comparison: direct `NOT NULL` partition schemas are valid, and the generic
non-partitioned `MODIFY COLUMN` path already distinguishes safe no-NULL data from unsafe NULL data.

## Fix Direction

For partition columns, allow `NULL -> NOT NULL` to pass the partition flag gate, then run the
existing NULL data check and partition-definition validation.

Keep rejecting when:

- any existing row has NULL;
- the target type/charset/collation changes partition placement semantics; or
- target partition definitions are invalid under the new column.

## Quality

Medium wrong-error.

This is not data corruption, but it blocks a common schema-hardening migration: adding `NOT NULL`
to an existing partition key after data cleanup. Its method value is high because it sharpened S10
from "type length metric mismatch" to "flag/nullability allowlist mismatch against target-state
and data-fit references."
