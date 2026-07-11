# AI-Native Asset Store Validation
> 2026-07-10. Purpose: validate whether LOOP v2 can reuse selector/oracle/scenario/fault assets incrementally, instead of re-deriving everything in each bug-hunting round.

## Result

The asset-reuse idea is now validated on five held-out pipeline bugs:

- `issue59055/fix59157`: durability boundary in DDL notifier progress.
- `issue53843/fix53849`: lifecycle exactly-once boundary in DDL ingest cleanup.
- `issue48164/fix48163`: error identity preservation in S3 concurrent upload.
- `issue51846/fix52315`: owner/topology handoff preserving DDL scheduler processing state.
- `issue62424/fix62607`: DDL implicit commit leaving stale transaction startTS trusted by GC minStartTS reporting.

Neither is counted as a newly discovered bug. They are historical held-out replays used to test whether the method can turn stored proof obligations into executable RED/GREEN evidence.

The current campaign has also validated the incremental loop on newly discovered current-source bugs. The latest example is:

- `dxf/importinto + ERROR_IDENTITY_PRESERVATION`: `chunkWorker.Close` returned immediately after `dataWriter.Close` failed, so `indexWriter.Close` was skipped. RED preserved the root error but observed `indexCloseCount=0`; local GREEN preserved the same root error and observed index writer flush/close. Assets are stored in `/Users/bba/pc/ai-native-assets/error-identity-importinto-chunkworker-close-results.jsonl`.
- `ingestctrl + ERROR_IDENTITY_PRESERVATION`: `sstIter.Close` returned immediately after `iter.Close` failed, so `reader.Close` was skipped. RED showed the reader remained open after the root iterator error; local GREEN preserved the root error and observed the backing readable Close count reach 1. Assets are stored in `/Users/bba/pc/ai-native-assets/terminal-action-ingestctrl-sstiter-results.jsonl`.

This matters because it proves the asset loop is not only replaying historical bugs. A broad shared oracle (`oracle.injected-error-identity-survives.v1`) was narrowed into a current module obligation, a new terminal-action scenario, and a RED/GREEN run pair.

The second terminal-action hit also proves the negative-cache side of the loop. The same source-target batch retired parent-owned, defer-covered, and outer-finalizer-covered candidates before execution. That is important for incrementality: the database now remembers both the positive oracle and the reasons not to spend future turns on lookalike source shapes.

## Experiment

Target:

- Module: `ddl/notifier`
- Selector: `DURABLE_BEFORE_ACK`
- Obligation: notifier progress must not advance before the transaction commit that makes progress durable.
- Oracle: if the progress commit fails, the same logical event must remain deliverable.

Asset pack:

```text
total assets: 7
reused methodology assets: 4
new target-analysis assets: 3
open gaps: 0
```

Reused assets:

- `selector.durable-before-ack.v1`
- `oracle.delivery-retry-after-undurable-progress.v1`
- `scenario.single-event-at-least-once.v1`
- `schedule.fail-once-then-retry.v1`

Target-specific assets:

- `module.ddl-notifier.v1`
- `obligation.ddl-notifier.commit-progress.v1`
- `fault.ddl-notifier.before-handler-commit.v1`

## Evidence

The same temporary one-shot commit-failure hook and the same oracle were replayed on two historical revisions.

| Revision | Role | Control | Oracle result | Key evidence |
|---|---|---|---|---|
| `c34a6b69f66ed080bfd4938ae51e134fc70b917d` | vulnerable anchor | `TestBeginTwice` passed | RED | `handler_calls=1 commit_attempts=1 fault_hits=1`; event was not redelivered |
| `0fdb32530d6fb5e810632ea72ef055daf8cda967` | fixed PR anchor | `TestBeginTwice` passed | GREEN | `handler_calls=2 commit_attempts=2 fault_hits=1`; event was redelivered |

Command shape:

```text
go test -tags=intest ./pkg/ddl/notifier -run 'Test(BeginTwice|AINativeCommitFailureRetry)$' -count=1 -v
```

The first attempt without `-tags=intest` was classified as `INVALID(harness)`, because the TiDB test harness explicitly requires the intest tag. It was not used as product evidence.

## Stored Assets

Prototype files:

- `/Users/bba/pc/ai-native-assets/schema.sql`
- `/Users/bba/pc/ai-native-assets/store.py`
- `/Users/bba/pc/ai-native-assets/issue59055-seed.jsonl`
- `/Users/bba/pc/ai-native-assets/issue59055-results.jsonl`
- `/Users/bba/pc/ai-native-assets/issue59055-promote.jsonl`
- `/Users/bba/pc/ai-native-assets/assets.sqlite3`

Store stats after import:

```text
assets: 7
asset revisions: 10
runs: RED=1, GREEN=1
```

The asset count stayed stable while the revision count increased. That is the desired behavior: the reusable object stays the same, but its trust state evolves after RED/GREEN evidence.

## What This Proves

The important gain is not the test itself. The gain is that the next round can start from a database pack:

```text
selector -> obligation -> oracle -> scenario -> fault point -> schedule -> prior runs
```

For this held-out case, AI only had to add:

```text
module profile + concrete boundary obligation + concrete fault point
```

The selector, oracle, scenario, and schedule were reused. This is the core incrementality we wanted to test.

## Method Lessons

1. Store reasoning chains, not only scripts.
   A script is often too concrete. The reusable asset is the proof shape: what the system believed, where the boundary is, what must stay true after a fault, and which oracle can judge it.

2. Promote trust only after RED and GREEN.
   A single RED can be a bad hook, a bad oracle, or an environment artifact. The fixed revision proving GREEN is what makes the oracle useful for future mining.

3. Keep `INVALID` as a first-class result.
   The missing `-tags=intest` attempt was not noise. It records a harness precondition and prevents the loop from blaming product behavior for a broken experiment.

4. Source analysis still matters, but it shrinks.
   The asset DB did not remove source reading. It reduced source reading to the missing pieces: module profile, boundary card, and fault anchor.

5. The next metric is reuse efficiency.
   Each LOOP tick should report `reuse_ratio`, `new_asset_count`, `open_gap_count`, `invalid_rate`, and time from pack to terminal verdict.

## Limitations

This validates five high-value held-out shapes. It does not yet prove broad cross-module retrieval quality, production ranking, or TiDB Cloud deployment of the asset store.

The user-provided testbed `8220955` is authorized for QA work and remains the right place for cluster-level topology/config/fault experiments. This replay used local historical worktrees because intro/fix RED/GREEN is more deterministic there.

## Systemization Tick

2026-07-10 follow-up: the prototype is no longer only a result store. It now has a target queue and health view.

New commands:

```text
store.py queue [--include-done]
store.py next
store.py health
```

Queue before executing issue53843:

```text
validated:        issue59055/fix59157
ready_to_execute: issue53843/fix53849
needs_analysis:   issue48164/fix48163, issue51846/fix52315, issue62424/fix62607
```

`issue53843/fix53849` was selected by stored state, then moved from `needs_target_analysis` to `ready_to_execute` after adding three target-specific assets. Its pack has the same useful shape as issue59055:

```text
asset_count: 7
methodology_asset: 4
current_target_analysis: 3
open_gaps: []
```

This is the first concrete sign of an evolving system: one validated replay created reusable lifecycle assets, the queue selected the next held-out target, and target analysis advanced its state without changing the general selector/oracle/scenario/schedule.

## Second Validation Tick

2026-07-10 follow-up: `issue53843/fix53849` has now completed RED/GREEN.

Target:

- Module: `ddl/ingest`
- Selector: `LIFECYCLE_EXACTLY_ONCE`
- Obligation: concurrent cancel/rollback cleanup must close and unregister each ingest engine exactly once.
- Narrow oracle: overlapping `UnregisterEngines` calls must not close the same opened engine twice.

Evidence:

| Revision | Role | Oracle result | Key evidence |
|---|---|---|---|
| `cc127c14b8cc9887b1be946baa2f220690722c63` | vulnerable intro merge | RED | `close_calls=2 cleanup_calls=1`; second unregister closed the same engine while first cleanup was still in progress |
| `9c500ad9cb52c72372ad9d82f2a72190788d9478` | fix commit | GREEN | `close_calls=1 cleanup_calls=1 remaining_engines=0` |

Command shape:

```text
go test ./pkg/ddl/ingest -run '^TestAINativeUnregisterEnginesExactlyOnce$' -count=1 -v
```

The replay used Go 1.21 with isolated `GOROOT/GOPATH/GOCACHE`, because the historical revisions are Go 1.21-era code. Logs are stored at:

- `/Users/bba/pc/ai-native-assets/logs/issue53843-vulnerable-red-go121.log`
- `/Users/bba/pc/ai-native-assets/logs/issue53843-fixed-green-go121.log`

Asset changes:

- Added `/Users/bba/pc/ai-native-assets/issue53843-results.jsonl`.
- Added `oracle.concurrent-unregister-exactly-once.v1` as `execution_verified`.
- Moved `target.issue53843.ingest-writer-leak-on-cancel.v1` to `validated`.
- Kept broad `oracle.no-leak-after-cancel.v1` as `hypothesis`, because this run proves the root-cause boundary but not the full SQL/cluster cancel flow.

Store health after import:

```text
assets:          27
asset revisions: 30
runs:            RED=2, GREEN=2
targets:         validated=2, candidate=3
queue states:    validated=2, needs_target_analysis=3
oracle debt:     4 broad/hypothesis oracles
```

Method lesson:

The successful move was not "write a unit test". The move was:

```text
source/issue stack -> lifecycle exactly-once proof obligation
-> smallest overlap schedule at the mutable resource registry
-> strong counter oracle on close/cleanup counts
-> RED/GREEN across intro/fix
-> split broad oracle into a narrow verified oracle plus remaining E2E debt
```

That split is important. The system should promote only the part it actually proved.

## Third Validation Tick

2026-07-10 follow-up: `issue48164/fix48163` has now completed RED/GREEN.

Target:

- Module: `external-storage/s3`
- Selector: `ERROR_IDENTITY_PRESERVATION`
- Obligation: a concurrent S3 upload must preserve the root upload error identity across the background uploader and foreground pipe writer.
- Narrow oracle: injected upload error must be visible in final `Close`, not replaced by a pipe close/read/write error.

Evidence:

| Revision | Role | Oracle result | Key evidence |
|---|---|---|---|
| `5309c2ff7750a34a0137dd1d8bdb8c70aa533abc` | vulnerable intro merge | RED | background upload logged `mock error`, but final error was `io: read/write on closed pipe` |
| `b99d1c4f7eb2729f5c4f57ef6f5551f1d0136d9f` | fix commit | GREEN | background upload logged `mock error`, and `TestMultiUploadErrorNotOverwritten` passed |

Command shape:

```text
go test ./br/pkg/storage -run '^TestMultiUploadErrorNotOverwritten$' -count=1 -v
```

Logs are stored at:

- `/Users/bba/pc/ai-native-assets/logs/issue48164-vulnerable-red-go121.log`
- `/Users/bba/pc/ai-native-assets/logs/issue48164-fixed-green-go121.log`

Asset changes:

- Added `/Users/bba/pc/ai-native-assets/issue48164-analysis.jsonl`.
- Added `/Users/bba/pc/ai-native-assets/issue48164-results.jsonl`.
- Added `oracle.concurrent-pipe-upload-error-identity.v1` as `execution_verified`.
- Moved `target.issue48164.s3-uploader-error-precedence.v1` to `validated`.
- Kept broad `oracle.injected-error-identity-survives.v1` as `hypothesis`, because this run proves the pipe/uploader shape but not persisted job errors, retry classifiers, or all wrapper cases.

Store health after import:

```text
assets:          31
asset revisions: 36
runs:            RED=3, GREEN=3
targets:         validated=3, candidate=2
queue states:    validated=3, needs_target_analysis=2
oracle debt:     4 broad/hypothesis oracles
```

Method lesson:

This validates the asset-loop on a non-DDL-storage module. The reusable move was:

```text
source diff shows background worker + pipe close
-> P/Q/F says root error can be overwritten by secondary pipe error
-> reuse ERROR_IDENTITY selector/scenario/schedule
-> add S3 module card + fault point
-> replay upstream test across intro/fix
-> promote only the narrow pipe-upload oracle
```

## Fourth Scheduling Tick

2026-07-10 follow-up: `issue51846/fix52315` has now moved from target-analysis debt through root-boundary RED/GREEN to `validated`.

Target:

- Module: `ddl/job-scheduler`
- Selector: `OWNER_TOPOLOGY_HANDOFF`
- Obligation: owner retirement/re-entry must preserve the local fact that a long reorg job is still being processed by an existing worker.
- Fault point: owner retires and then becomes owner again while an ADD INDEX ingest/reorg worker is still active.

The important correction is that the scenario is not "PD leader partition" as a broad chaos label. The useful proof obligation is:

```text
code checked P: RetireOwnerHook fired, so this instance is not the owner
system believed Q: local runningJobs can be replaced
actual missing proof: old worker goroutines may still be running and the same instance may become owner again
```

Fix PR 52315 confirms this boundary. The vulnerable path replaced `d.runningJobs` with `newRunningJobs()`, losing `processingIDs`; the fix uses `d.runningJobs.clear()`, which clears unfinished dependency maps but keeps `processingIDs` so the dispatcher does not pick the same reorg job twice.

Asset changes:

- Added `/Users/bba/pc/ai-native-assets/issue51846-analysis.jsonl`.
- Added `/Users/bba/pc/ai-native-assets/issue51846-results.jsonl`.
- Added `module.ddl-job-scheduler.v1`.
- Added `obligation.ddl-job-scheduler.processing-preserved-across-owner-handoff.v1`.
- Added `fault.ddl-job-scheduler.owner-retire-reenter-active-reorg.v1`.
- Added `oracle.ddl-processing-id-survives-owner-retire.v1` as `execution_verified`.
- Moved `target.issue51846.ddl-topology-handoff.v1` to `validated`.

Evidence:

| Revision | Role | Oracle result | Key evidence |
|---|---|---|---|
| `bc841979a53e813d69c9fc8473ea0cc6703ef377` | vulnerable intro merge | RED | after retire semantics, `checkRunnable(job)` became true for the still-processing job; test failed with `Should be false` |
| `970962bdbc52547620be80817a7fc78e75b6221f` | fix commit | GREEN | after `clear()`, `checkRunnable(job)` stayed false; test passed |

Command shape:

```text
go test ./pkg/ddl -run '^TestAINativeOwnerRetirePreservesProcessingIDs$' -count=1 -v
```

Logs are stored at:

- `/Users/bba/pc/ai-native-assets/logs/issue51846-vulnerable-red-go121.log`
- `/Users/bba/pc/ai-native-assets/logs/issue51846-fixed-green-go121.log`

Store health after import:

```text
assets:          35
asset revisions: 42
runs:            RED=4, GREEN=4
targets:         validated=4, candidate=1
queue states:    validated=4, needs_target_analysis=1
oracle debt:     4 broad/hypothesis oracles
```

Method lesson:

This is exactly the asset-loop behavior we wanted. The shared selector/oracle/scenario/schedule were already present, but they were too generic to execute. The loop did not restart from scratch; it only added the missing target assets:

```text
historical issue/fix diff
-> sharpen broad topology label into a concrete P/Q/F
-> link to existing OWNER_TOPOLOGY assets
-> root-boundary RED/GREEN
-> promote only the narrow processingIDs oracle
```

The broad topology oracle remains hypothesis. The replay proves duplicate-dispatch guard preservation, not the full live-cluster `ADD INDEX + PD leader partition + terminal job state` path.

2026-07-10 follow-up: `issue62424/fix62607` has now moved from target-analysis debt through upstream integration RED/GREEN to `validated`.

Target:

- Module: `gc/ddl-transaction`
- Selector: `IMPLICIT_COMMIT_STATE_CLEANUP`
- Obligation: a DDL statement that has already been enqueued after implicit commit must not let its stale transaction startTS pin GC minStartTS.
- Fault point: hold `ADD INDEX` after the DDL job enters queue, while the DDL session still has an older `CurTxnStartTS` visible to processlist observers.

The useful proof obligation is:

```text
code checked P: processlist entry has CurTxnStartTS
system believed Q: this denotes an active transaction/read that GC must protect
actual missing proof: a queued DDL after implicit commit is not an active user transaction, even if stale CurTxnStartTS is still visible
```

Fix PR 62607 confirms this boundary. The vulnerable path lets `ReportMinStartTS` consider every `ProcessInfo.CurTxnStartTS`; the fix skips entries whose `StmtCtx.IsDDLJobInQueue` is true.

Asset changes:

- Added `/Users/bba/pc/ai-native-assets/issue62424-analysis.jsonl`.
- Added `/Users/bba/pc/ai-native-assets/issue62424-results.jsonl`.
- Added `module.gc-ddl-transaction.v1`.
- Added `obligation.gc-ddl-transaction.ignore-ddl-queued-startts.v1`.
- Added `fault.gc-ddl-transaction.ddl-queued-stale-curtxnstartts.v1`.
- Added `oracle.ddl-minstartts-ignores-queued-ddl.v1` as `execution_verified`.
- Moved `target.issue62424.ddl-implicit-commit-gc.v1` to `validated`.

Evidence:

| Revision | Role | Oracle result | Key evidence |
|---|---|---|---|
| `0501de48c5b033f17f300960ecfe4f40f9bc1742` | parent of fix | RED | `TestDDLInsideTXNNotBlockMinStartTS` failed at `integration_test.go:279` with `Condition never satisfied`; `GetMinStartTS()` never became the later active transaction's startTS |
| `e9e8a04fe71611ed08ebfcf0755993812a07c521` | fix commit | GREEN | the same upstream test passed; queued DDL session was ignored and minStartTS followed the real active transaction |

Command shape:

```text
go test -tags=intest ./pkg/executor/staticrecordset -run '^TestDDLInsideTXNNotBlockMinStartTS$' -count=1 -v
```

Logs are stored at:

- `/Users/bba/pc/ai-native-assets/logs/issue62424-vulnerable-red-go125.log`
- `/Users/bba/pc/ai-native-assets/logs/issue62424-fixed-green-go125.log`

Store health after import:

```text
assets:          39
asset revisions: 49
runs:            RED=5, GREEN=5
targets:         validated=5
queue states:    validated=5
oracle debt:     4 broad/hypothesis oracles
```

Method lesson:

The high-yield move was not "try DDL inside transaction" broadly. It was asking which background observer still trusts transaction-visible state after DDL implicit commit. The test then needed only two timestamps:

```text
ddlTs = old startTS on queued DDL session
tkTs  = later startTS from a real active transaction
oracle: ReportMinStartTS must converge to tkTs, not ddlTs
```

The broad stale-state oracle remains hypothesis. The replay proves the root minStartTS boundary, not live-cluster GC safepoint advancement under all DDL types.

## Refill Tick

2026-07-10 follow-up: after the five historical targets were all validated, `store.py next` correctly returned no active targets. The next methodology step was to refill the queue from oracle debt instead of hand-picking another historical case.

Refill result:

- Added `/Users/bba/pc/ai-native-assets/refill-live-lift-targets.jsonl`.
- Added `target.lift.issue62424.live-gc-safepoint.v1`.
- Added `obligation.gc-ddl-transaction.live-gc-safepoint-advances.v1`.
- Reused `oracle.no-stale-txn-state-after-ddl.v1` as the broad oracle debt.
- Reused issue62424's validated module/fault/schedule/scenario assets.

This exposed and fixed a control-plane bug in the prototype. Before the fix, `target_queue` grouped prior runs by `module + selector`; a new live-lift target under the same selector could be incorrectly marked `validated` because the historical issue62424 obligation already had RED/GREEN runs. `store.py` now scopes prior runs to `payload.obligation_key` when present.

The refill target was then executed on testbed `8220955` and completed GREEN:

- Added `/Users/bba/pc/ai-native-assets/issue62424-live-lift-results.jsonl`.
- Added `/Users/bba/pc/ai-native-assets/logs/issue62424-live-gc-lift-green-testbed8220955-evidence.log`.
- Observed DDL processlist `TxnStart=467568057103679489`.
- Observed `/tidb/server/minstartts` advance past it while the DDL was still visible: `467568057116524554`, then `467568066213183509`, then `467568072111423519`.

Store health after live lift:

```text
assets:          40
asset revisions: 51
runs:            RED=5, GREEN=6
targets:         validated=6
queue states:    validated=6
next target:     none
```

Method lesson:

Asset reuse needs target identity, not only selector identity. A selector is a family; an obligation is the executable claim. The queue should schedule obligations, while selectors help retrieve reusable context.

2026-07-10 follow-up: the refill step is now an actual CLI command.

Command:

```text
store.py refill --limit 10 --jsonl-output /Users/bba/pc/ai-native-assets/refill-candidates-20260710.jsonl
```

Before generation, a consistency audit found one asset hole: `target.issue53843.ingest-writer-leak-on-cancel.v1` was validated and had RED/GREEN runs, but `obligation.ddl-ingest.unregister-cleanup.v1` was still `candidate/hypothesis`. Added `/Users/bba/pc/ai-native-assets/issue53843-promote-obligation.jsonl` to promote that obligation and its fault asset to `execution_verified`.

Generated candidates:

```text
target.refill.target-issue53843-ingest-writer-leak-on-cancel-v1.oracle-no-leak-after-cancel-v1.v1
target.refill.target-issue48164-s3-uploader-error-precedence-v1.oracle-injected-error-identity-survives-v1.v1
target.refill.target-issue51846-ddl-topology-handoff-v1.oracle-allowed-state-after-topology-fault-v1.v1
```

At generation time, all three were intentionally `needs_target_analysis`: they reused a broad oracle and base obligation as provenance, but did not yet have their own executable `obligation_key`.

Store health after automated refill:

```text
assets:          40
asset revisions: 53
runs:            RED=5, GREEN=6
targets:         validated=6, candidate=3
queue states:    validated=6, needs_target_analysis=3
next target:     target.refill.target-issue53843-ingest-writer-leak-on-cancel-v1.oracle-no-leak-after-cancel-v1.v1
```

Additional control-plane lesson:

`base_obligation` is provenance; `obligation_key` is execution identity. A refill target with `broad_oracle` but no `obligation_key` must stay in `needs_target_analysis`, even if its base obligation already has RED/GREEN.

2026-07-10 follow-up: the first automated refill target has completed target analysis.

New asset file:

```text
/Users/bba/pc/ai-native-assets/issue53843-refill-target-analysis.jsonl
```

It adds one executable broad-lift obligation, one target-specific oracle, one fault point, and updates the refill target:

```text
obligation.ddl-ingest.sql-cancel-terminal-no-live-resource.v1
oracle.ddl-ingest-cancel-terminal-no-live-resource.v1
fault.ddl-ingest.sql-cancel-after-local-engine-open.v1
target.refill.target-issue53843-ingest-writer-leak-on-cancel-v1.oracle-no-leak-after-cancel-v1.v1
```

The target moved to `ready_to_execute`. The strong oracle is deliberately stricter than "ADMIN CANCEL DDL JOBS returned": it requires proof that local ingest resources were live when cancel was delivered, then terminal-state evidence that backend context, engines, opened writers, and duplicate-close counters are clean.

Store health after this target-analysis tick, before executing the GREEN run:

```text
assets:          43
asset revisions: 56
runs:            RED=5, GREEN=6
targets:         validated=6, ready=1, candidate=2
queue states:    validated=6, ready_to_execute=1, needs_target_analysis=2
next target:     target.refill.target-issue53843-ingest-writer-leak-on-cancel-v1.oracle-no-leak-after-cancel-v1.v1
```

Execution stop rule:

Do not record a GREEN for this refill target unless `active_resource_window_hit=true` is proven before cancel and terminal resource counters are observable. If either condition is missing, record `INVALID(harness)`.

2026-07-10 follow-up: the first execution of that refill target produced a current GREEN.

New result file and log:

```text
/Users/bba/pc/ai-native-assets/issue53843-refill-results.jsonl
/Users/bba/pc/ai-native-assets/logs/issue53843-refill-sql-cancel-green-go125.log
```

Run:

```text
run.issue53843.refill.current.13282a8.GREEN
```

Command shape:

```text
make failpoint-enable
go test -tags=intest ./pkg/ddl/ingest -run '^TestAINativeAddIndexCancelLeavesNoLiveMockIngestResource$' -count=1 -v
make failpoint-disable
```

Observed GREEN evidence:

```text
active_writes=64
registered=1
created_writers=2
finish_calls=1
live_engines=0
live_writers=0
closed_engines=1
duplicate_closes=0
disk_root_count=0
```

The DDL job reached `rollback done` with `ErrCancelledDDLJob`; the enhanced mock backend proved the active-write window was hit and no mock ingest resource remained live.

Store health after importing the GREEN:

```text
assets:          43
asset revisions: 56
runs then:       RED=5, GREEN=7
targets:         validated=6, ready=1, candidate=2
queue states:    validated=6, needs_counterpart_run=1, needs_target_analysis=2
next target:     target.refill.target-issue53843-ingest-writer-leak-on-cancel-v1.oracle-no-leak-after-cancel-v1.v1
```

Promotion decision:

No oracle promotion yet. This is a current/fixed GREEN only. The next step is a vulnerable RED counterpart for the same SQL-cancel no-live-resource obligation, and the RED harness must not hide the original duplicate-cleanup race by over-mocking cleanup.

2026-07-10 follow-up: a stronger vulnerable root-boundary RED was added, but deliberately not used to validate the SQL refill target.

New result file and log:

```text
/Users/bba/pc/ai-native-assets/issue53843-refill-root-boundary-memory-results.jsonl
/Users/bba/pc/ai-native-assets/logs/issue53843-refill-vulnerable-root-boundary-red-go121.log
```

Run:

```text
run.issue53843.refill.vulnerable.cc127c14.RED.memory-double-release
```

Command shape:

```text
GOROOT=/Users/bba/.gvm/gos/go1.21.0 \
GOTOOLCHAIN=local \
GOCACHE=/private/tmp/ai-native-go121-run2-cache \
GOMODCACHE=/private/tmp/ai-native-go121-modcache \
/Users/bba/.gvm/gos/go1.21.0/bin/go test ./pkg/ddl/ingest \
  -run '^TestAINativeConcurrentUnregisterDoesNotDoubleReleaseMemory$' \
  -count=1 -v
```

Observed RED evidence:

```text
expected_current_usage=0
actual_current_usage=-2877
attempt=0
```

This proves the vulnerable `UnregisterEngines` can release the same engine resource accounting more than once. It improves the root-boundary oracle because lifecycle exactly-once now checks the full ownership ledger, not only close/cleanup call counts.

Store health after importing this RED:

```text
assets:          43
asset revisions: 56
runs:            RED=6, GREEN=7
targets:         validated=6, ready=1, candidate=2
queue states:    validated=6, needs_counterpart_run=1, needs_target_analysis=2
next target:     target.refill.target-issue53843-ingest-writer-leak-on-cancel-v1.oracle-no-leak-after-cancel-v1.v1
```

Scope decision:

This RED belongs to `obligation.ddl-ingest.unregister-cleanup.v1`, not `obligation.ddl-ingest.sql-cancel-terminal-no-live-resource.v1`. It strengthens the base proof and explains what the SQL lift must expose, but the refill target correctly stays in `needs_counterpart_run`.

2026-07-10 follow-up: the missing SQL-level vulnerable RED counterpart has now been added and imported.

New result and promotion files:

```text
/Users/bba/pc/ai-native-assets/issue53843-refill-sql-red-results.jsonl
/Users/bba/pc/ai-native-assets/issue53843-refill-sql-promote.jsonl
/Users/bba/pc/ai-native-assets/logs/issue53843-refill-vulnerable-sql-cancel-red-go121.log
```

Run:

```text
run.issue53843.refill.vulnerable.cc127c14.RED.sql-cancel-double-cleanup
```

Command shape:

```text
GOROOT=/Users/bba/.gvm/gos/go1.21.0 \
GOTOOLCHAIN=local \
GOCACHE=/private/tmp/ai-native-go121-run2-cache \
GOMODCACHE=/private/tmp/ai-native-go121-modcache \
/Users/bba/.gvm/gos/go1.21.0/bin/go test -tags=intest ./pkg/ddl/ingest \
  -run '^TestAINativeIssue53843SQLCancelDoubleCleanupRED$' \
  -count=1 -timeout 60s -v
```

Observed RED evidence:

```text
jobID=106
registered=1
writes=1
unregister_calls=2
cleanup_ledger=-1
cancelled=true
alter result=ErrCancelledDDLJob
```

Why this counts as the SQL-level counterpart:

- The SQL path itself runs `ALTER TABLE ... ADD INDEX` and `ADMIN CANCEL DDL JOBS`.
- The active-resource window is proved by `registered=1` and `writes=1`.
- The duplicate cleanup ownership is produced by the vulnerable DDL flow: `indexWriteResultSink.Close()` and `LitBackCtxMgr.Unregister(job.ID)` both reach the same observed backend context.
- The observing mock backend manager exposes the old `litBackendCtxMgr` cleanup semantics as a counter/ledger; it does not synthesize SQL cancel or the two cleanup owners.

Store health after importing this RED and promotion:

```text
asset revisions: 59
runs:            RED=7, GREEN=7
targets:         validated=7, candidate=2
queue states:    validated=7, needs_target_analysis=2
next target:     target.refill.target-issue48164-s3-uploader-error-precedence-v1.oracle-injected-error-identity-survives-v1.v1
```

Promotion decision:

`obligation.ddl-ingest.sql-cancel-terminal-no-live-resource.v1`, `oracle.ddl-ingest-cancel-terminal-no-live-resource.v1`, and `fault.ddl-ingest.sql-cancel-after-local-engine-open.v1` are now `execution_verified`. The broad `oracle.no-leak-after-cancel.v1` remains `hypothesis`: this run proves the narrowed SQL ADD INDEX cancel cleanup-ownership obligation, not a live/testbed proof of real local backend file leakage.

## issue48164 refill: current S3 multipart writer RED/GREEN

New result file and logs:

```text
/Users/bba/pc/ai-native-assets/issue48164-refill-s3-multipart-results.jsonl
/Users/bba/pc/ai-native-assets/logs/issue48164-refill-current-s3-storage-part-fail-close-red.log
/Users/bba/pc/ai-native-assets/logs/issue48164-refill-current-s3-multipart-part-fail-close-green.log
/Users/bba/pc/ai-native-assets/logs/issue48164-refill-current-s3-multipart-writer-part-fail-close-green.log
```

This refill did not replay historical issue48164's concurrent pipe bug. It reused the broad error-identity oracle to inspect current `pkg/objstore/s3store` and derived a new target-specific obligation:

```text
obligation.external-storage-s3.multipart-failed-part-terminal-no-complete.v1
oracle.s3-multipart-failed-part-no-complete-preserve-root.v1
fault.external-storage-s3.uploadpart-error-after-prefix-part.v1
```

The proof obligation:

```text
P: multipart upload has started, part 1 succeeds, part 2 UploadPart returns a chosen root error.
Q: terminal Close must not CompleteMultipartUpload a prefix-only object and must preserve the root error.
F: current writer returns the UploadPart error from Write but does not store failed state; Close still completes accumulated completeParts.
```

Current RED:

```text
run:     run.issue48164.refill.current.13282a8.RED.s3-multipart-part-fail-close
commit:  13282a8bd06bd33324a4dbfd3c1c03685f3cd9aa
test:    TestAINativeS3StorageCreateUploadPartFailureThenCloseRED
command: go test ./pkg/objstore/s3store -run TestAINativeS3StorageCreateUploadPartFailureThenCloseRED -count=1 -timeout 60s -v

observed:
  writeErr=ai-native mock upload part failed
  closeErr=<nil>
  completeCalls=1
  completedParts=1
```

This is a current product bug candidate, not just a test artifact: the user-facing `Storage.Create` path wraps the multipart writer in `objectio.BufferedWriter`; a failed large S3 write can have cleanup `Close` publish a prefix-only object. The caller may still have the earlier `Write` error, but the remote storage terminal state is wrong.

Local GREEN with minimal fix:

```text
run:     run.issue48164.refill.local-fix.13282a8.GREEN.s3-multipart-part-fail-close
fix:     store the first UploadPart error in multipartWriter; after failure, Close calls AbortMultipartUpload and returns the stored root error.

storage entry observed:
  writeErr=ai-native mock upload part failed
  closeErr=ai-native mock upload part failed
  abortCalls=1

multipart writer entry observed:
  writeErr=ai-native mock upload part failed
  closeErr=ai-native mock upload part failed
  abortCalls=1
```

Validation scope:

- Verified for `pkg/objstore/s3store` direct `MultipartWriter` and `s3like.Storage.Create(..., Concurrency:1, PartSize:5)`.
- The same state-machine shape appears in `pkg/objstore/ossstore/client.go` and `pkg/objstore/s3store/ks3.go`, but those are follow-up targets until separately executed.
- A full `go test ./pkg/objstore/s3store` was attempted after the local fix; it failed in existing retry/read tests (`TestRetryError`, `TestS3ReadFileRetryable`) unrelated to this oracle. The narrow RED/GREEN tests passed.

Store health after import:

```text
asset revisions: 62
runs:            RED=8, GREEN=8
targets:         validated=8, candidate=1
queue states:    validated=8, needs_target_analysis=1
next target:     target.refill.target-issue51846-ddl-topology-handoff-v1.oracle-allowed-state-after-topology-fault-v1.v1
```

Promotion decision:

`obligation.external-storage-s3.multipart-failed-part-terminal-no-complete.v1`, `oracle.s3-multipart-failed-part-no-complete-preserve-root.v1`, and `fault.external-storage-s3.uploadpart-error-after-prefix-part.v1` are `execution_verified`. The broad `oracle.injected-error-identity-survives.v1` remains `hypothesis`: this run validates one terminal multipart state/action shape, not every injected-error consumer.

## issue51846 refill: current DDL owner epoch RED/GREEN

New result file and logs:

```text
/Users/bba/pc/ai-native-assets/issue51846-refill-owner-epoch-results.jsonl
/Users/bba/pc/ai-native-assets/logs/issue51846-refill-current-owner-epoch-collision-red.log
/Users/bba/pc/ai-native-assets/logs/issue51846-refill-local-owner-epoch-renewal-green.log
```

This refill did not replay historical issue51846's `runningJobs.processingIDs` loss. It reused the broad topology oracle to inspect current DDL owner/reorg result filtering and derived a new target-specific obligation:

```text
obligation.ddl-job-scheduler.owner-epoch-token-unique-across-handoff.v1
oracle.ddl-stale-reorg-result-rejected-by-owner-epoch.v1
fault.ddl-job-scheduler.owner-retire-rebecome-same-second.v1
```

The proof obligation:

```text
P: runReorgJob accepts a reorgFnResult when res.ownerTS equals current ownerTS.
Q: ownerTS equality proves the result belongs to the current DDL owner epoch.
F: OnBecomeOwner derives ownerTS from time.Now().Unix(), so two owner epochs on the same TiDB can collide inside one second.
```

Current RED:

```text
run:     run.issue51846.refill.current.13282a8.RED.owner-epoch-second-collision
commit:  13282a8bd06b
test:    TestAINativeOwnerEpochSecondCollisionRED

observed:
  previousOwnerTS=1000
  curOwnerTS=1000
  stale reorg result would pass the ownerTS equality filter
```

Local GREEN with minimal fix:

```text
run: run.issue51846.refill.local-fix.13282a8.GREEN.owner-epoch-renewal
fix: renew ownerTS as max(wallTS, previous+1)
test: TestAINativeOwnerEpochRenewalRejectsSameSecondStaleResult
result: PASS
```

Validation scope:

- Verified at the root boundary of the exact predicate used by `runReorgJob`.
- It proves the identity-token bug candidate and the fix shape, not frequency under live PD/owner topology churn.
- A cluster lift should force owner retire/re-become while a reorg worker is active and assert the old result takes the retry path.

Store health after import:

```text
asset revisions: 65
runs:            RED=9, GREEN=9
targets:         validated=9
queue states:    validated=9
next target:     null
```

Promotion decision:

`obligation.ddl-job-scheduler.owner-epoch-token-unique-across-handoff.v1`, `oracle.ddl-stale-reorg-result-rejected-by-owner-epoch.v1`, and `fault.ddl-job-scheduler.owner-retire-rebecome-same-second.v1` are `execution_verified`. The broad `oracle.allowed-state-after-topology-fault.v1` remains `hypothesis`: this run validates one owner-epoch identity boundary, not every legal final state after topology fault.

## SOURCE_TARGETS: identity-token async filter

After the refill queue was drained, the next useful move was not another refill. The loop mined a new source rule from the DDL owner epoch hit:

```text
selector.identity-token-async-filter.v1
oracle.identity-token-distinguishes-lifecycle.v1
scenario.async-work-overlaps-lifecycle-change.v1
schedule.rapid-lifecycle-renewal-vs-token-precision.v1
```

The rule:

```text
G1 token-gated decision: equality/inequality controls accept, reject, pause, retry, cleanup, or ownership.
G2 lifecycle overlap: old async work or old owner/session/task can overlap the new lifecycle.
G3 collision schedule: two distinct lifecycles can share the token under product timing.
G4 strong oracle: observe the state action or callback, not just token equality.
```

### Negative Screen: BR registry heartbeat

Result file:

```text
/Users/bba/pc/ai-native-assets/source-targets-identity-token-async-filter.jsonl
```

BR registry matched G1/G2:

```text
last_heartbeat_time equality gates stale-task transition to paused
UpdateHeartbeat writes FROM_UNIXTIME(time.Now().UTC().Unix())
isTaskStale treats heartbeat as active only if UNIX_TIMESTAMP(last_heartbeat_time) changes
```

But it failed G3:

```text
defaultHeartbeatIntervalSeconds = 60
isTaskStale checks once per minute for 5 minutes
token precision = 1 second
```

Therefore it was stored as:

```text
run.source.br-registry.current.13282a8.INVALID.heartbeat-token-precision-screen
```

This negative asset is important: the selector should not mine every coarse timestamp. It must prove a product-feasible collision schedule before execution.

### Positive Hit: BR storewatch same-second reboot

Result file and logs:

```text
/Users/bba/pc/ai-native-assets/source-storewatch-reboot-same-second-results.jsonl
/Users/bba/pc/ai-native-assets/logs/source-storewatch-current-reboot-same-second-red.log
/Users/bba/pc/ai-native-assets/logs/source-storewatch-local-green.log
```

Proof obligation:

```text
P: storewatch updateStore observes the same store ID and compares old/new Store.StartTimestamp.
Q: unchanged StartTimestamp proves no reboot/recovery callback is needed.
F: Store.StartTimestamp is a seconds-level lifecycle token; Offline->Up is itself a recovery edge and can occur with the same token value.
```

Current RED:

```text
run: run.source.br-storewatch.current.13282a8.RED.same-second-reboot-missed
test: TestAINativeOnRebootWhenStoreRestartsWithinSameSecondRED
observed:
  Up(T) -> Offline(T) -> Up(T)
  OnReboot callback was not called
```

Local GREEN:

```text
run: run.source.br-storewatch.local-fix.13282a8.GREEN.same-second-reboot-notified
fix: keep StartTimestamp-change trigger and also treat non-Up -> Up as OnReboot
command: go test ./br/pkg/utils/storewatch -count=1 -timeout 60s -v
result: PASS, including existing register/offline/reboot controls
```

Store health after import:

```text
asset revisions: 76
runs:            RED=10, GREEN=10, INVALID=1
targets:         validated=10, retired=1
queue states:    validated=10, retired=1
next target:     null
```

Promotion decision:

`obligation.br-storewatch.reboot-notified-after-offline-up-same-token.v1`, `oracle.br-storewatch-offline-up-reboot-notified.v1`, and `fault.br-storewatch.same-second-store-restart.v1` are `execution_verified`. The general `oracle.identity-token-distinguishes-lifecycle.v1` remains `used`, not trusted, because it now has one positive DDL instance, one negative BR registry screen, and one positive BR storewatch instance; it still needs more held-out/adversarial verification before becoming a trusted meta-oracle.

## SOURCE_TARGETS generator import and retirement: TiFlash MPP cache candidate

The identity-token source rule has a reusable CLI entry:

```text
python3 ai-native-assets/store.py source-targets --rule identity-token --repo /Users/bba/pc/tidb --jsonl-output /Users/bba/pc/ai-native-assets/source-targets-identity-token-generated-20260710.jsonl
```

This run produced one new queue target and skipped three already-covered cases:

```text
generated: target.source.tiflash-mpp-logical-core-starttimestamp.v1
skipped:
  DDL owner epoch result token       covered_asset_exists
  BR registry heartbeat token        target_exists
  BR storewatch same-second reboot   target_exists
```

Import result before target-analysis:

```text
imported target=1
asset revisions: 76
runs:            RED=10, GREEN=10, INVALID=1
targets:         validated=10, retired=1, candidate=1
queue states:    validated=10, retired=1, needs_target_analysis=1
next target:     target.source.tiflash-mpp-logical-core-starttimestamp.v1
```

Target state:

```text
module:   planner/tiflash-mpp-cache
selector: IDENTITY_TOKEN_ASYNC_FILTER
status:   candidate
state:    needs_target_analysis
missing:  module_profile, obligation, fault_point
```

Validation decision:

- This is not a confirmed bug and should not be counted as RED.
- The candidate is valid asset-store output because it has source evidence and a selector lineage.
- The next tick must either add a module-specific proof obligation and fault schedule, or retire it as `INVALID(schedule-proof/effect-proof)`.
- `oracle.identity-token-distinguishes-lifecycle.v1` remains `used`, not `trusted`. The generator added a held-out candidate, not another RED/GREEN validation.

Target-analysis result:

```text
asset file: /Users/bba/pc/ai-native-assets/source-targets-tiflash-mpp-cache-retire-analysis.jsonl
status:     retired
class:      LOW_VALUE
decision:   retired_invalid_schedule_effect_quality
```

Gate assessment:

```text
G1: pass
    equal StartTimestamp gates cache reuse vs hardware-info refresh.
G2: pass
    cached MPPServerInfo can outlive a TiFlash lifecycle change at the same address.
G3: not proven
    requires TiFlash restart/re-registration in the same second, address reuse, and changed
    logical CPU count; a forced unit test would not prove product feasibility.
G4: weak pass
    stale LogicalCPUCount can affect TiFlashFineGrainedShuffleStreamCount, but the consequence is
    planner/performance quality rather than row-set correctness or recovery safety.
```

Store health after retirement:

```text
asset revisions: 76
runs:            RED=10, GREEN=10, INVALID=1
targets:         validated=10, retired=2
queue states:    validated=10, retired=2
next target:     null
```

Generator rerun after retirement:

```text
candidates: []
skipped:
  BR registry        retired_target_exists
  BR storewatch      target_exists, status=validated
  TiFlash MPP cache  retired_target_exists
```

Refill rerun after retirement:

```text
python3 ai-native-assets/store.py refill --limit 10 --jsonl-output /Users/bba/pc/ai-native-assets/refill-candidates-20260710-after-tiflash-retire.jsonl

candidates: []
jsonl rows: 0
skipped:
  oracle.identity-token-distinguishes-lifecycle.v1  recursive_refill_base_only
  oracle.allowed-state-after-topology-fault.v1      already_has_refill_target
  oracle.injected-error-identity-survives.v1        already_has_refill_target
  oracle.no-leak-after-cancel.v1                    already_has_refill_target
  oracle.no-stale-txn-state-after-ddl.v1            already_has_refill_target
```

Validation decision update:

- The asset store correctly prevented a SOURCE_ONLY candidate from becoming a low-quality RED.
- The retired target should remain as negative selector calibration.
- `store.py refill` now blocks recursive refill bases, so oracle debt cannot self-replicate through
  a validated refill target.
- The next useful move is a different source-target rule or a held-out selector seed, not another
  TiFlash token test.

## State-ingress SOURCE_TARGETS validation: binding-history RED/GREEN

The next source-target rule came from S23/id1230001 rather than from identity-token:

```text
rule:     STATE_INGRESS_INTERNAL_SQL
seed:     id1230001 / NT-DML stale TxnReadTS split-range leak
target:   target.source.binding-history-executeinternal-txreadts.v1
module:   planner/binding-history
state:    validated
```

What was reused:

```text
selector idea:
  "external code clears/ignores one session state input, but internal SQL re-enters
   the generic session path and can consume a sibling input"

scenario:
  internal SQL scheduled between one-shot state setup and the user-visible read

oracle:
  pending TxnReadTS must either remain pending or be rejected explicitly; it must not be silently
  consumed by an unrelated internal lookup
```

New target-specific assets:

```text
module.planner-binding-history.v1
obligation.binding-history-preserves-pending-txreadts.v1
fault.binding-history.executeinternal-consumes-txreadts.v1
```

Evidence:

```text
current root-boundary RED:
  run.source.state-ingress.current.13282a8.RED.restricted-sql-root-boundary

current user-visible RED:
  run.source.binding-history.current.13282a8.RED.user-visible-txreadts-consumed

invalid local counterpart attempt:
  run.source.binding-history.local-fix.13282a8.INVALID.txreadts-restore-incomplete

TSO-stable current RED:
  run.source.binding-history.current.13282a8.RED.tso-stable-txreadts-consumed

local-fix GREEN:
  run.source.binding-history.local-fix.13282a8.GREEN.executeinternal-txreadts-isolation
```

Store health after import:

```text
asset revisions: 90
runs:            RED=13, GREEN=11, INVALID=2
targets:         validated=11, retired=2
queue states:    validated=11, retired=2
next target:     null before generator refill
```

Validation decision update:

- This is a real incrementality win: the loop moved from a confirmed S23 bug into a neighboring
  current source target without starting from scratch.
- The target is now methodologically validated: the same TSO-stable oracle has current RED and
  local-fix GREEN. The local patch saved/restored pending `TxnReadTS` and `SnapshotInfoschema`
  around `ExecuteInternal`, and the next user SELECT matched the AS OF control.
- It is still semantic-gray as a product bug until the contract for `SET TRANSACTION READ ONLY AS
  OF TIMESTAMP` is settled.
- The store state behaved correctly: `needs_counterpart_run` first preserved the open RED without
  over-counting it, then disappeared after the clean counterpart run was imported.

Generator follow-up:

```text
command:
  python3 ai-native-assets/store.py source-targets --rule state-ingress \
    --repo /Users/bba/pc/tidb \
    --jsonl-output /Users/bba/pc/ai-native-assets/source-targets-state-ingress-generated-20260710.jsonl

imported candidates:
  target.source.ddl-foreign-key-use-cur-session-state-ingress.v1
  target.source.executor-user-management-executeinternal-state-ingress.v1
  target.source.planner-index-advisor-executeinternal-state-ingress.v1

health after import:
  targets:      validated=11, retired=2, candidate=3
  queue states: validated=11, retired=2, needs_target_analysis=3
  next:         target.source.ddl-foreign-key-use-cur-session-state-ingress.v1
```

This validates the next asset-store requirement: a selector that produced a real RED/GREEN can be
reused as a source-target generator. The generated targets are not bug claims; the queue correctly
requires module profile, proof obligation, and fault point assets before execution.

## State-ingress SOURCE_TARGETS validation: generated batch closed

The first generated state-ingress batch has now been resolved:

```text
generated from:
  /Users/bba/pc/ai-native-assets/source-targets-state-ingress-generated-20260710.jsonl

retired:
  target.source.ddl-foreign-key-use-cur-session-state-ingress.v1
    reason: INVALID(session-ownership-proof)
    asset: /Users/bba/pc/ai-native-assets/source-targets-state-ingress-foreign-key-retire-analysis.jsonl

  target.source.executor-user-management-executeinternal-state-ingress.v1
    reason: INVALID(sys-session-isolation-proof)
    asset: /Users/bba/pc/ai-native-assets/source-targets-state-ingress-user-management-retire-analysis.jsonl

validated:
  target.source.planner-index-advisor-executeinternal-state-ingress.v1
    asset: /Users/bba/pc/ai-native-assets/source-state-ingress-indexadvisor-results.jsonl
```

The index-advisor target reused the same selector/oracle/schedule rather than inventing a new
bug-specific method:

```text
module.planner-index-advisor.v1
obligation.index-advisor-preserves-pending-txreadts.v1
fault.indexadvisor.executeinternal-consumes-txreadts.v1
oracle.pending-txreadts-preserved-across-internal-sql.v1
```

Evidence:

```text
current RED:
  run.source.indexadvisor.current.13282a8.RED.txreadts-consumed
  log: /Users/bba/pc/ai-native-assets/logs/source-state-ingress-indexadvisor-txreadts-red.log
  observed: before=467570885856329728 after=0 next_select_rows=[[1] [2]]

local-fix GREEN:
  run.source.indexadvisor.local-fix.13282a8.GREEN.executeinternal-txreadts-isolation
  log: /Users/bba/pc/ai-native-assets/logs/source-state-ingress-indexadvisor-txreadts-local-green.log
  observed: before=467570913639661568 after=467570913639661568 next_select_rows=[[1]]
```

Store health after import:

```text
asset revisions: 93
runs:            RED=14, GREEN=12, INVALID=2
targets:         validated=12, retired=4
queue states:    validated=12, retired=4
next target:     null
```

Validation decision update:

- This is the first source-target batch where the same selector produced both validated positives and
  retired negatives. That is a healthier signal than a single lucky bug.
- The new mandatory gate is `session-ownership-proof`: before running a RED probe, prove whether the
  internal SQL actually shares the user's session state. Foreign-key and user-management failed this
  gate and were recorded as negative cache.
- The effective local fix probe is ingress isolation, not only post-hoc restoration. Result-set
  drain/close can happen after `ExecuteInternal` returns, so the user pending state has to be hidden
  from internal SQL before it enters the generic session path.
- Product bug quality is medium-high for index advisor as method evidence, but filing/escalation
  still depends on the `SET TRANSACTION READ ONLY AS OF TIMESTAMP` contract.

## State-ingress SOURCE_TARGETS validation: dynamic queue refresh

After the first state-ingress batch drained, the generator was upgraded from static seeds to a
dynamic source scan:

```text
command:
  python3 ai-native-assets/store.py source-targets --rule state-ingress \
    --repo /Users/bba/pc/tidb \
    --limit 20 \
    --jsonl-output /Users/bba/pc/ai-native-assets/source-targets-state-ingress-dynamic-20260710.jsonl

import:
  python3 ai-native-assets/store.py import \
    /Users/bba/pc/ai-native-assets/source-targets-state-ingress-dynamic-20260710.jsonl
```

New queue state:

```text
imported targets: 9
targets:          validated=12, retired=4, blocked=1, candidate=8
queue states:     validated=12, retired=4, blocked=1, needs_target_analysis=8
next target:      target.source.dynamic-state-ingress.pkg-infoschema-infoschema.v1
```

Top candidates:

```text
target.source.dynamic-state-ingress.pkg-executor-show.v1
  priority: 90
  signal:   ExecRestrictedSQL + ExecOptionUseCurSession in ShowExec.fetchShowTableStatus
  result:   blocked/CONTRACT_NEEDED after live target-analysis on testbed 8220955

target.source.dynamic-state-ingress.pkg-infoschema-infoschema.v1
  priority: 90
  signal:   ExecRestrictedSQL + ExecOptionUseCurSession while loading masking policies
  debt:     prove user-visible caller and one-shot state contract before executing; now next target
```

Selector improvements encoded in `store.py`:

- Known validated/retired paths are skipped instead of regenerated.
- DDL worker paths, sys sessions, pooled sessions, new sessions, nil restricted SQL without
  `UseCurSession`, and internal helper paths are screened out or downgraded.
- A file-level sys/new-session marker is no longer an automatic whole-file veto. The classifier now
  scores the local hit line and carries unrelated file-level isolation markers as debt. This matters
  for `pkg/executor/show.go`, where the `SHOW TABLE STATUS` path has a local `UseCurSession` signal
  even though other code paths in the same file use sys/new sessions.
- Auxiliary execution wrappers such as BRIE/importer are retained at lower priority rather than
  promoted above direct user-statement wrappers.

Validation decision:

- This is a control-plane win, not a new bug claim. The asset store now turns the selector into
  incremental work and correctly stops every new target at `needs_target_analysis`.
- `SHOW TABLE STATUS` was a useful boundary sample. It really consumes the pending stale-read state:
  AS OF control saw row `1`, current control saw `1,2`, `SET TRANSACTION; SHOW TABLE STATUS; SELECT`
  saw `1,2`, and direct `SET TRANSACTION; SELECT` saw `1`. Evidence:
  `/Users/bba/pc/ai-native-assets/logs/source-state-ingress-show-table-status-testbed8220955.log`.
- The target is blocked rather than filed: `SHOW TABLE STATUS` is itself a user-visible SHOW/query
  statement, so whether it may consume `SET TRANSACTION` depends on product contract.
- The next useful action is not RED execution. It is target-analysis for
  `target.source.dynamic-state-ingress.pkg-infoschema-infoschema.v1`: prove the user-visible caller
  and session ownership before adding module profile, obligation, and fault point assets.
- The method improved because `session-ownership-proof` moved from a human note into generator
  behavior. That is exactly the reusable asset we want from each closed positive/negative batch.

### Follow-up: dynamic queue produced a state-restore bug

The next two dynamic targets were resolved by ownership proof before execution:

```text
target.source.dynamic-state-ingress.pkg-infoschema-infoschema.v1
  result: retired/INVALID(sys-executor-factory-proof)
  asset:  /Users/bba/pc/ai-native-assets/source-targets-state-ingress-infoschema-retire-analysis.jsonl
  reason: LoadMaskingPolicies uses the infoschema sys executor factory; UseCurSession stays on an
          internal/sys session, not the user statement session.

target.source.dynamic-state-ingress.pkg-executor-brie.v1
  result: retired/INVALID(new-glue-session-proof)
  asset:  /Users/bba/pc/ai-native-assets/source-targets-state-ingress-brie-retire-analysis.jsonl
  reason: BACKUP/RESTORE is user-visible, but subtask SQL runs through CreateSession/
          UseOneShotSession glue sessions, not the user's state-carrying session.
```

The following queue head, `check_table_index`, produced a real RED after the obligation pivoted from
pending `TxnReadTS` ingress to exact user session state restoration:

```text
target: target.source.dynamic-state-ingress.pkg-executor-check-table-index.v1
asset:  /Users/bba/pc/ai-native-assets/source-state-ingress-check-table-index-results.jsonl
run:    run.source.check-table-index.testbed8220955.RED.invisible-index-plan-drift
control: run.source.check-table-index.testbed8220955.INFO.fast-check-off-control
```

Source proof:

- `pkg/executor/check_table_index.go:295` forces
  `e.Ctx().GetSessionVars().OptimizerUseInvisibleIndexes = true`.
- `pkg/executor/check_table_index.go:296-298` defers a hard reset to `false` instead of restoring
  the previous value.
- The same file already has `backupFastCheckSysSessionVars` / `restoreTo` for sys sessions, so the
  missing proof is specifically the outer user-session state restore.

SQL-only RED on testbed 8220955:

- Version: `8.0.11-TiDB-v9.0.0-beta.2.pre-1895-g5c9198e948`.
- Setup: invisible index `idx_v(v)`, `tidb_enable_fast_table_check=ON`,
  `tidb_opt_use_invisible_indexes=ON`.
- Before `ADMIN CHECK TABLE`, `EXPLAIN FORMAT='brief' SELECT * FROM t WHERE v=20` used
  `IndexReader/IndexRangeScan`.
- After `ADMIN CHECK TABLE`, `@@tidb_opt_use_invisible_indexes` still returned `1`, but the same
  query used `TableReader/TableFullScan`.
- With `tidb_enable_fast_table_check=OFF`, the before/after plan stayed
  `IndexReader/IndexRangeScan`.
- Evidence:
  `/Users/bba/pc/ai-native-assets/logs/source-state-ingress-check-table-index-testbed8220955.log`
  and
  `/Users/bba/pc/ai-native-assets/logs/source-state-ingress-check-table-index-testbed8220955-fast-off-control.log`.

Updated queue health:

```text
asset revisions: 100
runs:            RED=15, GREEN=12, INVALID=2, INFO=1
targets:         validated=13, retired=6, blocked=1, candidate=5
queue states:    validated=13, retired=6, blocked=1, needs_target_analysis=5
next target:     target.source.dynamic-state-ingress.pkg-executor-grant.v1
```

Method lesson:

- A source-target label is only the entry point. The high-value move is to follow the proof
  obligation to the nearest state contract.
- `@@` variable reads were a weak oracle here: they stayed green while optimizer behavior changed.
  The stronger oracle was same-session plan behavior against an invisible index.
- Negative ownership proofs are still productive because they train the generator; the positive
  pivot added a new reusable selector, `selector.user-session-state-restore.v1`.

### Follow-up: grant/revoke produced a pooled sys-session state bug

The next dynamic queue head, `target.source.dynamic-state-ingress.pkg-executor-grant.v1`, did not
validate as a pending-`TxnReadTS` ingress target:

- `GrantExec` uses a sys session for the main privilege-table writes.
- `userExists` uses restricted SQL without `UseCurSession`, so it also does not consume the user's
  pending one-shot stale-read state.

The useful move was to keep following the state ownership proof. That exposed a sibling REVOKE
metadata obligation:

```text
P: GRANT copies the outer User into a pooled sys session; REVOKE later uses a pooled sys session.
Q: metadata written by REVOKE identifies the current privilege actor.
F: composeTablePrivUpdateForRevoke writes Grantor from ctx.GetSessionVars().User, where ctx is the
   internal session, not the outer user session.
O: cross-user partial revoke keeps mysql.tables_priv row visible and checks Grantor.
```

SQL-only RED on testbed 8220955:

- A grants `SELECT,INSERT` on `ai_grant_bug.t` to target user C.
- `mysql.tables_priv.Grantor` is `ai_grantor_a@%`.
- B revokes only `SELECT`, leaving `INSERT` so the row remains.
- The row remains with `Table_priv=Insert`, but `Grantor` becomes empty instead of identifying B.

Evidence:

- `/Users/bba/pc/ai-native-assets/logs/source-state-ingress-grant-revoke-sys-session-user-testbed8220955.log`
- `/Users/bba/pc/ai-native-assets/source-state-ingress-grant-revoke-results.jsonl`

Updated queue health:

```text
asset revisions: 107
runs:            RED=16, GREEN=12, INVALID=2, INFO=1
targets:         validated=15, retired=6, blocked=1, candidate=3
queue states:    validated=15, retired=6, blocked=1, needs_target_analysis=3
next target:     target.source.dynamic-state-ingress.pkg-infoschema-issyncer-syncer.v1
```

Method lesson:

- The target label was again only an entry point. The original pending-state oracle retired for
  this path, but the state-owner proof still produced a bug.
- Cross-user schedules are a strong oracle for actor metadata because old actor, current actor, and
  default internal-session actor become distinguishable.
- This added `selector.sys-session-pooled-state-isolation.v1`: pooled internal sessions must be
  initialized or restored before their state is used as user-visible metadata.

### Follow-up: state-ingress dynamic queue drained

The remaining dynamic state-ingress targets were resolved by ownership proof:

```text
target.source.dynamic-state-ingress.pkg-infoschema-issyncer-syncer.v1
  result: retired/INVALID(background-sys-session-pool-proof)

target.source.dynamic-state-ingress.pkg-executor-importer-job.v1
  result: retired/INVALID(task-manager-new-session-proof)

target.source.dynamic-state-ingress.pkg-executor-importer-precheck.v1
  result: retired/INVALID(new-session-precheck-proof)
```

Updated queue health:

```text
asset revisions: 107
runs:            RED=16, GREEN=12, INVALID=2, INFO=1
targets:         validated=15, retired=9, blocked=1
queue states:    validated=15, retired=9, blocked=1
next target:     null
```

Generator validation:

- `store.py source-targets --rule state-ingress` now returns no new candidates.
- `store.py refill` returns no new candidates.
- `store.py source-targets --rule identity-token` returns no new candidates.
- `store.py source-targets --rule pooled-session-state` was added as a narrow rule for mutable
  sys-session state. Its first run produced no duplicate target and skipped `pkg/executor/grant.go`
  as covered by the existing grant/revoke pivot.
- `store.py source-targets --rule user-session-state-restore` was added as a narrow rule for
  temporary user-session state mutation. Its first run produced no duplicate target, skipped
  `pkg/executor/check_table_index.go` as covered, and screened `pkg/util/admin/admin.go` as a
  restores-original green sample.

Method lesson:

Queue drain is useful evidence. It says the current selector lane has been mined to terminal states
under the current generator, not that the product has no more bugs. The next efficient move is to
turn newly validated selectors into low-noise source-target rules, then mine incrementally from
that expanded asset base.
