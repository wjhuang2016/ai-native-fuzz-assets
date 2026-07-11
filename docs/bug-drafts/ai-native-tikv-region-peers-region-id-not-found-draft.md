# Draft: tikv_region_peers region_id point lookup returns PD error instead of empty rows (id30022)
> 2026-07-03. Confirmed on testbed 8192975 / fp-tidb. Selector S3 refinement: external point lookup not-found must be a SQL empty rowset, not a backend exception.

## Minimal Reproduction

On the testbed, `information_schema.tikv_region_peers` has region peers, and the smallest real region id was `2`:

```sql
SELECT MIN(region_id), COUNT(*)
FROM information_schema.tikv_region_peers
WHERE region_id = (SELECT MIN(region_id) FROM information_schema.tikv_region_peers);
-- 2, 3
```

Querying a missing region id through the ordinary predicate:

```sql
EXPLAIN
SELECT region_id, store_id
FROM information_schema.tikv_region_peers
WHERE region_id = 0
LIMIT 1;
-- MemTableScan ... region_ids:[0]

SELECT COUNT(*)
FROM information_schema.tikv_region_peers
WHERE region_id = 0;
-- ERROR 1105 (HY000): request pd http api failed with status: '400 Bad Request', body: '{}'
```

The CASE-wrapped reference for the same SQL predicate returns an empty rowset:

```sql
SELECT COUNT(*)
FROM information_schema.tikv_region_peers
WHERE CASE WHEN region_id = 0 THEN TRUE ELSE FALSE END;
-- 0
```

The same not-found id also aborts an `IN` predicate that contains an existing region:

```sql
EXPLAIN
SELECT region_id, store_id
FROM information_schema.tikv_region_peers
WHERE region_id IN (0, 2)
LIMIT 10;
-- MemTableScan ... region_ids:[0,2]

SELECT COUNT(*)
FROM information_schema.tikv_region_peers
WHERE region_id IN (0, 2);
-- ERROR 1105 (HY000): request pd http api failed with status: '400 Bad Request', body: '{}'

SELECT COUNT(*) AS n,
       SUM(CASE WHEN region_id IN (0, 2) THEN 1 ELSE 0 END) AS ok
FROM information_schema.tikv_region_peers
WHERE CASE WHEN region_id IN (0, 2) THEN TRUE ELSE FALSE END;
-- n=3, ok=3
```

Green controls:

```sql
SELECT COUNT(*)
FROM information_schema.tikv_region_peers
WHERE region_id = 2;
-- 3

SELECT COUNT(*)
FROM information_schema.tikv_region_peers
WHERE store_id = 0;
-- 0
```

## User-Visible Symptom

A user querying region peers for a region id that no longer exists gets a TiDB error instead of an empty result. If the query mixes a missing id with existing ids, the missing id aborts the whole query and hides valid rows.

## Probe Result

Probe: `/Users/bba/pc/ai_native_tikv_region_peers_region_id_not_found_probe.py`

```text
FINDING tikv_region_peers_region_id_not_found missing_id=0, existing_id=2, fast_missing_err="ERROR 1105 ... request pd http api failed with status: '400 Bad Request', body: '{}'", ref_missing='0\tNULL', fast_mixed_err="ERROR 1105 ... request pd http api failed with status: '400 Bad Request', body: '{}'", ref_mixed='3\t3', fast_existing='3', ref_existing='3\t3', store_zero='0'
SUMMARY total=1 findings=1 skipped=0
```

## Source Chain

- `/Users/bba/pc/tidb/pkg/infoschema/tables.go:1100`: `REGION_ID` is a SQL-visible `BIGINT` column of `TIKV_REGION_PEERS`.
- `/Users/bba/pc/tidb/pkg/planner/core/memtable_predicate_extractor.go:1673-1678`: `TikvRegionPeersExtractor` extracts `region_id` and `store_id`, removes the original predicate, and stores `RegionIDs` / `StoreIDs`.
- `/Users/bba/pc/tidb/pkg/planner/core/memtable_predicate_extractor.go:1691-1696`: `EXPLAIN` exposes the fast path as `region_ids:[...]`.
- `/Users/bba/pc/tidb/pkg/executor/memtable_reader.go:962-971`: if `RegionIDs` is non-empty and no store-side prefetch found the region, executor calls `pdCli.GetRegionByID` and returns any error directly.
- `/Users/bba/pc/tidb/pkg/executor/memtable_reader.go:941-959`: the `store_id` path uses `GetRegionsByStoreID` and then filters peers, so missing store id returns an empty rowset instead of an error.

## Proof Obligation

```text
P_check:
  region_id = const is extracted into a backend PD point lookup.

Q_claim:
  The point lookup result is semantically equivalent to the SQL predicate.

F_effect:
  The original SQL predicate is removed; backend not-found errors are surfaced directly.

Missing D_dim:
  Backend object lookup not-found is not the same as SQL execution failure. In a filtered
  system table, not-found means no row satisfies the predicate. For IN-lists, one missing
  id must not abort rows for existing ids.
```

## Fix Direction

For `TIKV_REGION_PEERS` region-id point lookups, map PD "region not found" / invalid region id responses to an empty result for that id, then continue the remaining ids in the request. Preserve real transport/auth/PD availability errors as errors.
