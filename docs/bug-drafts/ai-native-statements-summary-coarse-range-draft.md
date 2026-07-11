# Draft: statements_summary coarse time-range skip drops satisfiable interval-overlap predicates (id30021)
> 2026-07-03. Confirmed on testbed 8192975 / fp-tidb. Selector S3, new sub-shape: interval-overlap coarse skip. This is not the `valueToLower=true` helper family.

## Minimal Reproduction

Pick one real statement-summary window. On the testbed, statement summary used a 30-minute window:

```sql
SET time_zone = '+00:00';

SELECT summary_begin_time, summary_end_time
FROM information_schema.statements_summary
WHERE TIMESTAMPDIFF(SECOND, summary_begin_time, summary_end_time) >= 60
ORDER BY summary_end_time DESC
LIMIT 1;
-- 2026-07-02 23:00:00  2026-07-02 23:30:00
```

Choose `A` and `B` inside that window with `A < B`:

```text
A = 2026-07-02 23:10:00
B = 2026-07-02 23:20:00
```

Fast path:

```sql
EXPLAIN
SELECT digest, summary_begin_time, summary_end_time
FROM information_schema.statements_summary
WHERE summary_begin_time <= TIMESTAMP '2026-07-02 23:10:00'
  AND summary_end_time >= TIMESTAMP '2026-07-02 23:20:00'
LIMIT 3;
-- MemTableScan ... skip_request: true

SELECT COUNT(*)
FROM information_schema.statements_summary
WHERE summary_begin_time <= TIMESTAMP '2026-07-02 23:10:00'
  AND summary_end_time >= TIMESTAMP '2026-07-02 23:20:00';
-- 0
```

CASE-wrapped reference:

```sql
SELECT COUNT(*) AS n,
       SUM(summary_begin_time <= TIMESTAMP '2026-07-02 23:10:00') AS begin_ok,
       SUM(summary_end_time >= TIMESTAMP '2026-07-02 23:20:00') AS end_ok
FROM information_schema.statements_summary
WHERE CASE WHEN summary_begin_time <= TIMESTAMP '2026-07-02 23:10:00' THEN TRUE ELSE FALSE END
  AND CASE WHEN summary_end_time >= TIMESTAMP '2026-07-02 23:20:00' THEN TRUE ELSE FALSE END;
-- n>0, begin_ok=n, end_ok=n
```

Green overlap control:

```sql
SELECT COUNT(*)
FROM information_schema.statements_summary
WHERE summary_begin_time <= TIMESTAMP '2026-07-02 23:20:00'
  AND summary_end_time >= TIMESTAMP '2026-07-02 23:10:00';
-- n>0, and EXPLAIN does not show skip_request:true
```

## User-Visible Symptom

The user asks for statement-summary windows that cover a target interval:

```text
summary_begin_time <= A
AND summary_end_time >= B
```

When `A < B`, this is a normal interval-containment predicate. A row with summary window `[23:00, 23:30]` satisfies `A=23:10, B=23:20`. TiDB nevertheless returns zero rows because the extractor classifies the coarse request as impossible.

## Probe Result

Probe: `/Users/bba/pc/ai_native_statements_summary_coarse_range_probe.py`

```text
FINDING  statements_summary_coarse_range  statements_summary skipped satisfiable interval-overlap predicates: window=[2026-07-02 23:00:00,2026-07-02 23:30:00], A=2026-07-02 23:10:00, B=2026-07-02 23:20:00, fast=['FAST\t0'], ref=['REF\t34\t34\t34'] (live count may grow on rerun), green=['GREEN\t34'] (live count may grow on rerun)
SUMMARY total=1 findings=1 skipped=0
```

## Source Chain

- `pkg/planner/core/memtable_predicate_extractor.go:1556-1561`: `StatementsSummaryExtractor` records a coarse time range for statement-summary tables.
- `pkg/planner/core/memtable_predicate_extractor.go:1580-1588`: if the derived coarse range has `StartTime > EndTime`, it sets `SkipRequest=true` and returns no rows.
- `pkg/planner/core/memtable_predicate_extractor.go:1619-1628`: `findCoarseTimeRange` derives `endTime` from `summary_begin_time` predicates and `startTime` from `summary_end_time` predicates.
- `pkg/infoschema/tables.go:1322-1324`: `summary_begin_time` and `summary_end_time` are SQL-visible timestamp columns.
- `pkg/util/stmtsummary/statement_summary.go:333-341`: statement summary windows have a refresh interval, so rows naturally cover nonzero time spans.

## Root Cause

```text
P_check:
  summary_begin_time <= A gives a coarse upper bound A
  summary_end_time >= B gives a coarse lower bound B

Q_claim:
  if B > A, no statement-summary row can satisfy the query

F_effect:
  MemTableScan sets skip_request:true and bypasses normal scalar evaluation
```

The missing semantic dimension is interval overlap/containment. A row is not a point. A row with `[begin,end]` can satisfy `begin <= A AND end >= B` exactly when it spans `[A,B]`, so `B > A` is not contradictory.

## Expected Behavior

The fast path must return the same rows as the SQL-visible predicates. If the coarse range is only an optimization hint, it must be a safe superset and the original predicates must remain available for scalar recheck. It must not convert a satisfiable interval-containment predicate into `skip_request:true`.

## Fix Direction

Options:

- do not set `SkipRequest` for the `summary_begin_time <= A AND summary_end_time >= B` overlap/containment shape when `B > A`;
- or derive a safe coarse scan range that still includes windows spanning `[A,B]`;
- or disable this coarse skip and rely on the retained scalar predicates for correctness.

## Methodology Note

This is a new S3 sub-shape:

```text
coarse shortcut range
+ row represents an interval, not a point
+ original predicates are kept but SkipRequest bypasses them
+ CASE reference proves satisfiable rows exist
```

The efficient move was not fuzzing many statement-summary predicates. It was reading the proof encoded in comments:

```text
summary_begin_time <= endTime AND summary_end_time >= startTime
```

and asking whether `startTime > endTime` is truly impossible for interval rows.
