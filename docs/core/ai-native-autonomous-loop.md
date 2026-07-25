# Autonomous Discovery Loop
> 2026-07-02. How to run the proof-obligation method as an unattended, self-iterating loop. This is the automation spec that sits on top of methodology-v2; v2 defines the moves, this defines the controller that plays them without a human in the inner loop.

## The core judgment (read this first)

The bottleneck for full automation is NOT the loop logic. It is **the trustworthiness of the judgment**. An oracle that false-negatives lets real bugs pass; an oracle that false-positives floods the queue with noise. Put either into an unattended loop and the loop just manufactures wrong conclusions faster. So the automation's first duty is not "find more bugs" — it is "keep the instruments that decide *bug vs not* calibrated." Everything below follows from that.

This is why the recent oracle work (adversarial verification, held-out, the refutation taxonomy, recursive trigger-evidence) is the prerequisite for automation, not a detour: it is the only thing that makes an unattended verdict believable.

## Campaign objective and severity admission

The object-loop is not a general bug counter. Its primary output is a **consequence-3 user
failure**: silent data loss/corruption, a published invariant violation, a durable cross-session
state leak, or a DDL/transaction liveness failure that prevents a user operation from completing.
Methodology samples still matter, but they must not silently displace that objective.

Before a target is eligible for `MINE_BUG`, its card must carry one of these admissions:

```text
C3_DIRECT       The predicted failure is already C3 and has a user-visible, state-observing
                oracle: row multiset/constraint/invariant, durable metadata plus a failing
                follow-up operation, or DDL/txn progress and terminal-state observation.
                For transient-fault availability targets, the card must also name a sibling
                green control and a terminal oracle that distinguishes "never retried" from
                "retried and still failed" (for example `err_count=1` rollback vs exhausted
                retry budget).

C2_WITH_LIFT     The first observable symptom may be C2, but the card states the concrete
                escalation path to C3 and the oracle that could prove it. Example: a rollback
                cleanup omission must be shown to leave a residual object that corrupts a retry,
                blocks future DDL, or changes visible rows. A local Close-count alone is not lift.

NOT_ADMITTED     C1 wrong-error, metadata-only drift, ordinary resource cleanup, or any target
                lacking a credible C3 consequence oracle. It may calibrate an oracle/selector,
                but cannot consume the main discovery slot or become a public-bug candidate.
```

`C2_WITH_LIFT` is a bounded investigation: if the escalation oracle is clean, close it as
calibration/negative evidence and return to C3 sourcing. Do not relabel a clean C2 symptom as a
high-severity bug merely because the source shape was interesting.

## Loop shape

One tick:

```text
1. SENSE     read state: selector ledger, oracle library, catalog queue, found_bug, health metrics
2. SCHEDULE  pick the highest-priority ready action (priority list below)
3. ACT       run that action to a terminal state (a Family-Resolution close, or an oracle verdict)
4. INTEGRATE write results back into the ledgers/library/catalog/bug-lib
5. HEALTH    recompute drift metrics; if degraded, self-correct or escalate
6. GATE      check human-escalation and budget conditions; pause if hit
7. repeat
```

The loop never "parks." Every action ends in a terminal state (methodology-v2 Family Resolution), so the loop always has a next move or a documented reason it stopped.

## Action space (finite, that is the point)

An autonomous agent chooses only from these; a bounded action space is what makes the loop analyzable.

```text
MINE_BUG        run one audit-card -> matrix -> pause-gate cycle on a queued target
VERIFY_ORACLE   held-out + adversarial pass on an oracle to move it up the evidence tiers
FIX_ORACLE      repair a REFUTED oracle by its refutation class (split/harden/rescope)
MINE_ORACLE     derive a new oracle from a Q_claim or a held-out FN ticket
MINE_SELECTOR   reverse-engineer a new selector from a fresh hit, or retire a dead one
EXTEND_BATTERY  add a D_dims entry from a hit; walk the battery for an under-covered target
SOURCE_TARGETS  refresh the catalog queue (prover-name sweep, diff-directed intake)
```

## Scheduling priority (judgment health before discovery)

Evaluate top-down; do the first action whose precondition is met.

```text
P0  FIX_ORACLE      any oracle is REFUTED. The instruments are broken; every downstream verdict
                    using it is suspect. Fix before mining anything else.
P1  VERIFY_ORACLE   any oracle used to CONFIRM a bug is below TRUSTED (single-shape held-out,
                    LLM-VERIFIED-only, or USED). Calibrate the instrument before trusting its output.
P2  VERIFY_ORACLE   any TRUSTED oracle has had no adversarial pass, OR a HYPOTHESIS oracle sits in
                    an active suite. Pre-empt false confidence.
P3  MINE_ORACLE     any open held-out FN ticket (a symptom class with no firing oracle).
P4  MINE_BUG        selector ledger has a live selector, the catalog queue has a C3_DIRECT or
                    C2_WITH_LIFT target, and novelty is healthy. Take the top admitted target
                    ordered consequence-first (see "How P4 orders targets" below), not just the
                    top-ranked selector's next. Before the candidate, record runtime commits and
                    pass a feature-specific positive capability baseline.
P5  MINE_SELECTOR / EXTEND_BATTERY   novelty is falling or hits cluster on one selector.
P6  SOURCE_TARGETS  the catalog queue is near-empty.
```

The ordering encodes the core judgment: a loop that keeps mining with a broken or unverified oracle is the failure mode. P0-P2 are the guardrail that stops an unattended agent from scaling a bad instrument.

A failed capability baseline terminates the tick as `INVALID(environment)`. The controller may
refresh a cached nightly and rerun the baseline, or build the exact source pin. It must not interpret
blocking, timeout, or a missing protocol path as candidate evidence. A refreshed nightly result is
scoped to current-head screening unless its runtime commit equals the configured pin.

### How P4 orders targets (consequence-first, severity-gated)

P4 orders the ready queue by the methodology-v2 Target Selection scoring, with the `consequence`
axis dominant. This is the scheduler-level fix for severity skew — the old five-axis order
rewarded the cheapest bug class, so the loop drifted into wrong-error enumeration (S15/S10/S11).

```text
1. Admission precedes sorting. `NOT_ADMITTED` targets are absent from the P4 queue. C2 targets
   enter only with a named, executable C3 lift oracle; C1 targets are oracle/selector calibration,
   not bug-mining work.
2. Sort admitted targets by consequence (3 > 2); break ties by the composite of the other five
   axes. A C2 lift candidate never outranks a live C3 target — the first five axes break ties
   within a class, they do not cross it.
3. Wrong-error cap: consequence-1-only targets are ineligible for P4. They reopen only when a
   new checklist step supplies a credible consequence escalation; a different owner is not enough.
4. High-consequence lane first: targets in the state-transforming-DDL family (reorg / backfill /
   id-swap / restore / pinned concurrent substate bypassing a normal-path invariant) are sourced
   and scheduled ahead of static-precheck targets. 4 of the 5 highest-severity roots to date live
   here, and its interleaving dimension (pinned substate x concurrent op) is barely exercised.
```

## Object-loop vs meta-loop

- **Object-loop** (P4): find bugs with current selectors/oracles.
- **Meta-loop** (P0-P3, P5): improve the instruments — oracles, selectors, battery.

Full automation is mostly meta-loop. A self-iterating agent that only mines bugs plateaus the moment its fixed instruments hit their blind spots; a self-*improving* agent keeps widening what it can see and aim at. The scheduler spends on the meta-loop whenever an instrument is unverified or novelty drops — it does not wait for a human to notice.

## Live-lift refinement rule

Transient-fault and liveness targets need one more controller rule, because a local semantic red and
a live green do not mean the same thing.

```text
LIFT_BLOCKED / NEGATIVE_BOUNDARY
  Preconditions:
    - local semantic RED is already confirmed
    - live observer proves the fault hit the active window
    - live terminal oracle remains GREEN under a coarse infrastructure fault

  Meaning:
    - not "false positive"
    - not "bug disproved"
    - the injected live fault shape is still too far from the classifier / bridge / state transition
      that the proof obligation is actually about
```

Controller response:

```text
1. INTEGRATE the green live run as a first-class boundary asset.
2. Freeze same-lane expansion of broader pod/owner/network chaos.
3. Enqueue a bridge-proximal harness task instead:
   - same active-window observer
   - narrower injection closer to the semantic bridge
   - same strong terminal oracle
   - same sibling green control when applicable
4. Reopen `MINE_BUG` on that lane only if the next task changes fault fidelity, not merely fault
   intensity.
```

Typical bridge-proximal upgrades:

```text
- failpoint-enabled live image instead of topology chaos
- TiDB<->specific-TiKV gRPC drop/blackhole instead of whole-pod delete
- one-shot worker-return / error-wrap injection instead of generic restart or freeze
```

This keeps the loop from wasting ticks on "bigger hammer" escalation after a meaningful green
boundary has already been learned.

## Cross-layer information-preservation gate

For a C3 target that crosses a planner, row builder, protocol, coprocessor, or storage boundary,
the loop adds an information-preservation check before it treats a source hypothesis as a live
root cause:

```text
producer row/state shape -> fields carried across the boundary -> consumer reconstruction
```

The proof obligation is not merely "the consumer can read the row." It is:

```text
if a value is absent at the consumer,
the consumer must have enough dependency metadata to reconstruct the semantically correct value.
```

The controller performs four bounded actions:

1. Capture the producer-side shape, including hidden/changing columns, handles, index layouts, and
   dependency relationships.
2. Enumerate the fields the boundary actually carries; a generic default is not equivalent to a
   dependency-aware cast or state transition.
3. Run a narrow counterfactual at the loss point and compare the row/image or terminal action.
4. Lift the same phase and workload to the live system, then run both a window oracle and an
   aftermath oracle. A transient user-visible error and a durable wrong state are separate verdicts.

This gate is especially important for DDL row-rewrite bugs: a local decoder fix can validate the
mechanism while saying nothing about the production coprocessor, and a live error can be real while
still belonging to an existing family rather than a new root.

## Health / drift metrics (machine-computable each tick)

```text
selector_hit_rate      rolling hits / nominations per selector (from the ledger)
oracle_debt            count of REFUTED + below-TRUSTED-but-in-use oracles
novelty                fraction of recent hits that are NEW root_cause_id values (not blast-radius
                       siblings sharing an existing root) — countable from found_bug directly
queue_burn             audited / total catalog targets; battery coverage per active target
concentration          share of recent hits from a single selector (high = fragile, one-trick)
consequence_mix        severity distribution of recent hits (share graded consequence-3 vs -1);
                       all-consequence-1 = the loop is drifting to cheap wrong-error targets
admission_mix          share of P4 actions that were C3_DIRECT / C2_WITH_LIFT / NOT_ADMITTED;
                       any NOT_ADMITTED P4 action is a controller violation
fn_pressure            open held-out FN tickets; trend of oracle false-negative findings
environment_invalid    candidate cells rejected because runtime commit/capability was not proved
```

Drift signals and the loop's automatic response:

```text
novelty -> 0 over K ticks        the lane is mined out -> downweight it, SOURCE_TARGETS elsewhere
oracle_debt rising               stop MINE_BUG (P4 blocked), force P0-P2 until debt falls
concentration -> 1               MINE_SELECTOR: the loop is riding one selector, diversify
consequence_mix -> no C3         severity drift -> block P4, SOURCE_TARGETS from the C3 lanes;
                                 do not compensate with easier C1/C2 targets
admission_mix has NOT_ADMITTED   controller violation -> stop P4, classify the leaked target,
                                 then repair the queue/admission rule before another run
fn_pressure rising               MINE_ORACLE: instruments are missing a whole class
environment_invalid rising       stop candidate execution; refresh or pin the runtime and add a
                                 reusable capability baseline before resuming P4
```

These are the anti-drift backstop: the classic failure of autonomous fuzzers — quietly degrading into blast-radius grinding or noise — is made visible and self-correcting instead of silent.

## Human-escalation boundary (automation is not zero-human)

Full automation means the *inner* loop runs unattended, not that nothing ever reaches a human. Escalate (pause + notify) on:

```text
- a CONFIRMED high-severity bug (correctness/data-loss): worth a human's eyes before anything external
- any destructive or outward-facing action (cluster config change, data delete, filing upstream):
  never autonomous — the standing rule from methodology-v2 still holds
- budget threshold crossed
- the loop cannot reach a terminal state, or a drift signal it cannot self-correct
- an oracle flips REFUTED after having confirmed bugs: those confirmations need re-review
```

Everything else — target selection, matrix runs, oracle verification, ledger updates — is inner-loop and needs no human. The escalation set is deliberately small and specific; that is what makes "unattended" safe rather than reckless.

## Convergence and restart

"Keep mining forever" is not "mine blindly forever."

```text
CONVERGE  a lane hits loop-until-dry (K consecutive ticks, no new root cause) -> downweight it.
          all lanes dry + queue empty -> WATCH mode: stop active mining, emit a run report.
RESTART   WATCH mode is broken by diff-directed intake: a newly merged PR touching a prover /
          adding a shortcut / fixing a bug re-activates SOURCE_TARGETS and the loop resumes.
```

This is what makes it genuinely continuous: the loop idles in WATCH when the current surface is exhausted and wakes itself on new code, rather than either stopping dead or spinning on exhausted targets.

## INTEGRATE contract (what a confirmed hit writes)

The INTEGRATE step is where the counting and severity rules actually bind. A surface that shares a
fix with an already-recorded bug must be written as *reach on an existing root*, not a new bug —
otherwise the loop inflates its own scoreboard by enumerating owners. Every confirmed MINE_BUG hit
writes, before the tick ends:

```text
- SILENT-ORACLE GATE (run before setting severity/consequence). For any invariant-bypass or
  state-transforming target, run the silent-consequence oracle even if a loud error already fired:
  ADMIN CHECK, uniqueness GROUP BY, row-multiset/COUNT(*), FK-orphan scan, ADMIN CHECKSUM, and
  DDL-job liveness (O28: poll ADMIN SHOW DDL >=2x for a wedged job — State running, ErrCount
  climbing, SchemaState frozen). The consequence grade is NOT valid until this comes back clean;
  a loud wrong-error must not be graded consequence-1 until the silent oracle has ruled out a
  hidden data-corruption or liveness C3. (id30038: the loud false-duplicate looked C1; O28 found
  the stuck-DDL liveness C3 the same defect also produces.)
- found_bug row: severity, method(selector), oracle, repro/expected/actual, AND root_cause_id.
  Assign root_cause_id with the Reopen test (methodology-v2 Blast-Radius Stop Rule):
    * would an already-recorded sibling's fix also fix this?  -> reuse that sibling's
      root_cause_id. This is a blast-radius surface: bump the root's "affects N owners" note,
      do NOT count a new bug.
    * needs a new checklist step, or escalates the consequence class?  -> mint a new slug.
  root_cause_id is NEVER left NULL. Headline output = COUNT(DISTINCT root_cause_id), not COUNT(*).
- selector ledger: nomination outcome, counted by root cause (a blast-radius surface is reach on
  an existing root, not a fresh hit).
- root-cause ledger (ai-native-root-cause-ledger.md): add the surface under its root row.
- catalog / oracle library: as before.
```

## State storage (where the loop's memory lives)

```text
selector ledger      ai-native-selector-ledger.md      (which target shapes predict)
oracle library       ai-native-oracle-library.md       (which verdicts are trustworthy, + scope)
catalog / queue      ai-native-proof-obligation-catalog.md
bug ledger           found_bug (tidbcloud bug lib); root_cause_id is the machine-countable
                     root key, headline = COUNT(DISTINCT root_cause_id)
root-cause ledger    ai-native-root-cause-ledger.md    (surface -> root map + counting convention)
battery              methodology-v2 appendix
health metrics       derived from the above each tick (no separate store needed yet)
```

For true automation these should become machine-readable (a small DB/JSON), not prose — the one engineering prerequisite the current markdown assets do not yet fully meet. First slice done: `found_bug.root_cause_id` makes the headline count and the surface->root map queryable instead of prose-only. The selector ledger and oracle tiers are the remaining prose-bound state.

Update 2026-07-10: the second slice now exists as a local asset-store prototype at `/Users/bba/pc/ai-native-assets/`. It stores `selector`, `oracle`, `scenario`, `schedule_template`, `fault_point`, `obligation`, `module_profile`, immutable asset revisions, typed asset links, and RED/GREEN/INVALID run results. The first validation pack was `ddl/notifier + DURABLE_BEFORE_ACK` for held-out `issue59055/fix59157`:7 assets total, 4 reused methodology assets, 3 target-specific assets, `open_gaps=[]`, and prior runs `RED=1,GREEN=1`. This does not replace `found_bug`; it is the control-plane memory that tells the loop what to reuse, what to promote, and what not to retry. Remaining engineering work:migrate the stabilized schema to TiDB Cloud or TiDB, add retrieval/ranking over existing ledgers, and make every future tick report reuse metrics.

Update 2026-07-10b: target scheduling is now represented in the same control plane. `target_queue` stores held-out/live targets with `discoverability`, `obligation_class`, priority, consequence, effort, uncertainty, and JSON provenance. The CLI exposes `queue`, `next`, and `health`. The first scheduler result selected `issue53843/fix53849` (`ddl/ingest + LIFECYCLE_EXACTLY_ONCE`) and, after target-analysis assets were added, its computed state moved to `ready_to_execute`. Current queue health:validated=1, ready_to_execute=1, needs_target_analysis=3. This is still a simple heuristic scheduler, but the important behavior is in place: the next action is derived from durable state, not from an ad hoc conversation choice.

Update 2026-07-10c: the scheduled `issue53843/fix53849` target completed RED/GREEN and moved to `validated`. The vulnerable revision `cc127c14b8cc9887b1be946baa2f220690722c63` produced `close_calls=2 cleanup_calls=1`; the fixed revision `9c500ad9cb52c72372ad9d82f2a72190788d9478` produced `close_calls=1 cleanup_calls=1 remaining_engines=0`. The loop also split oracle trust correctly: `oracle.concurrent-unregister-exactly-once.v1` is `execution_verified`, while broad `oracle.no-leak-after-cancel.v1` stays `hypothesis` until an E2E SQL/cluster cancel flow proves it. Current queue health:validated=2, needs_target_analysis=3; the next target is `issue48164/fix48163` (`external-storage/s3 + ERROR_IDENTITY_PRESERVATION`). This is the first complete `queue -> target-analysis -> execution -> promotion/scope-split -> next` control-plane cycle.

Update 2026-07-10d: the scheduled `issue48164/fix48163` target completed RED/GREEN and moved to `validated`. The vulnerable revision `5309c2ff7750a34a0137dd1d8bdb8c70aa533abc` logged the injected `mock error` in the background upload goroutine but returned `io: read/write on closed pipe`; the fixed revision `b99d1c4f7eb2729f5c4f57ef6f5551f1d0136d9f` preserved `mock error` and passed `TestMultiUploadErrorNotOverwritten`. The loop again split oracle trust: `oracle.concurrent-pipe-upload-error-identity.v1` is `execution_verified`, while broad `oracle.injected-error-identity-survives.v1` stays `hypothesis` for other wrappers/persisted/retry shapes. Current queue health:validated=3, needs_target_analysis=2; the next target is `issue51846/fix52315` (`ddl/job-scheduler + OWNER_TOPOLOGY_HANDOFF`).

Update 2026-07-10e: the scheduled `issue51846/fix52315` target completed target analysis and root-boundary RED/GREEN, then moved to `validated`. The key proof was sharpened from broad "PD leader network partition" into a scheduler-local invariant: `RetireOwnerHook` firing does not prove already-delivered reorg workers have stopped, so owner retirement/re-entry must preserve `runningJobs.processingIDs` until the old worker returns. The vulnerable revision `bc841979a53e813d69c9fc8473ea0cc6703ef377` made the still-processing job runnable after retire semantics (`Should be false`), while the fixed revision `970962bdbc52547620be80817a7fc78e75b6221f` kept it non-runnable and passed. The loop again split oracle trust: `oracle.ddl-processing-id-survives-owner-retire.v1` is `execution_verified`, while broad `oracle.allowed-state-after-topology-fault.v1` stays `hypothesis` until a live-cluster ADD INDEX/PD-partition E2E proves final job state. Current queue health:validated=4, needs_target_analysis=1; the remaining analysis candidate is `issue62424/fix62607`.

Update 2026-07-10f: the scheduled `issue62424/fix62607` target completed target analysis and upstream integration RED/GREEN, then moved to `validated`. The key proof was sharpened from broad "DDL inside transaction" into a GC observer invariant: `CurTxnStartTS` in processlist does not prove an active user transaction when the session is a queued DDL after implicit commit. The vulnerable revision `0501de48c5b033f17f300960ecfe4f40f9bc1742` failed `TestDDLInsideTXNNotBlockMinStartTS` because `ReportMinStartTS` never converged to the later real transaction startTS; the fixed revision `e9e8a04fe71611ed08ebfcf0755993812a07c521` passed after skipping `StmtCtx.IsDDLJobInQueue` entries. The loop again split oracle trust: `oracle.ddl-minstartts-ignores-queued-ddl.v1` is `execution_verified`, while broad `oracle.no-stale-txn-state-after-ddl.v1` stays `hypothesis` until live-cluster GC safepoint advancement is proved. Current queue health:validated=5; `store.py next` returns no active targets.

Update 2026-07-10g: the queue was refilled from oracle debt instead of another hand-picked historical case. Added `target.lift.issue62424.live-gc-safepoint.v1` and `obligation.gc-ddl-transaction.live-gc-safepoint-advances.v1`, reusing issue62424's validated root-boundary assets but aiming at the broader live-cluster minStartTS effect on authorized testbed `8220955`. This exposed a control-plane issue: target state previously grouped runs by `module + selector`, so a new obligation under the same selector could inherit a neighbor's RED/GREEN and look validated. `store.py` now scopes prior runs to `payload.obligation_key` when present. The live-lift then completed GREEN: while DDL processlist still showed `TxnStart=467568057103679489`, `/tidb/server/minstartts` advanced past it to `467568057116524554` and later `467568066213183509` / `467568072111423519`. Current queue health:validated=6; `store.py next` returns no active targets.

Update 2026-07-10h: `store.py refill` now automates the oracle-debt refill step. Before running it, the asset audit found one consistency hole: issue53843 had a validated target and RED/GREEN runs, but `obligation.ddl-ingest.unregister-cleanup.v1` remained candidate/hypothesis; `/Users/bba/pc/ai-native-assets/issue53843-promote-obligation.jsonl` promoted it and its fault asset to execution_verified. `store.py refill --limit 10 --jsonl-output /Users/bba/pc/ai-native-assets/refill-candidates-20260710.jsonl` generated three candidates from remaining broad oracle debt: ingest no-leak-after-cancel, S3 injected-error identity, and owner/topology allowed-state. A second control-plane fix was needed: refill candidates with `broad_oracle` but no `obligation_key` must stay `needs_target_analysis`; `base_obligation` is provenance, not execution identity. Queue health immediately after refill:validated=6, needs_target_analysis=3; next target was the ingest no-leak refill.

Update 2026-07-10i: the first automated refill candidate completed target analysis and moved to `ready_to_execute`. `/Users/bba/pc/ai-native-assets/issue53843-refill-target-analysis.jsonl` added `obligation.ddl-ingest.sql-cancel-terminal-no-live-resource.v1`, `oracle.ddl-ingest-cancel-terminal-no-live-resource.v1`, and `fault.ddl-ingest.sql-cancel-after-local-engine-open.v1`, then updated the issue53843 refill target with a concrete `obligation_key`. The proof obligation is now E2E-shaped: after ADD INDEX local ingest has opened an engine, cancel must reach an allowed terminal state with no live backend context, engines, opened writers, duplicate close, or panic log. This prevents the loop from accepting a shallow SQL success as GREEN. Queue health after target analysis:validated=6, ready_to_execute=1, needs_target_analysis=2; the next executable target was the issue53843 SQL-cancel no-live-resource lift.

Update 2026-07-10j: the issue53843 SQL-cancel no-live-resource lift produced a current GREEN with an instrumented mock backend. The local experiment added `pkg/ddl/ingest/ai_native_sql_cancel_test.go`, enabled TiDB failpoint transformation, ran `go test -tags=intest ./pkg/ddl/ingest -run '^TestAINativeAddIndexCancelLeavesNoLiveMockIngestResource$' -count=1 -v`, then disabled failpoints. Evidence: `active_writes=64`, `registered=1`, `created_writers=2`, `finish_calls=1`, `live_engines=0`, `live_writers=0`, `closed_engines=1`, `duplicate_closes=0`, `disk_root_count=0`, and the DDL job reached rollback-done with `ErrCancelledDDLJob`. The result is stored as `run.issue53843.refill.current.13282a8.GREEN`. The target intentionally moved to `needs_counterpart_run`, not `validated`: broad oracle promotion now requires a vulnerable RED counterpart that does not mask the original duplicate cleanup race.

Update 2026-07-10k: the issue53843 vulnerable side gained a stronger root-boundary RED, but the SQL refill target correctly stayed open. A package-level harness on vulnerable `cc127c14b8cc9887b1be946baa2f220690722c63` ran `TestAINativeConcurrentUnregisterDoesNotDoubleReleaseMemory` and failed immediately with `expected_current_usage=0, actual_current_usage=-2877`, proving the old `UnregisterEngines` can double-release the same engine resource accounting. The run is stored as `run.issue53843.refill.vulnerable.cc127c14.RED.memory-double-release`; total runs are now `RED=6, GREEN=7`. Method lesson: lifecycle exactly-once oracles should cover the full ownership ledger, not just close/cleanup calls. Control-plane lesson: this RED strengthens `obligation.ddl-ingest.unregister-cleanup.v1`, but it is not a RED for `obligation.ddl-ingest.sql-cancel-terminal-no-live-resource.v1`, so no broad oracle promotion is allowed yet.

Update 2026-07-10l: the issue53843 SQL-cancel refill now has the missing vulnerable RED counterpart and is validated. A SQL-level observing harness on vulnerable `cc127c14b8cc9887b1be946baa2f220690722c63` ran `TestAINativeIssue53843SQLCancelDoubleCleanupRED`; the SQL flow itself performed `ALTER TABLE ... ADD INDEX` and `ADMIN CANCEL DDL JOBS`, while the observing backend manager exposed cleanup ownership as a ledger. Evidence: `registered=1`, `writes=1`, `unregister_calls=2`, `cleanup_ledger=-1`, `cancelled=true`, and the ALTER returned `ErrCancelledDDLJob`. The run is stored as `run.issue53843.refill.vulnerable.cc127c14.RED.sql-cancel-double-cleanup`; the narrow SQL-cancel obligation/oracle/fault are now `execution_verified`, but broad `oracle.no-leak-after-cancel.v1` remains `hypothesis`. Current health: `asset_revisions=59`, `runs RED=7/GREEN=7`, `targets validated=7/candidate=2`, `queue_states validated=7/needs_target_analysis=2`; `store.py next` now selects the S3 injected-error-identity refill. Method lesson: AI-native fuzz becomes more powerful when it can modify TiDB/harness to expose hidden ownership ledgers, but promotion must be scoped by the exact obligation and observer strength.

Update 2026-07-10m: the S3 injected-error-identity refill found a new current bug and validated the asset-reuse loop. The broad oracle from historical issue48164 was narrowed into `obligation.external-storage-s3.multipart-failed-part-terminal-no-complete.v1`: after multipart part 1 succeeds and part 2 `UploadPart` returns an injected root error, terminal `Close` must not `CompleteMultipartUpload` a prefix-only object and must preserve the root error. Current master `13282a8` RED evidence from `TestAINativeS3StorageCreateUploadPartFailureThenCloseRED`: `writeErr=ai-native mock upload part failed`, `closeErr=<nil>`, `completeCalls=1`, `completedParts=1`. A minimal local fix stored the failed state and made `Close` call `AbortMultipartUpload`; the same storage entry and direct multipart writer entry both passed with `abortCalls=1` and `closeErr=ai-native mock upload part failed`. The run pair is stored as `run.issue48164.refill.current.13282a8.RED.s3-multipart-part-fail-close` and `run.issue48164.refill.local-fix.13282a8.GREEN.s3-multipart-part-fail-close`; current health is `asset_revisions=62`, `runs RED=8/GREEN=8`, `targets validated=8/candidate=1`, `queue_states validated=8/needs_target_analysis=1`. Method lesson: for storage/error-identity oracles, final error text is not enough; the LOOP must also observe terminal state actions such as Complete vs Abort. This is exactly the "asset reuse -> target-specific P/Q/F -> small matrix -> RED/GREEN -> selector refinement" path the evolving system is meant to automate.

Update 2026-07-10n: the issue51846 owner-topology refill found a new current DDL owner epoch bug candidate and drained the active queue. The broad oracle was narrowed into `obligation.ddl-job-scheduler.owner-epoch-token-unique-across-handoff.v1`: `runReorgJob` accepts a `reorgFnResult` when `res.ownerTS == curTS`, but `OnBecomeOwner` allocated ownerTS with `time.Now().Unix()`, so rapid retire/re-become on the same TiDB can give two owner epochs the same token. Current master `13282a8` RED evidence from `TestAINativeOwnerEpochSecondCollisionRED`: `previousOwnerTS=1000 curOwnerTS=1000`, meaning a stale reorg result would pass the equality filter. A minimal local fix added monotonic `renewOwnerTS(wallTS)` and made `OnBecomeOwner` call it; GREEN `TestAINativeOwnerEpochRenewalRejectsSameSecondStaleResult` passed. The run pair is stored as `run.issue51846.refill.current.13282a8.RED.owner-epoch-second-collision` and `run.issue51846.refill.local-fix.13282a8.GREEN.owner-epoch-renewal`; current health is `asset_revisions=65`, `runs RED=9/GREEN=9`, `targets validated=9`, `queue_states validated=9`, and `store.py next` returns `null`. Method lesson: identity-token uniqueness is a first-class proof obligation for async result filters; cluster chaos should be the lift, not the first oracle, when a deterministic token-collision boundary exists in source.

Update 2026-07-10o: after the active queue drained, SOURCE_TARGETS promoted the owner-epoch lesson into `selector.identity-token-async-filter.v1`. The selector requires four gates: token-gated state action, lifecycle overlap, product-feasible token collision schedule, and a strong state-action oracle. The first source pass produced both sides of the selector calibration. BR registry heartbeat was stored as `run.source.br-registry.current.13282a8.INVALID.heartbeat-token-precision-screen`: it matched token equality but failed the schedule gate because heartbeat and stale-check cadence are one minute. BR storewatch then produced a new current RED/GREEN: `Up(T)->Offline(T)->Up(T)` skipped `OnReboot` when `StartTimestamp` did not change, even though BR backup/restore consumers rely on that callback for retry/recovery. The local fix preserves StartTimestamp-change detection and adds `non-Up -> Up` as a conservative reboot notification; full `go test ./br/pkg/utils/storewatch` passed. Current health is `asset_revisions=76`, `runs RED=10/GREEN=10/INVALID=1`, `targets validated=10/retired=1`, and `store.py next` remains `null`. Method lesson: SOURCE_TARGETS must emit positive targets and retired negative screens; that pair prevents a new selector from decaying into broad pattern scanning.

Update 2026-07-10p: `store.py source-targets --rule identity-token` now turns the SOURCE_TARGETS rule into an executable queue refresh. On current `/Users/bba/pc/tidb` it skipped the already-covered DDL owner epoch, BR registry, and BR storewatch cases, then emitted one held-out candidate: `target.source.tiflash-mpp-logical-core-starttimestamp.v1`. The source shape is TiFlash MPP logical-core cache reuse: `splitTiFlashLogicalCoreCache` refreshes only when cached `StartTimestamp` differs, while TiFlash server info exposes `tiflash.StartTime.Unix()` as a seconds-level token. The target was imported as `candidate` and correctly lands in `needs_target_analysis`, missing `module_profile`, `obligation`, and `fault_point`; current health is `targets validated=10/retired=1/candidate=1`, `queue_states validated=10/retired=1/needs_target_analysis=1`, `runs RED=10/GREEN=10/INVALID=1`. Method lesson: an evolving loop needs a queue-refresh primitive after refill drains. The generator may propose source-shaped work, but the queue gate must force G3 schedule proof and G4 effect proof before any RED claim.

Update 2026-07-10q: the TiFlash MPP cache candidate was intentionally retired as `LOW_VALUE` rather than executed. G1/G2 were real, and G4 had a weak observable through `TiFlashFineGrainedShuffleStreamCount`, but G3 was not proven: a meaningful RED would require TiFlash restart/re-registration within the same second, address reuse, and changed logical CPU count. A forced unit test could make `StartTimestamp` collide, but that would test the harness assumption rather than product behavior. `/Users/bba/pc/ai-native-assets/source-targets-tiflash-mpp-cache-retire-analysis.jsonl` records the decision as `retired_invalid_schedule_effect_quality`. `store.py source-targets` now reports retired targets as `retired_target_exists`, and a rerun produced `candidates=[]`; current health is `targets validated=10/retired=2`, `queue_states validated=10/retired=2`, `next=null`. A follow-up `store.py refill` exposed and fixed a control-plane issue: the broad identity-token oracle could generate a recursive "refill of a refill" from the owner-epoch refill target. Refill now rejects bases whose key starts with `target.refill.`, whose obligation class contains `REFILL`, or whose provenance is `refill_candidate`; the rerun writes 0 rows and reports `recursive_refill_base_only`. Method lesson: negative cache is part of the loop. A selector improves when low-quality near-misses are retained with gate-failure reasons, and the scheduler improves when it refuses recursive work items, not just when it finds bugs.

Update 2026-07-10r: a new S23-derived source-target lane produced a current RED but correctly stayed open. The selector is `STATE_INGRESS_INTERNAL_SQL`: current-session internal SQL between one-shot state setup and the user's intended read must not silently consume sibling session state. The target is `target.source.binding-history-executeinternal-txreadts.v1`. Current master `13282a8` has two RED runs: a root-boundary run showing current-session restricted SQL consumes pending `TxnReadTS`, and a user-visible run where `CREATE SESSION BINDING FROM HISTORY USING PLAN DIGEST` after `SET TRANSACTION READ ONLY AS OF TIMESTAMP` makes the next `SELECT` read current rows `[1,2]` instead of stale rows `[1]`. A local save/restore experiment was recorded as `INVALID`, not GREEN, because the timestamp/SnapshotInfoschema oracle was incomplete. Current health is `asset_revisions=87`, `runs RED=12/GREEN=10/INVALID=2`, `targets validated=10/retired=2/running=1`, `queue_states validated=10/retired=2/needs_counterpart_run=1`; `store.py next` returns the binding-history target. Method lesson: the autonomous loop needs an explicit middle state for "RED is worth pursuing, but promotion is blocked by missing counterpart or contract." That state is what keeps the system honest.

Update 2026-07-10s: the same binding-history source target was closed with a TSO-stable RED/GREEN pair. The RED probe now derives `@stale_ts` from row1 `LastCommitTS + 10ms`, verifies direct `AS OF @stale_ts` sees only row `[1]`, then shows current master consuming pending `tx_read_ts`: `before=467570589524557824 after=0 next_select_rows=[[1] [2]]`. The GREEN uses the same probe under a temporary `ExecuteInternal` isolation patch that saves/restores pending `TxnReadTS` and `SnapshotInfoschema`: `before=467570643908952064 after=467570643908952064 next_select_rows=[[1]]`, PASS. The run pair is stored in `/Users/bba/pc/ai-native-assets/source-state-ingress-binding-history-tso-pair-results.jsonl`; temporary source edits and probe were removed. Current health is `asset_revisions=90`, `runs RED=13/GREEN=11/INVALID=2`, `targets validated=11/retired=2`, `queue_states validated=11/retired=2`, and `store.py next` returns `null`. Method lesson: `needs_counterpart_run` worked as intended: it held a high-signal RED, forced oracle cleanup, then promoted only after the same user-visible rowset oracle passed under a plausible boundary fix.

Update 2026-07-10t: `store.py source-targets --rule state-ingress` now turns the validated S23 selector into a queue-refresh primitive. It skips the already validated binding-history target and emits three conservative `SOURCE_ONLY` candidates: DDL foreign-key current-session restricted SQL, executor user-management ExecuteInternal lookups, and planner index-advisor ExecuteInternal lookups. After importing `/Users/bba/pc/ai-native-assets/source-targets-state-ingress-generated-20260710.jsonl`, health is `targets validated=11/retired=2/candidate=3`, `queue_states validated=11/retired=2/needs_target_analysis=3`; `store.py next` returns `target.source.ddl-foreign-key-use-cur-session-state-ingress.v1`. Method lesson: a productive selector should become an incremental candidate generator, but generated targets must still stop at target-analysis until P/Q/F, product-feasible wrapper, and strong oracle are named.

Update 2026-07-10u: the generated state-ingress batch is now closed. Foreign-key was retired as `INVALID(session-ownership-proof)` because the restricted SQL runs in the DDL worker/internal session, not the user's session. User-management was retired as `INVALID(sys-session-isolation-proof)` because the path uses sys sessions. Planner index-advisor became the second positive: `RECOMMEND INDEX RUN` passes the current session into `indexadvisor.AdviseIndexes`, whose helper calls `ExecuteInternal` and drains the result set. Current RED on `13282a8`: `before=467570885856329728 after=0 next_select_rows=[[1] [2]]`. Local-fix GREEN: temporarily isolate pending `TxnReadTS`/`SnapshotInfoschema` before internal SQL and restore after; the same probe recorded `before=467570913639661568 after=467570913639661568 next_select_rows=[[1]]`. Assets are stored in `/Users/bba/pc/ai-native-assets/source-state-ingress-indexadvisor-results.jsonl`; current health is `asset_revisions=93`, `runs RED=14/GREEN=12/INVALID=2`, `targets validated=12/retired=4`, `queue_states validated=12/retired=4`, and `store.py next` returns `null`. Method lesson: a selector becomes stronger when it carries both positive siblings and negative ownership screens. Also, post-hoc restoration after `ExecuteInternal` returns is too late for some paths; the correct fix-probe boundary is ingress isolation before internal SQL enters the generic session state machine.

Update 2026-07-10v: the state-ingress generator was upgraded from static seeds to a dynamic source scan with the `session-ownership-proof` gate encoded in tool behavior. It now skips known validated/retired paths, screens or downgrades DDL worker/sys/session-pool/new-session/internal-helper/nil-restricted-SQL cases, scores local `UseCurSession` hits higher, and no longer treats unrelated file-level sys/new-session markers as a whole-file veto. Running `store.py source-targets --rule state-ingress --repo /Users/bba/pc/tidb --limit 20 --jsonl-output /Users/bba/pc/ai-native-assets/source-targets-state-ingress-dynamic-20260710.jsonl` produced 9 new targets and they were imported. The first target, `target.source.dynamic-state-ingress.pkg-executor-show.v1`, was target-analyzed on testbed 8220955: `SET TRANSACTION READ ONLY AS OF TIMESTAMP @ts; SHOW TABLE STATUS LIKE 't'; SELECT ...` makes the final SELECT read current rows `1,2`, while direct `SET TRANSACTION; SELECT` reads stale row `1`. Evidence is stored in `/Users/bba/pc/ai-native-assets/logs/source-state-ingress-show-table-status-testbed8220955.log`, and the target is now `blocked/CONTRACT_NEEDED(show-is-next-query-statement)` rather than filed as a bug. Current health is `targets blocked=1/candidate=8/validated=12/retired=4`, `queue_states blocked=1/needs_target_analysis=8/validated=12/retired=4`; `store.py next` selects `target.source.dynamic-state-ingress.pkg-infoschema-infoschema.v1`. Method lesson: retired negatives now become selector gates, validated positives become source patterns, and live-but-contract-gray behavior becomes a blocked target instead of a premature bug claim.

Update 2026-07-13: current-source terminal-boundary analysis found `id1770003` without PR/review
input. `ProcessChunk` deferred both local-engine writer Close calls, logged their errors, and
returned the earlier nil processing result. A local mock matrix proved exact error loss; the live
testbed matrix then produced a finished file import with table/index counts 3/0 and ADMIN CHECK
8223 when checksum was off. A named-return/error-join counterfactual returned the injected error
before engine publication and left 0/0 with ADMIN green. The promoted selector is
`DEFERRED_TERMINAL_ERROR_DOMINATES_SUCCESS`: finalizer reachability is not enough; a failed
durability-boundary finalizer must own the public result. Store health after import is 212 asset
revisions, 14 C3_DIRECT targets, RED=39/GREEN=34, and no severity-admitted active target.

Update 2026-07-10w/x: the dynamic state-ingress queue produced two useful negative screens and one new SQL-only RED. `infoschema` was retired as `INVALID(sys-executor-factory-proof)`: masking-policy load uses a sys executor factory session, so `UseCurSession` does not mean the user statement session. `BRIE` was retired as `INVALID(new-glue-session-proof)`: BACKUP/RESTORE is user-visible, but subtask SQL runs through newly created glue/one-shot sessions. The next target, `check_table_index`, pivoted from pending `TxnReadTS` ingress to exact user-session state restoration. Source anchor: `pkg/executor/check_table_index.go:295-298` forces `OptimizerUseInvisibleIndexes=true` and defers a hard reset to `false`. On testbed 8220955, with `tidb_enable_fast_table_check=ON` and `tidb_opt_use_invisible_indexes=ON`, an invisible-index query used `IndexReader/IndexRangeScan` before `ADMIN CHECK TABLE`, then `@@tidb_opt_use_invisible_indexes` still showed `1` while the same query used `TableReader/TableFullScan`. With fast check OFF, the plan stayed on the invisible index. Assets are stored in `/Users/bba/pc/ai-native-assets/source-targets-state-ingress-infoschema-retire-analysis.jsonl`, `/Users/bba/pc/ai-native-assets/source-targets-state-ingress-brie-retire-analysis.jsonl`, and `/Users/bba/pc/ai-native-assets/source-state-ingress-check-table-index-results.jsonl`; logs are in `/Users/bba/pc/ai-native-assets/logs/source-state-ingress-check-table-index-testbed8220955.log` and `...-fast-off-control.log`. Current health is `asset_revisions=100`, `runs RED=15/GREEN=12/INVALID=2/INFO=1`, `targets validated=13/retired=6/blocked=1/candidate=5`, and `store.py next` selects `target.source.dynamic-state-ingress.pkg-executor-grant.v1`. Method lesson: generated source targets should not be forced to keep their original selector label. If P/Q/F leads to a stronger state contract, promote that as a new reusable selector; here `selector.user-session-state-restore.v1` is the real asset.

## Minimal runnable form (first tick, concretely)

The loop can run today with the existing tools (subagent orchestration for parallel finders/skeptics, a scheduler for the tick cadence). A concrete first tick against current state:

```text
SENSE:     oracle library has O9->O9' and O2->O2' not yet full-blind-verified; O1 rescoped.
SCHEDULE:  P1 fires (oracles used to confirm bugs are below TRUSTED) before any P4 mining.
ACT:       VERIFY_ORACLE O9' — run its multi-shape blind held-out (drop+re-add, type-change,
           multi-column, controls); adversarial pass; land counterexamples in execution.
INTEGRATE: update oracle library O9' tier; open any FN tickets found.
HEALTH:    oracle_debt decremented; novelty unchanged; no drift.
GATE:      no escalation; under budget -> next tick.
```

Note what the scheduler did NOT do: it did not go mine a new bug, even though selectors are hot,
because an instrument it would judge with is still unverified. That single ordering decision is the
difference between an automated bug factory and an automated bug factory you can trust.

## First tick, EXECUTED (2026-07-02)

The tick above was run by hand to prove the controller is executable, not just describable:

```text
SENSE:     oracle library: O9' at LLM-VERIFIED+partial, O2' TRUSTED-on-3-shapes, both used to
           judge metadata/wrong-result bugs -> below full TRUSTED.
SCHEDULE:  P1 fired (verify an in-use sub-TRUSTED oracle) over P4 (mine a bug) — even though
           selectors S3/S6 were hot and the catalog queue was non-empty.
ACT:       VERIFY_ORACLE O9' via ai_native_heldout_o9prime.py — multi-shape blind held-out.
           Result: tp=3 fn=0 fp=0 tn=3, scope boundary 2/2 clean (rename left to O9).
INTEGRATE: oracle library O9' -> TRUSTED (value-staleness shapes + verified scope boundary);
           opened one held-out ticket (bucket-level staleness after type change).
HEALTH:    oracle_debt -1; novelty unchanged; no drift.
GATE:      no escalation; under budget.
NEXT:      P1 still non-empty (O2' full multi-shape run incl. CE-2/CE-3; then O5/O7/O8 adversarial
           passes) -> the loop keeps servicing instruments before returning to P4 mining.
```

The scheduler chose instrument-verification over bug-mining on its own priority rule, and the
verification not only promoted O9' but confirmed the earlier O9/O9' obligation split holds under
blind test. That is the loop working as designed: it spent a tick making its own judgment more
trustworthy before using it. This is the executable proof that "judgment health before discovery"
is a rule an unattended agent can actually follow, not just a slogan.

## Second P1 tick, EXECUTED (2026-07-03)

This tick used the requested "one subagent per round" shape: the subagent ran the adversarial
review of O4 while the main loop executed the held-out matrix on testbed `8192975`.

```text
SENSE:     O4 had confirmed/candidate extractor findings (id30010/id30013) but was only USED.
           S3 is the strongest live selector, so a noisy O4 would corrupt future P4 mining.
SCHEDULE:  P1 fired again: verify/harden an in-use sub-TRUSTED oracle before mining new bugs.
ACT:       VERIFY_ORACLE O4 -> O4':
           - RED positive: InfoSchema TABLE_NAME LIKE under utf8mb4_bin returned `Acase` on the
             fast path; CASE-wrapped scalar reference returned only `a%b,a_b`.
           - GREEN(triggered): lowercase-only control used the same fast extractor and matched
             the CASE-wrapped reference (`a_b,a_c`).
           - INVALID guard: `LOWER(table_name) LIKE 'a_%'` bypassed the extractor but changed
             semantics, so it is not a valid reference and cannot count as green.
           - INFO guard: cluster_log `type='PD'` vs `type LIKE 'PD'` split fast extractor from
             scalar semantics, but remains contract-ambiguous and routes to O6/reference diff.
INTEGRATE: O4 was replaced by O4' in the oracle library with explicit RED/GREEN/INVALID/INFO
           classification and trigger-evidence requirements.
HEALTH:    oracle_debt improved in quality but not fully decremented: O4' is execution-hardened,
           not TRUSTED; O5/O7/O8 remain USED and need later P1 ticks.
GATE:      no new high-severity confirmed bug in this tick; no human escalation.
NEXT:      Continue P1 on O5/O7/O8 or run a blind O4' held-out before using S3 for another P4
           mining round.
```

The important automation lesson is that an oracle can end a tick without promotion and still be
progress: it learned how to say "INVALID" and "INFO" instead of forcing every mismatch into
GREEN/RED. That reduces future false positives and prevents S3 from turning contract ambiguity
into fake confirmations.

## Follow-up P4 tick, EXECUTED (2026-07-03)

After O4' was hardened, the loop used S3 again on a deliberately new sub-shape rather than more
InfoSchema LIKE variants:

```text
SENSE:     S3 was still hot, but id30010/id30013 blast-radius expansion was guarded.
SCHEDULE:  P4 was allowed because O4' had RED/GREEN/INVALID/INFO guards and a concrete queued
           shortcut target existed.
ACT:       MINE_BUG on InfoSchema object-name scalar pushdown:
           shortcut extractor + LOWER/UPPER pushdown + value normalization + removed predicate.
           Result: id30018 confirmed. Fast path returned `Acase` for wrong-case constants, while
           the projected self predicate was 0 and the CASE reference returned no rows.
INTEGRATE: found_bug id30018 inserted; selector ledger S3 gained the "composed shortcut" rule;
           proof catalog and O4' gained row self-predicate evidence.
HEALTH:    novelty remained healthy because the hit used a new mechanism, not another LIKE
           pattern variant. Concentration risk remains, so further InfoSchema object-name variants
           are guarded.
NEXT:      Reuse S3 on a different shortcut/cache/extractor owner, or return to DDL-only owner
           selectors if the lane needs re-anchoring.
```

## Follow-up P4 blast-radius tick, EXECUTED (2026-07-03)

The next tick deliberately reused S3 on a different owner, but stopped once it proved the same
generic helper was the root shape:

```text
SENSE:     id30018 showed `extractCol(..., valueToLower=true)` can combine scalar pushdown,
           value/key normalization, and predicate removal unsafely. The open question was whether
           this was InfoSchema-specific or helper-level.
SCHEDULE:  P4 was allowed for one representative cross-owner check, not for broad helper-user
           enumeration.
ACT:       MINE_BUG on `information_schema.metrics_summary`:
           `MetricSummaryTableExtractor` calls `extractCol(..., "metrics_name", true)`.
           Result: id30019 confirmed. `METRICS_NAME='TIDB_QPS'` returned `tidb_qps`, while the
           projected self predicate was 0 and the CASE reference returned no rows.
INTEGRATE: found_bug id30019 inserted; proof catalog, O4', handoff, selector ledger, draft, probe,
           and method case updated.
HEALTH:    novelty would now fall if the loop kept enumerating `valueToLower=true` users, so the
           selector gained a helper-level stop rule.
NEXT:      Stop this helper family. Choose a different shortcut mechanism or return to DDL owner
           selectors.
```

Automation lesson: one cross-owner blast-radius case is useful evidence; a third or fourth user of
the same helper is usually low-novelty enumeration. The loop should record the representative case,
learn the stop rule, and spend the next tick on a different proof obligation.

## Follow-up P4 cache-purity tick, EXECUTED (2026-07-03)

The next tick obeyed the id30019 stop rule and moved to a different shortcut mechanism instead of
enumerating more `valueToLower=true` helper users:

```text
SENSE:     S3 helper-family novelty was exhausted. Source scan switched to another reuse path:
           Apply cache. Planner enables cache from correlated-key NDV; executor keys only the
           correlated values and reuses cached inner chunk.List on hit.
SCHEDULE:  P4 was allowed because the target had a new proof obligation:
           key equality must imply cached payload purity.
ACT:       MINE_BUG on correlated scalar subquery with `UUID()`:
           cache ON collapsed 24/16 duplicate-key outer rows to 1/1 distinct UUIDs; cache OFF
           restored 24/16 distinct UUIDs. Deterministic `CONCAT('v', inner_t.a)` stayed green.
INTEGRATE: found_bug id30020 inserted; selector ledger gained S7 cache payload purity; oracle
           library gained O10 cache-disabled volatile re-execution; proof catalog, draft, probe,
           handoff, and method case updated.
HEALTH:    novelty improved because the hit used a new mechanism and a new D_dim, not another
           S3 extractor variant.
NEXT:      For cache/reuse targets, require both key completeness and payload purity. Continue
           only on distinct cache owners with new D_dims, or return to DDL selectors.
```

Automation lesson: after a stop rule fires, the loop should not merely switch files. It should
switch proof obligations. Here the transfer was from "shortcut prefilter must preserve SQL
predicate" to "cached payload must be pure with respect to its key".

## Follow-up P4 interval-skip tick, EXECUTED (2026-07-03)

This tick returned to S3, but changed the D_dim instead of reopening the `valueToLower=true`
helper family:

```text
SENSE:     DDL next-owner scan had no obvious uncovered owner with strong oracle. Source scan
           moved to a different extractor mechanism: statements_summary coarse time range.
SCHEDULE:  P4 was allowed because the proof obligation was new:
           interval rows must not be treated as point ranges when deciding skip_request.
ACT:       MINE_BUG on `information_schema.statements_summary`:
           `summary_begin_time <= A AND summary_end_time >= B` with A<B triggered
           `skip_request:true` and returned 0 rows. The CASE-wrapped reference returned rows,
           each projecting both predicates as true. Green overlap control also returned rows.
INTEGRATE: found_bug id30021 inserted; S3 gained the interval-overlap coarse-skip rule; O4'
           gained the "triggered empty fast arm" classification; proof catalog, draft, probe,
           handoff, and method case updated.
HEALTH:    novelty stayed healthy: id30021 is not helper normalization and not Apply cache. It is
           a new D_dim under the same shortcut/extractor discipline.
NEXT:      Stop statement-summary predicate permutations. Continue only with a new D_dim or return
           to DDL owner selectors.
```

Automation lesson: a comment that states a shortcut proof is a high-value target. The useful move
is to ask whether the row model has dimensions that the shortcut abstraction erased.

## Follow-up P4 backend-not-found tick, EXECUTED (2026-07-03)

This tick stayed in S3 but changed the D_dim again: backend error domain vs SQL predicate domain.

```text
SENSE:     DDL candidates were either saturated or runtime-blocked: EXCHANGE PARTITION x CHECK
           was a WITHOUT VALIDATION boundary, and TiFlash availability needed unavailable runtime hooks.
SCHEDULE:  P4 was allowed because `tikv_region_peers` had a different shortcut contract:
           REGION_ID predicates become PD point lookups.
ACT:       MINE_BUG on `information_schema.tikv_region_peers`:
           `region_id=0` and `region_id IN (0,2)` triggered `region_ids:[...]` and returned PD
           400 errors. CASE references returned 0 rows for `0` and 3 rows for `IN(0,2)`.
INTEGRATE: found_bug id30022 inserted; S3 gained backend-not-found-as-empty-rowset rule; O4'
           gained wrong-error classification for triggered shortcut errors whose reference succeeds.
HEALTH:    novelty stayed healthy: id30022 is not value normalization, time precision, interval
           skip, or cache purity. It adds the backend error-domain D_dim.
NEXT:      Stop `tikv_region_peers` region-id variants. Reuse only on another backend point-lookup
           owner with a distinct oracle, or return to DDL owner selectors.
```

Automation lesson: external APIs often have a narrower error contract than SQL predicates. Before
trusting a shortcut, ask whether backend "not found" is an exceptional state or simply an empty SQL
rowset.

## Follow-up P4 request/render-context tick, EXECUTED (2026-07-03)

This tick reused S3 but did not enumerate `tikv_region_peers`; it moved to a different table and a
different proof obligation: backend request context vs SQL-visible row construction context.

```text
SENSE:     `TIKV_REGION_STATUS` numeric table_id extraction was green. Source scan then found
           `tidb_hot_regions_history`: extractor uses session tz for the backend time range, but
           row construction calls `updateTimestamp.In(tz)` without assigning the returned value.
SCHEDULE:  P4 was allowed because this is a distinct request/render context split, not a
           `tikv_region_peers` id variant.
ACT:       MINE_BUG on `information_schema.tidb_hot_regions_history`:
           under `time_zone='+14:00'`, fast `update_time` range returned 69 rows displayed as
           `2026-07-02 23:40:41`; projected predicate sum was 0. CASE self-recheck returned 0.
INTEGRATE: found_bug id30023 inserted; S3 gained request-context vs row-render-context rule; O4'
           gained another self-predicate false-row classification.
HEALTH:    novelty is medium: time-zone D_dim existed from id30012, but the owner and root shape
           are different (`Time.In` return value ignored). Stop further time-column enumeration.
NEXT:      Reuse only when source shows a distinct request/render context split, or return to DDL
           selectors.
```

Automation lesson: a shortcut can be correct in what it asks the backend for and still wrong in
how it materializes rows. The oracle must check the returned row's own predicate, not just the
backend request bounds.

## Follow-up S7 semantic-switch tick, EXECUTED (2026-07-03)

This tick reused S7 but did not enumerate Apply-cache variants. It moved to plan cache key
construction and asked which semantic switches are consumed before the cached object exists.

```text
SENSE:     after id30020, inspect `NewPlanCacheKey` and expression construction. Source shows
           `tidb_sysdate_is_now` rewrites `sysdate()` into `now()` during scalar function build,
           but the key omits `SysdateIsNow`.
SCHEDULE:  P4 allowed because this is a distinct S7 sub-shape: semantic-switch coverage, not
           volatile payload reuse.
ACT:       MINE_BUG on prepared plan cache:
           OFF->cache->ON kept `sysdate(6)=now(6)` at 0 with `@@last_plan_from_cache=1`, while
           `ADMIN FLUSH SESSION PLAN_CACHE` made the same prepared statement return 1.
           ON->cache->OFF symmetrically kept 1 until flush, then returned 0.
INTEGRATE: id30024 documented locally and inserted into remote `found_bug` as confirmed
           (`MAX(id)=30024,COUNT=26`). S7 gained semantic-switch coverage; O11 registered.
HEALTH:    cache-key candidates need a green gate: prove whether the cache hit rebuilds the
           relevant semantic boundary. The timezone/DST plan-cache candidate was GREEN because
           range rebuild used the current session timezone.
NEXT:      Do not enumerate session variables. Reopen only when source proves the variable is
           consumed during cached-object construction, omitted from the key, and has a same-query
           flush/off-cache oracle.
```

## Follow-up S7 coarse-key tick, EXECUTED (2026-07-03)

This tick reused the previous timezone GREEN calibration instead of discarding it. The question was:
which timezone-dependent boundary is not rebuilt after a cache hit?

```text
SENSE:     `NewPlanCacheKey` stores only the current timezone offset. TIMESTAMP range rebuild was
           GREEN because the boundary was rebuilt under the current session timezone after hit.
SCHEDULE:  P4 allowed because this is a distinct S7 sub-shape: coarse-key sufficiency for folded
           values, not another random timezone/function enumeration.
ACT:       MINE_BUG on prepared plan cache:
           Africa/Johannesburg and Europe/Amsterdam have the same current offset, but differ for
           2025-01-15. `UNIX_TIMESTAMP('2025-01-15 12:00:00')` cached under Johannesburg stayed
           `1736935200` after switching to Amsterdam with `@@last_plan_from_cache=1`; flush
           returned `1736938800`. Reverse direction reproduced. A summer date with the same
           historical offset stayed GREEN.
INTEGRATE: id30025 inserted into remote `found_bug` as confirmed (`MAX(id)=30025,COUNT=27`).
           S7 gained coarse-key sufficiency; O11 generalized to cache-hit plus flush reference.
HEALTH:    coarse-key candidates need both a RED value/date where the omitted detail matters and a
           GREEN value/date where it does not, otherwise the oracle overclaims.
NEXT:      Pause this family. Reopen only when another cache key stores an approximation and source
           proves a cached/folded value depends on omitted details.
```

## Follow-up S3 type-domain conversion tick, EXECUTED (2026-07-03)

This tick respected the S7 pause gate and moved to a different proof obligation instead of
enumerating more time/cache variants.

```text
SENSE:     `extractCol` removes extracted predicates before table-specific extractors convert
           values into backend request domains. `parseUint64` silently ignores parse failures.
SCHEDULE:  P4 allowed because this is a distinct S3 sub-shape: SQL type-domain conversion, not
           another backend-not-found or timezone/render variant.
ACT:       MINE_BUG on `information_schema.tikv_region_peers`:
           `region_id=-1`, `store_id=-1`, and `region_id IN (-1)` returned the full 269-row peer
           table. CASE-wrapped references returned 0, and returned rows projected the predicate
           as false. `peer_id=-1` was a GREEN control because it was not extracted; mixed
           `IN(-1, valid_region_id)` matched the valid rows only.
INTEGRATE: id30026 inserted into remote `found_bug` as confirmed (`MAX(id)=30026,COUNT=28`).
           S3 gained type-domain conversion: conversion into a narrower backend request domain
           is part of Q_claim.
HEALTH:    Do not enumerate every `parseUint64` owner. Reopen only when another owner has a
           distinct consequence oracle or contract surface.
NEXT:      Continue pulling new targets from the selector ledger. Current best rule remains:
           source first, small matrix second, strong oracle third, pause after a representative hit.
```

## Follow-up S3 cache-key-granularity tick, EXECUTED (2026-07-03)

This tick respected the id30026 pause gate. It did not enumerate more numeric conversion owners;
it moved to a different S3 mechanism: table-name-only cache reuse inside inspection memtables.

```text
SENSE:     `MemTableReaderExec.Next` can serve inspection cacheable tables from
           `SessionVars.InspectionTableCache` by table name. The code comment already notes that
           cached rows are returned fully. `inspection_result` first scans `cluster_config`
           broadly, then later asks for type-specific details.
SCHEDULE:  P4 allowed because this is a distinct S3 sub-shape: cache snapshot key granularity, not
           type-domain conversion, backend not-found, or request/render context.
ACT:       MINE_BUG on `information_schema.inspection_result`:
           direct `cluster_config WHERE type='tikv' AND key='foo-test'` returned only
           `tikv-a,tikv-b`, but `inspection_result` produced a `type='tikv'` config detail that
           included `tidb-a`. Trigger evidence showed the detail query consumed `type='tikv'`
           into `node_types:["tikv"]`, leaving only `key='foo-test'` as scalar Selection.
INTEGRATE: id30027 inserted into remote `found_bug` as confirmed (`MAX(id)=30027,COUNT=29`).
           S3 gained cache key granularity: cache keys must include extractor-consumed dimensions
           or cache hits must reapply them.
HEALTH:    Novelty stayed healthy. This is not another system-table value normalization case; it
           is a cache/reuse proof obligation with a direct diagnostic-output oracle.
NEXT:      Stop broad `InspectionTableCache` enumeration. Reopen only when another cacheable table
           has a distinct missing dimension or stronger user-visible consequence.
```

Automation lesson: a cache TODO is a proof-obligation beacon when the cached object is broader
than later query semantics. The oracle should compare the cached user-facing report with a direct
reference query, not just prove that the cache was used.

## Follow-up S8 prepared/preprocess-freeze tick, EXECUTED (2026-07-03)

This tick respected the id30027 pause gate. It did not enumerate more inspection-cache users; it
moved to a different reuse mechanism: prepared statements that store an AST after PREPARE-time
preprocessing.

```text
SENSE:     `GeneratePlanCacheStmtWithAST` runs `Preprocess(..., InPrepare, ...)` during PREPARE.
           `checkSelectNoopFuncs` and `checkGroupBy` consume `tidb_enable_noop_functions`, while
           `planCachePreprocess` only re-runs Preprocess on schema-version changes.
SCHEDULE:  P4 allowed because this is a new selector: prepared/preprocess semantic freeze, not
           S7 physical plan-cache key completeness and not S3 diagnostic cache reuse.
ACT:       MINE_BUG on prepared statements:
           direct OFF rejected `SQL_CALC_FOUND_ROWS` and `GROUP BY expr DESC` with error 1235.
           The same statements prepared under ON then executed under OFF returned rows with
           warning_count=0. `ADMIN FLUSH SESSION PLAN_CACHE` did not fix it; the same prepared
           statements still returned rows with `@@last_plan_from_cache=0`.
INTEGRATE: id30028 inserted into remote `found_bug` as confirmed (`MAX(id)=30028,COUNT=30`).
           Selector ledger gained S8; oracle library gained O12 direct-vs-prepared semantic
           switch; proof catalog, draft, handoff, and method case updated.
HEALTH:    Good novelty. The flush/off-cache controls prevent misclassifying this as another
           prepared plan cache bug; the reusable shape is PREPARE-time validation consuming a
           session switch without execute-time freshness.
NEXT:      Do not enumerate every `tidb_enable_noop_functions` surface. Reopen S8 only for another
           preprocessor/session switch with a direct current-session reference and a cache-flush
           or off-cache proof.
```

Automation lesson: when a cache/reuse hypothesis survives cache flush, do not keep calling it a
cache-key bug. Move the proof boundary earlier and ask which semantic validation was already
frozen before the cached object existed.

## Follow-up S8 AST-mutation tick, EXECUTED (2026-07-03)

This tick reopened S8 only under the documented condition: a different preprocessor/session
switch and a direct current-session reference. It did not enumerate more noop syntax.

```text
SENSE:     `hasAutoConvertWarning` reads `SQLMode.HasStrictMode()` during preprocessing and, under
           non-strict mode, mutates overlong VARCHAR to TEXT/BLOB while emitting warning 1246.
SCHEDULE:  P4 allowed because this is a distinct S8 sub-shape: PREPARE-time AST mutation, not the
           id30028 stale noop validation result.
ACT:       MINE_BUG_CANDIDATE on prepared CREATE TABLE:
           direct strict `VARCHAR(70000) CHARACTER SET utf8mb4` returned error 1074. Direct
           non-strict converted to `mediumtext` with warning 1246. PREPARE under non-strict then
           EXECUTE under STRICT_TRANS_TABLES succeeded and created `mediumtext`; PREPARE under
           strict failed immediately. ALTER TABLE same shape did not reproduce.
INTEGRATE: id30029 inserted into remote `found_bug` as candidate (`MAX(id)=30029,COUNT=31`).
           S8 gained an AST-mutation sub-shape, and O12 gained INFO/CANDIDATE classification for
           prepared DDL cases where PREPARE itself emitted the relevant warning.
HEALTH:    Useful but contract-sensitive. The direct-vs-prepared split is real, but product may
           choose PREPARE-time DDL normalization as authoritative. Do not count as confirmed until
           that contract is settled.
NEXT:      Guard S8 now. After id30028 + id30029, ordinary session-switch enumeration is low
           novelty. Reopen only for a different consequence oracle or non-DDL/current-session
           contract with lower ambiguity.
```

Automation lesson: a selector can produce a candidate rather than a confirmed bug. That is still
method progress when it sharpens the oracle's classification boundary and tells the loop where to
stop.

## Follow-up S9 identity-fast-path tick, EXECUTED (2026-07-03)

This tick returned to DDL and did not continue ordinary S8 session-switch enumeration.

```text
SENSE:     `REORGANIZE PARTITION` has a nonclustered duplicate-rowid repair path. Source comments
           say duplicate `_tidb_rowid` can happen across partitions after `EXCHANGE PARTITION`,
           and the code skips a row when the target key exists and raw bytes are equal.
SCHEDULE:  P1 allowed because this is a new proof obligation inside DDL: equality used as identity
           proof, not another reorg/global-index iterator variant.
ACT:       MINE_BUG on partition reorg:
           ordinary reorg preserved count 2->2. After `EXCHANGE PARTITION ... WITHOUT VALIDATION`
           created two old partitions each containing `(1,1,_tidb_rowid=1)`, `ALTER TABLE ...
           REORGANIZE PARTITION p0,p1` succeeded but changed `COUNT(*)` from 2 to 1.
           Guard cells showed same rowid with different bytes is repaired to a new rowid, and same
           bytes with different rowid preserves both rows.
INTEGRATE: id600001 inserted into remote `found_bug` as confirmed (`MAX(id)=600001,COUNT=32`).
           Selector ledger gained S9 identity proof fast path; oracle library gained O13 rowset
           cardinality invariant; draft, handoff, and method case updated.
HEALTH:    High novelty and high quality. This is not more global-index/reorg enumeration: the
           reusable shape is "payload equality is not object identity when source/container ID is
           omitted."
NEXT:      Guard this exact reorg owner. Reopen S9 only for a different fast path that converts
           equality/existence into identity while skipping a safe repair, or for fix validation.
```

Automation lesson: when the code comment names a known hard case, look at the proof the fix uses
to decide "already handled." The fastest bug is often the one-cell adversary that satisfies the
check but violates the identity relation the check is standing in for.

## Follow-up S10 precheck-metric tick, EXECUTED (2026-07-03)

This tick stayed in DDL after S9, but deliberately moved to a different proof shape instead of
enumerating more `REORGANIZE PARTITION` syntax.

```text
SENSE:     `MODIFY COLUMN` no-reorg-with-check validates existing rows by building restricted SQL.
           For non-integer checks, `buildCheckSQLFromModifyColumn` uses `LENGTH(col) > newFlen`.
SCHEDULE:  P4 allowed because this is a new DDL selector: validation metric mismatch, not another
           identity proof or partition iterator case.
ACT:       MINE_BUG on MODIFY COLUMN shrink:
           direct target references accepted `_utf8mb4'中中中'` in both `varchar(3)` and
           `char(3)` with `LENGTH=9,CHAR_LENGTH=3`. ASCII `abc` from `varchar(4)` to `varchar(3)`
           succeeded. But `varchar(4)->varchar(3)` and `char(4)->char(3)` for `中中中` both
           failed with ERROR 1265, leaving the old schema unchanged.
INTEGRATE: id630001 inserted into remote `found_bug` as confirmed (`MAX(id)=630001,COUNT=33`).
           Selector ledger gained S10 DDL precheck metric mismatch; oracle library gained O14
           target-type acceptance reference; draft, handoff, methodology, and method case updated.
HEALTH:    Medium bug quality, high method quality. The user-visible symptom is false rejection,
           not data loss, but the oracle is strong and the source-to-matrix path is short.
NEXT:      Guard this modify-column owner. Reopen S10 only for a different precheck metric, a
           silent wrong-acceptance consequence, or fix validation across binary/indexed controls.
```

Automation lesson: for validation fast paths, the most productive adversarial question is often
"what unit did this checker measure?" If the checker measures bytes but the contract is in
characters, the red matrix is smaller than any random fuzz run could make it.

## Follow-up S10 target-state validation tick, EXECUTED (2026-07-03)

This tick stayed in DDL and reused S10, but did not enumerate id630001's charset/string variants.
It moved from value-fit prechecks to target-state validators.

```text
SENSE:     FK creation accepts string columns with matching type/charset/collation even when
           parent/child VARCHAR lengths differ. FK modify validation separately requires
           newFlen >= originalFlen and newFlen >= relatedFlen.
SCHEDULE:  P4 allowed because this is a distinct S10 sub-shape: target-state validation metric
           mismatch, not byte-vs-character data-fit checking.
ACT:       MINE_BUG on FK MODIFY COLUMN:
           direct target schemas p10/c10, p10/c15, and p15/c20 created successfully. But child
           varchar(20)->varchar(10/15) failed with ERROR 1832, and parent varchar(10)->varchar(15)
           with child varchar(20) failed with ERROR 1833. Existing child data had max CHAR_LENGTH
           10. Widening controls child 20->25 and parent 10->20 succeeded.
INTEGRATE: id630002 inserted into remote `found_bug` as confirmed (`MAX(id)=630002,COUNT=34`).
           S10 generalized from precheck metric mismatch to DDL validation metric mismatch; O14
           now covers direct target-schema acceptance, not only target-type value acceptance.
HEALTH:    Good novelty inside DDL. This is a wrong-error bug, not data loss, but it validates a
           reusable source move: compare transition validators against sibling create/add target
           validators for the same final schema.
NEXT:      Guard FK type-pair enumeration. Reopen S10 only for a different validation metric, a
           silent wrong-acceptance consequence, or fix validation across parent/child directions.
```

Automation lesson: if two DDL entrypoints produce the same final metadata relation, the stricter
one must justify every extra predicate. A hidden inequality over old metadata is a compact red
flag when the final schema is accepted directly.

## Follow-up S10 partition-column validation tick, EXECUTED (2026-07-03)

This tick stayed in DDL and reused S10, but did not enumerate FK varchar length pairs. It moved to
partition-column modify validation, a different validator owner.

```text
SENSE:     Partition-column MODIFY validation contains a target partition-definition validator,
           but an earlier allowlist only permits string length extension.
SCHEDULE:  P4 allowed because this is a distinct S10 owner: partition-column transition allowlist
           vs target partition-definition/data-fit contract.
ACT:       MINE_BUG on LIST/RANGE/KEY partition columns:
           direct target schemas with varchar(5) partition columns and fitting literals/data
           succeeded; non-partition varchar(6)->varchar(5) with max CHAR_LENGTH=3 succeeded;
           partition-column varchar(6)->varchar(5) failed with ERROR 8200 across LIST/RANGE/KEY;
           partition-column varchar(6)->varchar(7) succeeded as checker-aligned control.
INTEGRATE: id630003 inserted into remote `found_bug` as confirmed (`MAX(id)=630003,COUNT=35`).
           S10 now has three calibrated sub-shapes: value metric mismatch, target-state hidden
           inequality, and partition-column transition allowlist mismatch.
HEALTH:    Medium quality wrong-error, user-visible, strong O14 oracle. The methodology value is
           higher than the bug severity: the selector jumped to a new DDL validator without random
           enumeration.
NEXT:      Guard partition/string variant enumeration. Reopen S10 only for a different metric,
           silent wrong-acceptance, or fix validation across LIST/RANGE/KEY and binary boundaries.
```

Automation lesson: once a validator contains both a coarse transition allowlist and a later
target-state validator, put the allowlist on trial first. The matrix should ask whether the final
state and data fit, not whether the transition name sounds risky.

## Follow-up S11 dependency-gate tick, EXECUTED (2026-07-03)

This tick stayed in DDL but left S10. It targeted generated-column dependency checks, a different
proof shape from length or target-state validation.

```text
SENSE:     `MODIFY COLUMN` computes whether generated columns depend on the base column. Rename
           uses that fact precisely, but a later common gate rejects every MODIFY when dependency
           exists.
SCHEDULE:  P4 allowed because this is a new DDL selector: dependency existence vs semantic-change
           proof, not another validation metric mismatch.
ACT:       MINE_BUG on generated-column dependency:
           direct target schemas for base-column COMMENT and DEFAULT changes created and evaluated
           correctly. ALTER COMMENT and ALTER DEFAULT on the depended-on base column failed with
           ERROR 3106/3108. Non-dependent column COMMENT and generated-column own COMMENT were
           green. True base-column type change remained a green reject.
INTEGRATE: id630004 inserted into remote `found_bug` as confirmed (`MAX(id)=630004,COUNT=36`).
           Selector ledger gained S11 DDL dependency gate overbroad; O14 gained a generated-column
           target-schema behavior reference.
HEALTH:    Medium quality wrong-error. Strong methodology value because it demonstrates a new
           P/Q pattern: "dependency exists" was used as "all operations are unsafe".
NEXT:      Guard generated-expression enumeration. Reopen S11 only for a different dependency
           owner, silent wrong-acceptance, or fix validation across virtual/stored/functional-index
           boundaries.
```

Automation lesson: dependency owners are high-density only when the matrix separates graph
existence from operation semantics. If both red and green controls use the same dependency graph,
the selector is testing the proof, not the syntax.

## Follow-up S13 shallow-copy target-mutation tick, EXECUTED (2026-07-03)

This tick stayed in DDL. It first tried a CHECK-constraint entrypoint gap and rediscovered old
`found_bug` id1, so that path was classified as duplicate and stopped. The next target moved to a
different proof shape: target reconstruction from source metadata.

```text
SENSE:     `CREATE TABLE ... LIKE` builds target table metadata from source `TableInfo`. The code
           does a top-level copy and then renames CHECK constraints for the target.
SCHEDULE:  P4 allowed because this is not more CHECK expression enumeration. It tests whether
           target reconstruction proves nested metadata ownership.
ACT:       MINE_BUG on `CREATE TABLE dst_auto LIKE src_auto`:
           direct sibling CREATE TABLE controls produced independent `d1_chk_1` / `d2_chk_1`.
           But after LIKE, source `SHOW CREATE TABLE src_auto` changed from `src_auto_chk_1` to
           `dst_auto_chk_1`, including from a new SQL connection. Violating inserts on the source
           reported `dst_auto_chk_1`. `information_schema.check_constraints` still listed both
           source and target names, so SQL-visible metadata surfaces disagreed.
INTEGRATE: id630005 inserted into remote `found_bug` as confirmed (`MAX(id)=630005,COUNT=37`).
           Selector ledger gained S13 DDL shallow-copy target mutation; oracle library gained O16
           source/target metadata isolation.
HEALTH:    Medium quality metadata-corruption bug. The methodology value is high because it adds a
           new proof family: top-level copy is not nested ownership proof.
NEXT:      Guard LIKE option and CHECK expression enumeration. Reopen S13 only for another
           pointer-backed metadata owner, behavior-changing source mutation, or fix validation.
```

Automation lesson: for DDL clone/rebuild paths, the first oracle should inspect the source after
creating the target. If the code mutates nested pointers, the target can look fine while the source
quietly becomes wrong.

## Follow-up S14 recovery-namespace tick, EXECUTED (2026-07-03)

This tick stayed in DDL and reused the CHECK-constraint metadata owner without enumerating CHECK
expressions. The new question was whether sibling recovery paths re-prove the schema-level
namespace invariants that normal create/add paths prove.

```text
SENSE:     `CREATE TABLE` and `ALTER TABLE ADD CHECK` call `checkConstraintNamesNotExists`, but
           `RecoverTable` checks only table name and table ID before publishing recovered
           `TableInfo`. `FLASHBACK TABLE ... TO new_name` changes only `TableInfo.Name`.
SCHEDULE:  P4 allowed because this is a new proof shape: old metadata valid in a drop snapshot is
           not necessarily valid in the current schema namespace.
ACT:       MINE_BUG on `FLASHBACK TABLE f TO f_old`:
           normal duplicate `CREATE TABLE` rejected `base_chk_1` with ERROR 3822. Then a dropped
           `f(a CHECK a>0)` and a recreated current `f(a CHECK a>1)` both had `f_chk_1`.
           `FLASHBACK TABLE f TO f_old` succeeded, leaving `SHOW CREATE f` and `SHOW CREATE f_old`
           both with `CONSTRAINT f_chk_1`. `information_schema.check_constraints` listed two
           `f_chk_1` rows with different clauses, and both tables' CHECK violation errors named
           `f_chk_1`. `CREATE TABLE like_copy LIKE f` was the green sibling reconstruction control
           and produced `like_copy_chk_1`.
INTEGRATE: id630006 inserted into remote `found_bug` as confirmed (`MAX(id)=630006,COUNT=38`).
           Selector ledger gained S14 DDL recovery namespace validation bypass; oracle library
           gained O17 schema CHECK constraint namespace oracle.
HEALTH:    Medium quality metadata-corruption bug. The method value is high because it separates
           "restore object identity" from "prove current schema-level invariants".
NEXT:      Guard recover-field enumeration. Reopen S14 only for another create/add validator
           skipped by recovery, a stronger behavioral consequence, or fix validation.
```

Automation lesson: recovery paths are high-density only when a normal sibling path has an explicit
validator and the recovered metadata owner has a SQL-visible current-schema oracle. Otherwise,
"restored object looks odd" is only a boundary sample, not a confirmed bug.

## Follow-up S11 expression-index companion tick, EXECUTED (2026-07-03)

This tick stayed in DDL but switched away from recovery. It reused S11 on a second dependency owner:
expression indexes.

```text
SENSE:     Expression indexes are represented by hidden generated columns. The same dependency
           checker used for generated columns returns `ErrDependentByFunctionalIndex` for a base
           column referenced by an expression index.
SCHEDULE:  P4 allowed because the owner is different from id630004, but root-cause accounting is
           required: this may validate selector blast radius rather than a new root-cause family.
ACT:       MINE_BUG on `INDEX idx_expr ((a+1))`:
           direct target schema `a INT COMMENT ...` with the expression index succeeded, rows
           queried correctly, and `ADMIN CHECK TABLE` passed. Direct `a INT DEFAULT 5` with the
           same expression index also succeeded and default insert returned `5,6`. But ALTER
           COMMENT and ALTER DEFAULT on the existing expression-index base column both failed with
           ERROR 3106 wrapping `ddl:3837`, saying the column cannot be dropped or renamed. Non-
           dependent column COMMENT and DROP INDEX then COMMENT were green; true type change
           remained a green reject.
INTEGRATE: id630007 inserted into remote `found_bug` as confirmed (`MAX(id)=630007,COUNT=39`).
           Selector ledger and O14 were updated to record expression-index owner coverage.
HEALTH:    Medium quality wrong-error. Method value is honest blast-radius validation: S11
           generalized to a second user-facing dependency owner, but shares id630004's common
           MODIFY gate root cause.
NEXT:      Guard expression-index syntax enumeration. Reopen S11 only for a different dependency
           code path, silent wrong-acceptance, or fix validation.
```

Automation lesson: cross-owner hits need a two-column scorecard: selector generalization and
root-cause novelty. The former can be high while the latter is low; recording both keeps the method
from drifting into inflated bug counts.

## Follow-up S15 idempotence-flag tick, EXECUTED (2026-07-03)

This tick stayed in DDL. It first tried to reuse S14 on FK constraint names, but the green control
showed ordinary TiDB DDL allows the same FK name on two different child tables, so that is not a
schema-level namespace red cell. The loop then pivoted to a source-comment proof obligation in
`ALTER TABLE ADD CONSTRAINT`.

```text
SENSE:     `ALTER TABLE ADD CONSTRAINT` dispatch passes `constr.IfNotExists` to ADD INDEX and
           ADD COLUMNAR INDEX, but the ADD FOREIGN KEY branch has a comment saying IF NOT EXISTS
           is ignored and calls `CreateForeignKey` directly.
SCHEDULE:  P4 allowed because this is a new DDL proof family: parser/AST idempotence flag
           propagation across sibling owners.
ACT:       MINE_BUG on `ADD FOREIGN KEY IF NOT EXISTS`:
           first `ALTER TABLE c ADD CONSTRAINT fk_pid FOREIGN KEY IF NOT EXISTS (pid) REFERENCES
           p(id)` succeeded and `information_schema.referential_constraints` showed exactly one
           FK row. Re-running the same statement failed with ERROR 1826 duplicate FK name. Plain
           duplicate ADD FOREIGN KEY also failed, as expected. Sibling `ADD INDEX IF NOT EXISTS`
           duplicate returned Note 1061 and preserved one index. `DROP FOREIGN KEY IF EXISTS` was
           rejected by the parser and excluded.
INTEGRATE: id630008 inserted into remote `found_bug` as confirmed (`MAX(id)=630008,COUNT=40`).
           Selector ledger gained S15 DDL idempotence flag dropped; oracle library gained O18
           idempotent DDL flag oracle.
HEALTH:    Low-to-medium wrong-error. No data corruption, but user-facing idempotent migration
           scripts can fail. Method value is high because grammar flags are now first-class proof
           obligations.
NEXT:      Guard FK option enumeration. Reopen S15 only for another DDL idempotence flag owner,
           silent duplicate-write/wrong-acceptance, or fix validation.
```

Automation lesson: a negative calibration can be the bridge to the real hit. FK-name recovery was
not a bug because the normal path allowed cross-table duplicate FK names; the source comment then
exposed a sharper sibling flag-propagation proof.

## Follow-up S11 partial-index condition tick, EXECUTED (2026-07-03)

This tick returned from S15 to DDL dependency gates and found a distinct checker owner: partial
index condition columns.

```text
SENSE:     `checkColumnReferencedByPartialCondition` returns ERROR 8272 whenever a column appears
           in `idx.AffectColumn` for a partial index condition. The MODIFY path calls it before
           distinguishing metadata-only COMMENT/DEFAULT from semantic changes.
SCHEDULE:  P4 allowed because this is a different dependency checker from generated/expression
           indexes. The matrix must prove target-schema validity, not merely show an ALTER error.
ACT:       MINE_BUG on `INDEX idx_a(a) WHERE b > 0`:
           direct target schemas with `b INT COMMENT 'new-comment'` and `b INT DEFAULT 5` both
           succeeded and passed `ADMIN CHECK TABLE`; the default target inserted `b=5`. Existing
           tables with the same partial index rejected `ALTER TABLE ... MODIFY COLUMN b INT COMMENT
           ...` and `... DEFAULT 5` with ERROR 8272. Non-condition column COMMENT succeeded, and
           DROP INDEX then condition-column COMMENT succeeded.
INTEGRATE: id630009 inserted into remote `found_bug` as confirmed (`MAX(id)=630009,COUNT=41`).
           Selector ledger and O14 were updated to record partial index condition owner coverage.
HEALTH:    Low-to-medium wrong-error. Method value is high: S11 now covers an independent
           dependency gate, not only the generated-column hidden-column machinery.
NEXT:      Guard partial-index predicate enumeration. Reopen S11 only for silent wrong-acceptance,
           another dependency checker, or fix validation.
```

Automation lesson: a negative S10 metric probe still paid off because it forced the next source
search to ask "which dependency gate gives a precise P/Q split?" The high-yield move was not more
types; it was switching from metric mismatch to dependency-existence-as-danger proof.

## Follow-up S15 spec-splitting flag tick, EXECUTED (2026-07-03)

This tick stayed in DDL idempotence flags but did not enumerate FK options. It looked for a
different way a parser flag can disappear: a parent `AlterTableSpec` is split into child specs that
read a different flag owner.

```text
SENSE:     parser.y accepts `ADD IfNotExists (TableElementList)` and stores the flag on
           `spec.IfNotExists`. `ResolveAlterTableSpec` splits `NewConstraints` into
           `AlterTableAddConstraint`, but the index branch calls `createIndex(...,
           constr.IfNotExists)` and the CHECK branch never checks `spec.IfNotExists`.
SCHEDULE:  P4 allowed because this is a new S15 sub-shape: spec-splitting / AST-rewrite flag
           ownership, not the FK executor branch from id630008.
ACT:       MINE_BUG on `ALTER TABLE ... ADD IF NOT EXISTS (...)`:
           outer column retry `ADD IF NOT EXISTS (b INT)` returned Note 1060; outer key retry
           `ADD IF NOT EXISTS (KEY idx_a(a))` failed with ERROR 1061; inner key retry
           `ADD IF NOT EXISTS (KEY IF NOT EXISTS idx_a(a))` returned Note 1061; outer CHECK retry
           failed with ERROR 3822. Index/CHECK counts stayed one.
INTEGRATE: id630010 inserted into remote `found_bug` as confirmed (`MAX(id)=630010,COUNT=42`).
           Selector ledger S15 now records two sub-shapes: sibling executor branch flag loss and
           parent-spec split flag loss. O18 and the proof catalog were updated.
HEALTH:    Low-to-medium wrong-error. No data corruption, but user-facing migration idempotence
           fails. Method value is high because future S15 scans must follow parse node -> resolved
           spec -> split child job -> executor helper args.
NEXT:      Guard table-element syntax enumeration. Reopen S15 only for another spec-splitting or
           AST-rewrite flag loss, silent duplicate-write/wrong-acceptance, or fix validation.
```

Automation lesson: once a selector mentions a parser/AST bit, the audit must follow ownership
through every intermediate representation. A copied parent struct is not proof that the child
executor reads the copied field.

## Follow-up S16 validator-ordering tick, EXECUTED (2026-07-03)

This tick stayed in DDL but moved away from idempotence flags. It targeted a validator-ordering
shape in `MODIFY COLUMN`: the FK compatibility check runs before column options are applied.

```text
SENSE:     `checkModifyColumnWithForeignKeyConstraint` returns nil when type/flen/decimal are
           unchanged. In `buildModifyColumnAndConstraint`, that check runs before
           `ProcessModifyColumnOptions`, which later applies `ColumnOptionNotNull`.
SCHEDULE:  P4 allowed because this is a new proof shape: validator runs on incomplete target state,
           not another S15 flag-loss or S11 dependency-overbroad case.
ACT:       MINE_BUG on child FK columns with SET NULL actions:
           direct target schemas `pid INT NOT NULL` + `ON DELETE SET NULL` and `ON UPDATE SET NULL`
           both rejected with ERROR 1830. But nullable child FK tables could be altered to
           `pid INT NOT NULL` with no warning. `SHOW CREATE TABLE` showed the invalid final state,
           and parent DELETE/UPDATE then failed with ERROR 1048 when SET NULL tried to write NULL.
           `ON DELETE RESTRICT` was the green control and accepted nullable->NOT NULL.
INTEGRATE: id630011 inserted into remote `found_bug` as confirmed (`MAX(id)=630011,COUNT=43`).
           Selector ledger gained S16 DDL validator ordering gap; oracle library gained O19
           target-state rejection reference; proof catalog and handoff were updated.
HEALTH:    Medium wrong-acceptance. It is fail-stop rather than silent corruption, but DDL accepts
           an illegal target schema that normal CREATE/ADD FK rejects. Method value is high because
           it adds "complete target state before validation" as a reusable proof obligation.
NEXT:      Guard FK action/type enumeration. Reopen S16 only for another validator-before-options
           gap, a stronger silent consequence, or fix validation.
```

Automation lesson: when a validator receives an object named "new", prove that it is actually the
final target object. If later code applies flags/options that change the claim, those later
mutations become first-class D dimensions.

## Follow-up S16 proof-precision tick, EXECUTED (2026-07-03)

This tick stayed in DDL and deliberately did not enumerate more FK actions. It used id630011's
source predicate to ask which dimensions `type/flen/decimal` equality failed to prove.

```text
SENSE:     CREATE/ADD FK compatibility compares type, unsigned flag, charset, and collation.
           `checkModifyColumnWithForeignKeyConstraint` returns early on only type/flen/decimal.
           That coarse predicate omits nullability, signedness, charset, and collation.
SCHEDULE:  P4 allowed because this is the same S16 validator but a different missing dimension
           with its own behavior oracle. A coverage pass downweighted dimensions with later safe
           validators: primary-key NULL and indexed-column collation both looked suspicious but
           were blocked later.
ACT:       MINE_BUG on child FK signedness:
           direct parent `INT` / child `INT UNSIGNED` FK rejected with ERROR 3780; valid
           signed/signed `ON UPDATE CASCADE` control updated parent and child from `1` to `-1`;
           valid signed/signed FK followed by child `MODIFY a INT UNSIGNED` succeeded and
           published the FK; parent update `1 -> -1` then failed with ERROR 1264. Dropping and
           re-adding the same FK after the red ALTER failed with ERROR 3780.
INTEGRATE: id630012 inserted into remote `found_bug` as confirmed (`MAX(id)=630012,COUNT=44`).
           Selector ledger S16 now records nullability and signedness as proven target-state
           dimensions, and O19 records the signedness cascade/round-trip oracle.
HEALTH:    Medium wrong-acceptance. The consequence is fail-stop DML, not silent corruption, but
           the method value is high: one selector produced a second confirmed bug and two useful
           green calibrations without broad FK fuzzing.
NEXT:      Guard FK dimension enumeration. Reopen S16 only for a missing dimension not covered by
           a later complete-target validator, a silent consequence, or fix validation.
```

Automation lesson: after a hit, do not blindly vary syntax. Re-read the exact predicate that made
the previous proof too weak, list the omitted D dimensions, and spend SQL only on dimensions that
lack a later safety owner.

## Follow-up S17 reorg-invariant tick, EXECUTED (2026-07-03)

This tick stayed in DDL but moved from schema validators to data rewrite owners. The target was
`MODIFY COLUMN` reorg, where existing rows are decoded, cast to the new column type, and written
back through a low-level path.

```text
SENSE:     ordinary AddRecord/UpdateRecord calls CheckRowConstraint, and ADD CHECK scans existing
           rows through verifyRemainRecordsForCheckConstraint. The MODIFY COLUMN update worker
           instead casts the old value and writes the encoded row with txn.Set.
SCHEDULE:  P4 allowed because this is a new owner boundary: a raw DDL writer must re-prove row
           invariants after conversion. It is not another FK dimension.
ACT:       MINE_BUG on CHECK(a > 0) with lossy-but-successful conversions:
           DECIMAL(10,2) 0.40, DOUBLE 0.4, and VARCHAR '0.4' each satisfied CHECK(a > 0) before
           ALTER. `ALTER TABLE ... MODIFY a INT` succeeded with no warnings and produced final
           rows where a=0 and a>0=0. ADD CHECK on an INT table containing 0 rejected with ERROR
           3819, and ordinary INSERT 0 into the altered table also rejected with ERROR 3819.
INTEGRATE: id630013 inserted into remote `found_bug` as confirmed (`MAX(id)=630013,COUNT=45`).
           Selector ledger gained S17 DDL reorg constraint bypass; oracle library gained O20
           post-conversion CHECK oracle; proof catalog, handoff, draft, and method case were
           updated. The side rediscovery of CREATE TABLE LIKE source CHECK-name mutation was
           classified as duplicate id630005 and not reinserted.
HEALTH:    High data-integrity bug. The CHECK constraint remains published while existing rows
           violate it, and ADMIN CHECK TABLE does not catch the inconsistency.
NEXT:      Guard type-pair enumeration. Reopen S17 only for another raw DDL writer, another row
           invariant owner, or fix validation that routes MODIFY reorg through post-conversion
           constraint evaluation.
```

Automation lesson: proof obligations are not only in validators. Any special writer that bypasses
the normal safe write path must prove every invariant the safe path would have checked.

## Follow-up S4 side-owner remap tick, EXECUTED (2026-07-03)

This tick returned to DDL side-state ownership after a green masking-policy baseline. It did not
expand the old masking-policy matrix; it searched for a sibling DDL entrypoint that changed the same
owner key but bypassed the helper that made the baseline green.

```text
SENSE:     masking-policy rename/drop/column/truncate paths were green, and source showed explicit
           owner-specific helpers. Truncate remaps table_id; rename rewrites names. EXCHANGE
           PARTITION swaps a standalone table ID with a partition physical ID, but its check and
           exchange path did not mention masking-policy side state.
SCHEDULE:  P4 allowed under S4 because this is a new ID-swap entrypoint on a side sys table keyed
           by object ID and exposed through logical table DDL.
ACT:       MINE_BUG on masking policy x EXCHANGE PARTITION:
           before exchange, ALTER TABLE nt DISABLE/ENABLE MASKING POLICY mp_nt worked. After
           ALTER TABLE pt EXCHANGE PARTITION p0 WITH TABLE nt, the policy row still said
           table_name=nt but table_id matched pt.p0's tidb_partition_id. DISABLE/DROP by nt and
           by pt both failed with ERROR 1105. Recreating mp_nt on nt created a second row on the new
           table ID; DISABLE affected only the new row and left the old row ENABLED.
INTEGRATE: id630014 inserted into remote `found_bug` as confirmed (`MAX(id)=630014,COUNT=46`).
           Selector ledger S4 now has two post-birth hits: stats lock and masking policy after
           EXCHANGE PARTITION. Oracle library gained O21 side-state owner remap; proof catalog,
           handoff, owner matrix, draft, and method case were updated.
HEALTH:    High side-state ownership bug. The user-visible oracle is management DDL failing to
           reach a visible policy row; it is not just a sys-table display mismatch.
NEXT:      Guard masking-policy basic rewrite/cleanup. Reopen S4 for a different ID-swap or
           move/rekey owner, id630014 fix validation, or a stronger security/data behavior oracle.
```

Automation lesson: a green owner matrix can be more than a negative result. It identifies which
helpers make common paths safe; the next high-yield search is the sibling path that changes the same
dimension without those helpers.

## Follow-up S18 embedded-owner handoff tick, EXECUTED (2026-07-03)

This tick stayed in DDL and focused on CHECK constraints after the reorg-invariant hit. The target
was not another type-conversion matrix; it was the ownership boundary between `ADD COLUMN` and
`ADD CHECK`.

```text
SENSE:     buildColumnAndConstraint extracts ColumnOptionCheck into ast.Constraint, and CREATE
           TABLE consumes that constraint into table metadata. But CreateNewColumn calls the helper
           as `col, _, err := ...`, discarding constraints, and AddColumn submits only
           ActionAddColumn/TableColumnArgs.
SCHEDULE:  P4 allowed because this is a new proof-obligation shape: a parent DDL owner accepts an
           embedded child obligation but may not transfer it to the child owner. This is not S17
           type-pair enumeration and not S15 idempotence-flag enumeration.
ACT:       MINE_BUG on `ALTER TABLE ... ADD COLUMN b INT DEFAULT 1 CHECK(b > 0)`:
           direct CREATE with inline CHECK published CHECK and rejected b=0; sequential ADD COLUMN
           then ADD CHECK also published CHECK and rejected b=0. Inline ALTER ADD COLUMN CHECK
           succeeded with @@warning_count=0, published no CHECK in SHOW CREATE or
           information_schema.check_constraints, and accepted b=0. A named inline CHECK behaved
           the same, proving this was not anonymous-name generation.
INTEGRATE: id30032 inserted into remote `found_bug` as confirmed (`MAX(id)=630014,COUNT=49`).
           Selector ledger gained S18 embedded constraint owner loss; oracle library gained O23
           target-schema constraint reference; proof catalog, handoff, draft, pending SQL, and
           method case were updated.
HEALTH:    Medium schema-integrity bug. The DDL silently removes a requested data-integrity
           constraint and allows future bad writes, but it is not as severe as id630013 because a
           published CHECK is not left containing violating existing rows.
NEXT:      Guard column-option enumeration. Reopen S18 only for another embedded child owner,
           same-root fix validation, or a stronger consequence oracle.
```

Automation lesson: when the source proves a parent object is valid, ask whether the statement also
contains child obligations with a different owner. The red cell often appears when the parent path
submits its own job and quietly drops the child proof obligation.

## Follow-up S19 validation-builder tick, EXECUTED (2026-07-03)

This tick exercised the updated P4 scheduler: it stayed in the high-risk state-transforming DDL
lane instead of taking another static-precheck target. The hit is only consequence-1, so HEALTH
records that honestly and keeps the next tick biased toward higher-consequence outcomes.

```text
SENSE:     EXCHANGE PARTITION WITH VALIDATION builds restricted SQL to prove standalone rows
           belong to the target partition. Source TODOs in the LIST/LIST COLUMNS builders said
           DEFAULT partitions were not handled.
SCHEDULE:  P4 allowed because the target is state-transforming DDL validation, not S15/S10/S11
           static precheck enumeration. The direct oracle was cheap: prove ordinary DML routes the
           row to DEFAULT, then compare exchange validation against sibling controls.
ACT:       MINE_BUG on LIST DEFAULT exchange validation:
           value 3 routes to `PARTITION pdef DEFAULT`; ordinary no-DEFAULT LIST exchange
           validation succeeds; `ALTER TABLE pt_default EXCHANGE PARTITION pdef WITH TABLE
           nt_default` fails with `ERROR 1064 ... near ") limit 1"`; the same legal row swaps with
           `WITHOUT VALIDATION`. LIST COLUMNS DEFAULT hits the same builder shape.
INTEGRATE: id630025 inserted into remote `found_bug` as confirmed
           (`COUNT(*)=68,COUNT(DISTINCT root_cause_id)=46`).
           Reopen test minted `root_cause_id=exchange-default-validation-sql`: this is not
           id630016 blast radius because the fix is DEFAULT-complement validation SQL, not
           duplicate/existence ordering. Selector ledger gained S19; oracle library gained O24;
           root-cause ledger, proof catalog, handoff, next-owner scan, draft, method case, and
           pending SQL were updated.
HEALTH:    consequence_mix records a new C1 hit. This is acceptable for one tick because P4 chose
           the high-risk lane and found a new root, but the drift response is active: do not keep
           mining partition validation wrong-errors. Pull another consequence-3/2 target next, or
           reopen S19 only for wrong-acceptance/data-placement or fix validation.
NEXT:      Guard partition syntax enumeration. Source the next high-risk lane target from
           reorg/backfill/id-swap/restore/pinned-substate with a consequence oracle stronger than
           syntax/wrong-error when possible.
```

Automation lesson: consequence-first scheduling is a target-selection rule, not a promise every
hit will be severe. When a high-risk target lands as C1, record the root honestly, learn the
selector, and let `consequence_mix` steer the next tick away from cheap repeats.

## Non-DDL proof-obligation calibration tick, EXECUTED (2026-07-03)

This tick followed the scope correction: stop mining reorg/partition-validation variants, and use
other modules only to improve the AI-native bug-finding method. It intentionally treated green
results as selector training data instead of as failed bug hunts.

```text
SENSE:     Two source-shaped candidates looked dangerous:
           1. S7 cache payload purity: `windowing_use_high_precision` is read by aggregate builder
              code but is not a prepared-plan cache key dimension.
           2. Prepared PointGet fast path: cached point-get plans have a source-side
              `skipPrivCheck` branch because the executor is "specially handled".
SCHEDULE:  P4 allowed this as calibration, not as a new bug-count push. The question was whether
           the P/Q/F template overclaims when it sees a missing key dimension or a skipped checker.
ACT:       Window aggregate matrix:
             direct ON/OFF semantics differed on cancellation-prone DOUBLE windows
             (`row5/row7: 0` vs `-1`), and the prepared statement hit cache after the switch.
             The cached execution followed the current OFF result, and a flush-cache reference
             matched it. GREEN: cache hit did not reuse old aggregate payload.
           Prepared PointGet privilege matrix:
             user prepared `SELECT * FROM t WHERE id=?`, executed once uncached and once cached,
             root revoked SELECT, and the same prepared EXECUTE in the original session failed
             with ERROR 1142. GREEN: the apparent `skipPrivCheck` did not produce a privilege
             bypass; the safe path still blocked execution.
           Predicate simplification source revisit:
             id30002 remains a strong existing red cell. `updateInPredicate`/`mergeInAndNotEQLists`
             delete `!=` after shrinking `IN`, while a sibling contradiction checker has explicit
             collation-compatibility guards. This sharpens the selector to "predicate deletion
             after value substitution without carrying collation/coercibility".
INTEGRATE: No new bug was inserted. S7 getter scan was updated with the windowing green result.
           Methodology v2 gained a four-gate red-cell rule and a requirement to label which gate
           a green cell failed.
HEALTH:    High methodology value, no bug-count value. The pass reduced false positives in two
           attractive source candidates and preserved id30002 as the better next optimizer
           selector.
NEXT:      For the next non-DDL search, start from a proof obligation whose oracle can distinguish
           all four gates: direct semantic drift, trigger evidence, stale payload / skipped safe
           path, and current-reference behavior. Do not reopen PointGet privilege or windowing
           precision unless source changes identify a different payload or safe-path owner.
```

Automation lesson: source comments and omitted key dimensions create candidates, not claims. A
confirmed red cell needs the whole chain: old/new semantics differ, the shortcut fires, the shortcut
reuses or trusts the wrong thing, and a strong reference path proves the current semantics.

## Follow-up S20 semantic-domain rewrite tick, EXECUTED (2026-07-03)

This tick used a non-DDL planner target only to improve the method. It deliberately picked a new
proof shape instead of continuing reorg/partition-validation variants.

```text
SENSE:     Source showed `join_key_type_cast` rewrites mixed INT/VARCHAR join equality from
           DOUBLE-domain comparison into INT equality, guarded by signed-int round-trip.
SCHEDULE:  P4 allowed because this had a consequence-2 oracle and a precise P/Q/F card:
           the rule checked integer round-trip but claimed equivalence to the original mixed
           comparison domain.
ACT:       MINE_BUG on scientific-notation VARCHAR values:
           scalar contract gave `10='1e1' -> 1`, `CAST('1e1' AS DOUBLE)=10`,
           `CAST('1e1' AS SIGNED)=1`, and rule guard=0. Default join returned
           `1:1,2:2e0,10:10,10:10.0`; CASE-wrapped and rule-disabled references returned
           `1:1,2:2e0,10:10,10:10.0,10:1e1`.
INTEGRATE: id30040 inserted into remote `found_bug` as confirmed
           (`COUNT(*)=69,COUNT(DISTINCT root_cause_id)=47`).
           Reopen test minted `root_cause_id=join-key-type-cast-domain-narrowing`.
           Selector ledger gained S20; oracle library gained O25; root-cause ledger, proof
           catalog, handoff, draft, method case, and pending SQL were updated.
HEALTH:    Novelty is healthy: this is not S3 extractor loss, not S7 cache reuse, and not DDL
           side-state blast radius. It validates the improved method's `D_dims` discipline:
           name D_old and D_new, then search for the smallest value where they disagree.
NEXT:      Guard numeric-string enumeration. Reopen only for another semantic-domain rewrite,
           a stronger consequence, or fix validation of join_key_type_cast.
```

Automation lesson: "code checks P, system believes Q" becomes much sharper when Q names the
semantic domain it is preserving. The AI speedup came from reading the rewrite and asking which
parser/equality domain was lost, not from trying many values.

## Follow-up S23 txn state-ingress tick, EXECUTED (2026-07-09)

This tick obeyed the no-partition boundary and moved into txn/NT-DML only to validate the improved
proof-obligation method on a new state container.

```text
SENSE:     Source comment in HandleNonTransactionalDML says NT-DML is a write and should not be
           affected by read_staleness; code clears ReadStaleness but buildShardJobs runs an
           internal SELECT through se.Execute.
SCHEDULE:  P4 allowed because the proof obligation was not "try txn combos"; it was a precise
           sibling-input audit: ReadStaleness, TxnReadTS, tidb_snapshot.
ACT:       MINE_BUG on SET TRANSACTION READ ONLY AS OF TIMESTAMP:
           AS OF control saw only old row `1:10`; ordinary UPDATE under tx_read_ts rejected
           read-only stale write; NT-DML without tx_read_ts updated `1:110,2:120`; NT-DML with
           tx_read_ts reported one successful job and left `1:110,2:20`.
INTEGRATE: id1230001 inserted into remote `found_bug` as confirmed
           (`MAX(id)=1230001,COUNT(*)=72,COUNT(DISTINCT root_cause_id)=50`).
           Reopen test minted `root_cause_id=ntdml-tx-read-ts-split-range-stale`.
           Selector ledger gained S23; oracle library gained O29; root-cause ledger, proof
           catalog, handoff, draft, and method case were updated.
HEALTH:    Novelty is healthy: not savepoint stack semantics (S21), not plan cache (S7), not DDL.
           It validates the improved "state ingress inventory" rule: a code path clearing one
           state input has not proven that sibling inputs are impossible.
NEXT:      Guard BATCH syntax enumeration. Reopen only for another stale input channel, stronger
           DELETE/INSERT-SELECT consequence, or fix validation.
```

Automation lesson: when a wrapper internally executes SQL, the wrapper inherits the full session
state machine unless it explicitly clears or rejects every relevant state input. That is a sharp
selector for AI because the candidate list comes from source-owned state fields, not random SQL.

## Follow-up S6 recovery identity-drift tick, EXECUTED (2026-07-12)

This tick stayed in DDL and reused the existing `FLASHBACK TABLE` FK validator obligation, but
changed one hidden dimension: the historical referenced object was replaced by a different
same-name object before recovery.

```text
SENSE:      RecoverTable checks schema/table-name and table-ID availability, then clones historical
            TableInfo and rows. Normal create/FK validation proves current parent structure, but
            recovery has no proof that the current same-name object is the historical parent or
            that recovered child rows still belong to its current rowset.
SCHEDULE:   P4 allowed this one-dimensional mutation because the candidate had a direct consequence-3
            oracle: existing-row FK differential, followed by a normal action on the current parent.
            Do not enumerate more FK actions after the root boundary is established.
ACT:        Same-name empty parent remained RED: FLASHBACK TABLE succeeded, an existing child row
            was orphaned, and ADMIN CHECK TABLE stayed green. A same-key replacement with
            ON DELETE CASCADE deleted the recovered child row. A second sibling with ON UPDATE
            CASCADE changed recovered (10,1) to (10,2) after UPDATE p SET id=2.
INTEGRATE:  Keep id1500002 as one candidate root, flashback-fk-rebinds-recreated-parent. The delete
            and update siblings are consequence escalation, not separate bugs. Assets and the
            recovery oracle were updated; asset store is 138 revisions, RED=23.
HEALTH:     High-value candidate: the published schema looks structurally valid and future invalid
            inserts are still rejected, while normal actions against the replacement parent mutate
            or delete historical rows. ADMIN CHECK TABLE is therefore a weak recovery oracle.
NEXT:       Source-target refresh found no new high-consequence DDL owner: terminal-action scan only
            produced a consequence-1 non-DDL candidate, and the remaining DDL hits are covered or
            have cleanup guards. Keep the negative cache and choose a new selector before executing.
```

Automation lesson: for restore/recovery paths, `metadata shape + future-write validation` is not a
complete oracle. After recovery, exercise the current referenced object and compare the recovered
rowset. This turns an identity claim into a behavioral proof and catches silent cascade effects that
`ADMIN CHECK TABLE` cannot see. The queue must also treat a screened-out low-consequence source
candidate as negative evidence, not as permission to widen the matrix blindly.

## Follow-up S6 repair-index metadata tick, EXECUTED (2026-07-12)

This tick kept the DDL/recovery boundary and selected a source TODO with a direct wrong-result
oracle instead of expanding the already-covered multi-schema or backfill families.

```text
SENSE:      RepairTable preserves the old table/index IDs and accepts a new CREATE TABLE after
            checking only index name, column names, and index type. The source TODO says the new
            TableInfo should be verified against actual data; PrefixLen and Unique are not checked.
SCHEDULE:   P4 allowed because the candidate has a precise P/Q/F card and a C3-style rowset oracle.
            A natural multi-schema rollback control was also run first and stayed GREEN, so the
            explicit multi_schema_change TODO was negative evidence rather than a new target.
ACT:        Exact physical KEY idx_v(v(3)) repaired as KEY idx_v(v(3)) was GREEN. Repairing the
            same physical index as v(2) made table scan find abc-two while FORCE INDEX returned no
            row. More strongly, physical KEY repaired as UNIQUE caused the default plan to become
            Point_Get and return only id=4 from an existing three-row duplicate set; a duplicate
            insert still succeeded and ADMIN CHECK TABLE stayed silent.
INTEGRATE:  Added the candidate target, source obligation, fault, oracle, four runs, and live log
            to the asset store. The official ADMIN documentation says repair is untrusted and the
            operator must ensure the supplied definition covers the original metadata, so the
            mismatched cells are reclassified INVALID(contract), not counted as a new bug.
            Keep the selector/oracle as a guardrail; it remains distinct from ADD INDEX downscale
            and FLASHBACK FK identity drift.
HEALTH:     The observed consequence is high-quality wrong-result, but it is caused by intentionally
            violating the documented recovery contract. This is exactly why contract admission must
            precede severity admission for repair paths.
NEXT:       Retain the exact-definition control and reopen only if a product-feasible path can
            produce a definition believed exact while physical index data still differs; otherwise
            use this selector only to screen future recovery candidates.
```

Automation lesson: a recovery command that reuses physical IDs must be tested as a
**metadata-to-physical reconciliation** problem. `SHOW CREATE` and `ADMIN CHECK TABLE` are weak
oracles here; the decisive cell is a differential between a reference table scan, a forced index
scan, the default planner choice, and a future write under the published constraint.

## Follow-up S29 partial-index proof-input tick, EXECUTED (2026-07-12)

This tick revisited the existing partial-index root only to complete current-master verification
and convert the earlier candidate into an owner-facing severe asset. It did not expand the
predicate matrix.

```text
SENSE:      The source gate claims query predicate => stored partial-index predicate. Query
            predicates pass normal range preparation, while CheckPartialIndexes parses the raw
            metadata condition immediately before CheckConstraints.
SCHEDULE:   P4 allowed because the old candidate had a live rowset mismatch and an upstream issue
            draft. The decisive cell was reduced to NOT NULL values to remove NULL semantics.
ACT:        Five rows, pi(b) WHERE a < 3, query a >= 0 ORDER BY b LIMIT 5. IGNORE returned
            ids 1..5; default and FORCE returned ids 1..3; EXPLAIN used pi; ADMIN CHECK was green.
            A temporary planner observation showed raw metadata range [-inf,+inf] on first use,
            versus [-inf,3) after normal predicate handling.
INTEGRATE:  found_bug id30001 is now issue-filed/high with #69779. Added S29, method case,
            structured run record, and current-master evidence log. No formal product test was
            added and no TiDB product source was changed.
HEALTH:     High-quality silent wrong-result: planner visits an incomplete physical subset while
            storage remains internally consistent. Hint and no-hint observations are one root.
NEXT:       Stop partial-index enumeration. Reopen only for proof-input normalization fix
            validation or a genuinely different proof owner/consequence.
```

Automation lesson: for planner fast paths, the proof obligation includes the representation and
normalization of every proof input. A correct-looking implication algorithm fed an under-normalized
metadata predicate is still an unsound proof system.

## Follow-up C3 host screen: issue61255 mixed-owner merge, EXECUTED (2026-07-12)

The next severity seed was `MULTI_ARTIFACT_OWNER_HOMOGENEITY`, using the existing non-partition
mixed unique/non-unique `ADD INDEX` probe. The run reached the intended pause and all consequence
oracles were green, but the target-shape guard rejected the result:

```text
requested worker count: 4
merge entry log:        type="merge temporary index" workerCnt=1 regionCnt=2
terminal state:         synced/public
ADMIN CHECK TABLE:      green
rowset differential:    green
verdict:                GREEN_BOUNDARY / INVALID(target-shape)
```

This is not evidence that mixed-owner merge is safe in general. The proof obligation specifically
needs an owner-homogeneity distinction that is live at merge consumption; with one merge worker,
the planned multi-worker race cannot occur. The asset log and JSONL run are stored under
`assets/store/logs/issue61255-nonpartition-merge-worker1-green-20260712.log` and
`assets/store/issue61255-mixed-owner-results.jsonl`.

Method lesson: a strong oracle does not rescue a dead target shape. Before grading a GREEN sibling,
prove that the target's controlling dimension exists at the exact phase where the obligation is
claimed. Otherwise the right output is a reusable negative boundary and a host-selection constraint.

## Follow-up C3 capability-boundary tick: issue59701 topology lift, EXECUTED (2026-07-12)

The scheduler consumed the next severity seed only after `issue61255` had been retired as
`INVALID(target-shape)`. The smallest reachable topology control was then tested on the authorized
testbed:

```text
shape:        ordinary non-partition ADD INDEX, classic txn reorg, 300000 rows, 64 regions
phase:        active write reorganization, first fault at row_count=42535
fault:        four consecutive same-instance HTTP owner resigns, 1s apart
terminal:     synced/public
oracle:       ADMIN CHECK green; table=300000, index=300000; visible [PRIMARY, idx_c]
verdict:      GREEN boundary, not a broad topology proof
```

The job remained `running` while the owner was repeatedly resigned, then resumed and completed.
This closes the same-instance resign/re-election shape as a strong negative boundary. It does not
exercise PD leader isolation or a distinct surviving TiDB owner, so it cannot supply the RED/GREEN
counterpart required by the broad `OWNER_TOPOLOGY_HANDOFF` C3 obligation. The target is therefore
`blocked` until that controlling dimension is available, rather than being re-run with more rows or
more same-instance resigns.

The source-target refill rules were also run for state ingress, pooled-session state, session-state
restore, identity tokens, and terminal-action errors. No new C3 target was produced; the only fresh
terminal-action candidate was consequence-1. The severity scheduler now returns no admitted target.

Automation lesson: add a **GREEN-only exhaustion gate** to incremental fuzzing. A strong GREEN is
valuable only when the target's controlling dimension was live. If the environment can exercise only
a weaker sibling, record the result as `GREEN_BOUNDARY`, normalize it to `INVALID` in the storage
schema when necessary, and block the target until the missing fault ingress or survivor topology is
available. This prevents the loop from converting repeated reachable GREEN runs into false evidence
of family-wide safety.

## Follow-up selector-reuse tick: table placement external effect, EXECUTED (2026-07-13)

This tick deliberately excluded PR review findings. It reused validated selector S35 against current
source and found a second durable owner in `onAlterTablePlacement`: the new PD bundle is published
while the DDL metadata transaction is still abortable.

```text
SENSE:      stage table policy locally, publish PD bundle externally, then finish the DDL job.
SCHEDULE:   pause only after PD success; use supported ADMIN CANCEL; compare both owners.
ACT local:  ALTER 8214, metadata p1/r1, mock PD p2/r2. Normal and compensation controls GREEN.
ACT live:   job 5369 cancelled, SHOW CREATE p1/three voters, real PD p2/two voters. Normal job
            5372 aligned metadata and PD on p2/two voters.
INTEGRATE:  id1800003 high; reuse S35; add owner-specific O42, obligation, scenario, and fixture.
HEALTH:     high-quality control-plane correctness failure with a direct replica-redundancy impact.
NEXT:       do not enumerate policy values. Return to current-source discovery after cleanup.
```

Automation lesson: **reuse selectors, not findings**. A validated selector may generate candidates
in other current-source handlers, but each candidate must rebuild P/Q/F, name its own durable owners,
and earn an independent RED with an owner-specific oracle before history is consulted. This makes the
asset database genuinely incremental without turning past bugs or review findings into the test set.

## Follow-up consumer-altitude tick: TiFlash replica cancellation, EXECUTED (2026-07-13)

S35 found another precommit external effect in current source: `onSetTableFlashReplica` updates the
PD rule before local metadata publication. The local owner matrix was RED, but severity remained
unproven until the loop added a real TiFlash consumer.

```text
SENSE:      count=0 deletes PD rule before TiFlashReplicaInfo is cleared locally.
ACT local:  job 120 cancelled; metadata count=1/available=true; mock PD rule absent.
ACT live:   precondition query 5/150; job 5382 cancelled; metadata available; PD rule absent;
            mpp[tiflash] query timed out with 9012.
CONTROL:    restore only the committed rule -> progress 1 and 5/150; normal removal -> metadata/PD
            absent and immediate 1815.
INTEGRATE:  id1830003 / #69785, O43, owner profile, real-TiFlash scenario, and reusable fixture.
```

Automation lesson: control-plane drift severity must be tested at the **consumer altitude**. Extend
the owner chain until either a downstream layer heals/rejects safely or the proposed user consequence
is directly observed. Metadata-versus-PD RED admitted the target; query timeout justified high
severity. This prevents the system from promoting every external-state mismatch on inference alone.

## Follow-up lineage-binding tick: CRR resume state, EXECUTED (2026-07-13)

This tick used current source only. A disaster-consequence scan led to CRR's persisted fast path:

```text
P:          resume progress may skip only artifacts from its producing replication lineage.
Q:          fixed downstream path plus task name is treated as lineage proof.
F:          state contains progress only; no cluster/task generation or storage identity.
RED 1:      saved 100 + current upstream 10 -> calculator 100, object checks 0.
RED 2:      resume 100 + storage checkpoint 10 -> PITR max recoverable 100.
CONTROL:    same-lineage 100/100 and no-state current 10 both GREEN.
INTEGRATE:  id1860003 high, S39, O44, module/obligation/scenario/scaffold assets.
```

Automation lesson: a persisted state format needs **semantic lineage binding**, not only parse
compatibility. For every token that enables a skip, generate a same-name/same-path two-lineage
matrix and follow the token to its highest consumer. This adds a new high-yield selector that is
independent of PR review findings.

## Follow-up lineage-binding tick: Lightning importinto, EXECUTED (2026-07-13)

S39 was reused against a different owner. The first idea compared GroupKey values, but source tracing
showed the importer restores the old key; the matrix was corrected to change the hidden input-file
lineage while keeping table name, checkpoint path, and group equal.

```text
RED:       Finished checkpoint + new-lineage.csv -> SubmitAndWait nil, submissions 0.
CONTROL:   no checkpoint + same current input -> submissions 1.
BASELINE:  finished resume path remains GREEN.
INTEGRATE: id1890003 high; input-owner module, obligation, oracle, scenario, and scaffold.
```

Automation lesson: when a weak identity token is actively copied into the new run, comparing that
token cannot expose drift. The LOOP must identify and mutate the semantic owner that the token is
supposed to represent. This correction is part of selector execution, not an after-the-fact fix.

## Third lineage-binding tick: BR backup checkpoint, EXECUTED (2026-07-13)

S39 was applied from current source to the backup source-cluster owner. The config hash preserves PD
address strings but the checkpoint metadata has no actual cluster ID.

```text
P:          completed ranges and SSTs belong to the current source cluster and snapshot.
Q:          same config hash, PD address strings, and storage prefix prove that lineage.
F:          cluster identity is absent; retry reuses old BackupTS, ranges, checksums, and files.
RED:        cluster 222 + old range -> admission nil, TS 200->100, incomplete=0,
            backupmeta=[old-cluster.sst].
CONTROL:    no checkpoint -> one current range remains.
COUNTERFACTUAL: persist cluster 111 and compare with 222 -> reject before backupmeta.
INTEGRATE:  id1920003 high; complete module/obligation/oracle/scenario/schedule/fault pack.
```

Method improvement: missing lineage metadata is not sufficient evidence. Follow the weak token to
the highest consumer and jointly observe skipped current work plus the published artifact. Also link
every obligation to selector, oracle, scenario, schedule, and fault assets; otherwise the database
contains the pieces but an incremental agent cannot retrieve a complete executable pack.

## Replay-compensation closure tick: savepoint retry, RETIRED GREEN (2026-07-13)

This tick began from current source, without PR/review/issue/history input. Field inventory found
that savepoint restore does not snapshot `StmtHistory`, suggesting a rolled-back write might be
replayed after an optimistic commit retry.

```text
P:          ROLLBACK TO permanently excludes post-savepoint effects, including after retry.
Q:          MemDB rollback is treated as sufficient although StmtHistory is not truncated.
TRIGGER:    compile failpoints, inject retryability, fail exactly one commit.
TRACE:      BEGIN -> SAVEPOINT -> INSERT(1) -> ROLLBACK TO -> INSERT(2) -> COMMIT.
RESULT:     ExecRetryCount=1; final rows=[(2,20)], identical to no-retry control.
CLASSIFY:   GREEN; replayed compensation dominates the apparent checkpoint omission.
INTEGRATE:  S40, exact-history oracle, schedule, fault, probe, and two GREEN runs.
```

Method improvement: add **replay compensation closure** between source proof and admission. For an
event-sourced owner, compute checkpoint restore plus forward replay plus compensating replay. A
missing snapshot field earns a C3 matrix only when compensation is absent, reordered, or interpreted
under changed semantic context. This prevents field-diff analysis from manufacturing false targets.

## Target-generation lineage tick: classic Lightning checkpoint, EXECUTED (2026-07-13)

S39 was applied to a new owner after current-source inspection found a declared checkpoint hash fed
by constant value 30 and a file-driver `TODO check if hash matches`.

```text
P:          completed status, engines, and chunks belong to the current target table generation.
Q:          same table name and checkpoint path prove that generation.
LOCAL RED:  expected ID=202/Loaded/0 engines; got ID=101/Analyzed/2 engines.
LIVE RED:   first ID=5412 with 2 rows; recreate as ID=5415 empty; second Lightning exit=0,
            ID=5415 remains empty and ADMIN CHECK is green.
INVALID CF: a TableID-only guard collapses to 0==0 in TiDB backend.
INTEGRATE:  id1950003 high; 6 target assets, 5 links, 4 runs, open_gaps=[].
```

Method improvement: split persisted lineage into source identity, target generation, configuration,
and output artifacts. Then verify that the selected identity field is actually materialized by every
backend. A locally plausible guard is not a fix proof when its live values collapse to a sentinel.

## External-owner severity calibration: TRUNCATE affinity, MODERATE RED (2026-07-13)

This tick used current source only and reused S35 without using PR review findings. The first three
runs were INVALID because the existing failpoint was before, not after, affinity mutation. A new
test-only fault at the exact boundary produced the intended local RED.

```text
P:          new affinity group created and old group deleted before local schema publication.
Q:          cancellation is assumed to restore the external owner for the still-committed old ID.
RED:        ADMIN CANCEL; InfoSchema old TableID + AFFINITY=table; old PD group absent.
CONTROL:    normal TRUNCATE coherent; rebuild from committed TableInfo restores the old group.
CLASSIFY:   real bug, moderate, NOT_ADMITTED for the severe queue.
```

Method improvement: insert **user-promise calibration before fault injection**. Follow the owner to
the highest consumer and classify the direct promise before spending on a live lift. Official
affinity semantics are experimental Region colocation for query latency, so this split disables an
optimization but does not imply data corruption, replica weakening, or required-path unavailability.
Selector reuse identifies a bug-rich ordering shape; it does not inherit severity from earlier S35
hits. The target is retained as an execution-verified moderate asset and testbed work stops here.

## Restore dependency-closure tick: FLASHBACK CLUSTER cache side state, EXECUTED (2026-07-13)

This tick started from current source and did not use PR review findings, issues, history, or
partition paths to generate the target. It compared Flashback's included user ranges with excluded
system-table owners, then followed restored state bits to mandatory consumers.

```text
P:          restored CACHED ON TableInfo has exactly one usable table_cache_meta row.
Q:          restoring user metadata while excluding mysql state preserves that dependency.
LOCAL RED:  cached metadata + missing row; SELECT fallback green, INSERT commit terminal error.
LIVE RED:   job 5432 synced/public; table 5428 CACHED ON; side rows=0; SELECT works; INSERT 1105.
CONTROL:    replace only the missing row; identical INSERT succeeds and rowset becomes 1,2.
INTEGRATE:  id1980003 high; S41 plus module/obligation/oracle/scenario/schedule/fault pack.
```

Method improvement: restore analysis must compute a **dependency closure**, not only enumerate
special objects or restored key ranges. For every restored capability bit, trace its highest
mandatory runtime consumer and ask whether all required owners are inside the restore domain or are
reconciled before publication. Also preserve lower-layer recovery semantics in mocks: the rejected
split hypothesis looked like a hot loop only after its bounded client backoff was removed.

## Derived-context cache tick: PREPARE dedup stale read, EXECUTED (2026-07-13)

This tick was generated from current-source fast-path proof obligations. PR reviews, issues, fixes,
and history were excluded until an independent local RED existed.

```text
P:          SQL, parse context, database, and schema version match the dedup key.
Q:          every prepare-time semantic derivative is safe to reuse.
F:          fresh Preprocess runs, but cached SnapshotTSEvaluator overwrites its current result.
LOCAL RED:  warm at read_staleness=-1; clear; update 1->2; same SQL dedup hit returns 1.
CONTROL:    same SQL with only dedup disabled returns 2.
COUNTERFACTUAL: use ret.SnapshotTSEvaluator; full matrix returns 2 and passes.
LIVE RED:   real COM_STMT_PREPARE on testbed 8220955 returns dedup-on=1, dedup-off=2.
INTEGRATE:  id2010003 high; S42, O52, module/obligation/scenario/schedule/fault assets.
```

Method improvement: cache analysis must inventory **derived fields and all of their producers**, not
only visible key inputs. Give extra priority to hit paths that perform fresh analysis and then
overwrite part of it from the template: the discarded fresh value supplies a precise counterfactual
before testing. Provenance must also be enforced by actual worker behavior; a discovery worker that
opens review/history tools invalidates the entire generation round even when its prompt said not to.

## Clone alias-graph tick: CorrelateSolver wrong result, EXECUTED (2026-07-13)

This tick came from current-source clone and shortcut obligations. PR reviews, issues, fixes, and
history were unavailable during candidate generation, ranking, and the local RED/GREEN matrix.

```text
P:          the alternative subtree and each AccessPath are deep-cloned.
Q:          canonical stats producer and active physical consumer still share path state.
F:          canonical and active views are independently cloned into different objects.
LOCAL RED:  aggregate IN OFF=[1,2,3], ON=[]; Apply -> HashAgg -> TableDual.
MASK:       plain IN rebuilds leaf paths and returns [1,2,3] in both modes.
EXACT GREEN: map active paths to canonical clones; same Apply, real scan, 9/9 cells match.
LIVE RED:   identical SQL-only result on testbed 8220955 with real TiKV and default costs.
INTEGRATE:  id2070003 high; S44, O54, eight assets, four runs, open_gaps=[].
```

Method improvement: clone analysis now carries an alias graph and a repair-path dimension. A deep
copy can be simultaneously correct across alternatives and incorrect inside one alternative.
Passing siblings that activate a rebuild owner are retained as mask evidence; the next matrix
changes owner reachability before it changes syntax or data volume.

## Retry side-effect closure tick: pessimistic SETVAR wrong data, EXECUTED (2026-07-13)

This tick started from current-source retry acceptance and rollback ownership. PR review findings,
issues, fixes, and history were excluded until after the independent local RED.

```text
P:          KV statement state is rolled back and the executor is rebuilt.
Q:          every failed-attempt input consumed after re-entry is restored.
F:          SETVAR mutates UserVars before a later LockKeys write conflict; rollback omits UserVars.
LOCAL RED:  late conflict changes expected v/@x=1/1 to 2/2.
CONTROLS:   pre-evaluation conflict=1/1; idempotent assignment=7/7.
NATURAL RED: concurrent u=1 owner; UPDATE returns nil and commits (1,2),(2,1).
EXACT GREEN: restore entry UserVars; duplicate key returns and rows stay (1,10),(2,1).
LIVE RED:   SQL-only SETVAR+SLEEP race on testbed 8220955 with real TiKV.
INTEGRATE:  id2100003 high; S45, O55, eight assets, seven runs, open_gaps=[].
```

Method improvement: retry analysis now builds a mutation/rollback/consumer graph and varies fault
altitude around the mutation before varying syntax. A synthetic error is only the locator. The loop
then replaces it with a natural competing owner and promotes severity only when terminal error plus
durable state diverge. Timing controls without a boundary-landed observer are stored as INVALID.

## Deferred return-slot tick: IMPORT conflict deletion false success, EXECUTED (2026-07-13)

This tick was generated by the current-source retry closure scan. PR review findings, issues, fixes,
and history were excluded until the local RED and exact counterfactual existed.

```text
P:          delete staging completed without an earlier error.
Q:          nil from deleteBufferedKeys proves txn.Commit succeeded.
F:          return nil fixes an unnamed result before defer writes Commit error to local err.
LOCAL RED:  Commit wrapper rolls back and returns error; function returns nil; key remains.
LIVE RED:   job finished; PRIMARY/unique/secondary=2/1/2; ADMIN CHECK 8223.
CONTROL:    same process after one-shot fault=1/1/1; ADMIN green.
EXACT GREEN: named return exposes one retryable error; retry commits; 1/1/1; ADMIN green.
INTEGRATE:  id2130003 high; #69792; S46 plus seven assets, four links, five runs.
```

Method improvement: terminal-action analysis now resolves the actual language-level return slot.
Reaching `Commit` and assigning its error is not enough when the defer writes a local or shadowed
variable after an unnamed return value was fixed. Prefer a one-shot retryable fault: it separates
error visibility from permanent outage and lets the existing retry owner serve as the exact
counterfactual consumer.

The same round improved scanner precision with two source-proven negatives. Downgrade captured
outputs that every attempt authoritatively overwrites before use, and downgrade attempt-entry reset
plus fixed-source replay when the outer frontier advances only after success. Those rules retired
auto-ID allocation and nonpartition DDL backfill before execution.

## Typed retry-effect tick: ADMIN CLEANUP INDEX state replay, EXECUTED (2026-07-13)

This tick was generated from current-source retry callbacks. PR reviews, issues, fixes, and history
were unavailable until the independent local RED.

```text
P:          RunInNewTxn rolls back a failed cleanup transaction.
Q:          the next attempt starts from the same committed batch state.
F:          fetch/delete helpers mutate executor fields outside KV rollback ownership.
SMALL RED:  3 dangling entries plus Commit retry report 9.
BOUNDARY:   20001 entries plus retry panic at idxValsBufs[20000].
INVALID:    configured failpoint without source conversion produced no retry witness.
GREEN:      restore receiver state at attempt entry; exact counts and ADMIN CHECK pass.
INTEGRATE:  id2160003 moderate/C2; S45 typed-effect and edge-witness calibration.
```

Method improvement: source generation now resolves direct captured receiver calls by concrete
receiver type and expands one level of field effects. Admission requires a post-mutation retry edge;
execution requires an observed edge witness. The loop keeps consequence scoring independent: this
hit proves the selector works but does not enter the severe queue because it did not prove wrong
durable data.

## Retry terminal-publication tick: LAST_INSERT_ID residue, EXECUTED (2026-07-13)

This tick started from current source and S45's missing-state-owner rule. PR review findings,
issues, fixes, and history remained closed until local and real-TiKV RED.

```text
P:           StmtRollback and ResetForRetry remove failed-attempt statement state.
Q:           completion publishes values produced by the successful attempt only.
F:           LAST_INSERT_ID(expr) sets LastInsertID/LastInsertIDSet; reset omits both.
LOCAL RED:   natural unique conflict; retry hits zero rows; published/sink 99, row 1 unchanged.
LIVE RED:    SQL-only pessimistic RC; affected=0, published=99, sink=99 on testbed 8220955.
CONTROL:     same final gate/key state without a failed attempt; affected=0, published/sink 7.
EXACT GREEN: clear the two fields in ResetForRetry; same conflict publishes/persists 7.
INTEGRATE:   id2190003 high; #69796; six assets, eight links, four runs; C3_DIRECT.
```

Method improvement: retry residue does not need to influence re-entry. If the successful attempt
does zero work, terminal publication can expose a value/validity pair that only the failed attempt
set. Add zero-work re-entry to the small matrix, compare against the same final database state, and
follow the published value through one downstream durable consumer. This reused S45's selector,
fault boundary, and schedule while adding only the target-specific publication obligation and
oracle.

## Bounded transaction source-packet tick: no severe hit, method promoted (2026-07-13)

This tick stayed in COLD_SOURCE mode and did not touch testbed 8220955. It closed the remaining
commit-outcome and lock-generation proof debts before selecting another target.

```text
NEGATIVE: lock resolver caches only durable determined status; pessimistic no-op actions are not cached.
NEGATIVE: lost LockedWithConflictTS can leave a TTL-bounded lock, but no C3 consumer was found.
NEGATIVE: pipelined exclusive-end cleanup can omit a boundary Region, but read resolution and GC own recovery.
NEGATIVE: delayed fair-lock rollback is bounded by older forUpdateTS; later LockKeys installs the newer owner.
NEGATIVE: primary batch Region relocation rebuilds batches and re-labels primary by key.
SCOUT:    47 KiB/9 regions -> hard timeout at 75s; 25 KiB/9 regions -> valid JSON in about 45s.
SCANNER:  terminal-action scan over pinned client-go found only KVStore close ordering, no C3 txn owner.
```

Method improvement: AI source reasoning is now a compiled-packet stage. The main loop selects the
proof debt and owner ranges; `txnlab source-packet-scout` enforces 32 KiB, line, region, candidate,
and wall-clock budgets, isolates the child from repository search, and validates JSON locally. A
child-proposed schedule still requires direct owner-transfer verification. This tick caught a
plausible three-attempt fair-lock schedule and then rejected it because the child conflated TiDB
transaction-context publication with client-go committer mutation.

The next legal transaction proof debt is `ASYNC_SECONDARY_SET_COMPLETENESS`: verify that every key
whose async prewrite may be accepted is represented in the primary lock's secondary set across
filtering, batching, Region relocation, fallback, and duplicate-key handling. Do not revisit shared
locks, pipelined exclusive-end cleanup, status-cache classification, or primary Region relocation
unless a new owner or higher consumer appears.

## Savepoint mutable-value tick: local temporary size residue, EXECUTED (2026-07-13)

This tick started from current-source restoration ownership. Issues, fixes, history, and PR review
findings remained closed through local RED and exact counterfactual.

```text
P:           ROLLBACK TO SAVEPOINT restores MemDB; the temporary table is empty.
Q:           later size admission observes savepoint-equivalent transaction-local state.
F:           TemporaryTables survives, and its mutable value retains post-savepoint dirty size.
LOCAL RED:   roll back 1.2 MB; COUNT(*)=0; one-byte INSERT returns error 1114.
CONTROL:     empty table without the rolled-back segment accepts the one-byte INSERT.
EXACT GREEN: restore only per-table dirty size; the same RED arm succeeds.
LIVE RED:    SQL-only reproduction on testbed 8220955 at TiDB 5c9198e9484d.
INTEGRATE:   id2220003 moderate; S51, proof catalog, logs, method case, and asset graph.
```

Method improvement: restoration analysis now traverses mutable values behind containers and
interfaces. Container membership and value lifecycle are separate proof obligations. The loop also
keeps discovery quality separate from severity: this current-source, counterfactual-closed,
testbed-reproduced hit validates the selector, but its highest consumer is transaction-local write
availability, so it does not satisfy the severe queue.

The async-secondary completeness pass completed negative in the same tick. A child packet's zero
candidate result was accepted only after the parent closed predecessor, filtering, lifetime, and
highest-consumer owners. This is now a packet-compiler gate for future cross-layer work.

## MDL-on retry capability tick: hidden advisory lock, EXECUTED (2026-07-14)

This tick kept metadata locking at its default enabled value and used no concurrent DDL. Candidate
generation came from current-source retry ownership; issue and history search remained closed until
local RED.

```text
P:           StmtRollback and ResetForRetry restore a failed pessimistic statement attempt.
Q:           a capability owned only by that attempt cannot survive successful zero-work retry.
F:           GET_LOCK owns session map state plus an internal txn outside retry rollback.
LOCAL RED:   natural conflict; retry=1; success/zero rows; competitor GET_LOCK=0.
LIVE RED:    real TiKV; retry=1; IS_USED_LOCK=owner; competitor=0; MDL=1.
CONTROL:     same final state without failed attempt; IS_USED_LOCK=NULL; competitor=1.
RECOVERY:    hidden owner RELEASE_LOCK changes competitor to 1.
INTEGRATE:   id2310003 high-consequence/low-frequency; #69820; external-capability consumer.
```

Method improvement: retry side-effect inventory now follows capability ownership as well as values.
The strong oracle is owner identity plus independent contention plus cleanup recovery. Row-dependent
arguments prevent constant evaluation from polluting the zero-work control. Stop advisory-lock
variants; generate the next target from a different external capability owner.

## MDL-on protocol-output tick: stale explicit insert ID, EXECUTED (2026-07-14)

This tick generated candidates from current-source terminal consumers. Public issues, fixes,
history, and PR review remained closed until local RED.

```text
P:           rollback plus ResetForRetry rebuild a successful statement attempt.
Q:           the OK packet contains only successful-attempt output.
F:           failed-attempt InsertID survives; zero-work retry does not overwrite it.
LOCAL RED:   retry=1; retry result 0/42; same-state control 0/0; sink 42/0.
EXACT GREEN: clear only InsertID; retry remains 1; both results and sink become 0/0.
LIVE RED:    real TiKV through database/sql; slow log proves retry=1; MDL=1.
INTEGRATE:   id2460003 high; #69827; singleton protocol-output owner.
```

Method improvement: reset-differential generation is now consumer-first. Enumerate terminal fields,
backward-slice to owners, intersect with accepted-retry mutations, subtract reset coverage and
successful-attempt overwrite guarantees, then force zero-work re-entry. This removes the old
value/flag-pair shape assumption that missed singleton `InsertID`. Stop this owner and continue from
a different public output owner.

## Recovery-certificate proof-closure tick: failed uniqueness proof omitted, EXECUTED (2026-07-14)

This tick revisited no terminal root. It corrected the set definition behind the previously negative
async-secondary pass: recovery completeness must cover every logical commit prerequisite, not only
mutations that can leave an accepted lock.

```text
P:           primary async lock plus listed secondaries is a complete recovery certificate.
Q:           every prerequisite for committing the logical transaction succeeded.
F:           cross-Region CheckNotExists returned AlreadyExist but wrote no lock and was omitted.
RAW RED:     definite ErrKeyExist; empty async recovery set; independent resolver committed primary.
SQL RED:     MDL ON; COMMIT duplicate; fresh account balance 0 -> -100; 3/3 without Region delay.
EXACT GREEN: only hasNoNeedCommitKeys => no async; identical SQL/fault leaves balance 0.
INTEGRATE:   id2550003 high / critical consequence; 124 surfaces, 101 roots, 47 high, 109 confirmed.
```

Method improvement: replace `accepted lock keys - recovery members` with
`all commit prerequisites - durable recovery evidence`. Partition candidates into effect mutations and
proof-only predicates, force an effect prefix to succeed while a proof fails naturally, remove only
compensation, then invoke an independent recovery owner. The new selector is
`RECOVERY_CERTIFICATE_PROOF_CLOSURE`; the C3 oracle is definite constraint failure versus fresh durable
business state. Async commit is opt-in, but lazy uniqueness and MDL remained at their defaults.

## Safe-point retirement consumer-closure tick: deleted row resurrection, EXECUTED (2026-07-14)

This tick started from current-source GC protection and consumer closure. Issues and history remained
closed until raw and SQL-level real-TiKV RED.

```text
P:           an old active startTS is omitted from min-start-TS, so GC may reclaim its evidence.
Q:           every surviving consumer of that startTS is fail-closed before creating effects.
F:           reads CheckVisibility; nonempty KVTxn.Commit reaches prewrite without that guard.
RAW RED:     real TiKV GC/compaction; stale commit nil; fresh KV value "resurrected".
SQL RED:     MDL ON; ordinary 2PC; assertion OFF; COMMIT nil; fresh row [[1 11]].
MASK:        fresh-install FAST assertion rejects the UPDATE shape with Assertion=Exist.
EXACT GREEN: pre-prewrite CheckVisibility returns 9006; fresh row remains absent.
INTEGRATE:   id2580003 high / critical consequence; #69833; 125 surfaces, 102 roots.
```

Current-master expansion on TiDB `94b834d9`, client-go `01bd8f99`, and real TiKV `c27c6620`
showed why a one-dimensional mask matrix is insufficient. With MDL/FK checks ON and ordinary 2PC,
insert-delete ABA returned write conflict without GC but committed `(1,11)` after GC under both FAST
and STRICT. A strict FK cell likewise returned write conflict/no orphan without GC, but after GC the
old child COMMIT returned nil and a fresh anti-join found orphan `(1,1)`. This remains the same
safe-point-retirement root and does not increment the bug count.

Method improvement: after a first GREEN, classify the guard's provenance before retiring the
candidate. Mandatory owner guards close a proof obligation; new-install overrides, upgrade fallbacks,
session settings, and best-effort checks define a production matrix and may only mask one surface.
The matrix must also vary the semantic proof representation: existing/absent, assertion-bearing,
lock-only, FK, and proof-only mutations. A GREEN `AssertExist` cell cannot retire an `AssertUnknown`
or cross-key proof cell.
For retirement bugs, separately prove the natural wall-clock chain and the deterministic compressed
test: the latter may advance time or request compaction, but must keep real GC, storage conflict
reclamation, commit, and fresh-state observation intact. The new selector is
`SAFE_POINT_RETIREMENT_CONSUMER_CLOSURE`.

## Value-replacement proof-revalidation tick: cached-table lease crossed, EXECUTED (2026-07-14)

This tick started from current-source proof ownership. Issue and fix history stayed closed until a
local product RED and exact owner GREEN existed.

```text
P(x):         initial commitTS x is below the cached-table WRITE lease.
Q:            every Commit request uses a commitTS satisfying P.
F:            CommitTsExpired replaces x with x2 and retries without rechecking P(x2).
LOCAL RED:    natural minCommitTS push; 2 Commit RPCs; SQL success; cache/source = 0/1.
DURABLE RED:  INSERT SELECT consumes cache 0 into regular sink; source remains 1.
OWNER GREEN:  recheck replacement; checker calls 2; Commit RPCs 1; key absent/rollback.
LIVE RED:     pinned real TiKV; real prewrite/CheckTxnStatus/CommitTsExpired; MDL ON.
LIVE GREEN:   same real-TiKV schedule; first rejection remains, replacement fails before RPC.
INTEGRATE:    id2610003 high / critical consequence; #69836; 126 surfaces, 103 roots, 49 high,
              111 confirmed.
```

Production reachability is a first-class gate. The compressed hold corresponds to a writer-local
network, runtime, CPU, or scheduling pause longer than the fixed five-second cache lease while a peer
remains healthy. A primary lock must remain live: current client-go gives a roughly 4 MiB write about
12 seconds, whereas the small-transaction three-second TTL is a negative control.

This must be written as a seven-field production trigger card before issue promotion: supported
workload, natural event producer, exact ordering/lifetime inequalities, defaults and non-defaults
including MDL, healthy/unhealthy component topology, public result plus fresh-session durable-state
consequence, and a control that breaks one required inequality or enables the exact protection.
"Network failure" or "an in-flight RPC returns an error" is not a producer description. The card must
also explain why a real client reaches any later `COMMIT`, retry, recovery, or second request required
to make the consequence durable. Failpoints may compress the named schedule, but cannot replace it.
A candidate without this card remains SOURCE/LOCAL evidence even when its injected RED is deterministic.

Method improvement: store proof facts as argument-bearing tokens such as
`checked(commitTS=x, lease=L)`, not booleans such as `commitTSChecked=true`. Enumerate every assignment
to either argument before the irreversible consumer; require exact revalidation, a proven monotonic
implication, or fail-closed behavior. The new selector is `VALUE_REPLACEMENT_PROOF_REVALIDATION`.

## Rollback-checkpoint horizon tick: failed FK statement later commits, EXECUTED (2026-07-14)

This tick started from current-source rollback ownership. Issues and history stayed closed until local
and real-TiKV RED plus exact owner GREEN.

```text
P:            FK savepoint can undo all parent/cascade stages crossing intermediate StmtCommit.
Q:            nested FK-trigger success means the savepoint can be released.
F:            outer final LockKeys can still return a terminal error after release.
MOCK RED:     UPDATE 1205; later COMMIT; fresh parent/child = 2/2.
LIVE RED:     real TiKV; same result with one-second compressed timeout.
DEFAULT RED:  MDL=1; lock timeout=50; LockKeys=50.0017s; fresh state 2/2.
OWNER GREEN:  retain checkpoint through final locks; same 1205; fresh state 1/1.
INTEGRATE:    id2640003 high / critical consequence; #69838; 127 surfaces, 104 roots, 50 high,
              112 confirmed.
```

The production shape is a tenant/account key migration with `ON UPDATE CASCADE` and a no-op guard-row
assignment in the same multi-table UPDATE. An older long-running migration holds the guard beyond the
default timeout because of a large batch, hot Region, server-busy backoff, or storage pressure. The
racing service catches 1205 as a retryable statement conflict and commits earlier audit/progress work;
an always-ROLLBACK service is the explicit non-trigger.

Method improvement: model each rollback capability as `protects(C,E,until=T)` and compare its release
with every later public error site. Nested success is not the terminal boundary of the enclosing user
operation. The new selector is `ROLLBACK_CHECKPOINT_FALLIBILITY_HORIZON`; stop after one checkpoint
owner/release/highest-consumer root.

## Retry allowed-outcome calibration: scalar-subquery candidate rejected (2026-07-14)

```text
CANDIDATE:   pessimistic retry count 1; success + COMMIT; durable route/policy = 2/10.
WEAK ORACLE: fresh transaction from final state produced 2/20.
COUNTERFACT: forced replan still produced 2/10; declining retry returned 9007 and preserved target.
CONTRACT:    establish old RR snapshot, let publisher commit 2/20, run UPDATE once.
WITNESS:     retry count 0; durable route/policy = 2/10; ADMIN CHECK passed.
VERDICT:     INVALID(oracle-too-strong); no found_bug row and no issue.
```

The seven-field production card was useful but not sufficient. It proved that the schedule was real;
it did not prove that the outcome violated the isolation contract. The LOOP now adds an admission
step between production reachability and RED promotion: enumerate the legal one-attempt outcome set
under the same transaction snapshot, statement TS, and current-read/consistent-read split. A fresh
final-state control is only one member candidate, not the oracle by itself.

Method improvement: `RETRY_ALLOWED_ONE_ATTEMPT_SET`. A fail-closed GREEN is not causal proof unless
the original output is first shown to be outside every legal one-attempt outcome.

## Retry cache provenance pass: explicit ID/payload recombination (2026-07-14)

This tick started from current-source retry ownership and did not use a PR-review finding. The
historical issue search stayed closed until local and real-TiKV RED plus exact owner GREEN.

```text
P:            cached auto ID is reusable for the same logical retry row.
Q:            positional cache element i may replace current auto-ID datum i.
F:            dynamic source changes explicit ID 100->200, but cache lacks provenance/owner binding.
LIVE RED:     real 9007; Exec_retry_count=1; Succ=true; source 200/new; target 100/new.
ALLOWED SET:  {100/old, 200/new}; mixed 100/new is impossible in one coherent attempt.
OWNER GREEN:  classify current explicit datum before cache reuse; same retry; target 200/new.
DEDUP:        #20629/#20659 owns generated-ID buffer exhaustion, not silent identity rebinding.
INTEGRATE:    id2670003 high / critical consequence; #69845; 128 surfaces, 105 roots, 51 high,
              113 confirmed.
```

The production shape is a batch migration/reconciliation job copying explicit external IDs from
stable staging slots into an auto-increment materialization. A normal incremental publisher corrects
one slot mapping and touches another hot entity covered by the batch. Scan work or storage latency
keeps the old attempt alive until the publisher commits; the hot-row conflict naturally triggers
autocommit retry. No node failure or non-default SQL setting is involved.

Method improvement: model replay values as
`certificate(value,provenance,logical_owner,generation,predicate)`, not raw array elements. Add
`RETRY_CACHE_PROVENANCE_AND_IDENTITY` after allowed-outcome admission and before expensive fault
design. Stop after one cache/provenance/owner/consumer root.

## Failed membership publication tick: live replacement suppresses retry, EXECUTED (2026-07-14)

This tick started from the current-source MDL proof: DDL trusts leased server membership, and MDL
transactions skip schema-delta validation because they trust DDL's wait. Issues and history stayed
closed until the real-TiKV RED and exact owner GREEN.

```text
P:             replacement server-info session is live.
Q:             it is safe to install that session before membership Put succeeds.
F:             Put fails; loop waits on live unpublished session and never retries.
WEAK RED:      direct manager removal gives COMMIT success and table/index 1/0.
BROAD CONTROL: whole-process 95s stall restarts schema validator; COMMIT returns 8028.
INVALID:       custom Inject markers compiled as no-ops before make failpoint-enable.
EXACT RED:     same run logs StoreServerInfo error; ADD INDEX success; COMMIT success; 1/0; 8223.
OWNER GREEN:   restore completed prior owner and close unpublished replacement; retry succeeds;
               DDL waits; table/index 1/1; ADMIN green.
INTEGRATE:     id2700003 high / critical consequence; 129 surfaces, 106 roots, 52 high,
               114 confirmed; no exact post-RED issue match.
```

Production reachability is intentionally narrow: server-info session loss must not restart the
schema-sync session. Replacement lease grant succeeds, then all five registration Put attempts fail
during a short recovery flap, and the unpublished replacement remains live through DDL and COMMIT.
MDL and SQL variables remain at defaults. Whole-node outage is not claimed as the producer because
its sibling validator owner fails closed.

Method improvement: every injected matrix row now needs `fault_activation_witness` in the same run.
Enabling a named failpoint is not evidence; require an exact stack/log, call counter, captured RPC, or
state transition. Extend S37 with `FAILED_PUBLICATION_LIVE_OWNER_RETRY_SUPPRESSION`: audit
`create owner -> assign current -> publish` sequences where publication errors return to a loop that
waits only on the new owner's completion. Local liveness can be the reason shared state never heals.

## Attempt-generation tick: preprocessed scalar constant crosses RC retry, EXECUTED (2026-07-16)

This tick resumed from current source and the stored RR allowed-outcome negative. Issue/PR search
remained closed until independent local and real-TiKV RED.

```text
P:             scalar subquery evaluated during expression rewrite.
Q:             resulting Constant in ExecStmt.Plan remains valid across transparent retry.
F:             RC retry refreshes statement TS and rebuilds executor without rebuilding the plan.
RC CONTROL:    no retry; publisher after statement TS => old scalar/old source.
LIVE RED:      retry; UPDATE+COMMIT success; route 300/new, aggregate 30/old; ADMIN green.
OWNER GREEN:   rebuild plan after failed-attempt rollback; retry stays 1; new/new result.
RR NEGATIVE:   old scalar/new current read is legal without retry and remains INVALID as a bug.
DEDUP:         no exact issue/PR; #69826 owns CTEStorageMap rather than plan Constant.
INTEGRATE:     id2730003 high; 130 surfaces, 107 roots, 53 high, 115 confirmed.
```

The production shape is a route or resource allocation batch that stores a scalar balance, ledger,
or inventory aggregate. A concurrent allocator claims the old unique route, inserts a value included
by the aggregate, and advances configuration while the batch is in normal scan/storage latency. The
conflict is a supported natural retry producer; `SLEEP` only compresses that interval.

Method improvement: negative assets now participate in candidate generation. The earlier RR result
did not merely reject one test; it identified the ownership dimension to vary. RC binds consistent
and for-update reads to one attempt-local statement TS, shrinking the legal set and turning the same
symptom into a strong RED. Add `ATTEMPT_LOCAL_PREPROCESSED_CONSTANT_REUSE` and O66; future ticks scan
planning-time data reads that emit ordinary constants or fields, then intersect them with retry paths
that refresh an omitted generation argument.

## Required-repair fail-open tick: PiTR AUTO_ID rebase, EXECUTED (2026-07-25)

This cross-module tick reused the proof obligation behind a repaired historical failure, then varied
the repair's error contract. Issue and bug-library search remained closed until RED/GREEN.

```text
U:             PiTR raw replay advances persisted IncrementID but leaves autoid service stale.
R:             final per-table ForceRebase is the only owner that closes U.
F:             one metadata transaction error is warned and swallowed; helper returns nil.
RED:           generated REPLACE reuses id=2; ROW_COUNT=2; restored payload disappears.
GREEN:         disable only the fault; rebase=1004000; next id=1004001; preimage remains.
NATURAL MAP:   transient TiKV metadata error or autoid owner transition returning not leader.
DEDUP:         #69485 owns unconditional stale state; no exact fail-open repair root found.
INTEGRATE:     id3030003 high/major; 140 surfaces, 117 roots, 62 high, 124 confirmed.
```

Method improvement: add `SAFETY_REPAIR_ERROR_DOWNGRADED_TO_BEST_EFFORT`. A safety repair inherits
the severity of the unsafe state it closes until another owner proves closure. Enumerate repair
errors, trace each to the public terminal, reuse the original highest consumer, and pair one-error
RED with an exact no-error GREEN. Keep consequence separate from production frequency; this finding
is high rather than critical because its trigger conjunction is narrow. The asset graph imported
7 assets, 8 relations, 2 runs, and 1 validated target; the `br/pitr + S70` pack has no open gaps.

## Persisted evaluator context tick: partial TIMESTAMP index, EXECUTED (2026-07-25)

This cross-module tick started from a process-global partial-index evaluator and initially produced a
same-session GREEN. It became RED only after the input representation owner was added to the proof.

```text
P:             one process-global evaluator checks partial-index membership.
Q:             one schema predicate therefore defines one stable persisted row set.
F:             TIMESTAMP reaches it in the writer session's wall-clock representation.
CANONICAL KEY: one UTC instant and one unique key, written under -08:00 and +08:00.
RED:           full scan ids 1,2; partial index id 2; duplicate logical member; ADMIN 8223.
DML LIFT:      DELETE uses unique-index Point_Get, reports 1, leaves one predicate-true row.
GREEN:         same-time-zone second insert returns 1062 and ADMIN CHECK stays green.
DEDUP:         no exact TiDB issue or remote root after RED.
INTEGRATE:     id3210003 high; 146 surfaces, 123 roots, 68 high, 130 confirmed.
```

Method improvement: add `PERSISTED_EVALUATOR_CONTEXT_CLOSURE`. For every expression that controls
an index key, generated value, routing decision, or other durable derived state, inventory both the
evaluator context and the representation context of each operand. Hold the schema expression and
canonical value fixed, vary one representation owner, then compare source-of-truth rows with the
derived structure and its highest DML consumer. A fixed evaluator is not closure proof when its input
has already been shaped by mutable session state.

## Composable safety-gate tick: virtual generated index data loss, EXECUTED (2026-07-25)

The previous persisted-context selector was moved from partial indexes to generated values. The
decisive source shortcut was a rejected direct expression index and an accepted equivalent
composition.

```text
DIRECT GATE:   DATE(TIMESTAMP) expression index => ERROR 8200 unsafe function.
COMPOSITION:   virtual DATE generated column + ordinary secondary index => accepted.
P:             direct unsafe semantic graph is blocked under default config.
Q:             equivalent accepted compositions preserve the same safety invariant.
F:             non-GA check is enforced only for genType=typeIndex, not indexed typeColumn.
RED:           root set empty; index set {1}; returned row has predicate_holds=0.
DML LIFT:      default DELETE removes 1; root-owned twin removes 0 and preserves the row.
CONTROLS:      same timezone GREEN; DATETIME cross-timezone GREEN; direct syntax rejected.
INTEGRATE:     id3240003 high / critical consequence; 147 surfaces, 124 roots, 69 high,
               131 confirmed.
```

Method improvement: add `COMPOSABLE_SAFETY_GATE_CLOSURE`. Normalize a rejected operation into its
semantic graph, then rebuild that graph from lower-level accepted features. Revalidate the original
admission predicate at every composition boundary. The rejection reason selects the first matrix
dimension, while the derived structure's highest irreversible consumer selects the oracle. This
turns product guards into candidate generators and avoids broad syntax fuzzing.

## Persisted-ID cleanup-owner tick: BR success then GC data loss, EXECUTED / KNOWN ROOT (2026-07-25)

This tick deliberately moved beyond transaction code into BR, DDL, and TiKV GC. Target selection
used only current source and stored selectors; issue search stayed closed until the RED was complete.

```text
PERSIST:       interrupted BR has one durable completed-range checkpoint.
RETIRE:        DROP partial target schedules delete range for TableID 1648.
RESUME:        checkpoint preallocation recreates the target as TableID 1648.
PUBLICATION:   BR success; 59,788 KV skipped; 128,000 rows initially visible.
CLEANUP:       ordinary GC sends UnsafeDestroyRange for t1648.
RED:           fresh primary/index rowsets both become zero; ADMIN CHECK remains green.
GREEN:         discard checkpoint; fresh restore allocates 1669; old cleanup cannot intersect.
DEDUP:         exact root is TiDB #68709, already severity/critical.
INTEGRATE:     id3270003 high / known-duplicate / confirmed=0; do not increment new-root count.
```

Method improvement: add `PERSISTED_ID_CLEANUP_OWNER_CLOSURE`. For each persisted identity, enumerate
older lifecycle owners as well as future consumers. The terminal oracle belongs after the latest
delayed cleanup horizon, not merely after public success. This blind rediscovery is useful evidence
that the LOOP can reach known critical bugs without PR review seeds. The next round should transfer
the selector to non-BR checkpoint, lease, tombstone, temporary-engine, and orphan-cleanup owners
instead of enumerating more BR DROP/GC variants.

## Primitive-differential tick: DOUBLE-to-UNSIGNED wrong DELETE, EXECUTED (2026-07-25)

This tick broadened target selection beyond transaction code while retaining the same consequence
gate. Source comparison happened before value generation; issue search stayed closed until RED.

```text
LOCAL PRIMITIVE:  TiDB ConvertFloatToUint -> math.RoundToEven.
REMOTE PRIMITIVE: TiKV f64::to_uint -> f64::round.
BOUNDARY:         0.4 green, 0.5 red, 1.4 green, 1.5 green, 2.5 red.
RED ROWSET:       pushed ids 1,3; root id 3; id 1 projects predicate_holds=0.
DML LIFT:         pushed DELETE removes ids 1,3; root twin removes only id 3.
GREEN:            1.5/1.4/1.6 with cast=2 gives identical rowsets and deletes.
DEDUP:            no exact TiDB, TiKV, or asset-database root.
INTEGRATE:        id3330003 high / confirmed; default config, MDL ON, real TiKV.
```

Method improvement: add primitive-level comparison to `PUSHDOWN_ROWSET_SEMANTIC_CLOSURE`. Map each
evaluator to its final conversion primitive, classify the semantic difference, and derive the
smallest boundary partition from that class. Only a rowset RED proceeds to self-predicate and DML
oracles. This tick also reproduced the known MaxAllowedPacket transport gap as a default-config
wrong DELETE; post-RED dedup mapped it to TiKV #3736 and stored it as id3300003 known-duplicate.

## NULL-safe absence-proof tick: ADD FOREIGN KEY historical orphans, EXECUTED (2026-07-25)

The next source pass moved from evaluator code to a DDL publication validator. The proof obligation
was whether an empty generated-query result really proves that no historical violation exists.

```text
PREDICATE:      child tuple NOT IN referenced-key subquery.
BOUNDARY:       parent keys 1 versus 1,NULL; child keys fixed at 1,2.
RED PROOF:      implementation-shaped NOT IN=0; NULL-safe NOT EXISTS=1.
PUBLICATION:    ADD FOREIGN KEY succeeds; public constraint=1; historical orphan=1.
TERMINAL SPLIT: a new orphan is rejected with 1452 while the old orphan remains.
GREEN:          remove only parent NULL; the same ALTER returns 1452; constraint=0.
COUNTERFACTUAL: current-master focused test fails on original source and passes with NOT EXISTS.
DEDUP:          no exact internal root or upstream issue found.
INTEGRATE:      id3360003 high / confirmed; default config, MDL ON, real TiKV.
```

Method improvement: add `ABSENCE_PROOF_MUST_BE_NULL_SAFE`. Whenever an empty query authorizes
publication, cleanup, restore success, or destructive action, partition the predicate by SQL
three-valued inputs before generating a broad matrix. Compare the implementation predicate with a
NULL-safe oracle, then lift only a proven false negative to the terminal consumer.

Dedup now has two deliberately separate gates. Before execution, query internal root fingerprints
and lifecycle status only, which prevents compressed-context repetition. Search external issues,
pull requests, and history only after RED, preserving independent target selection.

## Hidden-input transfer tick: indexed generated-column physical corruption, EXECUTED / ROOT UPGRADE (2026-07-25)

This tick kept the module scope open and transferred the stored `default_week_format` getter from
plan cache and TiKV pushdown to a persistent secondary-index owner.

```text
DIRECT GATE:   expression index WEEK(d) => ERROR 8200 unsafe function.
COMPOSITION:   virtual g AS (WEEK(d)) + UNIQUE(g) => accepted.
WRITE 1:       mode 0 stores id1:key0 for date 2021-01-01.
WRITE 2:       mode 3 stores id2:key53; both source rows now project g=53.
PRE-DELETE:    source id1:g53,id2:g53; index id1:g0,id2:g53; ADMIN 8223.
DML LIFT:      DELETE g=0 affects 1; root-owned twin affects 0.
TERMINAL:      source only id2:g53; covering index only stale id1:g0; ADMIN 8223.
GREEN:         WEEK(d,3) rejects the second insert with 1062; ADMIN passes.
REPEAT:        self-contained real-TiKV scaffold reproduced 3/3.
DEDUP:         same admission owner and generic fix as id3240003; upgrade, no new ID.
```

Method improvement: store hidden inputs independently and transfer them through cache, remote,
persistence, recovery, and cleanup consumers. At each owner, raise the oracle to the strongest
irreversible operation. Deduplicate by owner-level repair closure: a stronger sibling upgrades the
root and its assets when one fix closes both witnesses.
