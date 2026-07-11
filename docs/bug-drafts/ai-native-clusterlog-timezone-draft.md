# Draft: cluster_log time extractor ignores session time_zone (id30012)
> 2026-07-02. Confirmed on testbed 8192975 (master `5c9198e948`). Selector S3 (shortcut/extractor lossy prefilter), second hit after id30010. Pause gate active.

## Minimal reproduction (deterministic)

Requires a session `time_zone` different from the server local zone (here server = UTC).
cluster_log has rows from pd/tikv heartbeats, so a recent 10-minute UTC window is always populated.

```sql
-- baseline: UTC window over recent logs
SET time_zone='+00:00';
SELECT COUNT(*) FROM information_schema.cluster_log
WHERE message LIKE '%' AND time>='2026-07-02 13:00:00' AND time<='2026-07-02 13:10:00';
-- 415

-- FORWARD BUG: same literals under +14:00 should target an absolute window 14h earlier
SET time_zone='+14:00';
SELECT COUNT(*) FROM information_schema.cluster_log
WHERE message LIKE '%' AND time>='2026-07-02 13:00:00' AND time<='2026-07-02 13:10:00';
-- 415  (identical row set — returns rows that violate the WHERE range)

-- REVERSE BUG: the +14:00 literal that, if tz-respected, targets the recent UTC window
SELECT COUNT(*) FROM information_schema.cluster_log
WHERE message LIKE '%' AND time>='2026-07-03 03:00:00' AND time<='2026-07-03 03:10:00';
-- 0  (drops rows that satisfy the WHERE range)
```

Symptom: cluster_log time filtering is wrong in both directions under any non-UTC session —
returns rows outside the requested window and misses rows inside it. This corrupts any log
investigation done from a client whose session time zone is not the server local zone
(the common case: server UTC, DBA session `+08:00`).

## Source chain

- `pkg/planner/core/memtable_predicate_extractor.go:816` — `ClusterLogTableExtractor.Extract`
  calls `extractTimeRange(ctx, schema, names, remained, "time", time.Local)` — **server local zone**.
- Sibling extractors all pass the **session** zone `ctx.GetSessionVars().StmtCtx.TimeZone()`:
  - `SlowQueryExtractor` :1334
  - `MetricTableExtractor` :1048
  - `StatementsSummaryExtractor` :1626 (via `findCoarseTimeRange` tz arg)
- `extractTimeRange` (:558-608) builds the absolute `time.Date(..., timezone).UnixNano()` with the
  passed zone, and for matched `GT/GE/LT/LE/EQ` predicates it does **not** re-append the predicate
  to `remained` (:610-626 — only `default` and non-matching branches append). So the extracted
  window is the sole filter; there is no scalar recheck to compensate for a wrong zone.

## Oracle

Absolute-instant equivalence: the same literal window under two different session zones must
select different absolute instants. Identical row sets across `+00:00`/`+14:00` prove the
extractor ignored the session zone. This is a row-return contract on a SQL-visible virtual
table, verified by two-directional differential — no plan-only evidence.

## Fix direction

Change `time.Local` to `ctx.GetSessionVars().StmtCtx.TimeZone()` at line 816, matching the three
sibling extractors. Fix validation: same-literal window under N distinct session zones must select
non-overlapping absolute windows; add a regression asserting cluster_log agrees with slow_query
time semantics under a non-UTC session.

## Assets

- Probe: `/Users/bba/pc/ai_native_clusterlog_timezone_probe.py` (SUMMARY total=1 findings=1;
  greens/reds carry trigger evidence — baseline non-empty check gates INVALID).
- Method case: `/Users/bba/pc/ai-native-id30012-method-case.md`.
- Selector ledger: S3 second hit — `/Users/bba/pc/ai-native-selector-ledger.md`.
