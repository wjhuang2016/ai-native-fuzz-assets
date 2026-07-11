# Draft: prepared plan cache ignores tidb_sysdate_is_now semantic switch (id30024)
> 2026-07-03. Confirmed on testbed 8192975 / fp-tidb. S7 refinement: cache keys must include semantic switches consumed during plan generation.

## Symptom

A prepared statement containing `sysdate()` keeps the old `tidb_sysdate_is_now` semantics when it hits prepared plan cache after the session variable changes.

Minimal reproduction:

```sql
SET tidb_enable_prepared_plan_cache=ON;
USE ai_pc_tz;

SET SESSION tidb_sysdate_is_now=0;
DROP TABLE IF EXISTS sys_t3;
CREATE TABLE sys_t3(a INT);
INSERT INTO sys_t3 VALUES (1);

PREPARE s FROM
  'SELECT sleep(a), now(6) AS n, sysdate(6) AS s,
          sysdate(6)=now(6) AS eq
     FROM sys_t3';

SELECT @@tidb_sysdate_is_now AS mode_before;
EXECUTE s;
SELECT @@last_plan_from_cache AS hit1;
EXECUTE s;
SELECT @@last_plan_from_cache AS hit2;

SET SESSION tidb_sysdate_is_now=1;
SELECT @@tidb_sysdate_is_now AS mode_after_toggle;
EXECUTE s;
SELECT @@last_plan_from_cache AS hit3_cached_after_toggle;

ADMIN FLUSH SESSION PLAN_CACHE;
EXECUTE s;
SELECT @@last_plan_from_cache AS hit4_after_flush_same_prepare;
```

Observed:

```text
mode_before = 0
first execute:  eq=0, hit1=0
second execute: eq=0, hit2=1

mode_after_toggle = 1
cached execute:   eq=0, hit3_cached_after_toggle=1
after cache flush: eq=1, hit4_after_flush_same_prepare=0
```

Reverse direction also reproduces:

```text
mode_before = 1
second execute: eq=1, hit2=1

mode_after_toggle = 0
cached execute:    eq=1, hit3_cached_after_toggle=1
after cache flush: eq=0, hit4_after_flush_same_prepare=0
```

So the user-visible behavior is not only a plan shape difference. The same prepared statement returns the old boolean result while `@@last_plan_from_cache=1`, and returns the current-session result after clearing the cache.

## Source Chain

- `pkg/expression/scalar_function.go:216-218`: when `ctx.GetSysdateIsNow()` is true, `sysdate` is rewritten to `now` during function construction.
- `pkg/expression/function_traits.go:153-157`: `sysdate` is treated as deferred only under `GetSysdateIsNow()`.
- `pkg/planner/core/plan_cache_utils.go:318-518`: `NewPlanCacheKey` includes many session dimensions, but not `vars.SysdateIsNow`.
- `pkg/planner/core/plan_cache.go:331-354`: on cache hit, TiDB reuses the cached plan after range rebuild; it does not rebuild scalar-function semantics unless the key misses.

## Root Cause

```text
P_check:
  same prepared SQL, same schema/version/key dimensions already included in NewPlanCacheKey

Q_claim:
  cached plan remains equivalent after session state changes

D_dim:
  semantic switch: tidb_sysdate_is_now changes function construction from SYSDATE to NOW

F_effect:
  prepared plan cache hit skips re-optimizing/rebuilding the scalar function tree
```

The missing proof is not only "are all optimizer knobs in the key?" It is:

```text
every session variable consumed while building expression semantics
must either be in the cache key or force cache miss/rebuild
```

`tidb_sysdate_is_now` fails that obligation.

## Expected Behavior

After changing `tidb_sysdate_is_now`, executing an existing prepared statement should follow the current session semantics. This is already what happens when prepared plan cache is off or when the session plan cache is flushed.

## Fix Direction

Include `vars.SysdateIsNow` in the prepared plan cache key, or mark prepared statements containing `sysdate()` as uncacheable when this switch can affect semantics. The narrower fix is preferable if `sysdate()` under a stable switch should remain cacheable.

## Methodology Note

This was found by refining S7 after the Apply-cache hit:

```text
cache key completeness
must include semantic switches consumed before the cached object is built
```

The efficient matrix was only two directions:

- OFF -> cache -> ON: cached result stays OFF semantics, flush result becomes ON semantics.
- ON -> cache -> OFF: cached result stays ON semantics, flush result becomes OFF semantics.

The crucial oracle is not timing precision. It is the boolean `sysdate(6)=now(6)` plus `@@last_plan_from_cache`, with `ADMIN FLUSH SESSION PLAN_CACHE` as the safe-path reference.
