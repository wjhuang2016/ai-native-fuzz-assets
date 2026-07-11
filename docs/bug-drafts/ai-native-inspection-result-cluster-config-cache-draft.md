# inspection_result leaks cached cluster_config rows across component types

Status: confirmed, inserted into remote `found_bug` as id30027.

## User-visible symptom

`information_schema.inspection_result` can report a `rule='config'`, `type='tikv'` row whose `details` field includes config values from a TiDB instance.

On testbed `8192975`, the direct reference query returned only TiKV instances:

```sql
SELECT GROUP_CONCAT(CONCAT(instance, '=', value) ORDER BY instance SEPARATOR ',') AS direct_tikv_rows
FROM information_schema.cluster_config
WHERE type='tikv' AND `key`='foo-test';
```

Result:

```text
tikv-a=tikv-a,tikv-b=tikv-b
```

But the product diagnostic table returned a TiKV inspection result whose details leaked a TiDB row:

```sql
SELECT rule,item,type,value, details LIKE '%tidb-a%' AS leaked_tidb, details
FROM information_schema.inspection_result
WHERE rule='config' AND item='foo-test' AND type='tikv';
```

Result:

```text
rule=config
item=foo-test
type=tikv
value=inconsistent
leaked_tidb=1
details:
tidb-a config value is tidb-a
tikv-a config value is tikv-a
tikv-b config value is tikv-b
```

`@@warning_count` was 0.

## Deterministic setup

The proof used the existing executor failpoint `github.com/pingcap/tidb/pkg/executor/mockClusterConfigServerInfo` to make cluster config rows deterministic:

```text
tikv,tikv-a,127.0.0.1:18081 -> {"foo-test":"tikv-a"}
tikv,tikv-b,127.0.0.1:18082 -> {"foo-test":"tikv-b"}
tidb,tidb-a,127.0.0.1:18083 -> {"foo-test":"tidb-a"}
```

This is not a mock-only behavior claim. The failpoint only supplies deterministic config-server rows. The SQL-visible bug is in the normal `inspection_result` query path.

## Trigger evidence

The direct detail query shape consumes `type='tikv'` into the cluster-table extractor:

```sql
EXPLAIN FORMAT='brief'
SELECT value, instance
FROM information_schema.cluster_config
WHERE type='tikv' AND `key`='foo-test';
```

Important plan shape:

```text
Selection eq(Column#3, "foo-test")
MemTableScan table:CLUSTER_CONFIG, node_types:["tikv"]
```

So `type='tikv'` is no longer a scalar `Selection`; it has been moved into `node_types`.

## Root cause

Source anchors:

- `/Users/bba/pc/tidb/pkg/executor/inspection_result.go`: `inspectionResultRetriever.retrieve` creates `SessionVars.InspectionTableCache`.
- `/Users/bba/pc/tidb/pkg/executor/inspection_result.go`: `configInspection.inspectDiffConfig` first scans `information_schema.cluster_config` grouped by `type,key`, populating a full table snapshot in the cache.
- `/Users/bba/pc/tidb/pkg/executor/inspection_result.go`: `configInspection.generateDetail` later runs `SELECT value, instance FROM information_schema.cluster_config WHERE type=? AND key=?`.
- `/Users/bba/pc/tidb/pkg/planner/core/memtable_predicate_extractor.go`: `ClusterTableExtractor` extracts `type` into `node_types` and removes the scalar predicate.
- `/Users/bba/pc/tidb/pkg/executor/memtable_reader.go`: `MemTableReaderExec.Next` serves `InspectionTableCache` by table name only, with a TODO noting cached rows are returned fully.

The chain is:

```text
First internal query caches full cluster_config rows under table name only.
Later internal query asks for type=tikv,key=foo-test.
The extractor consumes type=tikv into node_types and removes scalar Selection.
The cache hit returns the full table snapshot, not a node_types-filtered snapshot.
Only key=foo-test remains as a scalar filter, so tidb-a passes into the tikv detail.
```

## Expected behavior

For a `type='tikv'` inspection result, diagnostic details should match the direct `cluster_config WHERE type='tikv' AND key=?` reference and list only TiKV instances.

## Actual behavior

The detail leaks a TiDB instance from the table-name-only cached snapshot.

## Fix direction

Do one of:

- include extractor dimensions such as node type and address in the inspection cache key;
- cache only the post-filtered snapshot for the exact extractor request;
- preserve or reapply consumed scalar predicates when serving cached memtable rows;
- disable table-name-only cache reuse for queries whose shortcut dimensions differ from the cached scan.

## Method lesson

This is S3, not random system-table fuzzing:

```text
P_check:  the cache key is the memtable name
Q_claim:  cached rows are equivalent to rerunning the memtable query
D_dims:   extractor-consumed dimensions such as node type/address
F_effect: the later query uses cached rows and skips the normal extractor/backend filter
Oracle:   direct type-filtered cluster_config reference vs inspection_result detail
```

New selector refinement: shortcut caches need a proof that the cache key includes every dimension consumed by extractor/shortcut logic, or the hit path must reapply the missing dimension as a scalar filter.
