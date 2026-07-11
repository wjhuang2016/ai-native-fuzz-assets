# Draft: prepared plan cache reuses division constants after `div_precision_increment` changes
> 2026-07-03. Confirmed on testbed 8192975 / `fp-tidb`. Inserted into remote
> `found_bug` as id30035.

## Summary

Prepared plan cache can reuse a constant-folded decimal division result after the session changes
`@@div_precision_increment`. Direct SQL follows the new precision setting, while the cached prepared
plan keeps the old folded value.

This is user-visible. A query can project the wrong value, and a `WHERE` predicate derived from the
string value of the division can return no rows while direct SQL returns matching rows.

## Minimal Repro

Projection form:

```sql
SET tidb_enable_prepared_plan_cache = 1;

SET @@div_precision_increment = 4;
PREPARE s FROM 'SELECT 1/7 AS v';
EXECUTE s; -- 0.1429
SELECT @@last_plan_from_cache; -- 0

SET @@div_precision_increment = 8;
EXECUTE s; -- wrong: 0.1429
SELECT @@last_plan_from_cache, 1/7 AS direct_v;
-- last_plan_from_cache = 1, direct_v = 0.14285714

ADMIN FLUSH SESSION PLAN_CACHE;
EXECUTE s; -- 0.14285714
SELECT @@last_plan_from_cache; -- 0

DEALLOCATE PREPARE s;
SET @@div_precision_increment = 4;
```

Row-filtering form:

```sql
DROP DATABASE IF EXISTS ai_div_where_0703;
CREATE DATABASE ai_div_where_0703;
USE ai_div_where_0703;
CREATE TABLE t(id INT PRIMARY KEY);
INSERT INTO t VALUES (1), (2);

SET tidb_enable_prepared_plan_cache = 1;
SET @@div_precision_increment = 4;

PREPARE q FROM
  'SELECT COUNT(*) AS cnt FROM t WHERE CAST(1/7 AS CHAR) = ''0.142857142''';

EXECUTE q; -- cnt = 0
SELECT @@last_plan_from_cache; -- 0

SET @@div_precision_increment = 8;

EXECUTE q; -- wrong: cnt = 0
SELECT
  @@last_plan_from_cache,
  (SELECT COUNT(*) FROM t WHERE CAST(1/7 AS CHAR) = '0.142857142') AS direct_cnt;
-- last_plan_from_cache = 1, direct_cnt = 2

ADMIN FLUSH SESSION PLAN_CACHE;
EXECUTE q; -- cnt = 2
SELECT @@last_plan_from_cache; -- 0
```

Scalar contract:

```sql
SET @@div_precision_increment = 4;
SELECT 1/7, CAST(1/7 AS CHAR); -- 0.1429,     0.142857

SET @@div_precision_increment = 8;
SELECT 1/7, CAST(1/7 AS CHAR); -- 0.14285714, 0.142857142
```

## Controls

- `ADMIN FLUSH SESSION PLAN_CACHE` makes the same prepared statement return the current-precision
  result, proving the stale value is the cached payload.
- `SELECT a/b FROM t` over table columns follows the current `div_precision_increment` even when
  `@@last_plan_from_cache=1`, so runtime decimal division itself is not stale.
- The bad shape is the all-constant expression that constant folding turns into a plain cached
  `Constant`.

## Source Proof

- `pkg/expression/builtin_arithmetic.go:745`: decimal division result type is built with
  `ctx.GetEvalCtx().GetDivPrecisionIncrement()`.
- `pkg/expression/builtin_arithmetic.go:810`: decimal division evaluation calls
  `types.DecimalDiv(..., ctx.GetDivPrecisionIncrement())`.
- `pkg/planner/core/plan_cache_utils.go:360-455`: prepared plan-cache key omits
  `div_precision_increment`.
- `pkg/expression/constant_fold.go:230-253`: all-constant scalar functions are evaluated and
  returned as ordinary `Constant`s unless they contain `ParamMarker` / `DeferredExpr`.
- `pkg/expression/util.go:1720-1745`: the plan-cache over-optimization guard only tracks mutable
  constants, not scalar functions with implicit session inputs.

## Method Value

This is the second S7 `implicit session input -> folded cached payload` hit after id30034. It proves
the selector is not date-function-specific:

```text
function reads EvalContext/session state
+ all visible SQL arguments are constants
+ the session input is absent from the plan-cache key
+ constant folding stores a plain Constant
+ cache-hit-vs-direct-plus-flush oracle exists
= high-value cache-payload target
```

The important next move is not enumerating all arithmetic functions. It is to scan the small set of
`EvalContext` / `BuildContext` getters that are not key dimensions and ask whether any all-constant
scalar expression can fold across them.

## Quality

Medium. The projection mismatch is visible by itself, and the `WHERE CAST(1/7 AS CHAR)=...` form
shows row-count wrong-result. The controls localize the root to folded constant payload reuse.

## Stop Rule

Do not enumerate decimal literals, division spellings, or every arithmetic function. Reopen only for
another hidden session/config input family, a different cache payload mechanism, or fix validation.
