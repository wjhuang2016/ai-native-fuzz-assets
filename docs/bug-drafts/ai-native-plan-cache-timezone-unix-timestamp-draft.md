# Draft: prepared plan cache reuses timezone-folded UNIX_TIMESTAMP literals (id30025)
> 2026-07-03. Confirmed on testbed 8192975 / fp-tidb. S7 refinement: coarse cache-key dimensions can be safe for rebuilt boundaries but unsafe for folded constants.

## Symptom

A prepared statement with `UNIX_TIMESTAMP()` over a literal DATETIME can keep the old named-timezone semantics after `time_zone` changes, if the two zones currently have the same offset but have different historical rules for the literal date.

Minimal reproduction:

```sql
SET tidb_enable_prepared_plan_cache=ON;
ADMIN FLUSH SESSION PLAN_CACHE;

SET time_zone='Africa/Johannesburg';
SELECT @@time_zone, UNIX_TIMESTAMP('2025-01-15 12:00:00');
-- 1736935200

SET time_zone='Europe/Amsterdam';
SELECT @@time_zone, UNIX_TIMESTAMP('2025-01-15 12:00:00');
-- 1736938800

SET time_zone='Africa/Johannesburg';
PREPARE s FROM "SELECT UNIX_TIMESTAMP('2025-01-15 12:00:00') AS u";
EXECUTE s;
SELECT @@last_plan_from_cache; -- 0
EXECUTE s;
SELECT @@last_plan_from_cache; -- 1

SET time_zone='Europe/Amsterdam';
SELECT UNIX_TIMESTAMP('2025-01-15 12:00:00') AS direct_after_toggle;
-- 1736938800

EXECUTE s;
SELECT @@last_plan_from_cache; -- 1
-- cached result: 1736935200

ADMIN FLUSH SESSION PLAN_CACHE;
EXECUTE s;
SELECT @@last_plan_from_cache; -- 0
-- flush reference: 1736938800
```

Reverse direction also reproduces:

```text
Europe/Amsterdam fill: 1736938800, hit=1
Switch to Africa/Johannesburg direct: 1736935200
Cached execute: 1736938800, hit=1
After flush: 1736935200, hit=0
```

Green control:

```text
2025-07-15 12:00:00 has the same historical offset in both zones.
Johannesburg -> Amsterdam cached result = direct result = flush result = 1752573600.
```

Boundary:

```text
PREPARE p FROM "SELECT UNIX_TIMESTAMP(?)";
EXECUTE p USING @x;
```

This parameterized form did not hit prepared plan cache in the probe (`@@last_plan_from_cache=0`), so the minimal confirmed surface is literal/constant-folded arguments.

## Source Chain

- `pkg/planner/core/plan_cache_utils.go:368-425`: `NewPlanCacheKey` stores only `time.Now().In(vars.TimeZone).Zone()` as `timezoneOffset`, not the location name or rule set.
- `pkg/expression/function_traits.go:147-168`: `UNIX_TIMESTAMP` is generally a deferred function under plan cache.
- `pkg/planner/core/expression_rewriter.go:3011-3016`: but `UNIX_TIMESTAMP` with arguments is explicitly handled as a normal expression, not a deferred expression.
- `pkg/expression/scalar_function.go:313-315`: `NewFunction` creates a scalar function with constant folding.
- `pkg/expression/builtin_time.go:4512-4564`: `builtinUnixTimestampIntSig` / `builtinUnixTimestampDecSig` convert the DATETIME argument using the session location.

## Root Cause

```text
P_check:
  same statement text and same current timezone offset

Q_claim:
  cached folded expression remains valid after time_zone changes

D_dim:
  named zones can share the current offset but differ for the literal's historical date

F_effect:
  prepared plan cache hit reuses the constant folded under the old zone
```

The previous timezone plan-cache calibration was green because range rebuild used the current session timezone after the hit. This case is different: the semantic boundary is folded into the cached expression, so there is no rebuild.

## Expected Behavior

After changing `time_zone`, `UNIX_TIMESTAMP('2025-01-15 12:00:00')` inside an existing prepared statement should match direct evaluation under the current session timezone, or the prepared plan cache should miss/rebuild when the named timezone changes in a way that can affect historical offsets.

## Fix Direction

Use a cache key dimension that distinguishes full `time_zone` location/rule semantics when cached expressions depend on historical offsets, or prevent constant folding/caching of timezone-dependent `UNIX_TIMESTAMP` literal expressions across `time_zone` changes.

## Methodology Note

This is not a contradiction of the earlier timezone green result. It improves the selector:

```text
coarse cache-key dimension omitted/full detail missing
  -> GREEN if hit path rebuilds the semantic boundary
  -> RED if hit path reuses a folded/evaluated value built under the old boundary
```

The tiny matrix was enough:

- historical-offset-different date: Johannesburg -> Amsterdam RED, Amsterdam -> Johannesburg RED;
- historical-offset-same date: GREEN;
- flush/off-cache reference: proves current-session semantics.
