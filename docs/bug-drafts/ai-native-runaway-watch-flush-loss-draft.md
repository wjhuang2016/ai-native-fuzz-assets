# Runaway watch rules are silently lost after a transient batch flush error

Status: confirmed high severity on current source and testbed `8220955`; remote `found_bug id1740003`.

## User impact

When a TiDB detects a runaway query with `WATCH EXACT/PLAN/SIMILAR`, it applies the rule in local
memory and asynchronously persists it so other TiDB nodes can enforce the same KILL, COOLDOWN, or
SWITCH_GROUP policy. If that batch write has one transient SQL error, the flusher discards the
entire batch. The detecting node appears protected, while other nodes silently continue running
the quarantined query. This failure is most likely during the same storage or load incident that
runaway protection is intended to contain.

## Source proof

- `pkg/resourcegroup/runaway/manager.go:272-278` installs the rule locally, then hands its only
  durable copy to `quarantineChan`.
- `pkg/resourcegroup/runaway/flusher.go:127-139` observes `flushFn` failure and increments an error
  metric, but unconditionally replaces `f.buffer`.
- No retry queue, WAL, or reconciliation owner retains the failed records.

P: a detected watch rule must be durably published for every TiDB.

Q: a transient publication failure leaves the batch owned by the flusher for retry.

F: the error path destroys the buffer, so the next flush has no record to retry.

## Reproduction

Use two TiDB frontends sharing one PD/TiKV cluster. Inject one error only at frontend A's
`quarantine-record` flush.

```sql
SET GLOBAL tidb_enable_resource_control='ON';
CREATE RESOURCE GROUP ai_rg_flush RU_PER_SEC=1000
  QUERY_LIMIT=(EXEC_ELAPSED='100ms' ACTION=KILL WATCH EXACT DURATION '10m');

-- Run on A; it is detected and locally watched.
SELECT /*+ RESOURCE_GROUP(ai_rg_flush) */ SLEEP(0.3) AS ai_native_watch_probe;

-- Prevent later executions from independently crossing the threshold.
ALTER RESOURCE GROUP ai_rg_flush RU_PER_SEC=1000
  QUERY_LIMIT=(EXEC_ELAPSED='24h' ACTION=KILL WATCH EXACT DURATION '10m');

SELECT * FROM mysql.tidb_runaway_watch
WHERE resource_group_name='ai_rg_flush';

-- Run the exact original SELECT once on A and once on B.
```

Actual: the shared watch table is empty; A returns `ERROR 8254`, while B completes `SLEEP(0.3)`
normally. Expected: the failed batch remains pending, is persisted after recovery, and both nodes
return `ERROR 8254`.

The no-fault control generated the same rule on B, persisted one watch row, and both A and B
returned `ERROR 8254` after synchronization.

## Fix direction

Clear a batch only after successful publication. On failure, retain it for retry, or transfer it to
another durable retry owner. New arrivals must merge without overwriting failed entries. Add a
failed-then-healthy test that observes the second publication attempt and a cross-node sync test.

Post-RED asset and GitHub issue searches found no exact duplicate.
