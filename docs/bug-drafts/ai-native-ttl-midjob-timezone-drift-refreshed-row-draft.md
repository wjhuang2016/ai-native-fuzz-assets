# TTL can delete a refreshed DATETIME row when global time_zone changes during a job

## Summary

A TTL job stores its expiration point as an epoch, but scan and delete statements independently
render that epoch with `FROM_UNIXTIME(...)` after resetting their sessions to the current global
`time_zone`. If the global time zone changes between scan and delete, the same job epoch becomes a
different wall-clock cutoff for a `DATETIME` TTL column.

An application can refresh a row beyond the cutoff used by the scan, yet the delete-time safety
predicate can expand under the new time zone and silently delete that refreshed row. The TTL job
finishes successfully, so the data loss is not surfaced as an error.

Severity: **High**. The trigger requires a global time-zone change during an active TTL job and a
concurrent refresh in the shifted cutoff window, but the consequence is silent deletion of current
user data.

Bug library: `id1620002` (`confirmed`, root cause
`ttl-midjob-timezone-context-drift`).

## Source proof

- `pkg/ttl/sqlbuilder/sql.go:170-188` emits `ttl_col < FROM_UNIXTIME(expire.Unix())` for both scan
  and delete SQL.
- `pkg/ttl/ttlworker/session.go:244-266` resets each TTL statement to the then-current global time
  zone immediately before execution.
- `pkg/ttl/ttlworker/session.go:286-332` validates table and TTL metadata but does not validate the
  time-zone context. Its pointer-equality path returns immediately.
- `docs/design/2022-11-17-ttl-table.md` says the delete predicate must protect a row refreshed after
  scan and that a TTL job must notice `time_zone` changes in time to prevent unexpected deletion.

The code proves only that both phases carry the same epoch (`P`), then assumes they evaluate the
same `DATETIME` cutoff (`Q`). `FROM_UNIXTIME` makes `Q` depend on mutable session context that is
reloaded independently in each phase.

## Actual-worker reproduction

Use a non-partition table and pause a test TiDB immediately before the TTL delete statement calls
`ExecuteSQLWithCheck`. The pause is after scan has produced the row handle but before delete resets
its session time zone.

```sql
SET GLOBAL time_zone = '+00:00';
SET GLOBAL tidb_ttl_job_enable = ON;

CREATE DATABASE ai_ttl_tz_worker;
CREATE TABLE ai_ttl_tz_worker.t (
  id BIGINT PRIMARY KEY,
  ts DATETIME NOT NULL
) TTL = ts + INTERVAL 1 MINUTE TTL_JOB_INTERVAL = '1h';

SET time_zone = '+00:00';
INSERT INTO ai_ttl_tz_worker.t VALUES (1, NOW() - INTERVAL 1 DAY);
```

Trigger a manual TTL job and wait until the delete worker reaches the pause. Read the job cutoff:

```sql
SELECT current_job_ttl_expire
FROM mysql.tidb_ttl_table_status
WHERE table_id = TIDB_TABLE_ID('ai_ttl_tz_worker', 't');
```

For the observed job, the cutoff was `2026-07-12 18:23:32` under UTC. Refresh the selected row to
four hours after that cutoff, then change the global time zone:

```sql
SET time_zone = '+00:00';
UPDATE ai_ttl_tz_worker.t
SET ts = '2026-07-12 22:23:32'
WHERE id = 1;

-- Under the scan context this returns 0: the row is current.
SELECT ts < FROM_UNIXTIME(1783880612)
FROM ai_ttl_tz_worker.t WHERE id = 1;

SET GLOBAL time_zone = '+08:00';
```

Release the delete worker. It resets to `+08:00`, where the same epoch renders as
`2026-07-13 02:23:32`, and deletes the row:

```sql
SELECT COUNT(*) FROM ai_ttl_tz_worker.t;
-- 0
```

The TTL job reports normal completion.

## Controls

1. **No context drift:** repeat the same actual-worker schedule and refresh the row to cutoff plus
   four hours, but keep global `time_zone='+00:00'`. The job completes and the row remains.
2. **Historical #41043 scenario:** insert rows under `Asia/Shanghai`, switch to UTC before the job
   starts, and run TTL. Current source preserves the expected rows, so the old pre-job bug fixed by
   #41044 remains green.

The second control is important: this finding was derived from current-source proof obligations,
then compared with history only after the worker RED. It is an uncovered mid-job context-stability
failure, not a replay of #41043's pre-job cutoff-rendering bug.

## Fix direction

Pin the expiration interpretation context for the entire TTL job, or store a wall-clock cutoff for
`DATE`/`DATETIME` together with the time-zone identity used to derive it. Before every scan/delete
statement, either execute with that pinned context or abort/restart the job when global time zone
differs.

A regression test must synchronize between scan and delete, refresh the row beyond the original
cutoff, change global time zone, and prove that the row survives. A test that changes time zone only
before job creation does not cover this bug.
