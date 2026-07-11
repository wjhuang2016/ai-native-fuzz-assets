# Draft: metrics_summary METRICS_NAME extractor returns rows that fail binary equality predicate (id30019)
> 2026-07-03. Confirmed on testbed 8192975 / fp-tidb. Selector S3 (shortcut/extractor lossy prefilter), representative blast-radius of `extractCol(..., valueToLower=true)` outside InfoSchema object names.

## Minimal Reproduction

```sql
SHOW FULL COLUMNS FROM information_schema.metrics_summary LIKE 'METRICS_NAME';
-- METRICS_NAME  varchar(64)  utf8mb4_bin

SELECT metrics_name, metrics_name = 'TIDB_QPS' AS self_ok
FROM information_schema.metrics_summary
WHERE metrics_name = 'TIDB_QPS'
LIMIT 3;
-- tidb_qps  0

SELECT metrics_name, metrics_name = 'TIDB_QPS' AS self_ok
FROM information_schema.metrics_summary
WHERE metrics_name = 'tidb_qps'
  AND CASE WHEN metrics_name = 'TIDB_QPS' THEN TRUE ELSE FALSE END
LIMIT 3;
-- empty
```

The scalar-pushdown form is also red:

```sql
SELECT metrics_name, LOWER(metrics_name), LOWER(metrics_name) = 'TIDB_QPS' AS self_ok
FROM information_schema.metrics_summary
WHERE metrics_name = 'tidb_qps'
  AND LOWER(metrics_name) = 'TIDB_QPS'
LIMIT 3;
-- tidb_qps  tidb_qps  0
```

Green controls:

```sql
SELECT metrics_name, metrics_name = 'tidb_qps' AS self_ok
FROM information_schema.metrics_summary
WHERE metrics_name = 'tidb_qps'
LIMIT 3;
-- tidb_qps  1

SELECT metrics_name, LOWER(metrics_name), LOWER(metrics_name) = 'tidb_qps' AS self_ok
FROM information_schema.metrics_summary
WHERE metrics_name = 'tidb_qps'
  AND LOWER(metrics_name) = 'tidb_qps'
LIMIT 3;
-- tidb_qps  tidb_qps  1
```

## User-Visible Symptom

The query returns a row that fails its own SQL-visible predicate:

- `METRICS_NAME` is `utf8mb4_bin`;
- `WHERE metrics_name = 'TIDB_QPS'` returns `tidb_qps`;
- the projected predicate `metrics_name = 'TIDB_QPS'` on that same row is `0`;
- the CASE-wrapped reference returns no rows.

## Probe Result

Probe: `/Users/bba/pc/ai_native_metrics_summary_name_case_probe.py`

```text
FINDING  metrics_summary_name_case  METRICS_NAME='TIDB_QPS' returned scalar-false row: ['tidb_qps\t0']; LOWER(METRICS_NAME)='TIDB_QPS' returned scalar-false row: ['tidb_qps\ttidb_qps\t0']
SUMMARY total=1 findings=1 skipped=0
```

## Source Chain

- `pkg/planner/core/memtable_predicate_extractor.go:1141-1145`: `MetricSummaryTableExtractor.Extract` calls `extractCol(..., "metrics_name", true)`.
- `pkg/planner/core/memtable_predicate_extractor.go:274-334`: `extractCol` lowercases extracted constants when `valueToLower=true` and removes the matched predicate from `remained`.
- `pkg/executor/metrics_reader.go:211-217`: `MetricsSummaryRetriever` uses `e.extractor.MetricsNames` as a prefilter over `infoschema.MetricTableMap`.
- No scalar recheck is applied after the metrics-name prefilter, so a lowercased metric key can admit rows that fail the binary predicate.

## Root Cause

```text
P_check:
  metrics_name='TIDB_QPS' can be used as a metric-name prefilter

Q_claim:
  lowercasing the requested metric key preserves the SQL-visible METRICS_NAME predicate

F_effect:
  the original predicate is removed from remained, and the retriever enumerates the lower-case metric name directly
```

The prefilter is useful, but it is wider than the SQL predicate for `utf8mb4_bin` columns.

## Expected Behavior

Rows returned by `WHERE metrics_name = 'TIDB_QPS'` must satisfy the same predicate when projected in the SELECT list. If the product wants case-insensitive metric lookup, the table's SQL-visible comparison semantics must still be coherent; otherwise the original predicate must remain as a scalar recheck.

## Fix Direction

Options:

- use exact SQL-visible comparison semantics when extracting `METRICS_NAME`;
- or keep the original `metrics_name` predicate in `remained` after using the lowercase key as a prefilter;
- or declare and implement a coherent case-insensitive collation/semantics for the exposed column, so `WHERE P` and projected `P` agree.

## Methodology Note

This is not a new selector. It is a representative cross-owner blast-radius proof for the S3 rule:

```text
valueToLower=true
+ predicate removed
+ SQL-visible binary column
+ row self-predicate oracle
= shortcut can return rows that fail WHERE
```

Stop here for this helper family. Do not enumerate every `valueToLower=true` extractor; the useful next move is to migrate S3 to a different shortcut mechanism, or return to a DDL owner selector.
