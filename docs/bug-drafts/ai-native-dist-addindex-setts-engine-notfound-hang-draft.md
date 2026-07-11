# Distributed ADD INDEX hangs on persistent SetTSBeforeImportEngine engine-not-found

## Status

- Remote `found_bug`: id1350002
- Severity: high
- Status: confirmed
- Root cause id: `dist-addindex-runtime-fundamental-retry-hang`

## User-visible shape

During distributed ingest-mode `ADD INDEX`, a source-native runtime fundamental error can leave the
DDL job `running` in `write reorganization` instead of failing or rolling back. The user-visible
symptom is a hanging `ALTER TABLE ... ADD INDEX` that resumes only after the fault is removed.

This is high-severity because it hits long-running online schema change, blocks the client, and
keeps retrying a path that the inner import layer already considers un-retryable.

## Live matrix

Environment:

- testbed `8220955`
- commit-matched failpoint owner worktree: `/private/tmp/fp-build-5c9198`
- cluster commit: `v9.0.0-beta.2.pre-1895-g5c9198e948`
- DDL knobs during the probe:
  - `tidb_enable_dist_task=ON`
  - `tidb_ddl_enable_fast_reorg=ON`

Injected point:

```text
github.com/pingcap/tidb/pkg/ingestor/ingestctrl/mockAINativeSetTSBeforeImportEngineErr
```

The failpoint is placed inside `SetTSBeforeImportEngine` and can return the exact source-native
shape:

```text
engine %s not found in SetTSBeforeImportEngine
```

Observed matrix:

1. Baseline GREEN
   - 5k-row distributed `ADD INDEX`
   - job `2313`
   - final state: `synced`
2. One-shot GREEN
   - failpoint: `1*return("engine_not_found")`
   - same workload shape
   - job `2322`
   - final state: `synced`
3. Persistent RED
   - failpoint: `return("engine_not_found")`
   - same workload shape
   - job `2319`
   - `ADMIN SHOW DDL JOBS`: `running`, `write reorganization`
   - `mysql.tidb_global_task`: task `270003`, `running`, `step=1`, `type=backfill`
   - client `ALTER TABLE ... ADD INDEX` blocked until the fault was removed
4. Fault-removed GREEN
   - delete the failpoint
   - the same held RED job `2319` immediately finished `synced`

This is the important shape: same owner, same point, same source-native error, but one-shot GREEN
and persistent RED.

## Log contradiction that closes the case

The owner log shows a direct contradiction across layers:

```text
set TS failed
engine ... not found in SetTSBeforeImportEngine
meet un-retryable error
meet retryable error
subtask in running state and is idempotent
run subtask start
```

That is the bug in one screen:

```text
inner import/lightning layer: not retryable
outer DXF task executor: retryable, rerun
```

## Source proof

Relevant source anchors:

- `/private/tmp/fp-build-5c9198/pkg/ingestor/ingestctrl/local.go`
  - `SetTSBeforeImportEngine`
- `/Users/bba/pc/tidb/pkg/ddl/backfilling_import_cloud.go`
  - plain runtime fundamental import/setup errors
- `/Users/bba/pc/tidb/pkg/ddl/backfilling_dist_executor.go`
  - `backfillDistExecutor.IsRetryableError`
- `/Users/bba/pc/tidb/pkg/dxf/framework/taskexecutor/task_executor.go`
  - retryable/idempotent subtasks stay `running` and rerun
- `/Users/bba/pc/tidb/pkg/lightning/common/retry.go`
  - source of the inner retryability judgment

The likely fix locus is the distributed backfill retry classifier:

```text
backfillDistExecutor.IsRetryableError
  -> common.IsRetryableError(err) || isRetryableError(err, true)
```

That `unknown=true` fallthrough is too permissive for source-native runtime fundamentals.

## Severity call

This clears the current severe bar:

1. it hits a real online DDL path, not just a unit-only probe;
2. the user symptom is a hang / retry wedge, not a cosmetic wrong-error;
3. the one-shot GREEN control proves the DDL shape itself is otherwise healthy;
4. the persistent RED plus fault-removal GREEN proves the system is choosing the wrong recovery
   path, not simply suffering from a dead environment.

## Fix direction

Candidate repair directions:

1. tighten the distributed backfill retry classifier so source-native import/setup fundamentals do
   not fall through the unknown-retryable path;
2. explicitly blacklist shapes such as `engine not found`, `local backend not found`,
   `engine not started`, and similar setup/runtime fundamentals before the idempotent rerun path;
3. preserve stronger retryability identity between the import path and the DXF framework instead of
   collapsing everything into a broad retryable bucket.

## Method value

This was a very strong methodology proof:

1. start from a source-native proof obligation, not from random fault injection;
2. compress it into a tiny same-altitude matrix;
3. use a strong liveness oracle;
4. stop after the first real RED/green boundary is explained.

That is a better severe-bug loop than widening chaos or enumerating many sibling DDLs too early.
