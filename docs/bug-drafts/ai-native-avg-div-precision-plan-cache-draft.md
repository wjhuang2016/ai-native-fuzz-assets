# Draft: prepared plan cache reuses AVG decimal scale after `div_precision_increment` changes
> 2026-07-03. Confirmed on testbed 8192975 / `fp-tidb`. Inserted into remote
> `found_bug` as id30036.

## Summary

Prepared plan cache can reuse the decimal scale inferred for `AVG()` after the session changes
`@@div_precision_increment`. Direct SQL follows the new precision setting, while the cached prepared
plan keeps the old aggregate return scale.

This is user-visible. Projection returns the old string representation, and a predicate over that
string can return `0` while direct SQL and the same prepared statement after plan-cache flush return
`1`.

## Minimal Repro

Setup and scalar contract:

```sql
DROP DATABASE IF EXISTS ai_avg_dpi_0703;
CREATE DATABASE ai_avg_dpi_0703;
USE ai_avg_dpi_0703;
CREATE TABLE t(x DECIMAL(10,0));
INSERT INTO t VALUES (1), (2);

SET @@div_precision_increment = 4;
SELECT AVG(x), CAST(AVG(x) AS CHAR) FROM t;
-- 1.5000, 1.5000

SET @@div_precision_increment = 8;
SELECT AVG(x), CAST(AVG(x) AS CHAR) FROM t;
-- 1.50000000, 1.50000000
```

Projection form:

```sql
SET tidb_enable_prepared_plan_cache = 1;

SET @@div_precision_increment = 4;
PREPARE s FROM 'SELECT AVG(x) AS avg_x, CAST(AVG(x) AS CHAR) AS avg_s FROM t';
EXECUTE s; -- 1.5000, 1.5000
SELECT @@last_plan_from_cache; -- 0

SET @@div_precision_increment = 8;
EXECUTE s; -- wrong: 1.5000, 1.5000
SELECT
  @@last_plan_from_cache,
  (SELECT CAST(AVG(x) AS CHAR) FROM t) AS direct_avg_s;
-- last_plan_from_cache = 1, direct_avg_s = 1.50000000

ADMIN FLUSH SESSION PLAN_CACHE;
EXECUTE s; -- 1.50000000, 1.50000000
SELECT @@last_plan_from_cache; -- 0

DEALLOCATE PREPARE s;
SET @@div_precision_increment = 4;
```

Row-filtering form:

```sql
SET tidb_enable_prepared_plan_cache = 1;
SET @@div_precision_increment = 4;

PREPARE q FROM
  'SELECT COUNT(*) AS cnt
     FROM (SELECT CAST(AVG(x) AS CHAR) AS avg_s FROM t) dt
    WHERE avg_s = ''1.50000000''';

EXECUTE q; -- cnt = 0
SELECT @@last_plan_from_cache; -- 0

SET @@div_precision_increment = 8;

EXECUTE q; -- wrong: cnt = 0
SELECT
  @@last_plan_from_cache,
  (SELECT COUNT(*)
     FROM (SELECT CAST(AVG(x) AS CHAR) AS avg_s FROM t) dt
    WHERE avg_s = '1.50000000') AS direct_cnt;
-- last_plan_from_cache = 1, direct_cnt = 1

ADMIN FLUSH SESSION PLAN_CACHE;
EXECUTE q; -- cnt = 1
SELECT @@last_plan_from_cache; -- 0
```

## Source Proof

- `pkg/expression/aggregation/base_func.go:127-139`: aggregate `TypeInfer` calls
  `typeInfer4Avg`.
- `pkg/expression/aggregation/base_func.go:274-285`: `typeInfer4Avg` reads
  `ctx.GetDivPrecisionIncrement()` and stores the result scale in `RetTp`.
- `pkg/expression/aggregation/avg.go:80-91`: decimal `AVG` evaluation divides with current
  `GetDivPrecisionIncrement()`, then rounds by `af.RetTp.GetDecimal()`.
- `pkg/planner/core/plan_cache_utils.go:390-449`: prepared plan-cache key includes SQL mode,
  timezone offset, charset/collation, read-only state, foreign-key checks, and related plan
  dimensions, but not `div_precision_increment`.

## Why This Is Distinct from id30035

id30035 is an all-constant scalar-expression fold: `1/7` becomes a cached `Constant`.

id30036 uses table rows and aggregate execution. The stale payload is the aggregate descriptor's
return type/scale (`RetTp.Decimal`), not a folded scalar value. Execution partially uses the current
session variable for decimal division, then rounds the result through old cached metadata.

## Method Value

This adds a new S7 payload class:

```text
hidden session input
+ build-time type/descriptor inference
+ cached plan reuses descriptor after session switch
+ execution output is rounded/rendered by cached metadata
= stale aggregate/type payload bug
```

The important search upgrade is to scan hidden-context getters by consumer and payload type:

- folded scalar values: id30034, id30035
- cached semantic trees or rewrites: id30024, id30025
- cached type/descriptor metadata: id30036

## Quality

Medium. It is a wrong-result issue under prepared plan cache. The projection mismatch is visible by
itself, and the derived-table `COUNT(*)` form shows a row-count difference under current-session
semantics.

## Stop Rule

Do not enumerate AVG argument types or decimal literals. Reopen this family only for another hidden
input, a different cached payload class, or fix validation.
