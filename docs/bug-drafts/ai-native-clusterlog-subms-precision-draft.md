# Draft: cluster_log equality with sub-millisecond literal returns rows that fail the predicate (id30015)
> 2026-07-03. Confirmed on testbed 8192975 / fp-tidb. Selector S3 (shortcut/extractor lossy prefilter). New D_dim after id30012: precision-lowering from SQL DATETIME(6) to millisecond backend search.

## Minimal Reproduction

Pick any existing `cluster_log` row whose displayed time has millisecond precision, for example:

```sql
SELECT time, type, level, LEFT(message, 80)
FROM information_schema.cluster_log
WHERE time >= '2026/07/02 00:00:00'
  AND time <  '2026/07/04 00:00:00'
  AND message LIKE '%'
ORDER BY time DESC
LIMIT 1;
-- 2026/07/02 22:00:45.416  pd  INFO  ...
```

Now query a literal in the same millisecond but not equal to the row value:

```sql
SELECT COUNT(*), MIN(time), MAX(time), SUM(time = '2026/07/02 22:00:45.416500')
FROM information_schema.cluster_log
WHERE time = '2026/07/02 22:00:45.416500'
  AND message LIKE '%';
-- count=2, min=max=2026/07/02 22:00:45.416, predicate_sum=0

SELECT COUNT(*)
FROM information_schema.cluster_log
WHERE time >= '2026/07/02 22:00:45.416'
  AND time <  '2026/07/02 22:00:45.417'
  AND message LIKE '%'
  AND CASE
        WHEN time = '2026/07/02 22:00:45.416500' THEN 1
        ELSE 0
      END = 1;
-- 0
```

Smoke result from the probe:

```text
DATA base=2026/07/02 22:00:45.416 probe=2026/07/02 22:00:45.416500
FAST count=2 true_predicate_sum=0 min=2026/07/02 22:00:45.416 max=2026/07/02 22:00:45.416
REF  count=0 true_predicate_sum=0 min=NULL max=NULL
RED cluster_log_subms_eq fast path returned rows whose own time=probe predicate is false
```

## User-Visible Symptom

An exact `WHERE time = '<timestamp with microseconds>'` query over `information_schema.cluster_log` can return log rows whose visible `time` value is different from the literal. This can mislead log investigation tools that use microsecond timestamps or construct exact timestamp filters from client-side time values.

The returned rows visibly violate the predicate:

```sql
SELECT time, time = '2026/07/02 22:00:45.416500' AS pred
FROM information_schema.cluster_log
WHERE time = '2026/07/02 22:00:45.416500'
  AND message LIKE '%';
-- 2026/07/02 22:00:45.416  0
-- 2026/07/02 22:00:45.416  0
```

## Source Chain

- `pkg/planner/core/memtable_predicate_extractor.go:576-593`: `extractTimeRange` converts the literal to a DATETIME(6)-derived nanosecond timestamp.
- `pkg/planner/core/memtable_predicate_extractor.go:595-602`: for `EQ`, the timestamp becomes both `startTime` and `endTime`, and the matched predicate is not appended back to `remained`.
- `pkg/planner/core/memtable_predicate_extractor.go:816-819`: `ClusterLogTableExtractor.Extract` passes the timestamp through `extractTimeRange`, then truncates both ends by dividing by `time.Millisecond`.
- `pkg/executor/memtable_reader.go:460-465`: executor sends only the millisecond `StartTime`/`EndTime` to `SearchLogRequest`; there is no SQL-visible `time = literal` recheck.

## Why This Is Separate From id30012

id30012 is a context bug: `cluster_log` uses `time.Local` instead of the session time zone. id30015 is a precision bug: even in one time-zone context, a DATETIME(6) equality literal is lowered to a millisecond backend request and then trusted as exact.

The shared methodology lesson is the same S3 selector: a custom extractor turns a scalar SQL predicate into a cheaper backend request and drops the predicate. The new sub-rule is: if the backend request has lower precision than the SQL predicate, the scalar predicate must remain as a safe recheck.

## Oracle

CASE-wrapped scalar recheck over the same millisecond window:

- Fast arm: `WHERE time = '<base + 500us>' AND message LIKE '%'`.
- Safe arm: explicit `[base, base + 1ms)` backend window plus `CASE WHEN time = '<base + 500us>' THEN 1 ELSE 0 END = 1`.
- Red condition: fast arm returns rows, every returned row has `time = probe` as false, and the safe arm returns 0.

## Fix Direction

Any one of these would make the shortcut safe:

- keep the original `time = literal` predicate in `remained` whenever the log-search request is millisecond-granularity but the literal has sub-millisecond precision;
- or round equality/range boundaries conservatively and keep a scalar recheck for all non-millisecond-aligned literals;
- or expose/compare `cluster_log.time` at the same precision as the backend search contract, with a clear SQL-visible type/format contract.

Regression should include an equality literal such as `.416500` against rows at `.416`, and assert the ordinary WHERE path matches the CASE oracle.

## Assets

- Probe: `/Users/bba/pc/ai_native_clusterlog_subms_precision_probe.py`
- Related confirmed issue: `/Users/bba/pc/ai-native-clusterlog-timezone-draft.md` (id30012)
