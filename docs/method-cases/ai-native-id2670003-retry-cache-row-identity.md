# id2670003: retry caches need provenance and row-identity binding

Status: issue-filed high-severity / critical-consequence root:
[TiDB #69845](https://github.com/pingcap/tidb/issues/69845).

## Proof obligation

```text
P(cached[i]): cached[i] is a generated allocation reusable for the same logical retry row.
Q:           cached[i] may replace the current auto-increment datum at position i.
F:           INSERT SELECT is re-executed over a changed source row, but Q is reused without
             checking generated-vs-explicit provenance or current row identity.
```

The code does not merely cache a value. It caches a value with two omitted arguments:

```text
bad:  autoIDs[i] = 100
good: autoID(value=100, provenance=generated|explicit, logical_row=K, input_generation=G)
```

When the producer is re-executed, position `i` is not a stable identity token.

## Small matrix

| Source transition | Conflict/retry | Result | Verdict |
| --- | --- | --- | --- |
| `100/old -> 100/old` | yes | `100/old` | GREEN |
| `100/old -> 200/new` | no, B before A | `200/new` | GREEN control |
| `100/old -> 200/new` | yes | `100/new` | RED |
| same RED schedule + exact cache-use counterfactual | yes | `200/new` | GREEN |

The complete one-attempt outcome set is `{100/old, 200/new}`. The mixed result is stronger than a
same-final-state comparison because it is outside every coherent serialization.

## Selector

Store this as `RETRY_CACHE_PROVENANCE_AND_IDENTITY`:

```text
candidate = state retained across replay
            intersect positional or ordinal binding
            intersect producer that is re-read or rebuilt
            intersect values with semantic provenance or identity
            intersect overwrite before an irreversible consumer
            minus revalidation / stable-key binding / fail-closed behavior
```

Prioritize caches named IDs, handles, offsets, ordinals, indexes, slots, generated values, or
resolved keys. For each cache, ask:

1. Is the cached value generated or user supplied?
2. What logical object was it generated for?
3. Does replay preserve that object or only the array position?
4. Is the current value parsed and compared before the cache wins?
5. Can the mismatch reach a row key, index key, commit request, or success response?

## Why the production card mattered

The first version used a source primary-key change. Recasting the producer as a stable staging slot
whose `target_id` mapping is corrected made the real workload clear: migration and reconciliation
systems routinely preserve work-item identity while replacing the external ID to publish. The hot
target conflict comes from an ordinary concurrent incremental batch. `SLEEP` is only a schedule
compressor for scan or storage latency; no injected error is part of the root.

## Historical calibration

#20629 already proved that the retry row count can change and treated cached auto IDs as a refillable
buffer. Searching only for cache exhaustion would rediscover that issue. The stronger selector asks
whether the buffer element is still semantically valid when a same-position row changes. This turns
an old availability-oriented design into a new identity-integrity proof obligation.

## Method improvement

For every replay cache, record a typed certificate rather than a raw scalar:

```text
certificate(value, provenance, logical_owner, generation, validity_predicate)
```

Replay may consume it only if all binding arguments still match. Otherwise it must recompute,
revalidate, or fail closed. This is the retry-cache analogue of value-replacement proof
revalidation: not only can the value change after a proof, the consumer itself can be rebound to a
different logical row while retaining the same ordinal.
