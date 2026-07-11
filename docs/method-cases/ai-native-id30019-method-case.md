# Method Case id30019: Representative blast-radius after selector refinement
> 2026-07-03. `information_schema.metrics_summary.METRICS_NAME`. This note records the methodology result, not the bug details.

## What Was Being Tested

After id30018, the question was not "how many more object-name variants can we find?" The next useful question was:

```text
does the refined S3 rule transfer to a different owner,
and if yes, should we keep mining or stop as blast radius?
```

`metrics_summary` is a different public surface from `information_schema.tables`, but it uses the same generic extractor helper shape:

```text
extractCol(..., valueToLower=true)
+ remove original predicate
+ later enumerate lower-case keys
```

## Why This Target Was Picked

The source scan found `MetricSummaryTableExtractor` calling:

```text
extractCol(..., "metrics_name", true)
```

`METRICS_NAME` is a SQL-visible `utf8mb4_bin` column. That means a wrong-case equality is a tiny adversarial cell:

```sql
WHERE metrics_name = 'TIDB_QPS'
```

No DDL setup, no timing, no broad fuzzing. The row can judge the shortcut by projecting the predicate itself.

## Tiny Matrix

Red cells:

1. `metrics_name='TIDB_QPS'` returns `tidb_qps`, but projected `metrics_name='TIDB_QPS'` is `0`.
2. `LOWER(metrics_name)='TIDB_QPS'` also returns `tidb_qps`, but projected predicate is `0`.

References:

1. CASE-wrapped reference under `metrics_name='tidb_qps'` returns no rows.

Green controls:

1. `metrics_name='tidb_qps'` returns `tidb_qps` with predicate `1`.
2. `LOWER(metrics_name)='tidb_qps'` returns `tidb_qps` with predicate `1`.

## Why It Worked

The selector was already sharpened by id30018. The new ingredient was moving from object names to metric names:

```text
same helper
different owner
same oracle
same violation shape
```

This proves the source-level P/Q/F was not just an InfoSchema object-name accident. The bug sits at the boundary where a useful product shortcut, case-insensitive lookup by lower-case key, is exposed through a binary-collation SQL column without keeping a scalar recheck.

## Quality

Medium. It is a deterministic wrong-result on a diagnostic virtual table:

- the column contract is visible as `utf8mb4_bin`;
- the returned row fails the query predicate;
- CASE reference and matching-case controls separate the bug from environment noise;
- the repro only touches one metric, so it avoids full Prometheus scan instability.

The method value is not another "new selector hit"; it is a blast-radius calibration. Once one non-object-name owner proves the helper-wide issue, further `valueToLower=true` tables should be grouped, not mined one by one.

## Methodology Improvement

Add a new stop rule to S3:

```text
if a generic helper bug is proven across a second owner:
  record one representative blast-radius case
  stop enumerating all users of the helper
  move to a different shortcut mechanism or a different selector
```

This protects the loop from confusing blast-radius counting with discovery efficiency.
