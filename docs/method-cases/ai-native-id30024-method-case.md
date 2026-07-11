# Method Case id30024: Semantic switches belong in cache proofs
> 2026-07-03. Prepared plan cache + `tidb_sysdate_is_now`.

## Why This Target Was Picked

After the Apply-cache bug, S7 said:

```text
cache key equality does not prove cached payload purity
```

The next useful refinement was to inspect plan-cache key construction itself. `NewPlanCacheKey` explicitly says all plan-affecting information should be considered, and it includes many session dimensions. That makes omitted session variables a good source-level selector.

The high-signal candidate was `tidb_sysdate_is_now` because it is not just a cost knob. Source shows it changes expression construction:

```text
sysdate()
  -> sysdate function when tidb_sysdate_is_now=0
  -> now function when tidb_sysdate_is_now=1
```

But the cache key does not include that switch.

## Tiny Matrix

Red direction 1:

```text
set tidb_sysdate_is_now=0
prepare and execute twice until @@last_plan_from_cache=1
set tidb_sysdate_is_now=1
execute again
```

Cached result:

```text
@@last_plan_from_cache=1
sysdate(6)=now(6) => 0
```

Safe-path reference:

```text
ADMIN FLUSH SESSION PLAN_CACHE
execute same prepared statement
@@last_plan_from_cache=0
sysdate(6)=now(6) => 1
```

Red direction 2:

```text
ON -> cached -> OFF
```

Cached result stays `eq=1`; flush reference becomes `eq=0`.

## Why It Worked

The selector did not ask "which random prepared statements behave oddly?" It asked:

```text
which session variables are consumed while building the cached object,
but are absent from the reuse key?
```

That narrows the search sharply. The red cell then becomes obvious: choose a boolean query whose answer is exactly the semantic switch.

## Quality

High:

- user-visible wrong result, not only a bad plan;
- normal prepared statement and session variable, no failpoint;
- trigger evidence is `@@last_plan_from_cache=1`;
- safe-path reference is the same prepared statement after `ADMIN FLUSH SESSION PLAN_CACHE`;
- bidirectional matrix confirms the cached plan freezes whichever semantics were used at cache fill time.

## Methodology Improvement

Upgrade S7 from payload purity to a three-part cache proof:

```text
1. key completeness for stable inputs
2. payload purity for evaluated result objects
3. semantic-switch coverage for session/config variables consumed before caching
```

The third item is the new part. AI can find this class efficiently by reading cache-key builders, then asking which omitted variables are read during expression construction, rewrite, or plan generation and have a simple behavioral oracle.
