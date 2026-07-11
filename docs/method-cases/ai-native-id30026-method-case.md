# Method Case id30026: Type-domain conversion is part of the shortcut proof
> 2026-07-03. `TIKV_REGION_PEERS` extractor + negative numeric predicates.

## Why This Target Was Picked

After id30025, the cache/timezone family hit its pause gate. The next useful target needed a different proof obligation, not another time-function variant.

`memtable_predicate_extractor.go` had the right S3 shape:

```text
extractCol recognizes a SQL predicate
then removes the original predicate
then a table-specific extractor converts the extracted values into a backend request shape
```

That creates a hidden proof:

```text
the converted request-domain values are exactly equivalent to the SQL predicate
```

The code made this proof fragile for numeric IDs because `parseUint64` silently ignores parse failures.

## Tiny Matrix

Red cells:

```text
TIKV_REGION_PEERS total rows: 269

region_id = -1
  fast:   269
  oracle: 0
  returned rows project `region_id = -1` as 0

store_id = -1
  fast:   269
  oracle: 0
  returned rows project `store_id = -1` as 0

region_id IN (-1)
  fast:   269
  oracle: 0
```

Green/control cells:

```text
peer_id = -1
  fast:   0
  oracle: 0
  reason: peer_id is not extracted by TikvRegionPeersExtractor, so Selection remains.

region_id IN (-1, valid_region_id)
  fast:   valid rows only
  oracle: valid rows only
  reason: at least one value parses, so the backend filter is not empty.
```

Secondary symptom:

```text
region_id = 'abc'
  plan:   region_ids:[0]
  fast:   PD 400 Bad Request
  oracle: 0
```

## Why It Worked

The selector did not start from "try weird IDs". It started from a source-level ownership chain:

```text
extractCol consumes predicate
parseUint64 owns type-domain conversion
executor trusts the converted filter and skips SQL recheck
```

That made `-1` the smallest adversarial value:

- it is a legal SQL literal for a signed `bigint` column;
- it is impossible for the backend uint64 ID domain;
- it makes `parseUint64` fail;
- the failure is ignored after the SQL predicate has already been removed.

## Quality

High:

- deterministic wrong result;
- no failpoint, no timing, no concurrency;
- rows returned by the query visibly fail their own `WHERE` predicate;
- CASE-wrapped oracle gives a clean scalar reference;
- `peer_id=-1` and mixed `IN(-1, valid)` are useful controls.

Scope is narrower than the symptom looks:

- the confirmed surface is `information_schema.tikv_region_peers` `region_id` / `store_id`;
- other `parseUint64` owners are possible blast radius, but should be reopened only with a distinct oracle or owner consequence.

## Methodology Improvement

Add `type-domain conversion` to S3:

```text
If a shortcut extracts SQL values and converts them into a narrower backend request domain,
the conversion result is part of Q_claim.
The original SQL predicate can be dropped only after conversion success/empty/impossible
semantics are proven equivalent.
```

This is the same skeleton as earlier S3 hits, but the fragile dimension changed:

```text
id30010/id30018/id30019: collation and normalization
id30021: interval-vs-point abstraction
id30022: backend not-found error domain
id30023: request/render context split
id30026: SQL type domain vs backend request domain
```

The reusable selector is:

```text
code checked "predicate is extractable"
system believed "converted backend request is equivalent"
fast path dropped scalar Selection
conversion failure widened or narrowed the result
```
