# id2430003: completed materialization survives transparent retry

Remote bug DB: `found_bug id2430003`, confirmed high consequence.
Upstream: [TiDB #69826](https://github.com/pingcap/tidb/issues/69826).

## Starting proof obligation

After #69823 exhausted mutable fields inside expression `Clone`, the owner boundary moved outward:

```text
statement owns materialized result M
failed attempt marks M complete
executor Close deliberately preserves complete M
retry builder finds M and skips reconstruction
successful attempt combines M with fresh state
```

Transparent retry requires every attempt-scoped materialization to be rebuilt, restored from a
statement-entry snapshot, or proven independent of the retry snapshot.

## How the candidate was generated

1. Run a typed AST pass over expression clones; only the terminal RAND owner survived.
2. Move to state initialized once per statement and loaded again by executor build.
3. Search for `map + sync.Once/Done + preserve-on-Close` lifecycle shapes.
4. Intersect them with `ResetForRetry` omissions and same-context rebuild.
5. Require a C3 consumer before writing a test.

`CTEStorageMap` survived every gate. A bounded eight-region source packet independently returned the
same candidate and retired partial-result, correlated-parameter, and cross-statement siblings.

## P/Q/F

- **P**: a retryable lock error is accepted and a new executor is built.
- **Q**: every statement-owned input to that executor belongs to the successful attempt.
- **F**: completed CTE storage remains in the same statement context; `initOnce` prevents a fresh
  producer, so the retry consumes failed-attempt rows.

## Strong oracle

Store as `MIXED_ATTEMPT_ROW_COHERENCE`:

```text
one target row consumes:
  field A <- fresh ordinary read
  field B <- suspected materialized read

competitor changes both source fields at the retry boundary
same-final-state direct execution supplies the coherent reference row
```

The RED row `(u=2,v=10)` is self-authenticating: neither the old state `(1,10)` nor the new state
`(2,20)` owns it. It is a mixed-attempt row. This is stronger than checking a cache field or timing.

## Compressed matrix

| Cell | CTE materialized | Retry | Row 1 |
| --- | ---: | ---: | --- |
| Natural unique conflict | yes | 1 | `(1,2,10)` |
| Same final state direct control | yes | 0 | `(1,2,20)` |
| Reset materialization before rebuild | yes | 1 | `(1,2,20)` |

Plan evidence requires `CTEFullScan`; slow-log evidence requires retry count one. The live lift uses
real TiKV and default-on MDL without failpoints.

## Selector improvement

New selector: `REPLAY_PERSISTENT_MATERIALIZATION_STATE`.

```text
statement-scoped materializations marked complete
intersect
state preserved by Close and reused by retry build
intersect
fresh-state consumers in the successful attempt
minus
retry reset/rebuild/version binding/idempotency guards
```

The source generator should look for lifecycle triads rather than field names:

```text
initialize once -> preserve completed state -> reset only at outer statement boundary
```

Then compare that outer boundary with every inner replay boundary. CTE storage is one instance;
spools, subquery caches, temporary result sets, and materialized lookup tables are future owner
classes only when they have independent consumers.

## Quality and stop rule

This is silent durable wrong data through ordinary SQL, so it is C3/high. Materialized CTE DML plus
a well-timed conflict is less common than plain DML. One CTE storage lifecycle is one root. Recursive
forms, consumer counts, CTE query shapes, SQL verbs, delays, and conflict schedules are blast radius.

