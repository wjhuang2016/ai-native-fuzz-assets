# Draft: tikv_region_peers negative IDs drop predicates and return all rows (id30026)
> 2026-07-03. Confirmed on testbed 8192975 / fp-tidb. S3 refinement: numeric type-domain conversion is part of the shortcut proof.

## Symptom

Impossible negative predicates on `information_schema.tikv_region_peers` return the full peer table instead of an empty rowset.

Minimal reproduction on testbed 8192975:

```sql
SELECT COUNT(*) AS peers_total
FROM information_schema.tikv_region_peers;
-- 269

EXPLAIN FORMAT='brief'
SELECT * FROM information_schema.tikv_region_peers
WHERE region_id = -1;
-- MemTableScan table:TIKV_REGION_PEERS, no Selection, no region_ids filter

SELECT COUNT(*) AS direct
FROM information_schema.tikv_region_peers
WHERE region_id = -1;
-- 269

SELECT COUNT(*) AS oracle
FROM information_schema.tikv_region_peers
WHERE CASE WHEN region_id = -1 THEN TRUE ELSE FALSE END;
-- 0
```

The returned rows prove the predicate was not actually true:

```sql
SELECT region_id, store_id, peer_id, region_id = -1 AS predicate_value
FROM information_schema.tikv_region_peers
WHERE region_id = -1
LIMIT 5;
```

Observed:

```text
region_id  store_id  peer_id  predicate_value
2756       1         2757     0
2756       12        2758     0
2756       13        2759     0
6          1         7        0
6          12        15       0
```

`store_id = -1` reproduces the same way:

```sql
SELECT COUNT(*) FROM information_schema.tikv_region_peers WHERE store_id = -1;
-- 269

SELECT COUNT(*) FROM information_schema.tikv_region_peers
WHERE CASE WHEN store_id = -1 THEN TRUE ELSE FALSE END;
-- 0
```

Secondary symptom:

```sql
EXPLAIN FORMAT='brief'
SELECT * FROM information_schema.tikv_region_peers
WHERE region_id = 'abc';
-- MemTableScan table:TIKV_REGION_PEERS, region_ids:[0]

SELECT COUNT(*) FROM information_schema.tikv_region_peers
WHERE region_id = 'abc';
-- ERROR 1105: request pd http api failed with status: '400 Bad Request', body: '{}'

SELECT COUNT(*) FROM information_schema.tikv_region_peers
WHERE CASE WHEN region_id = 'abc' THEN TRUE ELSE FALSE END;
-- 0
```

## Source Chain

- `pkg/planner/core/memtable_predicate_extractor.go:292-349`: `extractCol` recognizes `EQ` / `IN` / `OR`, extracts values, and removes the original predicate when `colName == extractColName`.
- `pkg/planner/core/memtable_predicate_extractor.go:644-654`: `parseUint64` silently ignores `strconv.ParseUint` failures.
- `pkg/planner/core/memtable_predicate_extractor.go:1662-1683`: `TikvRegionPeersExtractor` extracts `region_id` and `store_id`, then assigns `e.RegionIDs, e.StoreIDs = e.parseUint64(...)`.

For `region_id = -1`:

```text
extractCol:
  recognized region_id equality
  extracted "-1"
  removed the original SQL predicate

parseUint64:
  ParseUint("-1") failed
  ignored the error
  returned an empty []uint64

executor:
  saw no region_ids filter
  saw no remaining Selection
  scanned all peers
```

## Root Cause

```text
P_check:
  The predicate is an extractable EQ/IN predicate on region_id or store_id.

Q_claim:
  The extracted uint64 ID set is an equivalent replacement for the SQL predicate.

D_dim:
  SQL numeric comparison and internal uint64 request domains are not the same domain.
  Negative values and invalid strings are out-of-domain for the backend point-lookup API.

F_effect:
  The original SQL predicate is removed before the conversion result is proven equivalent.
```

The bug is not simply "negative IDs have no rows". The bug is that conversion failure turns an impossible SQL predicate into no internal filter at all.

## Expected Behavior

`region_id = -1`, `region_id IN (-1)`, and `store_id = -1` should return an empty rowset. Invalid or out-of-domain extracted values should preserve SQL semantics and should not cause a full scan or a backend request error.

## Fix Direction

Make numeric extractor conversion part of the proof:

- preserve the original predicate if any extracted value cannot be represented in the backend request domain;
- or set `skip_request` when all extracted-only values are impossible;
- or make `parseUint64` return an error/validity marker so callers cannot drop the predicate with an empty internal filter.

Also keep id30022's rule: backend object-not-found or invalid point lookup must map to SQL empty-row semantics when the SQL predicate is otherwise well-formed.

## Methodology Note

This is a new S3 sub-shape:

```text
extractable predicate
  + lossy type-domain conversion
  + original predicate dropped before conversion proof
  + CASE/self-predicate oracle
```

The efficient move was to stop after the first clean red. `TIKV_REGION_PEERS` is the representative confirmed owner. Other `parseUint64` users are potential blast radius, but should not be enumerated blindly unless a distinct owner contract or consequence oracle is needed.
