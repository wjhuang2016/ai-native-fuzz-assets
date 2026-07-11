# Draft: InfoSchema scalar-pushdown extractor returns rows that fail LOWER/UPPER predicate (id30018)
> 2026-07-03. Confirmed on testbed 8192975 / fp-tidb. Selector S3 (shortcut/extractor lossy prefilter), new sub-shape: scalar-pushdown + value normalization.

## Minimal Reproduction

```sql
DROP DATABASE IF EXISTS ai_s3_funcpush;
CREATE DATABASE ai_s3_funcpush;
USE ai_s3_funcpush;
CREATE TABLE Acase(id INT);

SELECT table_name, LOWER(table_name) AS lowered,
       LOWER(table_name) = 'ACASE' AS self_ok
FROM information_schema.tables
WHERE table_schema='ai_s3_funcpush'
  AND LOWER(table_name) = 'ACASE';
-- Acase  acase  0

SELECT table_name, LOWER(table_name) AS lowered,
       LOWER(table_name) = 'ACASE' AS self_ok
FROM information_schema.tables
WHERE table_schema='ai_s3_funcpush'
  AND CASE WHEN LOWER(table_name) = 'ACASE' THEN TRUE ELSE FALSE END;
-- empty
```

The symmetric `UPPER` wrong-case constant shape is also red:

```sql
SELECT table_name, UPPER(table_name) AS uppered,
       UPPER(table_name) = 'acase' AS self_ok
FROM information_schema.tables
WHERE table_schema='ai_s3_funcpush'
  AND UPPER(table_name) = 'acase';
-- Acase  ACASE  0
```

Green control:

```sql
SELECT table_name, LOWER(table_name), LOWER(table_name) = 'acase'
FROM information_schema.tables
WHERE table_schema='ai_s3_funcpush'
  AND LOWER(table_name) = 'acase';
-- Acase  acase  1
```

## User-Visible Symptom

The query returns rows that fail its own visible predicate. This is not plan-only:

- fast query returns `Acase`;
- projected `LOWER(table_name) = 'ACASE'` on that returned row is `0`;
- CASE-wrapped reference returns no rows.

`EXPLAIN` shows the predicate was turned into a table-name prefilter and the scalar predicate disappeared:

```text
MemTableScan table:TABLES table_name:["acase"], table_schema:["ai_s3_funcpush"]
```

## Probe Result

Probe: `/Users/bba/pc/ai_native_infoschema_scalar_pushdown_case_probe.py`

```text
FINDING  infoschema_scalar_pushdown_case  LOWER(TABLE_NAME)='ACASE' fast path returned scalar-false rows: ['Acase\tacase\t0']; UPPER(TABLE_NAME)='acase' fast path returned scalar-false rows: ['Acase\tACASE\t0']
SUMMARY total=1 findings=1 skipped=0
```

## Source Chain

- `pkg/planner/core/memtable_infoschema_extractor.go:190-209`: InfoSchema object-name predicates call `extractCol(..., valueToLower=true)`.
- `pkg/planner/core/memtable_predicate_extractor.go:321-327`: `extractCol` enables scalar pushdown for equality and records `LOWER`/`UPPER` through `setColumnPushedDownFn`.
- `pkg/planner/core/memtable_predicate_extractor.go:333-349`: extracted constant values are merged with `valueToLower`, so `'ACASE'` becomes `'acase'`.
- `pkg/planner/core/memtable_infoschema_extractor.go:292-300`: `filter` checks `toLower` first; when true, it lowercases the row value and returns before consulting the pushed-down `LOWER`/`UPPER` function.

## Root Cause

The extractor conflates two different operations:

```text
P_check:
  LOWER/UPPER(TABLE_NAME) = const can be represented as an object-name prefilter

Q_claim:
  the prefilter is semantically equivalent to the original scalar predicate

F_effect:
  the original predicate is removed from remained and no scalar recheck runs
```

For object-name columns, `valueToLower=true` is correct for case-insensitive name lookup only when the extracted predicate is the plain object name equality. It is not correct for arbitrary scalar predicate equality such as `LOWER(TABLE_NAME) = 'ACASE'` or `UPPER(TABLE_NAME) = 'acase'`.

## Expected Behavior

The fast path should return the same rows as a scalar recheck:

- `LOWER(table_name) = 'ACASE'` should return no `Acase` row because `LOWER('Acase') = 'acase'`;
- `UPPER(table_name) = 'acase'` should return no `Acase` row because `UPPER('Acase') = 'ACASE'`.

## Fix Direction

Options:

- do not extract `LOWER/UPPER(object_name) = const` unless the transformed constant and comparison semantics are preserved exactly;
- or keep the original scalar predicate in `remained` after using it as a prefilter;
- or make `filter()` apply the pushed-down scalar function and compare against an unmodified extracted constant, instead of forcing the object-name lowercasing path.

Regression tests should assert both red cells and the lower-case green control.

## Methodology Note

This is S3, but not the same mechanism as the earlier LIKE bug. The selector improvement is:

```text
shortcut extractor supports scalar function pushdown
+ also has column-level value normalization
+ drops original predicate
+ CASE/self-predicate oracle exists
= high-signal target
```

The key oracle is row self-check: every returned row should satisfy the predicate projected in the SELECT list.
