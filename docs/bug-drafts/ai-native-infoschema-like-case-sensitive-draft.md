# information_schema LIKE predicate extraction is case-insensitive for binary TABLE_NAME

Status: confirmed on testbed 8192975

## Summary

`information_schema.tables.TABLE_NAME` and `information_schema.columns.TABLE_NAME` are exposed with `utf8mb4_bin` collation, so `LIKE` on those columns is case-sensitive. However, the InfoSchema predicate extractor lowers LIKE patterns and compiles case-insensitive regexps, then removes the original predicate. As a result, queries such as `table_name LIKE 'a_%'` can return an uppercase table name that does not satisfy the predicate.

## Minimal Repro

```sql
DROP DATABASE IF EXISTS aiis_000;
CREATE DATABASE aiis_000;
USE aiis_000;

CREATE TABLE `Acase` (id INT, c INT);
CREATE TABLE `a_b` (id INT, c INT);
CREATE TABLE `a%b` (id INT, c INT);

SHOW FULL COLUMNS FROM information_schema.tables LIKE 'TABLE_NAME';

SELECT table_name, table_name LIKE 'a_%' AS like_pred
FROM information_schema.tables
WHERE table_schema = 'aiis_000'
ORDER BY table_name;

SELECT GROUP_CONCAT(table_name ORDER BY table_name) AS normal
FROM information_schema.tables
WHERE table_schema = 'aiis_000'
  AND table_name LIKE 'a_%';

SELECT GROUP_CONCAT(table_name ORDER BY table_name) AS explicit_eval
FROM information_schema.tables
WHERE table_schema = 'aiis_000'
  AND table_name LIKE 'a_%'
  AND (table_name LIKE 'a_%') = 1;

SELECT GROUP_CONCAT(table_name ORDER BY table_name) AS case_wrapped
FROM information_schema.tables
WHERE table_schema = 'aiis_000'
  AND CASE WHEN table_name LIKE 'a_%' THEN 1 ELSE 0 END = 1;
```

Observed:

```text
TABLE_NAME collation: utf8mb4_bin

table_name  like_pred
Acase       0
a%b         1
a_b         1

normal:        Acase,a%b,a_b
explicit_eval: a%b,a_b
case_wrapped:  a%b,a_b
```

The same issue is visible through `information_schema.columns`:

```sql
SELECT GROUP_CONCAT(CONCAT(table_name, '.', column_name)
                    ORDER BY table_name, ordinal_position) AS normal
FROM information_schema.columns
WHERE table_schema = 'aiis_000'
  AND table_name LIKE 'a_%'
  AND column_name = 'id';

SELECT GROUP_CONCAT(CONCAT(table_name, '.', column_name)
                    ORDER BY table_name, ordinal_position) AS case_wrapped
FROM information_schema.columns
WHERE table_schema = 'aiis_000'
  AND CASE WHEN table_name LIKE 'a_%' THEN 1 ELSE 0 END = 1
  AND column_name = 'id';
```

Observed:

```text
normal:       Acase.id,a%b.id,a_b.id
case_wrapped: a%b.id,a_b.id
```

## Expected

The normal predicate result should match the actual `LIKE` evaluation for `TABLE_NAME`.

For `utf8mb4_bin`, `Acase LIKE 'a_%'` is false, so `Acase` must not be returned.

## Actual

The normal InfoSchema query returns `Acase` even though the same row evaluates `table_name LIKE 'a_%'` as `0`.

## Root Cause

Code anchors:

- `/Users/bba/pc/tidb/pkg/planner/core/memtable_infoschema_extractor.go:215`
- `/Users/bba/pc/tidb/pkg/planner/core/memtable_infoschema_extractor.go:224`
- `/Users/bba/pc/tidb/pkg/planner/core/memtable_infoschema_extractor.go:340`
- `/Users/bba/pc/tidb/pkg/executor/infoschema_reader.go:846`

`InfoSchemaBaseExtractor.Extract` calls `extractLikePatternCol(..., toLower=true, needLike2Regexp=true)` for pattern-matchable columns. It then compiles the regexp as `(?i)<pattern>`, making the prefilter case-insensitive, and removes the original predicate from `remained`.

For `information_schema.tables`, `HasTableName` is later evaluated against `t.TableName.L`, so `Acase` is treated as `acase` and passes `a_%`. Since the original binary-collation `LIKE` predicate has already been removed, the row is emitted.

## Fix Direction

Do not apply unconditional lower/case-insensitive extraction for InfoSchema columns whose SQL-visible collation is binary. Possible fixes:

- keep the original predicate in `remained` when the extractor cannot preserve collation semantics exactly;
- or make `extractLikePatternCol` / `filter` collation-aware and avoid `(?i)` plus `.L` for binary columns such as `TABLE_NAME`;
- add regression coverage for `information_schema.tables` and `information_schema.columns` with mixed-case table names and `LIKE` patterns.

