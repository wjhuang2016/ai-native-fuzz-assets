# Method Case id30020: Cache payload purity as a new selector
> 2026-07-03. `ParallelNestedLoopApplyExec` Apply cache. This note records the methodology result, not just the bug.

## What Was Being Tested

After id30019, the loop had to avoid enumerating more `extractCol(..., valueToLower=true)` users. The useful next question was:

```text
can the same proof-obligation workflow transfer to a different shortcut mechanism?
```

Apply cache was a good target because it makes a clear semantic promise:

```text
same correlated outer values
=> same inner result
=> reuse cached innerList
```

That promise hides an extra dimension: the inner result must be pure with respect to the correlated key.

## Why This Target Was Picked

The source scan found:

- planner enables cache from correlated-column NDV and memory quota;
- executor encodes only correlated outer values as the cache key;
- cache hit returns the cached `chunk.List` without reopening or re-evaluating the inner executor.

This is exactly the P/Q/F shape:

```text
P_check:  repeated correlated keys
Q_claim:  inner payload is determined by those keys
F_effect: reuse innerList, skip safe re-evaluation
```

The adversarial dimension is `D_dim=volatile inner expression`.

## Tiny Matrix

Red cell:

```sql
SELECT id, a,
       (SELECT UUID() FROM inner_t WHERE inner_t.a = outer_t.a LIMIT 1) AS u
FROM outer_t;
```

With cache ON:

```text
a=1 n=24 distinct_u=1
a=2 n=16 distinct_u=1
```

With cache OFF:

```text
a=1 n=24 distinct_u=24
a=2 n=16 distinct_u=16
```

Green control:

```sql
SELECT id, a,
       (SELECT CONCAT('v', inner_t.a)
        FROM inner_t
        WHERE inner_t.a = outer_t.a
        LIMIT 1) AS v
FROM outer_t;
```

This remains `distinct_v=1` per key in both modes, so the oracle is specifically about volatile payloads.

Trigger evidence:

```text
EXPLAIN ANALYZE ... cache:ON
EXPLAIN ANALYZE ... cache:OFF
```

## Why It Worked

The human-readable code path was small, but the real reason this worked is the explicit `D_dim` step. If we only ask "is the cache key complete?", the code looks plausible. If we ask "which semantic dimensions must Q preserve?", volatility falls out immediately.

This is a new selector, not another S3 extractor variant:

```text
S7: result/cache payload reuse
    + cache key covers stable inputs
    + cached payload can contain computation side effects or volatile values
    + safe path can be forced by disabling the cache
```

## Quality

High methodology value, medium product severity:

- ordinary user tables, not a diagnostic virtual table;
- no failpoint, timing, or concurrent race required;
- trigger-evidenced `cache:ON`/`cache:OFF` reference;
- deterministic green control keeps the report narrow;
- user-visible symptom is a wrong result for volatile scalar subquery semantics.

The product severity is medium because the query needs duplicate correlated keys and a volatile inner expression, but the bug is deterministic once the plan chooses Apply cache.

## Methodology Improvement

Add this rule to the improved P/Q/F template:

```text
For every cache/reuse fast path, prove both:
  key completeness: all stable inputs are in the key
  payload purity: the cached object is a pure function of that key
```

The second proof is easy to forget. AI can find these bugs efficiently by scanning for caches whose key is visibly small and whose value stores evaluated expressions, then using a cache-disabled differential and a volatile-expression red cell.
