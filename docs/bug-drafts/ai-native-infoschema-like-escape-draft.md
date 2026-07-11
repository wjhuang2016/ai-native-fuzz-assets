# information_schema LIKE with custom ESCAPE can return rows that fail the predicate

## Status

- `found_bug id30031`
- Status: `confirmed`
- Severity: `medium`
- Oracle: `O4_CASE_SELF_PREDICATE`
- Method: `S3_OPERATOR_SEMANTIC_ARITY`
- Testbed: `8192975`, `fp-tidb` via local port `14000`

## User-visible symptom

A normal `information_schema.tables` query can return a table name that evaluates the same
`LIKE ... ESCAPE` predicate as false, and omit the table name that evaluates it as true.

Repro setup:

```sql
DROP DATABASE IF EXISTS ai_show_like_escape_0703;
CREATE DATABASE ai_show_like_escape_0703;
USE ai_show_like_escape_0703;
CREATE TABLE `abc_def`(a INT);
CREATE TABLE `abc#x`(a INT);
CREATE TABLE `plain`(a INT);
```

SQL contract control:

```sql
SELECT
  'abc_def' LIKE '%#_%' ESCAPE '#' AS underscore_custom,
  'abc#x' LIKE '%#_%' ESCAPE '#' AS hash_custom,
  'abc#x' LIKE '%#_%' AS hash_default;
```

Result:

```text
underscore_custom  hash_custom  hash_default
1                  0            1
```

Fast arm:

```sql
SELECT table_name, table_name LIKE '%#_%' ESCAPE '#' AS self_true
FROM information_schema.tables
WHERE table_schema = DATABASE()
  AND table_name LIKE '%#_%' ESCAPE '#'
ORDER BY table_name;
```

Observed result:

```text
table_name  self_true
abc#x       0
```

Scalar reference:

```sql
SELECT table_name, table_name LIKE '%#_%' ESCAPE '#' AS self_true
FROM information_schema.tables
WHERE table_schema = DATABASE()
  AND table_name LIKE '%'
  AND (CASE WHEN table_name LIKE '%#_%' ESCAPE '#' THEN 1 ELSE 0 END) = 1
ORDER BY table_name;
```

Observed result:

```text
table_name  self_true
abc_def     1
```

Default-escape control stayed green:

```text
fast_default  abc#x:1
ref_default   abc#x:1
```

## Trigger evidence

Fast `EXPLAIN FORMAT='brief'`:

```text
MemTableScan table:TABLES table_schema:["ai_show_like_escape_0703"], table_name_pattern:[%#_%]
Projection Column#3, like(Column#3, %#_%, 35)
```

The predicate was consumed into `table_name_pattern`, while the projected `self_true` is `0`.

Reference `EXPLAIN FORMAT='brief'`:

```text
Selection eq(case(like(Column#3, "%#_%", 35), 1, 0), 1)
MemTableScan table:TABLES table_schema:["ai_show_like_escape_0703"], table_name_pattern:[%]
```

The CASE arm keeps scalar evaluation and returns the correct row.

## Source chain

- `/Users/bba/pc/tidb/pkg/planner/core/memtable_infoschema_extractor.go:215-235`
  extracts `LIKE` patterns for pattern-matchable InfoSchema columns, compiles regexps, and
  removes the original predicate from `remained`.
- `/Users/bba/pc/tidb/pkg/planner/core/memtable_infoschema_extractor.go:277-285`
  filters rows by the compiled regexps.
- `/Users/bba/pc/tidb/pkg/planner/core/memtable_predicate_extractor.go:439-465`
  extracts only the pattern string for `LIKE`; it does not preserve the third `ESCAPE` argument.
- `/Users/bba/pc/tidb/pkg/util/stringutil/string_util.go:260-263`
  compiles the pattern with the default backslash escape.

## Fix direction

The InfoSchema extractor should either preserve the actual `LIKE` escape character when compiling
its regexp, or keep scalar recheck for non-default `ESCAPE`.

Fix validation should include:

- `information_schema.tables` custom escape: fast/reference both return `abc_def`;
- default escape: fast/reference both return `abc#x`;
- projected self-predicate is true for every fast-row;
- at least one second pattern-matchable InfoSchema column as a regression guard, but no broad
  enumeration is needed.

## Method boundary

This is a representative cross-owner blast-radius case for the id30030 operator-semantic-arity
lesson. It proves the same missing `ESCAPE` dimension exists in `InfoSchemaBaseExtractor`, not
just `cluster_log`.

Do not keep filing one issue per pattern-matchable InfoSchema table/column unless a different
replacement mechanism or a different omitted semantic input is found.
