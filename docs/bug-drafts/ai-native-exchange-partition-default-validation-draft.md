# EXCHANGE PARTITION WITH VALIDATION fails on LIST DEFAULT partitions

## Status

Confirmed on testbed `8192975`; inserted into remote `found_bug` as id630025.

Remote state after insert: `COUNT(*)=68`, `COUNT(DISTINCT root_cause_id)=46`.

## User-visible symptom

`ALTER TABLE ... EXCHANGE PARTITION ... WITH TABLE ...` can fail with an internal SQL syntax
error when the target partition is a `LIST` or `LIST COLUMNS` `DEFAULT` partition, even when every
row in the standalone table belongs to that DEFAULT partition.

## Minimal repro

```sql
DROP DATABASE IF EXISTS ai_exchange_default2;
CREATE DATABASE ai_exchange_default2;
USE ai_exchange_default2;

CREATE TABLE pt(a INT) PARTITION BY LIST(a) (
  PARTITION p1 VALUES IN (1),
  PARTITION p2 VALUES IN (2),
  PARTITION pdef DEFAULT
);
CREATE TABLE nt(a INT);
INSERT INTO nt VALUES (3);

ALTER TABLE pt EXCHANGE PARTITION pdef WITH TABLE nt;
```

Actual:

```text
ERROR 1064 (42000): You have an error in your SQL syntax ... near ") limit 1"
```

## Expected

The statement should pass validation and exchange the table into `pdef`. Value `3` is not in the
explicit `LIST` partitions, so it routes to the DEFAULT partition.

## Evidence Matrix

```text
Direct target-state oracle:
  INSERT INTO pt_direct VALUES (3);
  SELECT * FROM pt_direct PARTITION(pdef);
  -> 3

Ordinary LIST exchange validation:
  pt_no_default p1 VALUES IN (1), nt_no_default contains 1
  ALTER TABLE pt_no_default EXCHANGE PARTITION p1 WITH TABLE nt_no_default
  -> succeeds

DEFAULT LIST exchange validation:
  pt_default pdef DEFAULT, nt_default contains 3
  ALTER TABLE pt_default EXCHANGE PARTITION pdef WITH TABLE nt_default
  -> ERROR 1064 near ") limit 1"

WITHOUT VALIDATION boundary:
  same legal row 3
  ALTER TABLE pt_default_wo EXCHANGE PARTITION pdef WITH TABLE nt_default_wo WITHOUT VALIDATION
  -> succeeds and pdef contains 3
```

The same syntax-error shape also reproduces for `LIST COLUMNS(... )` with `PARTITION pdef DEFAULT`.

## Root Cause

`checkExchangePartitionRecordValidation` validates the standalone table by building a restricted
SQL query. For `LIST` partitioning it calls:

```text
pkg/ddl/partition.go:4534 buildCheckSQLConditionForListPartition
pkg/ddl/partition.go:4554 buildCheckSQLConditionForListColumnsPartition
```

Both builders iterate only `pi.Definitions[index].InValues` and contain TODOs for DEFAULT
partitions. For a DEFAULT partition there are no ordinary `InValues`, so the generated predicate
is effectively:

```sql
not () limit 1
```

The restricted SQL parser fails before TiDB can decide whether the rows match the DEFAULT
partition.

## Fix Direction

Generate DEFAULT-partition validation as the complement of all explicit `LIST` values, for both
`LIST(expr)` and `LIST COLUMNS(...)`, or route validation through the partition locator instead of
hand-building a SQL predicate. Preserve `ErrRowDoesNotMatchPartition` for rows that belong to
explicit partitions.

## Method Lesson

This is a new low-severity root, not blast radius of id630016.

id630016 was `IF NOT EXISTS` duplicate classification after a DEFAULT capability gate. This bug is
the exchange validation safe path itself losing a semantic dimension while generating internal SQL.
The reusable selector is:

```text
DDL safe-path validation builds an internal SQL predicate
+ source TODO / shape shows a partition/operator semantic dimension is omitted
+ direct target-state oracle proves the row belongs
+ boundary path without that safe-path succeeds
= wrong-error or, in other builders, possible wrong-acceptance
```

Stop rule: do not enumerate partition syntaxes. Reopen only for another internal validation SQL
builder that omits a different semantic dimension, a wrong-acceptance/data-placement consequence,
or fix validation.
