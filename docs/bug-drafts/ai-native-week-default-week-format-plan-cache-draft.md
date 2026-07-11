# Draft: prepared plan cache reuses WEEK() constants after `default_week_format` changes
> 2026-07-03. Confirmed on testbed 8192975 / `fp-tidb`. Inserted into remote
> `found_bug` as id30034.

## Summary

Prepared plan cache can reuse a constant-folded `WEEK(date)` result after the session changes
`@@default_week_format`. The same SQL executed directly follows the new session setting, while the
cached prepared plan keeps the old folded value.

This is a user-visible wrong-result. In a `WHERE` predicate it can filter out rows that should match.

## Minimal Repro

```sql
DROP DATABASE IF EXISTS ai_week_where_0703;
CREATE DATABASE ai_week_where_0703;
USE ai_week_where_0703;

CREATE TABLE t(id INT PRIMARY KEY);
INSERT INTO t VALUES (1), (2);

SET tidb_enable_prepared_plan_cache = 1;
SET @@default_week_format = 0;

PREPARE s FROM
  'SELECT COUNT(*) AS cnt FROM t WHERE WEEK(''2008-02-20'') = 8';

EXECUTE s; -- cnt = 0, expected under default_week_format=0
SELECT @@last_plan_from_cache; -- 0

SET @@default_week_format = 1;

EXECUTE s; -- wrong: cnt = 0
SELECT
  @@last_plan_from_cache,
  (SELECT COUNT(*) FROM t WHERE WEEK('2008-02-20') = 8) AS direct_cnt;
-- last_plan_from_cache = 1, direct_cnt = 2

ADMIN FLUSH SESSION PLAN_CACHE;
EXECUTE s; -- cnt = 2
SELECT @@last_plan_from_cache; -- 0

DEALLOCATE PREPARE s;
SET @@default_week_format = 0;
```

Scalar contract:

```sql
SET @@default_week_format = 0;
SELECT WEEK('2008-02-20'); -- 7

SET @@default_week_format = 1;
SELECT WEEK('2008-02-20'); -- 8
```

## Controls

- `ADMIN FLUSH SESSION PLAN_CACHE` makes the same prepared statement return `8` / `cnt=2`, proving
  the stale value is the cached payload.
- `SELECT WEEK(d) FROM t` over a date column follows the current `default_week_format` even when
  `@@last_plan_from_cache=1`, so the bug is not the runtime `WEEK` evaluator.
- `WEEK(date, 1)` is stable under cache hit because the semantic input is explicit in the SQL.
- `YEARWEEK(date)` without mode is a boundary sample, not a blast-radius hit: source fixes mode `0`
  and direct results do not follow `default_week_format`.

## Source Proof

- `pkg/expression/builtin_time.go:1493-1510`: `builtinWeekWithoutModeSig.evalInt` reads
  `ctx.GetDefaultWeekFormatMode()` when `WEEK()` has no explicit mode argument.
- `pkg/planner/core/plan_cache_utils.go:360-455`: prepared plan-cache key includes SQL mode,
  `EnableNoBackslashEscapesInLike`, timezone offset, charset/collation, select limit, and
  `ForeignKeyChecks`, but not `default_week_format`.
- `pkg/expression/constant_fold.go:230-253`: when all arguments are constant, constant folding
  evaluates the scalar function and returns an ordinary `Constant`.
- `pkg/expression/util.go:1720-1745`: the plan-cache over-optimization guard only treats
  `ParamMarker` / `DeferredExpr` as mutable constants; it does not model scalar functions with
  implicit session inputs.
- `pkg/expression/builtin_threadsafe_generated.go:2568-2570`: `builtinWeekWithoutModeSig` uses the
  generic shareability check over its explicit args, so the hidden `default_week_format` dependency
  is not represented there either.

## Method Value

This sharpens S7 from "is every cache key dimension present?" to "is the cached payload a pure
function of the key and all implicit semantic inputs?"

The efficient selector was:

```text
function has a hidden session/config input
+ all explicit SQL arguments are constants
+ constant folding produces a plain cached Constant
+ the plan-cache key omits the hidden input
+ direct-vs-prepared-plus-flush oracle exists
= high-value S7 target
```

The important negative calibrations from the same pass:

- `foreign_key_checks` x prepared DML is green: the key includes `ForeignKeyChecks`, FK trigger plans
  with `FKChecks` are not cloned into plan cache, and the live matrix followed the current setting.
- partial-index parameter eligibility is green for the sampled `a > ?` case: the partial-index plan
  stayed uncached and the direct/reference rowsets matched after parameter change.

## Quality

Medium. This is not metadata drift or plan-shape-only behavior: a prepared user query can return the
wrong row count after a session variable change. The repro is compact and deterministic, and the
flush control pinpoints the cache boundary.

## Stop Rule

Do not enumerate every date function. Reopen only for another hidden session input that constant
folding turns into a cached payload, or for fix validation. The next methodological move is to scan
for functions whose evaluation reads `EvalContext` / session vars not represented in plan-cache keys
or deferred-constant rules.
