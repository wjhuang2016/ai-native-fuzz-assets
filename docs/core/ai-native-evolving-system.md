# AI-Native Evolving System
> 2026-07-10. This is the control-plane note for turning the LOOP into a continuously improving system.

## Current State

The asset store now has three layers:

```text
asset graph       selector/oracle/scenario/schedule/fault/obligation/module_profile
run memory        RED/GREEN/INVALID/INFO tied to exact assets
target queue      held-out or live targets with computed next state
```

Validated targets now include:

- `issue59055/fix59157`: `ddl/notifier + DURABLE_BEFORE_ACK`
- `issue53843/fix53849`: `ddl/ingest + LIFECYCLE_EXACTLY_ONCE`
- `issue48164/fix48163`: `external-storage/s3 + ERROR_IDENTITY_PRESERVATION`
- `issue51846/fix52315`: `ddl/job-scheduler + OWNER_TOPOLOGY_HANDOFF`
- `issue62424/fix62607`: `gc/ddl-transaction + IMPLICIT_COMMIT_STATE_CLEANUP`

The issue62424 live-lift remains important because it showed the queue can move from DDL scheduling into GC/session observer boundaries without re-deriving the whole method. The system picked `issue62424`, target analysis filled the missing assets, the same pack went to execution, and GREEN came back into the database.

The latest two validated refills show the same pattern on different mechanisms. issue48164's broad S3 injected-error oracle debt became a current multipart-writer terminal-state proof; issue51846's broad owner-topology oracle debt became a current DDL owner-epoch identity proof. In both cases, the stored shared assets were not enough by themselves; the loop had to add the missing P/Q/F obligation and a strong observer for the exact state action or identity predicate.

The newest state-ingress SOURCE_TARGETS tick first stopped at `running/needs_counterpart_run`, then closed the loop with a TSO-stable RED/GREEN pair for `CREATE SESSION BINDING FROM HISTORY`. The selector was then turned into `store.py source-targets --rule state-ingress`, which generated three fresh candidates. Two were retired by target-analysis before execution because the internal SQL did not run on the user's session (`ddl/foreign-key` uses DDL worker/internal sessions; `executor/user-management` uses sys sessions). The remaining candidate, `planner/index-advisor`, produced a second user-visible RED/GREEN pair. This is the most important control-plane behavior so far: the asset store did not merely remember one bug, it generated a small candidate batch, used ownership proofs as negative cache, validated one sibling target, and drained the queue.

The dynamic state-ingress refresh then proved a stronger point: the queue can pivot from the original selector to a nearby state contract without starting over. `SHOW TABLE STATUS` became a contract-blocked boundary sample. `infoschema` and `BRIE` were retired by sys/new-session ownership proofs. The next target, `check_table_index`, did not fail through pending `TxnReadTS`; source inspection found a more concrete user-session state restore obligation. Fast `ADMIN CHECK TABLE` forces `OptimizerUseInvisibleIndexes=true` and defers a hard reset to `false`. On testbed 8220955, a session with `tidb_opt_use_invisible_indexes=ON` kept `@@` showing ON after `ADMIN CHECK TABLE`, but the same query stopped using an invisible index. This added `selector.user-session-state-restore.v1`, and showed that behavior oracles can be stronger than variable-display oracles.

The following grant/revoke target confirmed the same control-plane behavior in a different way. The initial pending-`TxnReadTS` hypothesis retired after ownership proof, because GRANT/REVOKE privilege writes use sys sessions. But following the state ownership proof exposed a pooled sys-session metadata bug: GRANT copies the outer user into a pooled sys session, release does not restore it, and REVOKE writes `mysql.tables_priv.Grantor` from the internal session user instead of the current outer user. On testbed 8220955, A granted `SELECT,INSERT`, then B revoked only `SELECT`; the row remained with `Insert`, but `Grantor` became empty. This added `selector.sys-session-pooled-state-isolation.v1`: pooled internal session state must not become user-visible statement metadata.

The remaining dynamic state-ingress queue was then drained by ownership proof, not by more execution. `issyncer` retired as a background sys-session-pool loop, `importer/job` retired as task-manager new-session metadata SQL, and `importer/precheck` retired as explicit `CreateSession` precheck SQL. A refresh of state-ingress, refill, and identity-token source-targets produced no new candidates. The control-plane improvement was to feed this back into the generator: state-ingress now has negative/covered path cache for grant/revoke, issyncer, and importer; the new narrow `pooled-session-state` and `user-session-state-restore` source rules find the two pivot selectors without creating duplicate active targets.

The newest error-identity tick proves the same loop can leave executor/session-state territory and still stay tight. The broad `oracle.injected-error-identity-survives.v1` was narrowed into a terminal-action obligation in `dxf/importinto`: if `chunkWorker.Close` gets a root error from `dataWriter.Close`, it must still close the already-created `indexWriter`. RED showed root error preservation alone was insufficient (`indexCloseCount=0`), and local GREEN showed the first/root error can be returned while the index writer still reaches flush/close. The promoted scenario is `scenario.multi-writer-terminal-close-after-peer-error.v1`, which makes the reusable asset explicit: error identity oracles need a sibling terminal-action observer.

The follow-up terminal-action source-target pass validated the selector as an incremental mining tool, not just a one-off explanation. `store.py source-targets --rule terminal-action-error` generated a small batch from current source. Target-analysis retired several false positives before execution: parent-owned `lightning preprocessEngine`, defer-covered `ImportSelectedRows`, defer-covered `simplesst.flushSortedKVs`, and locally suspicious but outer-finalizer-covered `dxf/importinto.onFinished`. The high-quality positive was `pkg/ingestor/ingestctrl/engine.sstIter.Close`: iterator Close error skipped reader Close. RED showed the reader remained open after the root iterator error; GREEN changed `sstIter.Close` to close both resources and return the combined error. This added `scenario.multi-resource-terminal-close-after-peer-error.v1` and, more importantly, added an owner/finalizer gate to the method.

The held-out target queue was drained, then refilled from oracle debt. One live-lift target has completed and the latest validated refill target is:

```text
completed lift       -> target.lift.issue62424.live-gc-safepoint.v1
result               -> GREEN on testbed 8220955
latest refill closed -> target.refill.target-issue51846-ddl-topology-handoff-v1.oracle-allowed-state-after-topology-fault-v1.v1
state                -> validated after current RED plus local-fix GREEN
latest source targets -> binding-history and planner/index-advisor state-ingress targets
state                 -> binding-history/index-advisor validated after RED/GREEN; check_table_index and grant/revoke validated by SQL-only RED/control-style probes
latest error target   -> target.source.error-identity.pkg-dxf-importinto-chunkworker-close.v1
state                 -> validated after current RED plus local-fix GREEN
latest source target  -> target.source.terminal-action-error.pkg-ingestor-ingestctrl-engine.close.v1
state                 -> validated after current RED plus local-fix GREEN
retired source targets -> DDL foreign-key, executor user-management, infoschema, BRIE, issyncer, importer job/precheck, lightning preprocessEngine, dxf onFinished, ImportSelectedRows, simplesst flushSortedKVs
blocked source target  -> SHOW TABLE STATUS contract boundary
next generated target -> target.source.terminal-action-error.pkg-objstore-compress.close.v1
```

## Why This Matters

Before this step, the asset store could remember one successful replay. Now it can also decide what to do next:

```text
validated        issue59055, issue53843, issue48164, issue51846, issue62424, issue62424-live-lift, issue53843 SQL-cancel refill, issue48164 S3 multipart refill, issue51846 owner-epoch refill, BR storewatch source-target, binding-history state-ingress, index-advisor state-ingress, check_table_index state-restore pivot, grant/revoke pooled-session metadata pivot, dxf/importinto multi-writer terminal Close, ingestctrl sstIter terminal Close
retired screens  BR registry heartbeat source-screen, TiFlash MPP cache source-screen, DDL foreign-key state-ingress, executor user-management state-ingress, infoschema sys-factory, BRIE new-glue-session, terminal-action parent/defer/outer-finalizer false positives
blocked targets  SHOW TABLE STATUS stale-read contract boundary
active targets   three terminal-action candidates remain; next is objstore compress Close
```

That is the start of an evolving system. Each tick changes the state of the queue:

```text
candidate -> needs_target_analysis -> ready_to_execute -> RED/GREEN/INVALID -> validated/promoted
```

## Current Metrics

```text
asset revisions: 117
runs:            RED=18, GREEN=14, INVALID=2, INFO=1
targets:         validated=17, retired=13, blocked=1, candidate=3
queue states:    validated=17, retired=13, blocked=1, needs_target_analysis=3
```

Reuse for the validated issue62424 target:

```text
gc/ddl-transaction + IMPLICIT_COMMIT_STATE_CLEANUP pack:
  asset_count: 8
  methodology_asset: 4
  target/root-boundary assets: 3
  execution_verified_oracle: 1
  open_gaps: []
```

## Added Interfaces

The prototype CLI now supports:

```text
store.py import <jsonl>        import assets, runs, links, and targets
store.py pack --module M --selector S
store.py queue [--include-done]
store.py refill [--jsonl-output PATH]
store.py next
store.py health
store.py stats
store.py source-targets --rule state-ingress|identity-token|pooled-session-state|user-session-state-restore|terminal-action-error --repo PATH
```

`next` is intentionally simple. It ranks by priority, consequence, effort, uncertainty, reuse count, and missing asset gaps. The key point is not perfect ranking yet; the key point is that the loop's next action is now derived from stored state instead of a fresh human choice.

## Latest Execution Result

For `issue62424`, the execution produced:

```text
vulnerable 0501de48c5b033f17f300960ecfe4f40f9bc1742:
  RED  TestDDLInsideTXNNotBlockMinStartTS failed because ReportMinStartTS never converged to the later active transaction startTS

fixed e9e8a04fe71611ed08ebfcf0755993812a07c521:
  GREEN the same upstream test passed
```

The promoted asset is deliberately narrow:

```text
oracle.ddl-minstartts-ignores-queued-ddl.v1 -> execution_verified
oracle.no-stale-txn-state-after-ddl.v1      -> still hypothesis
```

That split records what was truly proved. The replay proves the root `ReportMinStartTS` boundary. It does not yet prove live-cluster GC safepoint advancement for every DDL shape.

## Previous Target Analysis

For `issue51846`, the target-specific assets are now:

- `module.ddl-job-scheduler.v1`
- `obligation.ddl-job-scheduler.processing-preserved-across-owner-handoff.v1`
- `fault.ddl-job-scheduler.owner-retire-reenter-active-reorg.v1`

The reusable assets are:

- `selector.owner-topology-handoff.v1`
- `oracle.allowed-state-after-topology-fault.v1`
- `scenario.long-ddl-owner-change.v1`
- `schedule.network-partition-then-recover.v1`

The P/Q/F was sharpened from "PD leader partition" to the actual scheduler proof:

```text
P: RetireOwnerHook fires, so code believes local runningJobs can be discarded.
Q: old worker goroutines have stopped or no longer matter.
F: the same instance becomes owner again while the old reorg worker is still active.
```

The fix in PR 52315 confirms this shape: vulnerable code replaced `d.runningJobs` with `newRunningJobs()`, while the fix uses `d.runningJobs.clear()` so `processingIDs` still blocks duplicate local dispatch of the same ADD INDEX reorg job.

The execution produced:

```text
vulnerable bc841979a53e813d69c9fc8473ea0cc6703ef377:
  RED  after retire semantics, the still-processing job became runnable

fixed 970962bdbc52547620be80817a7fc78e75b6221f:
  GREEN the still-processing job remained non-runnable after clear()
```

The promoted asset is deliberately narrow:

```text
oracle.ddl-processing-id-survives-owner-retire.v1 -> execution_verified
oracle.allowed-state-after-topology-fault.v1      -> still hypothesis
```

That split records what was truly proved. The replay proves the root duplicate-dispatch guard. It does not yet prove the full live-cluster `ADD INDEX + PD leader partition + final job state` path.

## Previous Execution Result

For `issue53843`, the target-specific assets were:

- `module.ddl-ingest.v1`
- `obligation.ddl-ingest.unregister-cleanup.v1`
- `fault.ddl-ingest.cancel-after-engine-open.v1`

The reusable assets are:

- `selector.lifecycle-exactly-once.v1`
- `oracle.no-leak-after-cancel.v1`
- `scenario.long-operation-cancel-window.v1`
- `schedule.cancel-active-resource.v1`

The execution produced:

```text
vulnerable cc127c14b8cc9887b1be946baa2f220690722c63:
  RED  close_calls=2 cleanup_calls=1

fixed 9c500ad9cb52c72372ad9d82f2a72190788d9478:
  GREEN close_calls=1 cleanup_calls=1 remaining_engines=0
```

The promoted asset is deliberately narrow:

```text
oracle.concurrent-unregister-exactly-once.v1 -> execution_verified
oracle.no-leak-after-cancel.v1               -> still hypothesis
```

That split records what was truly proved. The replay proves the root-cause cleanup race and the exact fix shape. It does not yet prove the full SQL-level `ADD INDEX + ADMIN CANCEL DDL JOBS` user flow on a live cluster.

## Latest Live Lift

The refill target was:

```text
target:      target.lift.issue62424.live-gc-safepoint.v1
module:      gc/ddl-transaction
selector:    IMPLICIT_COMMIT_STATE_CLEANUP
obligation:  obligation.gc-ddl-transaction.live-gc-safepoint-advances.v1
result:      GREEN
```

The testbed observer used the exact consumer state path:

```text
processlist TxnStart -> InfoSyncer server minStartTS -> PD/etcd /tidb/server/minstartts
```

Key evidence:

```text
DDL TxnStart: 467568057103679489
sample 16: DDL still visible, minStartTS=467568057116524554
sample 29: DDL still visible, minStartTS=467568066213183509
sample 47: DDL still visible, minStartTS=467568072111423519
```

This proves that, on the fixed testbed build, the queued DDL session's stale TxnStart did not pin server minStartTS. It does not prove the full GC safe point cycle, because `tidb_gc_run_interval` / `tidb_gc_life_time` remained at 10m.

## Automated Refill

The manual refill has now been turned into a command:

```text
store.py refill --limit 10 --jsonl-output /Users/bba/pc/ai-native-assets/refill-candidates-20260710.jsonl
```

It generated and imported three new candidates from remaining oracle debt:

```text
1. target.refill.target-issue53843-ingest-writer-leak-on-cancel-v1.oracle-no-leak-after-cancel-v1.v1
   module=ddl/ingest, selector=LIFECYCLE_EXACTLY_ONCE, initial state=needs_target_analysis

2. target.refill.target-issue48164-s3-uploader-error-precedence-v1.oracle-injected-error-identity-survives-v1.v1
   module=external-storage/s3, selector=ERROR_IDENTITY_PRESERVATION, state=needs_target_analysis

3. target.refill.target-issue51846-ddl-topology-handoff-v1.oracle-allowed-state-after-topology-fault-v1.v1
   module=ddl/job-scheduler, selector=OWNER_TOPOLOGY_HANDOFF, state=needs_target_analysis
```

At refill time, `store.py next` selected the ingest no-leak refill target first. This was intentional: refill only creates a candidate and marks that it needs target analysis. The next loop had to add a concrete `obligation_key` and P/Q/F boundary before execution.

The control-plane fix made during this refill remains important: `target_queue` now scopes prior runs to `payload.obligation_key` when present. Without that, a new target under the same `module + selector` could be incorrectly marked `validated` by a neighboring obligation's RED/GREEN history.

A second fix was needed after automating refill: a refill candidate with `broad_oracle` but no `obligation_key` must not inherit the base obligation's runs. In the queue, `base_obligation` is provenance; `obligation_key` is execution identity.

## First Refill Target Analysis

The first generated refill target has now been analyzed instead of executed blindly:

```text
target:     target.refill.target-issue53843-ingest-writer-leak-on-cancel-v1.oracle-no-leak-after-cancel-v1.v1
obligation: obligation.ddl-ingest.sql-cancel-terminal-no-live-resource.v1
oracle:     oracle.ddl-ingest-cancel-terminal-no-live-resource.v1
fault:      fault.ddl-ingest.sql-cancel-after-local-engine-open.v1
state:      validated after current GREEN plus vulnerable SQL-level RED
```

The important refinement is that `no-leak-after-cancel` is no longer treated as "SQL cancel succeeded." The target-specific GREEN requires:

```text
active_resource_window_hit=true
job reaches an allowed terminal state
backend context / engines / opened writers are all terminal
no duplicate close of the same engine UUID
no close-of-closed-channel or DDL worker panic log for the job
```

If the cancel lands before local ingest engine registration, or if the harness cannot expose resource counters, the result must be `INVALID(harness)`, not GREEN. This is the practical difference between broad oracle debt and an executable proof obligation.

First execution result:

```text
run:     run.issue53843.refill.current.13282a8.GREEN
command: make failpoint-enable && go test -tags=intest ./pkg/ddl/ingest -run '^TestAINativeAddIndexCancelLeavesNoLiveMockIngestResource$' -count=1 -v && make failpoint-disable
log:     /Users/bba/pc/ai-native-assets/logs/issue53843-refill-sql-cancel-green-go125.log

observed:
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

The target is not promoted to `validated`: this is a current/fixed GREEN only. The next proof step is a RED counterpart that does not mask the vulnerable cleanup race.

Follow-up root-boundary RED:

```text
run:     run.issue53843.refill.vulnerable.cc127c14.RED.memory-double-release
command: go1.21.0 test ./pkg/ddl/ingest -run '^TestAINativeConcurrentUnregisterDoesNotDoubleReleaseMemory$'
log:     /Users/bba/pc/ai-native-assets/logs/issue53843-refill-vulnerable-root-boundary-red-go121.log

observed:
  expected_current_usage=0
  actual_current_usage=-2877
  attempt=0
```

This was useful but not sufficient. It proved the vulnerable cleanup boundary can double-release the same engine resource ledger, so the selector should include "release quota once" alongside "close once" and "cleanup once." But it was still tied to `obligation.ddl-ingest.unregister-cleanup.v1`; it did not prove that the SQL cancel target had a vulnerable RED. The queue staying at `needs_counterpart_run` at that point was therefore a feature, not a failure.

Final SQL-level RED counterpart:

```text
run:     run.issue53843.refill.vulnerable.cc127c14.RED.sql-cancel-double-cleanup
test:    TestAINativeIssue53843SQLCancelDoubleCleanupRED
log:     /Users/bba/pc/ai-native-assets/logs/issue53843-refill-vulnerable-sql-cancel-red-go121.log

observed:
  jobID=106
  registered=1
  writes=1
  unregister_calls=2
  cleanup_ledger=-1
  cancelled=true
  alter result=ErrCancelledDDLJob
```

Why this closes the refill target:

```text
current GREEN obligation = obligation.ddl-ingest.sql-cancel-terminal-no-live-resource.v1
vulnerable RED obligation = obligation.ddl-ingest.sql-cancel-terminal-no-live-resource.v1
```

The SQL flow itself issues `ALTER TABLE ... ADD INDEX` and `ADMIN CANCEL DDL JOBS`; the observing backend manager only exposes the cleanup ownership ledger. This keeps the evidence narrower than a real local-file leak replay, but it is strong enough for the target-specific SQL-cancel oracle because it proves active ADD INDEX work (`registered=1,writes=1`) and duplicate cleanup ownership (`unregister_calls=2,cleanup_ledger=-1`) in the vulnerable DDL path.

Promotion result:

```text
obligation.ddl-ingest.sql-cancel-terminal-no-live-resource.v1 -> execution_verified
oracle.ddl-ingest-cancel-terminal-no-live-resource.v1         -> execution_verified
fault.ddl-ingest.sql-cancel-after-local-engine-open.v1        -> execution_verified

oracle.no-leak-after-cancel.v1                                -> still hypothesis
```

Store health after promotion:

```text
asset revisions: 59
runs:            RED=7, GREEN=7
targets:         validated=7, candidate=2
queue states:    validated=7, needs_target_analysis=2
next target:     target.refill.target-issue48164-s3-uploader-error-precedence-v1.oracle-injected-error-identity-survives-v1.v1
```

## Stop Rules

For issue53843, do not broaden into all ingest/add-index behavior. The validated proof obligation was:

```text
resource registered by one DDL job must be closed/unregistered exactly once on cancel/rollback
```

The remaining issue53843 debt should be reopened only if the next question is specifically stronger than this SQL-level proof: "can the same root-boundary oracle be lifted to a live/testbed local-backend file or writer-leak observer?"

## Second Refill Target: S3 current bug found

The next queue item was:

```text
target: target.refill.target-issue48164-s3-uploader-error-precedence-v1.oracle-injected-error-identity-survives-v1.v1
base:   historical issue48164 S3 concurrent uploader error identity
debt:   broad oracle.injected-error-identity-survives.v1 still hypothesis
```

The important asset-reuse lesson is that the system did not replay the old bug. It used the old selector as a search lens and found a new current-state obligation in `pkg/objstore/s3store`:

```text
P: multipart upload has started, part 1 succeeds, part 2 UploadPart returns an injected root error.
Q: after a part failure, Close must not complete a prefix-only object and must preserve the root error.
F: current multipartWriter returns the Write error but stores no failed state; Close always completes accumulated completeParts.
```

Current RED:

```text
run: run.issue48164.refill.current.13282a8.RED.s3-multipart-part-fail-close
observed:
  writeErr=ai-native mock upload part failed
  closeErr=<nil>
  completeCalls=1
  completedParts=1
```

Local GREEN:

```text
run: run.issue48164.refill.local-fix.13282a8.GREEN.s3-multipart-part-fail-close
fix shape:
  multipartWriter stores the first UploadPart error
  Close calls AbortMultipartUpload after failed state
  Close returns the stored root error

observed:
  writeErr=ai-native mock upload part failed
  closeErr=ai-native mock upload part failed
  abortCalls=1
```

This is the strongest evidence so far for the asset-store idea:

- A historical bug asset generated a refill target.
- The queue forced target analysis before execution, avoiding broad-oracle overclaim.
- Source P/Q/F extraction found a different current code path under the same selector.
- A small matrix produced a new RED on current master.
- A minimal local fix converted the same oracle to GREEN.

Store health after import:

```text
asset revisions: 62
runs:            RED=8, GREEN=8
targets:         validated=8, candidate=1
queue states:    validated=8, needs_target_analysis=1
next target:     target.refill.target-issue51846-ddl-topology-handoff-v1.oracle-allowed-state-after-topology-fault-v1.v1
```

Scope discipline:

`oracle.s3-multipart-failed-part-no-complete-preserve-root.v1` is validated. The broad `oracle.injected-error-identity-survives.v1` stays hypothesis. Similar code shapes in OSS/KS3 should become explicit follow-up targets, not silent blast-radius claims.

Method update:

The broad error-identity oracle is too weak if it only checks final error text. For storage writers, the oracle must also observe terminal remote action:

```text
root error injected
+ terminal path executed
+ Complete/Abort state action observed
+ final error identity checked
```

That additional state-action observer is what turned a possible style preference into a concrete correctness bug: current master can publish a prefix-only multipart object after a later part failed.

## Third Refill Target: DDL owner epoch current bug candidate

The next queue item was:

```text
target: target.refill.target-issue51846-ddl-topology-handoff-v1.oracle-allowed-state-after-topology-fault-v1.v1
base:   historical issue51846 DDL owner topology handoff
debt:   broad oracle.allowed-state-after-topology-fault.v1 still hypothesis
```

Again, the system did not replay the old bug. It reused the owner-topology selector and found a different current-state obligation:

```text
P: runReorgJob accepts a doneCh result when res.ownerTS equals current ownerTS.
Q: ownerTS equality proves same DDL owner epoch.
F: OnBecomeOwner allocates ownerTS with time.Now().Unix(), so rapid retire/re-become can reuse the same token.
```

Current RED:

```text
run: run.issue51846.refill.current.13282a8.RED.owner-epoch-second-collision
observed:
  previousOwnerTS=1000
  curOwnerTS=1000
  stale reorg result would pass the ownerTS equality filter
```

Local GREEN:

```text
run: run.issue51846.refill.local-fix.13282a8.GREEN.owner-epoch-renewal
fix shape:
  renew ownerTS as max(wallTS, previous+1)
  OnBecomeOwner calls renewOwnerTS(time.Now().Unix())

observed:
  TestAINativeOwnerEpochRenewalRejectsSameSecondStaleResult PASS
```

New reusable lesson:

- "identity token" is its own proof-obligation class. If an async result filter uses equality on a token, the code that mints the token must prove uniqueness across the lifecycle boundary it is meant to separate.
- Broad topology oracles should first be narrowed to a local identity predicate before cluster chaos. That made the RED deterministic and made the GREEN exact.
- The live cluster lift is still useful, but it answers frequency/severity. The root-boundary test already proves the predicate is unsound.

Store health after import:

```text
asset revisions: 65
runs:            RED=9, GREEN=9
targets:         validated=9
queue states:    validated=9
next target:     null
```

Queue implication:

The active queue is now drained. The next autonomous tick should run `SOURCE_TARGETS` or another explicit `refill` pass, unless the chosen direction is to lift the new owner-epoch oracle to the testbed.

## SOURCE_TARGETS Gap After Queue Drain

Running `store.py refill` after the owner-epoch import produced no new candidates:

```text
candidates: []
skipped:
  oracle.allowed-state-after-topology-fault.v1      already_has_refill_target
  oracle.injected-error-identity-survives.v1        already_has_refill_target
  oracle.no-leak-after-cancel.v1                    already_has_refill_target
  oracle.no-stale-txn-state-after-ddl.v1            already_has_refill_target
```

This exposes the next control-plane gap: once all existing broad-oracle refill targets have been executed, the system needs a `SOURCE_TARGETS` generator rather than another refill pass.

Candidate source rule from this tick:

```text
identity-token async filter:
  find code that mints a lifecycle/owner/session token
  later accepts or rejects async work by token equality
  require the token minting site to prove uniqueness across the lifecycle boundary
```

A naive scan for `time.Now().Unix()` is too broad. Most hits are random seeds, telemetry timestamps, statement-summary windows, or last-used metrics. They become candidates only if the value is later used as an equality token that gates ownership, result acceptance, cleanup, or retry. This is the source rule that should be encoded before the next autonomous run.

## SOURCE_TARGETS Execution: Identity Token Selector

The source rule was promoted into assets and immediately exercised:

```text
selector.identity-token-async-filter.v1
oracle.identity-token-distinguishes-lifecycle.v1
scenario.async-work-overlaps-lifecycle-change.v1
schedule.rapid-lifecycle-renewal-vs-token-precision.v1
```

The first pass produced one negative screen and one positive hit.

Negative screen:

```text
target.source.br-registry-heartbeat-token-precision.v1
run.source.br-registry.current.13282a8.INVALID.heartbeat-token-precision-screen
```

Why it matters:

```text
BR registry heartbeat matched token equality,
but default heartbeat and stale-check cadence are 60s,
so same-second token collision is not a product-feasible stale schedule.
```

Positive hit:

```text
target.source.br-storewatch-same-second-reboot.v1
run.source.br-storewatch.current.13282a8.RED.same-second-reboot-missed
run.source.br-storewatch.local-fix.13282a8.GREEN.same-second-reboot-notified
```

Why it matters:

```text
BR storewatch already treats Up -> Offline as a disconnect and StartTimestamp change as reboot.
The missing red cell is Up(T) -> Offline(T) -> Up(T):
  current code skips OnReboot because T did not change
  BR backup/restore consumers miss the conservative recovery signal
```

Local fix shape:

```text
OnReboot if StartTimestamp changed OR previous state was not Up and current state is Up.
```

Updated health:

```text
asset revisions: 76
runs:            RED=10, GREEN=10, INVALID=1
targets:         validated=10, retired=1
queue states:    validated=10, retired=1
next target:     null
```

Control-plane lesson:

SOURCE_TARGETS needs two outputs, not one:

```text
1. positive validated targets that prove the selector can still find new bugs
2. retired/INVALID screens that teach the selector what not to mine
```

The pair is what prevents method drift. Without the BR registry negative screen, the new identity-token selector would degrade into broad timestamp scanning. Without the BR storewatch positive hit, it would be only a retrospective explanation of the DDL ownerTS bug.

## SOURCE_TARGETS Generator: Identity Token, First Incremental Queue Tick

The source rule is now executable:

```text
python3 ai-native-assets/store.py source-targets \
  --rule identity-token \
  --repo /Users/bba/pc/tidb \
  --jsonl-output /Users/bba/pc/ai-native-assets/source-targets-identity-token-generated-20260710.jsonl
```

Observed behavior:

```text
covered:
  DDL owner epoch result token       -> covered_asset_exists
  BR registry heartbeat stale token  -> target_exists
  BR storewatch same-second reboot   -> target_exists

new candidate:
  target.source.tiflash-mpp-logical-core-starttimestamp.v1
```

Why this matters:

- The generator did not re-queue old DDL/BR cases. It used the asset store as memory.
- The raw scan still reports broad hits such as `time.Now().Unix()`, but only seed-backed token-gated decisions become targets.
- The new TiFlash MPP cache candidate is intentionally `SOURCE_ONLY` and lands in `needs_target_analysis`, not `ready_to_execute`.

Current candidate:

```text
module:       planner/tiflash-mpp-cache
selector:     IDENTITY_TOKEN_ASYNC_FILTER
evidence:
  pkg/planner/core/optimizer.go
    cached LogicalCPUCount is reused when Address and StartTimestamp match
  pkg/domain/infosync/tiflash_manager.go
    TiFlash StartTimestamp comes from tiflash.StartTime.Unix()
  pkg/store/copr/mpp_probe.go
    GlobalMPPServerInfoManager caches LogicalCPUCount + StartTimestamp
```

Queue health immediately after import:

```text
asset revisions: 76
runs:            RED=10, GREEN=10, INVALID=1
targets:         validated=10, retired=1, candidate=1
queue states:    validated=10, retired=1, needs_target_analysis=1
next target:     target.source.tiflash-mpp-logical-core-starttimestamp.v1
```

Promotion rule:

```text
Do not claim a bug from token precision alone.
Before execution, prove:
  G3: a TiFlash same-second restart/config-change schedule is product-feasible
  G4: stale LogicalCPUCount has a user-visible effect strong enough to observe
If either proof fails, record INVALID(schedule-proof/effect-proof) and keep the negative screen.
```

Control-plane lesson:

The loop now has a third motion after `refill` drains:

```text
validated RED/GREEN -> oracle debt refill -> source-target generator -> target-analysis gate
```

This is the first concrete step toward an evolving system. Old bugs become selector seeds, selector seeds propose held-out candidates, and the queue state decides whether the next tick should analyze, execute, or retire.

## SOURCE_TARGETS Negative Cache: TiFlash Candidate Retired

The TiFlash candidate was analyzed and retired instead of executed:

```text
target:       target.source.tiflash-mpp-logical-core-starttimestamp.v1
new status:   retired
class:        LOW_VALUE
asset file:   /Users/bba/pc/ai-native-assets/source-targets-tiflash-mpp-cache-retire-analysis.jsonl
```

Gate result:

```text
G1 token-gated decision:      pass
G2 lifecycle/cache overlap:   pass
G3 product collision schedule not proven
G4 effect oracle:             weak pass, planner/performance only
```

Why it was retired:

```text
The forced RED would require equal StartTimestamp plus different LogicalCPUCount.
That can be unit-tested, but it would not prove that TiFlash can restart/re-register
within the same second, reuse the same address, and change logical CPU count in a
product-feasible schedule.
```

Generator improvement:

```text
store.py source-targets now distinguishes:
  target_exists(status=validated)
  retired_target_exists(status=retired)
```

After the retire import and generator rerun:

```text
candidates:    []
skipped:
  DDL owner epoch    covered_asset_exists
  BR registry        retired_target_exists
  BR storewatch      target_exists(status=validated)
  TiFlash MPP cache  retired_target_exists

queue states: validated=10, retired=2
next target:  null
```

Refill guard added in the same tick:

```text
Problem:
  store.py refill generated a "refill of a refill" from
  oracle.identity-token-distinguishes-lifecycle.v1 back onto the owner-epoch refill target.

Fix:
  do not use these targets as refill bases:
    target_key starts with target.refill.
    obligation_class contains REFILL
    provenance.source_kind == refill_candidate

Verification:
  refill-candidates-20260710-after-tiflash-retire.jsonl has 0 rows
  skipped reason is recursive_refill_base_only
```

Control-plane lesson:

Negative cache is an asset, not cleanup. Without it, SOURCE_TARGETS would repeatedly rediscover
the same attractive but low-quality token shape. With it, the loop can preserve the selector while
moving on to a different seed rule or a new source-target lane. Refill needs the same discipline:
oracle debt should generate fresh target obligations, not recursive work items.

## State-Ingress Source Target: Binding History RED/GREEN

The S23/id1230001 method case produced a new source-target lane:

```text
selector: STATE_INGRESS_INTERNAL_SQL
target:   target.source.binding-history-executeinternal-txreadts.v1
module:   planner/binding-history
state:    validated
asset:    /Users/bba/pc/ai-native-assets/source-state-ingress-binding-history-tso-pair-results.jsonl
```

Source obligation:

```text
P: CREATE BINDING FROM HISTORY needs a plan_digest lookup from statement summary.
Q: that internal lookup is isolated from a pending user one-shot stale-read state.
F: it calls ExecuteInternal on the current session, which only toggles InRestrictedSQL and then
   re-enters the normal stale-read processor path.
```

Evidence:

```text
root-boundary RED:
  current-session restricted SQL consumes pending TxnReadTS
  log: /Users/bba/pc/ai-native-assets/logs/source-state-ingress-current-restricted-sql-txreadts-red.log

user-visible RED:
  SET TRANSACTION READ ONLY AS OF TIMESTAMP @ts
  CREATE SESSION BINDING FROM HISTORY USING PLAN DIGEST ...
  next SELECT expected stale rowset [1], actual current rowset [1,2]
  TSO-stable log: /Users/bba/pc/ai-native-assets/logs/source-state-ingress-binding-history-txreadts-tso-red.log

local-fix GREEN:
  temporary ExecuteInternal patch saved/restored pending TxnReadTS and SnapshotInfoschema
  before=467570643908952064 after=467570643908952064 next_select_rows=[[1]]
  log: /Users/bba/pc/ai-native-assets/logs/source-state-ingress-binding-history-txreadts-tso-local-green.log
```

Current health after import:

```text
asset revisions: 90
runs:            RED=13, GREEN=11, INVALID=2
targets:         validated=11, retired=2
queue states:    validated=11, retired=2
next target:     null before generator refill
```

Generator follow-up:

```text
command:      store.py source-targets --rule state-ingress --repo /Users/bba/pc/tidb
output:       /Users/bba/pc/ai-native-assets/source-targets-state-ingress-generated-20260710.jsonl
imported:     3 candidate targets
resolved:
  target.source.ddl-foreign-key-use-cur-session-state-ingress.v1
    -> retired / INVALID(session-ownership-proof)
  target.source.executor-user-management-executeinternal-state-ingress.v1
    -> retired / INVALID(sys-session-isolation-proof)
  target.source.planner-index-advisor-executeinternal-state-ingress.v1
    -> validated / current RED + local-fix GREEN
next target:  null
```

Why this is still semantic-gray as a product bug:

```text
The method evidence is promoted: same oracle RED on current and GREEN under a local boundary fix.
The product contract is still semantic-gray: if SET TRANSACTION READ ONLY AS OF TIMESTAMP is defined
as applying to any next statement, then CREATE BINDING consuming it may be low-value/design behavior.
If it is meant to apply to the next user read/execute statement, the RED is a real state-ingress bug.
```

Control-plane lesson:

`needs_counterpart_run` is a first-class useful state, and this tick proved it can close cleanly.
The loop remembered a concrete user-visible RED, forced a stronger oracle, accepted GREEN only when
the same rowset assertion passed, and then used the validated selector to refill a small candidate
batch. The second pass added a useful negative gate: a generated source target must prove the
internal SQL shares the user's session before execution. This is what prevented foreign-key and
user-management from turning into noisy RED attempts. Shared cross-module assets also need a real
`module=shared` identity in the store; otherwise the scheduler may report false asset gaps for
reusable selector/oracle/scenario records.

## State-Ingress Source Target: Index Advisor RED/GREEN

The second positive state-ingress target validated the same selector against a different user-visible
wrapper:

```text
selector: STATE_INGRESS_INTERNAL_SQL
target:   target.source.planner-index-advisor-executeinternal-state-ingress.v1
module:   planner/index-advisor
state:    validated
asset:    /Users/bba/pc/ai-native-assets/source-state-ingress-indexadvisor-results.jsonl
```

Source obligation:

```text
P: RECOMMEND INDEX RUN calls indexadvisor.AdviseIndexes with the current session context.
Q: index advisor helper SQL is isolated from a pending user one-shot stale-read state.
F: indexadvisor.exec casts the same session to SQLExecutor, calls ExecuteInternal, and drains the
   result set through the generic session stale-read path.
```

Evidence:

```text
current RED:
  log: /Users/bba/pc/ai-native-assets/logs/source-state-ingress-indexadvisor-txreadts-red.log
  before=467570885856329728 after=0 next_select_rows=[[1] [2]]

local-fix GREEN:
  log: /Users/bba/pc/ai-native-assets/logs/source-state-ingress-indexadvisor-txreadts-local-green.log
  before=467570913639661568 after=467570913639661568 next_select_rows=[[1]]
```

Important fix-probe lesson:

```text
Post-hoc restoration after ExecuteInternal returns is too late for some paths, because result-set
drain/close can consume and clean up TxnReadTS after ExecuteInternal has returned. The effective
local isolation probe clears pending one-shot state before internal SQL enters the generic session
path, then restores it after the internal statement boundary.
```

Current health after import:

```text
asset revisions: 93
runs:            RED=14, GREEN=12, INVALID=2
targets:         validated=12, retired=4
queue states:    validated=12, retired=4
next target:     null
```

## State-Ingress Generator V2: Ownership Gate Becomes System Behavior

The first state-ingress batch produced two validated positives and two retired negatives. The
important system step was to feed that experience back into the generator rather than keeping it as
a human memory.

New command/output:

```text
python3 ai-native-assets/store.py source-targets \
  --rule state-ingress \
  --repo /Users/bba/pc/tidb \
  --limit 20 \
  --jsonl-output /Users/bba/pc/ai-native-assets/source-targets-state-ingress-dynamic-20260710.jsonl

imported targets: 9
queue states: blocked=1, needs_target_analysis=8, validated=12, retired=4
next: target.source.dynamic-state-ingress.pkg-infoschema-infoschema.v1
```

What changed:

```text
old behavior:
  static seeds plus human session-ownership triage

new behavior:
  dynamic scan for ExecuteInternal / ExecRestrictedSQL / UseCurSession
  plus generator-side ownership gate and priority scoring
```

The gate now encodes these rules:

- Known validated/retired paths are skipped.
- DDL worker paths, background/session packages, sys sessions, session pools, new sessions, nil
  restricted SQL without `UseCurSession`, and internal helper packages are screened out or
  downgraded before execution.
- `UseCurSession` on a local hit is a high-priority positive signal.
- File-level sys/new-session markers are no longer whole-file vetoes. They become debt on the
  candidate. This recovered `pkg/executor/show.go`, whose `SHOW TABLE STATUS` path has a clear
  `ExecOptionUseCurSession` signal even though unrelated code in the file uses sys/new sessions.
- Auxiliary wrappers such as BRIE/importer remain in the queue but below direct user-statement
  wrappers.

Why this matters for the evolving system:

The loop now has a reusable "negative-memory to selector gate" mechanism. A retired target is not
just a failed attempt; it becomes a rule that protects future runs from noisy RED probes. A positive
target is not just a bug; it becomes a source pattern that can refill the queue. The first dynamic
candidate, `SHOW TABLE STATUS`, also added a third outcome: **live behavior, contract blocked**.

```text
P: SHOW TABLE STATUS performs current-session restricted SQL under ExecOptionUseCurSession.
Q: that internal metadata read should not consume a pending user one-shot stale-read state.
F: the restricted SQL enters the generic session stale-read path and may clean TxnReadTS.
oracle: direct AS OF/current rowset controls plus pending-TxnReadTS before/after observation.
```

Testbed 8220955 proved the behavior:

```text
AS OF control:        1
current control:      1,2
after SHOW:           1,2
direct SET+SELECT:    1
evidence: /Users/bba/pc/ai-native-assets/logs/source-state-ingress-show-table-status-testbed8220955.log
```

The target is blocked, not filed, because `SHOW TABLE STATUS` is itself a user-visible SHOW/query
statement. If the product contract says SHOW counts as the next query statement for
`SET TRANSACTION READ ONLY AS OF TIMESTAMP`, the behavior is expected. If the contract says only the
next user data read/execute should consume it, the same evidence becomes a bug candidate.

The scheduler correctly moved to:

```text
target.source.dynamic-state-ingress.pkg-infoschema-infoschema.v1
```

That next target must start with session-ownership proof for masking-policy loading before any RED
execution.

## 2026-07-13 cloned-view identity increment

The system imported id2070003 as a current-source-only optimizer target rather than a historical
review seed:

```text
target:          target.optimizer.correlate-clone-access-path-identity-loss.v1
selector:        CLONED_CANONICAL_ACTIVE_VIEW_IDENTITY
oracle:          alternative plan rowset plus scan altitude
asset revisions: 287
runs:            RED=59, GREEN=57, INVALID=10, INFO=1
pack:            7 directly reusable assets, open_gaps=[]
```

The reusable increment is an alias graph over cloned collections plus a negative boundary that
records when a downstream repair owner masks the defect. Future candidate generation should search
current clone/copy routines for canonical and filtered views, then rank producer/consumer splits
whose stale view feeds an empty, complete, cached, or fast-path decision. Upstream history remains a
post-RED dedup channel only.
