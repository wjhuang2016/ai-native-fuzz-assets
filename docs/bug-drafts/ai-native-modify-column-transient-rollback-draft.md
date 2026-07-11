# MODIFY COLUMN rolls back on single transient connection-family errors that ADD INDEX retries through

## Status

- Remote `found_bug`: id1350001
- Severity: high
- Status: confirmed
- Root cause id: `modify-column-reorg-transient-unknown-fatal`

## User-visible shape

During online `ALTER TABLE ... MODIFY/CHANGE COLUMN`, a single transient connection-style failure
inside the reorg backfill window can make the whole DDL fail and roll back immediately.

Confirmed user-visible error shapes include:

```text
[ddl:-1]invalid connection
[ddl:-1]driver: bad connection
[ddl:-1]read tcp: read: connection reset by peer
[ddl:-1]write tcp: write: broken pipe
[ddl:-1]dial tcp: connect: connection refused
```

The sibling `ADD INDEX` path, under the same one-shot injected transient errors, retries and
finishes `synced`.

This is high-severity because `MODIFY COLUMN` is often a long-running online DDL on large tables:
a short network/socket hiccup can waste the whole reorg instead of being absorbed by retry.

## Strong live + local evidence

Live bridge-level proof on testbed `8220955` (commit-matched failpoint owner lane):

- `context_deadline_exceeded` is a strong GREEN control on both siblings:
  - `ADD INDEX` job `1755` -> `synced`
  - `MODIFY COLUMN` job `1758` -> `synced`
- `driver_bad_conn` hits a clean split:
  - `ADD INDEX` job `1761` -> `synced`
  - `MODIFY COLUMN` job `1764` -> `rollback done`
- `net_conn_reset` hits the same split:
  - `ADD INDEX` job `1767` -> `synced`
  - `MODIFY COLUMN` job `1770` -> `rollback done`
- earlier bridge-proximal `grpc unavailable` was already split the same way:
  - `ADD INDEX` job `1723` -> `synced`
  - `MODIFY COLUMN` job `1726` -> `rollback done`

This is the decisive quality upgrade over the earlier draft: the bug is no longer only a
confirmed-local classifier asymmetry. It is now a live owner-lane red/green split on the same
bridge and the same transient-fault family.

End-to-end probes:

- `/Users/bba/pc/tidb/pkg/ddl/ai_native_reorg_grpc_probe_test.go`
  - `TestAINativeAddIndexRetriesTransientConnErrorFamilyProbe`
  - `TestAINativeModifyColumnFailsTransientConnErrorFamilyProbe`
- injected shapes:
  - `mysql_invalid_conn`
  - `driver_bad_conn`
  - `net_conn_reset`
  - `net_broken_pipe`
  - `net_conn_refused`

Verified with:

```text
go test --tags=intest ./pkg/ddl \
  -run 'TestAINative(AddIndexRetriesTransientConnErrorFamilyProbe|ModifyColumnFailsTransientConnErrorFamilyProbe)$' \
  -v -count=1
```

Observed:

- `ADD INDEX`:
  - logs `run DDL job failed, sleeps a while then retries it`
  - later finishes `State:synced`
- `MODIFY COLUMN`:
  - logs transition to `State:rollingback`
  - terminal state `rollback done`
  - SQL session returns the raw transient connection error to the user

Representative captured log points:

```text
ADD INDEX / net_conn_reset:
  error="read tcp: read: connection reset by peer"
  run DDL job failed, sleeps a while then retries it
  ...
  finish DDL job ... State:synced

MODIFY COLUMN / net_conn_refused:
  error="dial tcp: connect: connection refused"
  job State:rollingback
  terminal State:rollback done
```

Additional sibling proof already existed for gRPC-family shapes:

- single `grpc unavailable`: add-index PASS / modify-column rollback
- single `grpc dataloss`: add-index PASS / modify-column rollback

So this is not a one-off error string problem. It is a broader transient-error family.

## Source proof

The retry split is explicit in source:

- `/Users/bba/pc/tidb/pkg/ddl/index.go`
  - `isRetryableJobError(...){ return isRetryableError(err, true) }`
- `/Users/bba/pc/tidb/pkg/ddl/modify_column.go`
  - `isRetryableModifyColumnReorgJobError(...){ return isRetryableError(err, false) }`
- `/Users/bba/pc/tidb/pkg/ddl/job_worker.go`
  - outer worker retry logging happens after the updated job state is committed
  - this means a generic `sleeps a while then retries it` log can still be emitted even when the
    inner modify-column path has already flipped the job to `rollingback`
  - `toTError` synthesizes foreign errors into generic DDL unknown errors

The local classifier probe:

- `/Users/bba/pc/tidb/pkg/ddl/ai_native_retry_probe_test.go`

already showed the risky family:

```text
grpc_unavailable / grpc_dataloss / mysql_invalid_conn / driver_bad_conn /
net_conn_reset / net_broken_pipe / net_conn_refused

raw=true
ddl_synth=false
```

Meaning:

```text
P: backfill sees a transient foreign error that ordinary recovery code would treat as retryable.
Q: DDL preserves that retryability when routing the error through the reorg retry gate.
F: MODIFY COLUMN uses retryUnknown=false, so unknown foreign transient errors become fatal.
```

## Retry-log trap

One subtle but important observation from the preserved logs:

- `MODIFY COLUMN` does sometimes emit the outer generic retry log:
  - `run DDL job failed, sleeps a while then retries it.`
- but on the next persisted step the same job is already:
  - `State:rollingback`
  - then `State:rollback done`

So the retry log is not proof of recovery. It is only an outer worker sleep after the inner
`modify_column.go` handler has already decided to roll the job back.

Representative local evidence:

- `/Users/bba/pc/ai-native-assets/logs/modify-column-transient-family-local.log`
  - invalid connection: outer retry log at `6598`, then `rollingback` at `6607`, `rollback done`
    at `6610`
  - connection refused: outer retry log at `10326`, then `rollingback` at `10335`,
    `rollback done` at `10338`
- `/Users/bba/pc/ai-native-assets/logs/modify-column-transient-grpc-siblings-local.log`
  - grpc unavailable: `rollingback` at `2704`, `rollback done` at `2707`
  - grpc dataloss: `rollingback` at `4599`, `rollback done` at `4602`

By contrast the sibling `ADD INDEX` logs a retry and really does recover:

- `/Users/bba/pc/ai-native-assets/logs/modify-column-transient-family-local.log`
  - invalid connection: retry at `1786`, terminal `State:synced` at `1831`
  - connection refused: retry at `5630`, terminal `State:synced` at `5675`
- `/Users/bba/pc/ai-native-assets/logs/modify-column-transient-grpc-siblings-local.log`
  - grpc unavailable: retry at `1733`, terminal `State:synced` at `1778`
  - grpc dataloss: retry at `3628`, terminal `State:synced` at `3673`

## Why the live network-chaos green sample does not kill this bug

The live `owner -> TiKV NetworkChaos` probe on the testbed was GREEN: it froze progress, then
recovered. That is useful, but it only proves coarse recovery under infrastructure disturbance.

It does **not** prove the semantic retry bridge is correct, because:

- coarse infra faults validate `S-STATE` / `S-LIFE`
- bridge-proximal one-shot transient errors validate `S-ERR` / `S-RETRY`

This bug lives in the second lane.

## Fix direction

Do not let `MODIFY COLUMN` reorg treat unknown foreign transient errors as immediately fatal.

Candidate fixes:

1. Teach `isRetryableModifyColumnReorgJobError` to preserve retryability for the known transient
   foreign error family.
2. Narrow the fatal-only set to deterministic conversion/constraint errors, instead of making
   generic unknown foreign errors non-retryable.
3. Preserve richer transient/fatal identity across `toTError`, rather than collapsing it into a
   generic DDL unknown class before the retry gate.

## Method lesson

The efficient move was:

1. use a local classifier to find a retry-family asymmetry,
2. pin it with sibling end-to-end probes,
3. then extend from one error string to a small realistic family.

That is much higher signal than expanding more coarse network chaos after the first live green
sample. The live bridge-level matrix on `8220955` was the step that turned this from a strong
candidate into a confirmed high-severity DDL availability bug.
