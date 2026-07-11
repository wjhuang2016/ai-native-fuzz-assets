# Draft: Apply cache reuses volatile scalar subquery results for duplicate correlated keys (id30020)
> 2026-07-03. Confirmed on testbed 8192975 / fp-tidb. New selector S7: cache payload purity. This is a different shortcut mechanism from the `extractCol(..., valueToLower=true)` helper family.

## Minimal Reproduction

```sql
DROP DATABASE IF EXISTS ai_apply_cache_probe;
CREATE DATABASE ai_apply_cache_probe;
USE ai_apply_cache_probe;

SET tidb_enable_parallel_apply = 1;
SET tidb_executor_concurrency = 1;
SET tidb_mem_quota_apply_cache = 33554432;

CREATE TABLE outer_t(id INT PRIMARY KEY, a INT, KEY(a));
CREATE TABLE inner_t(a INT, KEY(a));

-- Many duplicate correlated keys so Apply cache is selected.
INSERT INTO outer_t VALUES
  (1,1),(2,1),(3,1),(4,1),(5,1),(6,1),(7,1),(8,1),
  (9,1),(10,1),(11,1),(12,1),(13,1),(14,1),(15,1),(16,1),
  (17,1),(18,1),(19,1),(20,1),(21,1),(22,1),(23,1),(24,1),
  (25,2),(26,2),(27,2),(28,2),(29,2),(30,2),(31,2),(32,2),
  (33,2),(34,2),(35,2),(36,2),(37,2),(38,2),(39,2),(40,2);
INSERT INTO inner_t VALUES (1),(2);
ANALYZE TABLE outer_t, inner_t;

EXPLAIN ANALYZE
SELECT id, a,
       (SELECT UUID() FROM inner_t WHERE inner_t.a = outer_t.a LIMIT 1) AS u
FROM outer_t
ORDER BY id;
-- Apply ... cache:ON

SELECT a, COUNT(*) AS n, COUNT(DISTINCT u) AS distinct_u
FROM (
  SELECT id, a,
         (SELECT UUID() FROM inner_t WHERE inner_t.a = outer_t.a LIMIT 1) AS u
  FROM outer_t
) AS x
GROUP BY a
ORDER BY a;
-- a=1  n=24  distinct_u=1
-- a=2  n=16  distinct_u=1

SET tidb_mem_quota_apply_cache = 0;

EXPLAIN ANALYZE
SELECT id, a,
       (SELECT UUID() FROM inner_t WHERE inner_t.a = outer_t.a LIMIT 1) AS u
FROM outer_t
ORDER BY id;
-- Apply ... cache:OFF

SELECT a, COUNT(*) AS n, COUNT(DISTINCT u) AS distinct_u
FROM (
  SELECT id, a,
         (SELECT UUID() FROM inner_t WHERE inner_t.a = outer_t.a LIMIT 1) AS u
  FROM outer_t
) AS x
GROUP BY a
ORDER BY a;
-- a=1  n=24  distinct_u=24
-- a=2  n=16  distinct_u=16
```

Deterministic green control:

```sql
SELECT a, COUNT(*) AS n, COUNT(DISTINCT v) AS distinct_v
FROM (
  SELECT id, a,
         (SELECT CONCAT('v', inner_t.a)
          FROM inner_t
          WHERE inner_t.a = outer_t.a
          LIMIT 1) AS v
  FROM outer_t
) AS x
GROUP BY a
ORDER BY a;
-- distinct_v=1 per key, both cache ON and cache OFF
```

## User-Visible Symptom

A normal user-table query returns repeated UUID values for different outer rows that share the same correlated key when Apply cache is enabled. Disabling Apply cache changes only the execution shortcut and restores one UUID per outer-row subquery execution.

This is not a plan-only symptom:

- `cache:ON` returns `COUNT(DISTINCT UUID())=1` for 24 and 16 outer rows;
- `cache:OFF` returns `COUNT(DISTINCT UUID())=24` and `16` for the same data;
- a deterministic inner expression stays stable in both modes, so the oracle is not claiming Apply cache is broadly wrong.

## Probe Result

Probe: `/Users/bba/pc/ai_native_apply_cache_volatile_probe.py`

```text
FINDING  apply_cache_volatile  Apply cache ON reused UUID() results for duplicate correlated keys: uuid_on={1: (24, 1), 2: (16, 1)}; uuid_off={1: (24, 24), 2: (16, 16)}; det_on={1: (24, 1), 2: (16, 1)}; det_off={1: (24, 1), 2: (16, 1)}
SUMMARY total=1 findings=1 skipped=0
```

## Source Chain

- `pkg/planner/core/exhaust_physical_plans.go:2278-2288`: planner estimates cache hit ratio from correlated-column NDV and enables Apply cache when the ratio is high enough and `tidb_mem_quota_apply_cache > 0`.
- `pkg/executor/parallel_apply.go:631-647`: executor builds the cache key from correlated outer-column values.
- `pkg/executor/parallel_apply.go:650-653`: a cache hit returns the cached `innerList` and does not reopen the inner executor.
- `pkg/executor/parallel_apply.go:657-714`: a cache miss opens the inner executor, evaluates the inner expression/filter, and stores the resulting `chunk.List`.
- `pkg/executor/internal/applycache/apply_cache.go:26-28`: Apply cache is intended to reuse inner rows for the same outer-row value.
- `pkg/expression/builtin_miscellaneous.go:1524-1530` and `pkg/expression/builtin_miscellaneous_vec.go:213-224`: `UUID()` generates a new UUID during evaluation.
- `pkg/expression/function_traits.go:49-55` and `pkg/expression/util.go:1502-1515`: TiDB already has metadata/helpers for non-foldable/non-deterministic functions, including UUID.

## Root Cause

```text
P_check:
  the correlated outer values are the same, and repeated keys make Apply cache profitable

Q_claim:
  the inner subquery result is the same for every outer row with the same correlated values

D_dim:
  payload purity / volatility. The inner result may depend on non-deterministic expression
  evaluation, not only on correlated-column values.

F_effect:
  on cache hit, the executor reuses the cached chunk.List and skips inner re-evaluation
```

The hidden proof obligation is stronger than key equality. The cached payload must be a pure function of the cache key. `UUID()` makes `P` true but `Q` false.

## Expected Behavior

For volatile inner expressions, enabling Apply cache must not change the number of evaluations or the visible result distribution. The cache may still be used for deterministic inner rowsets and deterministic projections.

## Fix Direction

Before setting `canUseCache=true`, reject Apply cache when the inner plan/projection/filter contains non-deterministic or uncacheable expressions. The existing expression-level `CheckNonDeterministic` / non-foldable function metadata is the likely building block, but the guard must cover expressions that materialize into the cached inner `chunk.List`.

Keep the deterministic case green: `CONCAT('v', inner_t.a)` should still be cacheable.

## Methodology Note

This bug was not found by broad executor fuzzing. It came from a reusable proof-obligation pattern:

```text
cache key equality
does not imply
cached payload purity
```

The efficient test matrix had only three cells:

- volatile inner expression + cache ON/OFF differential -> red;
- deterministic inner expression -> green;
- trigger evidence from `EXPLAIN ANALYZE` proving `cache:ON`/`cache:OFF`.

That is the improvement over the earlier P/Q/F template: add a `D_dim` for "purity of cached payload" whenever a fast path reuses a result object instead of redoing computation.
