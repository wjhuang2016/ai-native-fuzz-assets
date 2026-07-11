# Draft: cluster diagnostic `type` equality ignores bin collation (id30013, candidate)
> 2026-07-02. testbed 8192975 / master 5c9198e948. Selector S3 collation sub-family, third prediction. Classification: INFO(contract-ambiguous) → candidate, NOT confirmed.

## What was observed

`information_schema.cluster_log.type` is declared `utf8mb4_bin`, but `type='PD'` matches
`type='pd'` rows because `extractCol(valueToLower=true)` lowercases the user value and drops
the original predicate (no scalar recheck).

```sql
SET time_zone='+00:00';
SELECT COUNT(*) FROM information_schema.cluster_log
  WHERE type='PD' AND message LIKE '%' AND time>='2026-07-02 13:00:00' AND time<='2026-07-02 13:05:00';
-- 197   (all returned rows have type='pd')
SELECT COUNT(*) FROM information_schema.cluster_log
  WHERE type LIKE 'PD' AND message LIKE '%' AND time>=... AND time<=...;
-- 0     (LIKE is not consumed by extractCol -> scalar -> case-sensitive under bin)
```

## Reference-implementation differential (the reason this is precise, not vague)

The general SQL contract was settled with a reference implementation instead of arguing about it:

| path | `bin-column = 'PD'` | `= 'pd'` |
|---|---|---|
| TiDB cluster_log extractor | 197 (returns 'pd') | 197 |
| TiDB ordinary user table (utf8mb4_bin) | 0 | 1 |
| **MySQL 8.3.0 (utf8mb4_bin)** | **0** | 1 |

MySQL and a plain TiDB table both make `='PD'` case-sensitive (0 rows). So "a bin column's `=`
is case-sensitive" is not ambiguous — it is a firm contract the cluster_log extractor violates.
What remains genuinely for the owner: whether diagnostic tables *intentionally* exempt themselves
from that contract for convenience. MySQL cannot rule on that (it has no cluster_log), which is
exactly why this stays a candidate.

## Why candidate, not confirmed (and why that differs from id30010)

- `valueToLower=true` is an explicit, per-call design flag; case-insensitive matching of fixed
  enum values (`tidb`/`tikv`/`pd`) may be intended convenience.
- BUT the same column name `type` is passed `true` by ClusterTableExtractor:737 /
  ClusterLogTableExtractor:805 / InspectionRuleTableExtractor:1271 and `false` by
  HotRegionsHistoryTableExtractor:942. Same column, inconsistent case behavior across tables —
  evidence it is not a uniform, deliberate contract.
- Contrast id30010 (confirmed): `TABLE_NAME` is a user-chosen identifier where case sensitivity
  is core semantics with no reasonable "intended" defense. `type` is a system enum with one — so
  it is judged candidate. The distinction is consistent, not a double standard.

## Source

- `pkg/planner/core/memtable_predicate_extractor.go` `extractCol` (:292) drops matched EQ/IN/OR
  predicate from `remained`; `merge` (:274) lowercases when `valueToLower`.
- Inconsistent `type` flag: true@737/805/1271, false@942.

## Owner question / fix direction

If bin collation should be honored: stop lowercasing, or keep the original predicate in `remained`
for scalar recheck. If case-insensitivity is intended: make it uniform across all diagnostic
extractors and reconsider declaring these columns `utf8mb4_bin`.

## Assets
- Probe: `/Users/bba/pc/ai_native_clusterlog_type_collation_probe.py` (INFO(contract-ambiguous), trigger-evidenced).
- Bug library: pending insert (network to tidbcloud bug lib timed out) — `/Users/bba/pc/ai-native-found-bug-pending.sql`.
