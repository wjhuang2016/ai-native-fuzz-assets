# Partial TIMESTAMP index membership depends on writer session time zone

## Summary

A partial index whose condition compares a `TIMESTAMP` column with a string literal can persist
different membership for the same UTC instant when rows are written by sessions with different
`time_zone` values.

This can:

- allow two rows that both satisfy a partial unique constraint in an observer session;
- make full-table and index row sets disagree;
- make `ADMIN CHECK TABLE` fail with error 8223;
- make ordinary indexed `UPDATE` or `DELETE` silently miss matching rows.

The reproducer uses default strict mode, MDL enabled, one TiDB, one PD, and one real TiKV. It does
not need concurrency, retry, failpoints, source changes, or multiple TiDB nodes.

## Reproduction

```sql
DROP DATABASE IF EXISTS partial_tz_repro;
CREATE DATABASE partial_tz_repro;
USE partial_tz_repro;

CREATE TABLE t (
  id INT PRIMARY KEY,
  k INT,
  ts TIMESTAMP,
  UNIQUE INDEX uk(k) WHERE ts >= '2025-01-01 00:00:00'
);

SET time_zone='-08:00';
INSERT INTO t VALUES (1, 7, '2024-12-31 12:00:00');

SET time_zone='+08:00';
INSERT INTO t VALUES (2, 7, '2025-01-01 04:00:00');

SELECT id,k,ts
FROM t IGNORE INDEX(uk)
WHERE ts >= '2025-01-01 00:00:00' AND k=7
ORDER BY id;

SELECT id,k,ts
FROM t USE INDEX(uk)
WHERE ts >= '2025-01-01 00:00:00' AND k=7
ORDER BY id;

ADMIN CHECK TABLE t;

EXPLAIN FORMAT='brief'
DELETE FROM t
WHERE ts >= '2025-01-01 00:00:00' AND k=7;

DELETE FROM t
WHERE ts >= '2025-01-01 00:00:00' AND k=7;
SELECT ROW_COUNT();

SELECT id,k,ts,ts >= '2025-01-01 00:00:00' AS still_matches
FROM t;
```

The two inserted text values represent the same UTC instant:
`2024-12-31 20:00:00 UTC`.

## Actual result

- The full scan returns handles 1 and 2.
- The partial-index scan returns only handle 2.
- Two rows with `k=7` satisfy the partial unique predicate.
- `ADMIN CHECK TABLE` reports missing index handle 1.
- `DELETE` uses `Point_Get` on `uk`, reports one affected row, and succeeds.
- Handle 1 remains and still evaluates the `WHERE` predicate to true.

## Expected result

Partial-index membership for one stored value and one schema predicate must not depend on the
session that writes the row. The second logical unique member should be rejected, index and table
row sets should agree, and a successful matched `DELETE` should remove every preimage row.

## Source chain

- `pkg/table/tables/index.go` parses every partial-index condition with process-global
  `indexConditionECtx`.
- The context is initialized with a fixed SQL mode and UTC type context.
- `MeetPartialCondition` evaluates mutation `oldData` and `newData` through that expression.
- `pkg/table/tables/tables.go` persists the result as index-key presence or absence.
- A `TIMESTAMP` datum entering the mutation path carries a wall-clock representation shaped by the
  writer session.
- Planner implication later treats the matching SQL predicate as proof that the partial index is a
  complete source.

The fixed evaluator context does not make membership stable because the operand representation is
already session-dependent.

## Production trigger

A shared table has a partial index over a `TIMESTAMP` boundary. Separate application pools,
regional services, ETL jobs, or tenants set different session `time_zone` values. Their writes
cross the boundary in wall-clock form even when they represent the same instant. Later queries or
DML use the partial index under one of those time zones.

## Fix direction

Evaluate the schema-owned predicate over canonical TIMESTAMP values with frozen schema semantics.
The write path, DDL backfill/check path, and optimizer implication proof must use the same semantic
representation. Add a cross-session same-instant invariant covering insert, unique enforcement,
index/table equality, and DML closure.

## Severity

High. The result is durable record/index disagreement and silent wrong DML. The impact is critical
for affected tables, while the catalog severity remains high because it requires a TIMESTAMP
partial index and mixed session time zones.
