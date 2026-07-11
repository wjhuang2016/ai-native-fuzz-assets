# Draft: tidb_hot_regions_history returns rows that fail UPDATE_TIME predicate under non-UTC timezone (id30023)

## Symptom

On testbed `8192975`, `information_schema.tidb_hot_regions_history` has recent rows at UTC time:

```sql
SET time_zone='+00:00';
SELECT COUNT(*), MIN(update_time), MAX(update_time),
       SUM(update_time >= '2026-07-02 23:40:40' AND update_time < '2026-07-02 23:40:42')
FROM information_schema.tidb_hot_regions_history
WHERE update_time >= '2026-07-02 23:40:40'
  AND update_time <  '2026-07-02 23:40:42';
```

Result:

```text
69  2026-07-02 23:40:41  2026-07-02 23:40:41  69
```

The same absolute instant in `+14:00` should be visible as `2026-07-03 13:40:41`.
The extractor does request that absolute window:

```sql
SET time_zone='+14:00';
EXPLAIN SELECT COUNT(*)
FROM information_schema.tidb_hot_regions_history
WHERE update_time >= '2026-07-03 13:40:40'
  AND update_time <  '2026-07-03 13:40:42';
```

Plan evidence:

```text
MemTableScan table:TIDB_HOT_REGIONS_HISTORY start_time:2026-07-03 13:40:40, end_time:2026-07-03 13:40:41
```

But the plain query returns rows whose displayed `update_time` fails the predicate:

```sql
SELECT COUNT(*), MIN(update_time), MAX(update_time),
       SUM(update_time >= '2026-07-03 13:40:40' AND update_time < '2026-07-03 13:40:42')
FROM information_schema.tidb_hot_regions_history
WHERE update_time >= '2026-07-03 13:40:40'
  AND update_time <  '2026-07-03 13:40:42';
```

Result:

```text
69  2026-07-02 23:40:41  2026-07-02 23:40:41  0
```

The CASE-wrapped reference keeps the same extractor time range but forces scalar recheck:

```sql
SELECT COUNT(*), MIN(update_time), MAX(update_time)
FROM information_schema.tidb_hot_regions_history
WHERE update_time >= '2026-07-03 13:40:40'
  AND update_time <  '2026-07-03 13:40:42'
  AND CASE
        WHEN update_time >= '2026-07-03 13:40:40'
         AND update_time <  '2026-07-03 13:40:42'
        THEN 1 ELSE 0
      END = 1;
```

Result:

```text
0  NULL  NULL
```

## Source Chain

`pkg/planner/core/memtable_predicate_extractor.go`:

- `HotRegionsHistoryTableExtractor.Extract` calls `extractTimeRange(..., ctx.GetSessionVars().StmtCtx.TimeZone())`.
- The matched `update_time` predicates are removed from `remained`.
- The request sent to PD is a millisecond timestamp range.

`pkg/executor/memtable_reader.go`:

- `getHotRegionRowWithSchemaInfo` receives PD's millisecond timestamp.
- It calls `updateTimestamp := time.UnixMilli(hisHotRegion.UpdateTime)`.
- It then calls `updateTimestamp.In(tz)` but does not assign the returned value.
- It builds a SQL-visible `TIMESTAMP` from the unconverted `updateTimestamp`.

## Root Model

```text
P_check:  UPDATE_TIME predicates can be converted to a backend millisecond range using session tz.
Q_claim:  rows returned by that backend range will satisfy the SQL-visible UPDATE_TIME predicate.
D_dim:    request timezone and rendered row timezone must be the same SQL-visible context.
F_effect: original predicates are removed; returned rows bypass scalar recheck.
```

## Expected

Under `time_zone='+14:00'`, rows for the UTC instant `2026-07-02 23:40:41` should be rendered as
`2026-07-03 13:40:41`, or the original predicate must remain as a scalar recheck.

## Actual

The backend range is selected as if the user meant `+14:00`, but returned rows are rendered as
`2026-07-02 23:40:41`; the plain query returns rows whose projected predicate is false.

## Fix Direction

Assign the conversion result:

```go
updateTimestamp = updateTimestamp.In(tz)
```

Then verify:

- `+00:00` and `+14:00` equivalent absolute windows return rows whose displayed timestamps satisfy the predicate.
- CASE-wrapped reference equals the fast path.
- UTC/default timezone behavior remains unchanged.

Probe: `/Users/bba/pc/ai_native_hot_regions_history_timezone_probe.py`
