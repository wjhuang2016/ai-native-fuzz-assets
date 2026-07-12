# BR abort can delete a live restore by suppressing its heartbeat

## Summary

`FindAndDeleteMatchingTask` begins a pessimistic transaction and locks the matching restore-registry
row with `FOR UPDATE`. While retaining that lock, it waits up to five minutes for
`last_heartbeat_time` to change. The live restore writes its heartbeat through a separate session,
but that update needs the same row and conflicts with the abort transaction's lock.

The observer therefore suppresses the liveness signal it relies on, declares the still-running task
stale, and deletes its registry row. The caller then proceeds to clean the task's checkpoint data
and reports a successful abort.

Severity: **High**. Registry deletion against a live restore is execution-proven. The checkpoint
cleanup is a direct source-level caller consequence, but this harness did not execute a complete BR
restore command, so this record does not claim observed user-data corruption.

Bug library: `id1650002` (`confirmed`, root cause
`br-abort-lock-suppresses-live-heartbeat`).

## Source proof

- `br/pkg/registry/registration.go:254-284` keeps the callback inside one pessimistic transaction.
- `br/pkg/registry/registration.go:1001-1030` finds and locks the matching row with `FOR UPDATE`.
- `br/pkg/registry/registration.go:1041-1060` reads the initial heartbeat and calls the five-minute
  stale checker before releasing the transaction.
- `br/pkg/registry/registration.go:788-851` repeatedly reads the heartbeat through a separate
  restricted-SQL execution path and treats an unchanged value as stale.
- `br/pkg/registry/heartbeat.go:28-43` updates `last_heartbeat_time` on that same row through the
  dedicated heartbeat session; the production interval is 60 seconds.
- `br/pkg/registry/registration.go:1085-1093` deletes the task by ID after the stale decision.
- `br/pkg/task/restore.go:2907-2946` treats a nonzero deleted ID as authorization to clean all
  checkpoint managers and report a successful abort.

The proof shape is:

```text
P: FOR UPDATE stabilizes the row while abort decides whether deletion is safe.
Q: heartbeat observations still independently reveal whether the restore owner is alive.
F: the observer's lock blocks the heartbeat writer, manufacturing unchanged-heartbeat evidence.
```

## Deterministic real-TiKV reproduction

The test-only hooks change only scheduling and elapsed time:

1. shorten the one-minute ticker to one second and the five observations to two;
2. prove that the same background heartbeat advances before the abort transaction starts;
3. pause immediately after the abort transaction locks the matching row;
4. continue the same heartbeat writer and let the real stale/delete path finish;
5. require `deleted_id=0` and registry row count `1` for a live owner.

Run:

```bash
go test -v -run '^TestAINativeAbortDeletesLiveRestoreRED$' \
  -count=3 --tags=intest ./tests/realtikvtest/brregistrytest/...
```

All three runs follow the same chain:

```text
pre-lock: task heartbeat updated, task is active
post-lock: [kv:9007] Write conflict on mysql.tidb_restore_registry
two unchanged observations: task is stale
abort: successfully deleted matching task
oracle: deleted_id != 0 and registry row count = 0 (RED)
```

## Control

The same compressed stale window with no heartbeat writer passes three times: the genuinely stale
task is deleted and the registry row count becomes zero. This proves the instrumentation preserves
the intended stale-task behavior.

| Owner state | Observer lock | Signal result | Abort result | Verdict |
|---|---|---|---|---|
| live | absent during first check | heartbeat advances | retained | GREEN altitude |
| live | held during stale check | write conflict | deleted | RED, 3/3 |
| stale | normal abort path | no heartbeat exists | deleted | GREEN, 3/3 |

## User-visible trigger

A practical trigger is an operator issuing a matching PITR `restore point --abort` command while
the original restore process is still running and heartbeating. After the five-minute stale window,
the abort command can remove the live registration and clean its checkpoints even though the
original restore process has not stopped.

## Fix direction

Do not retain the registry-row lock while observing liveness. Observe the heartbeat outside the
transaction, then acquire the lock and use a conditional status transition or delete that rechecks
the expected heartbeat and status. Another valid design is to store the liveness signal in state
whose writer cannot be blocked by the observer lock.

A regression test must include a real heartbeat writer, prove pre-lock progress, and assert live-row
retention. A test containing only a stale row cannot detect this self-suppression root.
