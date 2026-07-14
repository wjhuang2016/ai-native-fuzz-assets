# Pessimistic retry can commit a row from two different attempts

Status: confirmed on current source and real TiKV; filed as
[TiDB #69826](https://github.com/pingcap/tidb/issues/69826); remote `found_bug id2430003`;
high consequence with a narrower materialized-CTE trigger.

## Summary

A pessimistic READ COMMITTED `UPDATE` can finish a materialized CTE, hit a retryable unique-key
conflict, and rebuild its executor without rebuilding the CTE storage. The successful retry reads a
fresh ordinary source row but reuses the failed attempt's completed CTE result. TiDB reports success
and commits one row assembled from two different attempts.

The confirmed row is `(id=1,u=2,v=10)`: `u=2` comes from the retry snapshot, while `v=10` comes from
the failed attempt. One execution from the same final database state commits `(1,2,20)`.

## P/Q/F

- **P**: `StmtRollback`, `ResetForRetry`, and executor rebuild create an observationally fresh
  statement attempt.
- **Q**: statement-owned materializations are also rebuilt from the successful attempt's read
  state.
- **F**: `StatementContext.CTEStorageMap` and its `sync.Once` survive retry. A completed `resTbl` is
  deliberately preserved by `CTEExec.Close`, so `buildCTE` loads it and skips producer creation.

## Compressed matrix

| Cell | Retry count | Row 1 | Meaning |
| --- | ---: | --- | --- |
| Natural conflict + materialized CTE | 1 | `(1,2,10)` | mixed failed/retry state |
| Same final DB state, direct execution | 0 | `(1,2,20)` | one coherent execution |
| Counterfactual: reset CTE storage before rebuild | 1 | `(1,2,20)` | owner-level GREEN |

The CTE is referenced twice, and `EXPLAIN` contains `CTEFullScan`. A `SLEEP` inside the CTE creates
the schedule window; no fault injection is used.

## Real TiKV evidence

The SQL-only reproduction returned success and persisted:

```text
retry rows:   (1,2,10),(2,1,0)
control rows: (1,2,20),(2,1,0)
```

The slow log records `Exec_retry_count=1`, `Exec_retry_time=20.00218629`,
`Query_time=20.005502414`, `Succ=1`, and `IsExplicitTxn=1`. Metadata locking stayed enabled, and
the test schema was removed afterward.

## Source ownership

- `pkg/executor/select.go`: initializes or clears `CTEStorageMap` only at statement-context setup.
- `pkg/executor/builder.go`: `buildCTE` loads the map entry and guards producer construction with
  `initOnce`.
- `pkg/executor/cte.go`: completed `resTbl` data survives executor close.
- `pkg/sessionctx/stmtctx/stmtctx.go`: `ResetForRetry` omits CTE storage.
- `pkg/executor/adapter.go`: retry builds another executor against the same context.

## Counterfactual

Before retry executor construction, close the old CTE storage and replace the map with an empty one.
The exact natural-conflict test then commits `(1,2,20)` and matches the direct control. This proves
the stale materialization owner, rather than conflict timing or RC itself, causes the wrong row.

## Quality

The consequence is silent durable wrong data and a within-row snapshot-coherence violation, so it
is C3/high. The trigger requires a materialized CTE in retryable pessimistic DML and a concurrent
conflict after materialization. Do not enumerate recursive/nonrecursive CTEs, consumer counts, DML
forms, sleep durations, or conflict shapes under this root.

