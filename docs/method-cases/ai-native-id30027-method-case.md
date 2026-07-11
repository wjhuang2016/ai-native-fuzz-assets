# Method Case id30027: Table-only inspection cache vs extractor dimensions

Bug: `inspection_result config details leak cached cluster_config rows across component types`

Remote bug DB: `found_bug` id30027, confirmed, medium, wrong-result.

## Why this target was selected

After id30026, the S3 family was paused for `tikv_region_peers` numeric conversion. The next useful move was not another negative-ID variant. The source scan moved to a different S3 mechanism: a cache fast path inside memtable execution.

The high-signal source smell was in `MemTableReaderExec.Next`: when `InspectionTableCache` exists and the table is cacheable, cached rows are returned by table name. The nearby TODO says cached rows are returned fully. That is a proof obligation, not just a performance comment.

## Audit card

```text
Target:
  information_schema.inspection_result -> configInspection -> cluster_config detail query

Source anchors:
  pkg/executor/memtable_reader.go
  pkg/executor/inspection_result.go
  pkg/planner/core/memtable_predicate_extractor.go

T_tests:
  Existing cache tests verify that the cache is used, but do not check whether a cached full
  snapshot is equivalent to a later extractor-filtered query.

P_check:
  The cache key is the memtable name, and cluster_config is marked inspection-cacheable.

Q_claim:
  A cached cluster_config snapshot is equivalent to rerunning the later cluster_config query.

D_dims:
  The later query's `type` predicate is consumed by ClusterTableExtractor into `node_types`.
  That dimension is absent from the table-name-only cache key.

F_effect:
  The later query is served from cache and skips the normal fetchClusterConfig path that would
  filter server info by node type.

O_oracle:
  Fast path: information_schema.inspection_result row for rule=config,item=foo-test,type=tikv.
  Reference: direct information_schema.cluster_config WHERE type='tikv' AND key='foo-test'.
  Required equality: tikv detail must include exactly the direct type-filtered component set.

R_redflag:
  The same config key exists in multiple component types, and values differ by instance.

S_selector:
  cache/reuse shortcut keyed by table/object name, while later query semantics depend on
  extractor-consumed dimensions that are not rechecked.
```

## Minimal matrix

```text
RED:
  direct cluster_config type='tikv',key='foo-test'
    -> tikv-a=tikv-a,tikv-b=tikv-b
  inspection_result rule=config,item=foo-test,type='tikv'
    -> details includes tidb-a

Trigger evidence:
  EXPLAIN on the detail query keeps only key as Selection and shows node_types:["tikv"]
  under MemTableScan. Therefore `type='tikv'` was consumed by the extractor.

Specificity:
  @@warning_count = 0.
  The mock config servers are deterministic; the failpoint does not bypass the normal
  inspection_result cache path.
```

## Why this is a good bug

The symptom is user-visible and low-noise: a diagnostic table says the row is for TiKV, but the diagnostic detail includes a TiDB instance. It is not a plan-only issue, not a timing issue, and not a contract ambiguity. The direct system-table reference gives the exact expected component set.

Severity is medium: the defect corrupts troubleshooting output, not user table data.

## Methodology improvement

This hit sharpens S3 from "extractor drops scalar predicate" to "any shortcut cache must preserve extractor dimensions." The important question becomes:

```text
What dimensions did the original query use to choose rows,
and are those dimensions still present after the cache/reuse fast path?
```

For future targets, look for:

- cache keys that are only table name, object ID, or statement shape;
- a later query whose predicates are consumed by an extractor, not left as scalar filters;
- cached snapshots that are broader than the later query;
- user-visible detail/report rows that can be compared with a direct reference query.

Stop rule: do not enumerate every `InspectionTableCache` user after this hit. Reopen the family only if another cacheable table has a distinct missing dimension or a stronger consequence oracle.
