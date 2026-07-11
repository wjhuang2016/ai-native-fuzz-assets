# perf-30003 / id30014: interrupted partitioned ANALYZE can leave a stale `running` analyze job with a dead `process_id`

## Status

Confirmed on testbed `8192975` / `fp-tidb` with failpoint control.

Severity: low. This is not a wrong-result bug and a clean rerun of `ANALYZE TABLE` can finish. The user-visible harm is stale progress/observability state: `SHOW ANALYZE STATUS` and `mysql.analyze_jobs` keep reporting an old partition job as `running` after the client already received `Query execution was interrupted` and the SQL process no longer exists.

Bug-library state: inserted into remote `found_bug` as id30014, confirmed.

## User-visible symptom

After killing a partitioned `ANALYZE TABLE` while one partition result is still between the analyze worker and the save worker:

- the client returns `ERROR 1317 (70100): Query execution was interrupted`;
- `information_schema.processlist` has no row for the killed connection ID;
- `mysql.analyze_jobs` still has one partition row in `running` with that dead `process_id`;
- `SHOW ANALYZE STATUS` shows the stale row as `running`, with no `end_time`, no `fail_reason`, and a nonsensical huge remaining time;
- a subsequent clean `ANALYZE TABLE` appends new `finished` rows but does not clear the old `running` row immediately.

Second clean repro evidence:

```text
after_interrupt analyze client:
  rc=1
  stderr=ERROR 1317 (70100) at line 1: Query execution was interrupted

processlist liveness:
  SELECT COUNT(*) FROM information_schema.processlist WHERE id=2889894344 -> 0

mysql.analyze_jobs:
  60162 p0 finished 5000 NULL
  60163 p1 finished 5000 NULL
  60164 p2 running  0    2889894344
  60165 p3 failed   5000 [executor:1317]Query execution was interrupted

SHOW ANALYZE STATUS:
  ai_perf_analyze_interrupt2 t p2 ... processed_rows=0 state=running
    process_id=2889894344 remaining_time=40006h29m27s
```

After a clean rerun, new p0..p3/global merge rows are `finished`, but old job `60164 / p2` remains `running`.

## Minimal repro harness

Reusable probe:

```bash
kubectl --kubeconfig=/Users/bba/pc/kubeconfig.yml port-forward pod/fp-tidb 14000:4000 18080:10080
python3 /Users/bba/pc/ai_native_perf_pf6_analyze_interrupt.py --db ai_perf_analyze_interrupt2
```

The probe uses this failpoint timing:

```text
github.com/pingcap/tidb/pkg/executor/analyzeBeforeSendToSaveResults = 2*off->pause
```

That lets the first two partition results proceed, then pauses a later result before it is sent into the save worker. The probe then issues `KILL QUERY <analyze_process_id>`, clears the failpoint, waits for the client result, and checks both `processlist` and `SHOW ANALYZE STATUS`.

## Source chain

`pkg/executor/analyze.go`:

- `analyzeWorker` calls `statsHandle.StartAnalyzeJob(task.job)` before doing partition work.
- In `handleResultsErrorWithConcurrency`, the main loop checks `SQLKiller.HandleSignal()` before receiving/sending each result. On kill it closes `saveResultsCh` and returns.
- The failpoint `analyzeBeforeSendToSaveResults` is immediately before `saveResultsCh <- results`.

Relevant shape:

```text
StartAnalyzeJob(task.job)
...
handleGlobalStats(...)
failpoint analyzeBeforeSendToSaveResults
saveResultsCh <- results
```

`pkg/executor/analyze_worker.go`:

- save worker only calls `finishJobWithLog` for results it receives from `saveResultsCh`;
- in drain mode it marks remaining queued results failed, but it cannot finish a result that the main loop never sent.

Therefore, when a job has been started and its result is paused before `saveResultsCh <- results`, a kill can make the main loop return without calling `finishJobWithLog` for that job. The existing corrupted-job cleanup has a delayed `>10 minutes` current-instance cleanup path, but the interrupted user query itself has already completed with visible stale state.

## Proof Obligation

P_check:
ANALYZE tracks every partition/global analyze sub-job in `mysql.analyze_jobs`, and each sub-job transitions `pending -> running -> finished/failed`.

Q_claim:
Once the parent `ANALYZE TABLE` returns, every sub-job started by that statement is terminal (`finished` or `failed`) and no row remains `running` with a dead SQL `process_id`.

F_effect:
The save pipeline assumes `finishJobWithLog` is owned by the save worker for successfully produced results. If interruption happens before enqueue, the started job can bypass both normal save completion and drain-mode failure completion.

Oracle:
Use a liveness oracle, not only job table state:

```sql
SELECT COUNT(*) FROM information_schema.processlist WHERE id = <process_id>;
SELECT id, partition_name, state, process_id FROM mysql.analyze_jobs
  WHERE table_schema='<db>' AND table_name='t' AND state IN ('pending','running');
SHOW ANALYZE STATUS;
```

Red condition:
`processlist` count is `0`, but `mysql.analyze_jobs` / `SHOW ANALYZE STATUS` still reports `running` for that process ID after the client received `ERROR 1317`.

## Fix direction

The invariant should be "every job that reached `StartAnalyzeJob` must eventually reach `FinishAnalyzeJob` when the parent statement exits."

Possible fixes:

- when `SQLKiller.HandleSignal()` fires in the main results loop, explicitly finish the current unsent result's job as failed before returning;
- or move ownership of started-job finalization into a defer / task lifecycle wrapper so every started job has exactly one terminal update, whether the result was saved, drained, or interrupted before enqueue;
- extend `TestAnalyzeKillDuringSaveDoesNotHang` to assert no `pending`/`running` rows remain for the analyzed table and no `SHOW ANALYZE STATUS` stale running row remains after the killed statement returns.

## Method Lesson

This hit refines PS1.

Earlier PS1 was too checkpoint-shaped: "background loop interrupted, progress reconstructed on re-entry, no durable checkpoint -> rework." That found `perf-30001` and `perf-30002`, but ANALYZE did not show a rework bug. The better selector is:

```text
background/internal multi-task pipeline
where sub-jobs have visible lifecycle rows
and a parent interruption can occur after StartAnalyzeJob but before the result enters the component that owns FinishAnalyzeJob
```

The strong oracle is not row-count conservation. It is lifecycle coverage:

```text
every started visible sub-job must be terminal when the parent operation returns
```

That is why this works: AI read the source and found a narrow ownership gap (`StartAnalyzeJob` in one component, `FinishAnalyzeJob` in another, a kill check before handoff), then compressed it into a 4-partition / third-result pause matrix with a processlist-backed oracle.
