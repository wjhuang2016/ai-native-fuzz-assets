# AI-Native Proof-Obligation Catalog
> 2026-07-02. Purpose: turn "AI can reason about code" into a repeatable bug-discovery workflow. A target enters this catalog only when we can name the proof obligation, the fast path that trusts it, and a low-noise oracle.

## Why This Catalog Exists
The recent partial-index wrong-result bug shows that high-yield AI fuzzing should not start from random SQL syntax. It should start from code that proves a semantic claim:

```text
if checker(P) returns true
then optimizer/executor assumes Q
and chooses fast path F
```

AI's job is to extract `(P, Q, F)`, generate counterexample families where `P` looks true but `Q` is false, and attach a differential oracle that compares the fast path against a safe path.

Scoring dimensions:
- **Target density**: how likely this code is under-tested or recently complicated.
- **Proof precision**: whether we can write the exact semantic claim.
- **Oracle strength**: whether a stable user-table differential, or a system-table scalar/equivalence check with SQL-visible semantics, can catch wrong results.
- **Execution cost**: whether the check runs through SQL quickly.
- **Noise control**: whether we can avoid non-determinism and plan-only false positives. System/virtual tables are allowed only when the oracle re-checks the visible predicate semantics, not merely a plan shape.

## P0 Validated/Semantic-Gray: Binding History ExecuteInternal Consumes Pending `tx_read_ts` (S23 state-ingress)

- Status: **CURRENT RED + local-fix GREEN**, stored as
  `target.source.binding-history-executeinternal-txreadts.v1` with queue state `validated`;
  inserted into remote `found_bug` as id1260001 with `status=contract-needed,confirmed=0`.
  Product severity is still semantic-gray until the `SET TRANSACTION READ ONLY AS OF TIMESTAMP`
  contract is settled.
- Proof obligation:
  - `P_check`: `CREATE SESSION BINDING FROM HISTORY USING PLAN DIGEST` needs an internal
    statement-summary lookup before creating the binding.
  - `Q_claim`: that internal lookup is isolated from a pending user one-shot stale-read state.
  - `F_effect`: the binding path calls `ExecuteInternal` on the current session. `ExecuteInternal`
    toggles `InRestrictedSQL` but does not isolate or save/restore `SessionVars.TxnReadTS`, so the
    stale-read processor can consume the pending `tx_read_ts`.
- Evidence:
  - Root-boundary RED on `13282a8`: current-session restricted SQL consumed pending `TxnReadTS`
    before the user's intended read.
  - TSO-stable user-visible RED on `13282a8`: after `SET TRANSACTION READ ONLY AS OF TIMESTAMP
    @ts`, `CREATE SESSION BINDING FROM HISTORY USING PLAN DIGEST ...` consumed the pending state;
    the next `SELECT` expected stale rowset `[1]` but returned current rowset `[1,2]`.
    Key log: `before=467570589524557824 after=0 next_select_rows=[[1] [2]]`.
  - Local-fix GREEN on the same probe: a temporary `ExecuteInternal` patch saved/restored pending
    `TxnReadTS` and `SnapshotInfoschema`; key log:
    `before=467570643908952064 after=467570643908952064 next_select_rows=[[1]]`.
  - Earlier local attempted fix remains recorded as `INVALID`: it preserved a state flag but did
    not validate the same rowset oracle, which is why the TSO-stable counterpart was necessary.
- Source:
  - `docs/design/2021-09-22-stale-read.md`: `SET TRANSACTION` stale read is described as making
    the next interactive transaction or query use the staleness timestamp.
  - `pkg/planner/core/planbuilder.go`: binding-from-history fetches statement summary through
    internal SQL.
  - `pkg/session/session.go`: `ExecuteInternal` / `executeInternalImpl` toggles restricted mode
    but does not isolate `TxnReadTS`.
  - `pkg/sessiontxn/staleread/processor.go`: `evaluateFromStmtTSOrSysVariable` calls
    `TxnReadTS.UseTxnReadTS()`.
  - `pkg/sessionctx/variable/session.go`: `CleanupTxnReadTSIfUsed` clears `readTS` and
    `SnapshotInfoschema`.
- Artifacts:
  - Asset results: `/Users/bba/pc/ai-native-assets/source-state-ingress-binding-history-results.jsonl`
  - TSO-stable asset pair:
    `/Users/bba/pc/ai-native-assets/source-state-ingress-binding-history-tso-pair-results.jsonl`
  - Root-boundary RED log:
    `/Users/bba/pc/ai-native-assets/logs/source-state-ingress-current-restricted-sql-txreadts-red.log`
  - TSO-stable RED log:
    `/Users/bba/pc/ai-native-assets/logs/source-state-ingress-binding-history-txreadts-tso-red.log`
  - Local-fix GREEN log:
    `/Users/bba/pc/ai-native-assets/logs/source-state-ingress-binding-history-txreadts-tso-local-green.log`
- Method lesson: S23's reusable selector is not "NT-DML is buggy"; it is "one-shot session state
  can enter through sibling channels when internal SQL re-enters the generic session path." The
  fast move was to mine current-session internal SQL wrappers, then use a tiny oracle: AS OF
  control, internal-management statement, next user SELECT rowset.
- Next proofs:
  - Decide the product contract: does `SET TRANSACTION READ ONLY AS OF TIMESTAMP` apply to any next
    statement, or to the next user read/execute statement?
  - If the contract says the state should survive management/internal SQL, turn the local
    `ExecuteInternal` isolation experiment into an upstream-quality fix design and broaden tests to
    nearby internal SQL entry points.
  - Regardless of product escalation, promote `STATE_INGRESS_INTERNAL_SQL` into a source-target
    generator for `ExecuteInternal` / `ExecRestrictedSQL UseCurSession` plus one-shot session state.

## P0 Validated/Semantic-Gray: Index Advisor ExecuteInternal Consumes Pending `tx_read_ts` (S23 state-ingress)

- Status: **CURRENT RED + local-fix GREEN**, stored as
  `target.source.planner-index-advisor-executeinternal-state-ingress.v1` with queue state
  `validated`; inserted into remote `found_bug` as id1260002 with
  `status=contract-needed,confirmed=0`. Product severity has the same contract caveat as
  binding-history.
- Proof obligation:
  - `P_check`: `RECOMMEND INDEX RUN` is a user-visible wrapper that invokes index advisor helper SQL.
  - `Q_claim`: helper SQL is isolated from pending one-shot stale-read state belonging to the user's
    next data read.
  - `F_effect`: `RecommendIndexExec.Next` passes the current session into `indexadvisor.AdviseIndexes`;
    `indexadvisor.exec` casts that same session to `SQLExecutor`, calls `ExecuteInternal`, and drains
    the result set through the generic stale-read path.
- Evidence:
  - Current RED on `13282a8`: after `SET TRANSACTION READ ONLY AS OF TIMESTAMP @ts`,
    `RECOMMEND INDEX RUN FOR ...` consumed pending `TxnReadTS`; the next `SELECT` expected stale
    rowset `[1]` but returned current rowset `[1,2]`.
    Key log: `before=467570885856329728 after=0 next_select_rows=[[1] [2]]`.
  - Local-fix GREEN on the same probe: temporarily hide pending `TxnReadTS` and `SnapshotInfoschema`
    before internal SQL enters `ExecuteInternal`, then restore after the internal boundary.
    Key log: `before=467570913639661568 after=467570913639661568 next_select_rows=[[1]]`.
- Source:
  - `pkg/executor/recommend_index.go:75`: calls `indexadvisor.AdviseIndexes(ctx, e.Ctx(), ...)`.
  - `pkg/planner/indexadvisor/utils.go:533-549`: calls `ExecuteInternal` and drains the record set.
  - `pkg/session/session.go`: `ExecuteInternal` toggles restricted mode but does not isolate
    `TxnReadTS`.
  - `pkg/sessiontxn/staleread/processor.go`: pending `TxnReadTS` is consumed by `UseTxnReadTS`.
- Artifacts:
  - Asset results:
    `/Users/bba/pc/ai-native-assets/source-state-ingress-indexadvisor-results.jsonl`
  - RED log:
    `/Users/bba/pc/ai-native-assets/logs/source-state-ingress-indexadvisor-txreadts-red.log`
  - Local-fix GREEN log:
    `/Users/bba/pc/ai-native-assets/logs/source-state-ingress-indexadvisor-txreadts-local-green.log`
- Live testbed lift (2026-07-12, no failpoints):
  - Testbed `8220955`, explicit endpoint `127.0.0.1:14000`, commit
    `13282a8bd06bd33324a4dbfd3c1c03685f3cd9aa`.
  - Direct `SET TRANSACTION ... AS OF` control returned `[1,10]`.
  - The same stale setup followed by a successful `RECOMMEND INDEX RUN` made the next
    user `SELECT` return `[1,10],[2,20]`.
  - The no-pending-state wrapper control returned `[1,10],[2,20]`, ruling out a general
    `RECOMMEND INDEX RUN` failure.
  - Raw record:
    `assets/store/logs/txn-index-advisor-txreadts-testbed8220955-20260712.log`;
    structured run:
    `assets/store/txn-index-advisor-txreadts-testbed-results.jsonl`.
- Method lesson:
  - This is the second positive sibling for `STATE_INGRESS_INTERNAL_SQL`, so the selector is no
    longer just a post-hoc explanation for binding-history.
  - A post-hoc restore after `ExecuteInternal` returns can be too late, because result-set drain/close
    may consume and clean up the pending state after the call returns. The stronger fix-probe model is
    ingress isolation: internal SQL should not see user pending one-shot state unless intentionally
    opted in.
- Product-quality gate:
  - The live RED proves a user-visible wrong-snapshot result, but the asset remains
    `contract-needed` until the phrase "next interactive transaction or query statement" is
    resolved for helper SQL executed inside a user-facing management statement. This is a
    severity gate, not a weakness in the live oracle.

## P0 Candidate Queue: Dynamic State-Ingress Source Targets

- Status: **candidate batch imported**, not bug claims. Stored in
  `/Users/bba/pc/ai-native-assets/source-targets-state-ingress-dynamic-20260710.jsonl`.
  Current queue state after the latest target-analysis/validation pass is `validated=13`,
  `retired=6`, `blocked=1`, and `needs_target_analysis=5`.
- Generator lesson:
  - `session-ownership-proof` is now part of the source-target generator, not only a manual review
    note. Known validated/retired paths are skipped; DDL worker, sys session, pooled session, new
    session, nil restricted SQL without `UseCurSession`, and internal helper shapes are screened out
    or downgraded.
  - Local `ExecOptionUseCurSession` is a strong positive signal. File-level sys/new-session markers
    are retained as debt instead of killing the whole file, because one Go file can contain multiple
    unrelated session paths.
- Contract-blocked boundary target:
  - `target.source.dynamic-state-ingress.pkg-executor-show.v1`
  - Source anchor: `pkg/executor/show.go`, especially `ShowExec.fetchShowTableStatus`, which calls
    `ExecRestrictedSQL` with `ExecOptionWithSnapshot(snapshot)` and `ExecOptionUseCurSession`.
  - Live evidence on testbed 8220955:
    `/Users/bba/pc/ai-native-assets/logs/source-state-ingress-show-table-status-testbed8220955.log`.
    `AS OF` control saw row `1`, current control saw `1,2`,
    `SET TRANSACTION; SHOW TABLE STATUS; SELECT` saw `1,2`, and direct
    `SET TRANSACTION; SELECT` saw `1`.
  - Decision: blocked as `CONTRACT_NEEDED(show-is-next-query-statement)`, because `SHOW TABLE STATUS`
    is itself a user-visible SHOW/query statement. A RED/GREEN fix probe would encode a product
    contract decision.
- Retired screens from the same dynamic queue:
  - `target.source.dynamic-state-ingress.pkg-infoschema-infoschema.v1`
    -> `INVALID(sys-executor-factory-proof)`.
    Asset: `/Users/bba/pc/ai-native-assets/source-targets-state-ingress-infoschema-retire-analysis.jsonl`.
  - `target.source.dynamic-state-ingress.pkg-executor-brie.v1`
    -> `INVALID(new-glue-session-proof)`.
    Asset: `/Users/bba/pc/ai-native-assets/source-targets-state-ingress-brie-retire-analysis.jsonl`.
- Current next target:
  - `target.source.dynamic-state-ingress.pkg-executor-grant.v1`
  - Same gate: prove user-visible wrapper, session ownership, and exact state contract before RED.

## P0 Validated: Fast ADMIN CHECK TABLE Does Not Restore Invisible-Index Optimizer State

- Status: **CURRENT RED**, SQL-only reproduction on testbed 8220955; asset-store validated as
  `target.source.dynamic-state-ingress.pkg-executor-check-table-index.v1`; inserted into remote
  `found_bug` as id1260003, confirmed.
- Proof obligation:
  - `P_check`: Fast `ADMIN CHECK TABLE` temporarily sets
    `e.Ctx().GetSessionVars().OptimizerUseInvisibleIndexes = true`.
  - `Q_claim`: The helper may force invisible-index checking internally, but it must restore the
    user's previous session optimizer behavior after the statement.
  - `F_fault`: `pkg/executor/check_table_index.go:296-298` defers a hard reset to `false` instead of
    saving/restoring the previous value.
- User-visible oracle:
  - Create a table with `KEY idx_v(v) INVISIBLE`.
  - Set `tidb_enable_fast_table_check=ON` and `tidb_opt_use_invisible_indexes=ON`.
  - Before `ADMIN CHECK TABLE`, `EXPLAIN FORMAT='brief' SELECT * FROM t WHERE v=20` uses
    `IndexReader/IndexRangeScan`.
  - After `ADMIN CHECK TABLE`, `@@tidb_opt_use_invisible_indexes` still displays `1`, but the same
    query uses `TableReader/TableFullScan`.
  - With `tidb_enable_fast_table_check=OFF`, the before/after plan remains
    `IndexReader/IndexRangeScan`.
- Evidence:
  - Asset: `/Users/bba/pc/ai-native-assets/source-state-ingress-check-table-index-results.jsonl`
  - RED log:
    `/Users/bba/pc/ai-native-assets/logs/source-state-ingress-check-table-index-testbed8220955.log`
  - Fast-off control:
    `/Users/bba/pc/ai-native-assets/logs/source-state-ingress-check-table-index-testbed8220955-fast-off-control.log`
- Method lesson:
  - This was discovered from a `STATE_INGRESS_INTERNAL_SQL` source target, but the real validated
    selector is `USER_SESSION_STATE_RESTORE`.
  - Reading `@@tidb_opt_use_invisible_indexes` alone was too weak; the behavior oracle caught a
    display/behavior split.
  - Fix shape: save the old `OptimizerUseInvisibleIndexes` value and restore it, matching the
    backup/restore discipline already used for sys session variables in the same file.

## P0 Pending: S3 Multipart Failed Part Terminal Completion (S-ERR refill)

- Status: **CURRENT RED**, asset-store validated with local-fix GREEN; inserted into remote
  `found_bug` as id1260005 with `status=current-red-green,confirmed=1`.
- Proof obligation:
  - `P_check`: S3 multipart writer has an active upload; part 1 succeeds; part 2 `UploadPart`
    returns an injected root error.
  - `Q_claim`: once any part upload fails, terminal `Close` must not make a prefix-only object
    visible via `CompleteMultipartUpload`; it should abort when possible and preserve the root
    error.
  - `F_effect`: current `multipartWriter.Write` returns the `UploadPart` error but does not store
    failed state; `multipartWriter.Close` always completes accumulated `completeParts`.
- Evidence:
  - Current RED on `13282a8`: `writeErr=ai-native mock upload part failed`,
    `closeErr=<nil>`, `completeCalls=1`, `completedParts=1`.
  - Local GREEN: failed state stored; `Close` calls `AbortMultipartUpload`, returns
    `ai-native mock upload part failed`, and `abortCalls=1` for both direct `MultipartWriter` and
    user-facing `Storage.Create`.
- Source:
  - `pkg/objstore/s3store/client.go`: multipart writer state machine.
  - `pkg/objstore/s3like/store.go`: `Storage.Create(..., Concurrency <= 1)` uses
    `MultipartWriter` behind `objectio.NewBufferedWriter`.
  - `pkg/objstore/objectio/writer.go`: buffered Close delegates to the underlying writer Close.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-s3-multipart-uploadpart-close-draft.md`
  - Asset results: `/Users/bba/pc/ai-native-assets/issue48164-refill-s3-multipart-results.jsonl`
  - RED log: `/Users/bba/pc/ai-native-assets/logs/issue48164-refill-current-s3-storage-part-fail-close-red.log`
  - GREEN log: `/Users/bba/pc/ai-native-assets/logs/issue48164-refill-current-s3-final-narrow-green.log`
- Method lesson: historical issue48164's broad error-identity oracle found a different current bug
  only after adding a terminal state-action observer. For storage writers, final error identity is
  insufficient; the oracle must also observe `Complete` vs `Abort`.
- Stop rule: do not claim OSS/KS3 blast radius until the same narrowed obligation is executed
  there. Source shape is similar, but evidence is scoped to S3.

## P0 Pending: DDL Owner Epoch Token Collision (S-ENV refill)

- Status: **CURRENT RED**, asset-store validated with local-fix GREEN; inserted into remote
  `found_bug` as id1260006 with `status=current-red-green,confirmed=0`; live cluster lift still
  pending.
- Proof obligation:
  - `P_check`: `runReorgJob` records `beOwnerTS` before launching a reorg function and later
    accepts the `doneCh` result when `res.ownerTS == current ownerTS`.
  - `Q_claim`: equality of ownerTS proves the result belongs to the current DDL owner epoch on the
    same TiDB instance.
  - `F_effect`: current `OnBecomeOwner` allocated ownerTS with `time.Now().Unix()`. If the same TiDB
    retires and becomes owner again within one wall-clock second, the previous and current owner
    epochs can share the same token, so a stale reorg result can be accepted instead of taking the
    owner-handoff retry path.
- Evidence:
  - Current RED on `13282a8`: `previousOwnerTS=1000`, `curOwnerTS=1000`; the test failed with
    `Should not be: 1000`.
  - Local GREEN: `renewOwnerTS(wallTS)` uses `max(wallTS, previous+1)`; same-second owner renewal
    produces distinct tokens and `TestAINativeOwnerEpochRenewalRejectsSameSecondStaleResult` PASS.
- Source:
  - `pkg/ddl/job_scheduler.go`: `OnBecomeOwner` owner token allocation.
  - `pkg/ddl/reorg.go`: `runReorgJob` compares `res.ownerTS` with current ownerTS.
  - `pkg/ddl/ddl.go`: `reorgContexts.beOwnerTS` stores the token.
- Artifacts:
  - Asset results: `/Users/bba/pc/ai-native-assets/issue51846-refill-owner-epoch-results.jsonl`
  - RED log: `/Users/bba/pc/ai-native-assets/logs/issue51846-refill-current-owner-epoch-collision-red.log`
  - GREEN log: `/Users/bba/pc/ai-native-assets/logs/issue51846-refill-local-owner-epoch-renewal-green.log`
- Method lesson: when async result filters use equality on a lifecycle token, the token minting
  code must prove uniqueness across the exact lifecycle boundary. A broad topology oracle becomes
  much sharper when reduced to an identity-token proof before cluster chaos.
- Stop rule: do not overclaim all `oracle.allowed-state-after-topology-fault.v1` outcomes. This
  run proves one narrowed owner-epoch identity boundary. The next useful step is either filing this
  candidate or lifting it to a testbed owner retire/re-become schedule while a reorg worker is
  active.

## P0 Confirmed: Fast Reorg ADD INDEX Treats Transient PD TSO Retry Timeout as Fatal (id1290001, S24)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id1290001 with
  `status=confirmed,confirmed=1` (current remote state after insert:
  `COUNT(*)=82`, `COUNT(DISTINCT root_cause_id)=59`).
- Proof obligation:
  - `P_check`: fast-reorg `ADD INDEX` uses PD TSO / checkpoint recovery during active write
    reorganization.
  - `Q_claim`: transient PD TSO stream creation failures stay inside the DDL retryable path and do
    not terminally roll back the job after a single hit once PD is healthy again.
  - `D_dim`: the fault enters as a PD normalized error with foreign RFC class `PD`, and the
    fast-reorg ingest path calls `pdCli.GetTS` from checkpoint/watermark code during the active
    reorg window.
  - `F_effect`: `PD:client:ErrClientCreateTSOStream(... retry timeout)` enters
    `isRetryableError` through the `*terror.Error` branch; `terror.ToSQLError` uses the unknown
    RFC-class fallback, so the error misses `ReorgRetryableErrCodes` / retryable classification
    and is treated as fatal.
- Evidence:
  - Live recheck on testbed 8220955 / current
    `v9.0.0-beta.2.pre-1895-g5c9198e948`:
    - RED job 1192: 150w rows, fast_reorg=ON, dist_task=OFF,
      `err.message='create TSO stream failed, retry timeout'`,
      `err.rfccode='PD:client:ErrClientCreateTSOStream'`, `err_count=1`, `state=rollback done`.
    - RED job 1204: 100w rows, same pattern, same `err_count=1`, same `rollback done`.
    - GREEN control: sibling `fast_reorg=OFF` job 1196 finished `synced` on the `txn` path.
  - Local classifier probe:
    `/Users/bba/pc/tidb/pkg/ddl/ai_native_retry_probe_test.go` constructs the raw PD error and
    current code logs `Unknown error class [class=PD]`; `isRetryableError(raw,true)=false` for
    raw/traced/stacked forms.
- Source:
  - `pkg/ddl/index.go`: `runIngestReorgJob` / `isRetryableJobError` route the decision through
    `isRetryableError`.
  - `pkg/util/dbterror/ddl_terror.go`: retryable code/message lists do not include this PD error
    family.
  - `pkg/parser/terror/terror.go`: `type Error = errors.Error`; unknown RFC class fallback in
    `ToSQLError`.
  - `pkg/ddl/ingest/checkpoint.go`: `afterImport` and `resumeOrInitCheckpoint` call
    `pdCli.GetTS` directly.
  - `pkg/ingestor/ingestctrl/checksum.go`: contrast case with an explicit retry loop for PD
    leader-change style failures.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-fast-reorg-pd-tso-retry-timeout-draft.md`
  - Local probe: `/Users/bba/pc/tidb/pkg/ddl/ai_native_retry_probe_test.go`
  - Live matrix and notes: `/Users/bba/pc/ai-native-fuzz-handoff.md`
- Method lesson: this was not "more chaos found more bugs." The efficient move was to fix the
  active-window observer first, then ask a narrower question: did the transient fault ever enter a
  retry budget? `err_count=1` plus `rollback done` plus a sibling GREEN control compressed a broad
  topology-fault schedule into a retry-classifier bug. This promoted selector `S24` and oracle
  `O31`.
- Stop rule: do not enumerate more bounce counts or PD error strings. Reopen only for another
  foreign error-domain retry-classification boundary, a stronger silent consequence, or fix
  validation.

## P0 Confirmed: Ingest-mode DDL Fatalizes Retryable Ingest/TiKV Leader-Change Errors at the DDL Classifier Bridge (id1320001, S24)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id1320001 with
  `status=confirmed,confirmed=1` (current remote state after insert:
  `COUNT(*)=83`, `COUNT(DISTINCT root_cause_id)=60`).
- Proof obligation:
  - `P_check`: ingest-mode `ADD INDEX` / `ADD PRIMARY KEY` can surface retryable
    `ErrNoLeader` / `ErrKVNotLeader` / `ErrKVRegionNotFound`-family errors during active
    write reorganization.
  - `Q_claim`: if those foreign leader-change errors are retryable inside ingest, they must stay
    on a retryable recovery path when they cross into the outer DDL retry gate; they must not
    terminally roll back the whole online DDL after one hit.
  - `D_dim`: the same error family can appear at two semantic altitudes: below the bridge inside
    `ingestctrl`, and at the bridge between `runReorgJobAndHandleErr` and
    `isRetryableJobError/isRetryableError`.
  - `F_effect`: below the bridge, ingest retry/rescan absorbs the family; at the bridge, foreign
    `Ingest` / `KV` errors fall out of the DDL retryable set after unknown-class normalization and
    are fatalized into rollback.
- Evidence:
  - Live lower-bridge GREEN on testbed 8220955 / current
    `v9.0.0-beta.2.pre-1895-g5c9198e948` using a commit-matched failpoint owner (`fp-tidb`):
    - `pkg/ingestor/ingestctrl/FailIngestMeta=1*return("notleader")` hit and logged
      `[Ingest:NotLeader]`, but `ADD INDEX` job 1548 finished `synced`.
    - `pkg/ingestor/ingestctrl/NoLeader=1*return(true)` hit and logged `[KV:ErrNoLeader]`, but
      `ADD INDEX` job 1551 finished `synced`.
  - Live bridge-proximal RED on the same owner lane using
    `pkg/ddl/mockDDLIngestClassifierErr` injected in `runIngestReorgJob` after
    `runReorgJobAndHandleErr`:
    - `ADD INDEX + noleader` -> job 1557 `rollback done`
    - `ADD PRIMARY KEY + noleader` -> job 1563 `rollback done`
    - `ADD INDEX + notleader` -> job 1584 `rollback done`
    - `ADD PRIMARY KEY + regionnotfound` -> job 1587 `rollback done`
    - Same-environment controls `t5/t9/tpk2/tpk6` all `synced`
  - Owner logs on RED cells consistently showed `Unknown error class [class=KV/Ingest]` and
    `run reorg job failed, convert job to rollback`.
- Source:
  - `pkg/lightning/common/retry.go`: retryable ingest/TiKV error family.
  - `pkg/ingestor/ingestctrl/job_worker.go`: ingest worker retry/rescan handling.
  - `pkg/ddl/index.go`: `runIngestReorgJob`, `isRetryableJobError`, `isRetryableError`.
  - `pkg/parser/terror/terror.go`: unknown RFC/error-class normalization behavior used by the
    outer DDL path.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-ingest-retryable-fault-rollback-draft.md`
  - Live logs:
    `/Users/bba/pc/ai-native-assets/logs/ingest-live-bridge-altitude-matrix.log`,
    `/Users/bba/pc/ai-native-assets/logs/ingest-live-bridge-altitude-matrix-extended.log`
  - Live owner worktree: `/private/tmp/fp-build-5c9198`
- Method lesson: the key was not "more chaos"; it was **bridge altitude**. The same retryable
  family staying GREEN below the bridge and RED at the bridge turns a vague transient-fault lane
  into a precise classifier-bridge bug. This strengthened selector `S24` and promoted oracle
  `O31` to a trustworthy narrow shape.
- Stop rule: do not enumerate more tables or more DDL shapes from the same bridge/family matrix.
  Reopen only for a true production-fault live lift, another foreign retry-family crossing the same
  bridge, a stronger silent consequence, or fix validation.

## P0 Confirmed: MODIFY COLUMN Fatalizes One-Shot Connection/Grpc Transient Errors at the Reorg Retry Bridge (id1350001, S24)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id1350001 with
  `status=confirmed,confirmed=1` (current remote state after insert:
  `COUNT(*)=84`, `COUNT(DISTINCT root_cause_id)=61`).
- Proof obligation:
  - `P_check`: online `ALTER TABLE ... MODIFY/CHANGE COLUMN` shares the common reorg/backfill
    worker boundary with index-family DDL during active write reorganization.
  - `Q_claim`: a one-shot transient foreign connection/grpc error that sibling `ADD INDEX`
    recovers from should stay on a retryable recovery path for `MODIFY COLUMN`; it must not force
    terminal rollback after one hit if the same bridge/fault schedule is otherwise valid.
  - `D_dim`: the transient family crosses the bridge between `runReorgJob` return and the
    per-DDL retry gate; `context deadline exceeded` is in the shared retryable set, while
    `driver: bad connection`, `connection reset by peer`, and `grpc unavailable` depend on how
    each sibling preserves unknown-foreign retryability.
  - `F_effect`: `pkg/ddl/modify_column.go` uses `isRetryableError(err, false)` and therefore
    fatalizes foreign transient errors that `pkg/ddl/index.go` keeps retryable with
    `isRetryableError(err, true)`.
- Evidence:
  - Live bridge-level matrix on testbed 8220955 / current
    `v9.0.0-beta.2.pre-1895-g5c9198e948` using a commit-matched failpoint owner lane:
    - shared GREEN control: `context_deadline_exceeded`
      - `ADD INDEX` job 1755 -> `synced`
      - `MODIFY COLUMN` job 1758 -> `synced`
    - RED/GREEN split: `driver_bad_conn`
      - `ADD INDEX` job 1761 -> `synced`
      - `MODIFY COLUMN` job 1764 -> `rollback done`
    - RED/GREEN split: `net_conn_reset`
      - `ADD INDEX` job 1767 -> `synced`
      - `MODIFY COLUMN` job 1770 -> `rollback done`
    - earlier bridge-proximal `grpc unavailable`
      - `ADD INDEX` job 1723 -> `synced`
      - `MODIFY COLUMN` job 1726 -> `rollback done`
  - Local family extension:
    - `pkg/ddl/ai_native_reorg_grpc_probe_test.go` confirmed the same root across
      `mysql_invalid_conn`, `driver_bad_conn`, `net_conn_reset`, `net_broken_pipe`, and
      `net_conn_refused`.
    - `pkg/ddl/ai_native_retry_probe_test.go` showed the risky family as
      `raw=true, ddl_synth=false` for transient connection/grpc errors.
- Source:
  - `pkg/ddl/modify_column.go`: `isRetryableModifyColumnReorgJobError` routes through
    `isRetryableError(err, false)` and converts the job to rollback on failure.
  - `pkg/ddl/index.go`: sibling `isRetryableJobError` uses `isRetryableError(err, true)`.
  - `pkg/ddl/job_worker.go`: outer retry sleep/logging happens after the updated job state is
    persisted, so a retry log is weaker evidence than the terminal job state.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-modify-column-transient-rollback-draft.md`
  - Local probe logs:
    `/Users/bba/pc/ai-native-assets/logs/modify-column-transient-family-local.log`,
    `/Users/bba/pc/ai-native-assets/logs/modify-column-transient-grpc-siblings-local.log`
  - Live summary and matrix notes: `/Users/bba/pc/ai-native-fuzz-handoff.md`
- Method lesson: this hit promoted S24 beyond index-family DDL. The efficient move was not more
  coarse network chaos; it was a bridge-level sibling matrix with one strong shared GREEN control
  and one new owner family. It also sharpened the gate: a synthetic all-RED matrix is not enough
  if the fault family is not proven natural at that altitude.
- Stop rule: do not enumerate more connection strings or keep widening the same sibling family.
  Reopen only for another external transient-fault family, a different retry bridge, a stronger
  silent consequence, or fix validation.

## P0 Confirmed: Distributed ADD INDEX Leaves DXF Task Running on Persistent SetTSBeforeImportEngine Engine-NotFound Errors (id1350002, S25)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id1350002 with
  `status=confirmed,confirmed=1` (current remote state after insert:
  `COUNT(*)=85`, `COUNT(DISTINCT root_cause_id)=62`).
- Proof obligation:
  - `P_check`: a distributed `ADD INDEX` backfill subtask that fails during import/setup must be
    classified correctly at the distributed reorg retry bridge before the framework decides
    whether to keep the subtask `running`.
  - `Q_claim`: a source-native `SetTSBeforeImportEngine` `engine-not-found` error is safe to
    admit into the idempotent rerun path.
  - `D_dim`: same owner, same point, same DDL shape, same source-native error family; vary only
    the fault schedule (baseline vs one-shot vs persistent vs fault-removed).
  - `F_effect`: `backfillDistExecutor.IsRetryableError` falls through to
    `isRetryableError(err, true)`, so a fundamental runtime error is treated as retryable; the
    DXF task executor keeps the subtask `running` and reruns it forever.
- Evidence:
  - Baseline GREEN on testbed `8220955`: distributed ingest `ADD INDEX` job `2313` finished
    `synced` with comment `ingest, DXF, thread=1, batch_size=32, max_node_count=3`.
  - Same-altitude one-shot GREEN control: with
    `mockAINativeSetTSBeforeImportEngineErr=1*return("engine_not_found")`, job `2322` still
    reached `synced`.
  - Persistent RED: with
    `mockAINativeSetTSBeforeImportEngineErr=return("engine_not_found")`, job `2319` stayed
    `running` in `write reorganization`; `mysql.tidb_global_task` showed task `270003` still
    `running` at `step=1`.
  - Strong owner-log contradiction on the RED path:
    - import path: `set TS failed` / `engine ... not found in SetTSBeforeImportEngine`
    - Lightning retry layer: `meet un-retryable error`
    - DXF executor: `meet retryable error` + `subtask in running state and is idempotent`
  - Recovery proof: after deleting the failpoint, the same held RED job `2319` immediately
    finished `synced`. This is a retry-loop wedge, not a dead cluster.
- Source:
  - `pkg/ingestor/ingestctrl/local.go`: source-native `SetTSBeforeImportEngine` path
  - `pkg/ddl/backfilling_import_cloud.go`: plain runtime fundamental error shapes in the import
    pipeline
  - `pkg/ddl/backfilling_dist_executor.go`: distributed backfill retry classifier
  - `pkg/dxf/framework/taskexecutor/task_executor.go`: retryable subtask stays `running` and is
    rerun when idempotent
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-dist-addindex-setts-engine-notfound-hang-draft.md`
  - Live summary and matrix notes: `/Users/bba/pc/ai-native-fuzz-handoff.md`
  - Commit-matched failpoint owner worktree: `/private/tmp/fp-build-5c9198`

## P0 Confirmed: Distributed ADD INDEX Has No Terminal Retry Budget for Persistent SetTSBeforeImportEngine Context-Deadline-Exceeded Errors (id1410001, S26)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id1410001 with
  `status=confirmed,confirmed=1` (current remote state after insert:
  `COUNT(*)=86`, `COUNT(DISTINCT root_cause_id)=63`).
- Proof obligation:
  - `P_check`: when a distributed `ADD INDEX` backfill subtask returns a retryable runtime error,
    the DXF task executor decides whether to keep the subtask `running` or escalate it into a
    terminal failed/reverting state.
  - `Q_claim`: if the runtime error is retryable, it is safe to keep rerunning the subtask until
    success; no outer retry budget or escalation is required.
  - `D_dim`: same owner, same point, same DDL shape, same source-native timeout family; vary only
    the fault schedule (one-shot vs persistent vs fault-removed), then contrast the outer DXF
    behavior with the lower ingest layer's explicit retry budget.
  - `F_effect`: `BaseTaskExecutor.markSubTaskCanceledOrFailed` leaves retryable subtasks
    `running`, and the DXF path has no per-subtask/per-DDL terminal retry budget. A persistent
    timeout therefore becomes an unbounded rerun loop rather than a bounded retry with failure or
    revert.
- Evidence:
  - Same-altitude one-shot GREEN control on testbed `8220955`: with
    `mockAINativeSetTSBeforeImportEngineErr=1*return("context_deadline_exceeded")`, job `4002`
    reached `synced` and task `300007` finished `succeed`.
  - Persistent RED: with
    `mockAINativeSetTSBeforeImportEngineErr=return("context_deadline_exceeded")`, job `4007`
    stayed `running` in `write reorganization` for >90s and task `300008` stayed `running` at
    `step=1`; the client `ALTER TABLE ... ADD INDEX` did not return.
  - Strong retry-loop proof on the RED path: owner log for `task-id=300008` recorded **247**
    repeated `meet retryable error` lines between `2026-07-11 10:16:11` and `10:17:39 UTC`,
    with the same cycle repeating: `set ingest ts before import -> import error "context deadline
    exceeded" -> run subtask failed -> meet retryable error`.
  - Recovery proof: after deleting the failpoint, task `300008` turned `succeed`, and the same
    held RED job `4007` finished `synced` about 2 seconds later. This is an unbounded retry wedge,
    not a dead cluster.
  - Bounded-retry contrast from source: the lower ingest layer already exposes
    `MaxWriteAndIngestRetryTimes = 30` and returns a terminal error once the retry budget is
    exhausted, while `pkg/dxf/framework/taskexecutor/task_executor.go` only fails non-retryable
    errors and keeps retryable subtasks `running`.
- Source:
  - `pkg/ingestor/ingestctrl/local.go`: source-native `SetTSBeforeImportEngine` timeout
  - `pkg/ingestor/ingestctrl/local.go` / `pkg/ingestor/ingestctrl/region_job.go`: bounded retry
    budget in the lower ingest layer
  - `pkg/ddl/ingest/backend.go`: import path logging around `set ingest ts before import`
  - `pkg/dxf/framework/taskexecutor/task_executor.go`: retryable subtask stays `running`
- Artifacts:
  - Live summary and matrix notes: `/Users/bba/pc/ai-native-fuzz-handoff.md`
  - Commit-matched failpoint owner worktree: `/private/tmp/fp-build-5c9198`
- Method lesson: this is the cleanest current example of "same point, same owner, one-shot GREEN
  and persistent RED." The efficient move was not broader chaos; it was to turn a source-native
  proof obligation into a tiny fault-schedule matrix and let a strong liveness oracle settle the
  shape.
- Stop rule: do not enumerate more import error strings from the same owner. Reopen only for
  another distributed reorg owner, another source-native runtime fundamental at the same retry
  bridge, a stronger user-visible consequence, or fix validation.

## P0 Confirmed: MDL-off ADD INDEX Breaks delayForAsyncCommit Safe-Window Protection for Concurrent Async Commit (id1440001, S27)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id1440001 with
  `status=confirmed,confirmed=1` (current remote state after insert:
  `COUNT(*)=87`, `COUNT(DISTINCT root_cause_id)=64`).
- Proof obligation:
  - `P_check`: when metadata lock is disabled, `delayForAsyncCommit` sleeps
    `SafeWindow + AllowedClockDrift` before a DDL job finishes.
  - `Q_claim`: that wait is enough to let async commit / 1PC transactions commit safely with the
    old schema even if the DDL changed schema state in the meantime.
  - `D_dim`: same DDL kind, same transaction shell, same DML shape, same natural same-start
    schedule; vary only the coordination sibling (`metadata_lock=OFF` vs `ON`), and keep a
    strong post-success semantic oracle on the green arm.
  - `F_effect`: if the claim is false, the old-schema transaction is rejected by schema
    validation (`ErrInfoSchemaChanged`) instead of committing and preserving the expected amended
    keys/rowset.
- Evidence:
  - Current-master live RED on testbed `8220955`, front `127.0.0.1:14001`: running
    `go run ./add_index_async_commit_cross_schema_probe.go -dsn root@tcp(127.0.0.1:14001)/ -pause-prewrite=false -hold=0ms -ddl-start-gap=0ms -ddl-kind add-index -txn-kind async-commit -txn-shape basic`
    with `metadata_lock=OFF` recorded `AFTER_HOLD ddl_status=running` and then
    `TXN_RESULT err=Error 8028 (HY000): Information schema is changed`.
  - Same-shape GREEN sibling: the same command with `-metadata-lock=true` succeeded immediately;
    the probe then passed `ADMIN CHECK TABLE`, index/table differential, and exact-row oracle for
    final rows `1:10` and `2:2`.
  - Historical same-day live matrix on the same testbed sharpened the natural red band for the
    plain `ADD INDEX + async commit + basic(insert -> update)` lane:
    `live-testbed-add-index-async-basic-gap-matrix-mdloff-20260711.log` recorded **10/10 RED**
    across `ddl_start_gap=0/1/2/5/10ms`, while the MDL-on control matrix was **6/6 GREEN**.
  - Source/test contract anchors are explicit:
    - `pkg/ddl/ddl.go:1300-1323` says `delayForAsyncCommit` "provides a safe window for async
      commit and 1PC to commit with an old schema".
    - skipped realtikv tests
      `tests/realtikvtest/pessimistictest/pessimistic_test.go:2150-2240`
      (`TestAsyncCommitWithSchemaChange`, `Test1PCWithSchemaChange`) expect the same family to
      succeed and preserve the amended keys.
  - Root-cause clue from current source: `pkg/kv/option.go` still defines `SchemaAmender`, but
    current transaction setup visibly wires only `SchemaLeaseChecker`, `InfoSchema`,
    `EnableAsyncCommit`, and `Enable1PC`
    (`pkg/sessiontxn/isolation/base.go:511-516`, `pkg/store/driver/txn/txn_driver.go:229-263`);
    the tree search does not show any `SetOption(kv.SchemaAmender, ...)` hookup.
- Source:
  - `pkg/ddl/ddl.go`: MDL-off safe-window contract
  - `tests/realtikvtest/pessimistictest/pessimistic_test.go`: skipped expected-success contract
  - `pkg/sessiontxn/isolation/base.go`, `pkg/store/driver/txn/txn_driver.go`, `pkg/kv/option.go`:
    current runtime wiring / missing schema-amender clue
- Artifacts:
  - Probe: `/Users/bba/pc/ai-native-probes/add_index_async_commit_cross_schema_probe.go`
  - Live rerun logs:
    `/Users/bba/pc/ai-native-assets/logs/live-testbed-add-index-async-basic-mdloff-rerun-20260711-1828.log`,
    `/Users/bba/pc/ai-native-assets/logs/live-testbed-add-index-async-basic-mdlon-rerun-20260711-1828.log`
  - Earlier natural-red/green matrices:
    `/Users/bba/pc/ai-native-assets/logs/live-testbed-add-index-async-basic-gap-matrix-mdloff-20260711.log`,
    `/Users/bba/pc/ai-native-assets/logs/live-testbed-add-index-async-basic-gap-matrix-mdlon-20260711.log`
- Method lesson: explicit runtime-safety comments and skipped expected-success tests are
  first-class proof obligations. Once a broad natural-red family appears, the efficient move is
  to compress it into a minimal OFF/ON sibling matrix with a strong post-success oracle, not to
  add more injected chaos.
- Stop rule: do not enumerate every DDL kind or every DML mix once the contract-level MDL
  OFF/ON split is established. Reopen only for another DDL path that relies on the same safe
  window, a stronger consequence such as silent mis-amend, or fix validation.

## P0 Pending: BR Storewatch Same-Second Reboot Notification (SOURCE_TARGETS)

- Status: **CURRENT RED**, asset-store validated with local-fix GREEN; inserted into remote
  `found_bug` as id1260007 with `status=current-red-green,confirmed=0`; live TiKV restart frequency
  lift still pending.
- Proof obligation:
  - `P_check`: `storewatch.updateStore` observes the same store ID and compares previous/current
    `Store.StartTimestamp`.
  - `Q_claim`: unchanged `StartTimestamp` proves no reboot/recovery callback is needed.
  - `F_effect`: `StartTimestamp` is a coarse lifecycle token. If a store is observed
    `Up(T) -> Offline(T) -> Up(T)`, current code calls `OnDisconnect` for the Offline state but
    skips `OnReboot` when it returns Up because `T` did not change.
- Evidence:
  - Current RED on `13282a8`: `TestAINativeOnRebootWhenStoreRestartsWithinSameSecondRED` failed
    with `Should be true`; `OnReboot` was not called for `Up(1000), Offline(1000), Up(1000)`.
  - Local GREEN: `updateStore` preserves StartTimestamp-change detection and additionally treats
    `non-Up -> Up` as `OnReboot`; full `go test ./br/pkg/utils/storewatch` passed.
- Source:
  - `br/pkg/utils/storewatch/watching.go`: watcher state transition and callback gate.
  - `br/pkg/backup/store.go`: `OnReboot` sets send-all backup retry policy.
  - `br/pkg/restore/data/data.go`: `OnReboot` records reboot stores for leader regeneration.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-br-storewatch-same-second-reboot-draft.md`
  - Asset results: `/Users/bba/pc/ai-native-assets/source-storewatch-reboot-same-second-results.jsonl`
  - RED log: `/Users/bba/pc/ai-native-assets/logs/source-storewatch-current-reboot-same-second-red.log`
  - GREEN log: `/Users/bba/pc/ai-native-assets/logs/source-storewatch-local-green.log`
- Method lesson: `IDENTITY_TOKEN_ASYNC_FILTER` is now more than a retrospective explanation of the
  DDL ownerTS bug. It produced a negative BR registry screen and a positive BR storewatch RED/GREEN.
  The required extra gate is G3: prove a product-feasible collision schedule before executing.
- Stop rule: do not broaden into every storewatch callback. Reopen for live TiKV restart lift,
  fix validation, or a different lifecycle token that passes G1-G4.

## Retired Screen: TiFlash MPP Logical-Core Cache StartTimestamp Token (SOURCE_TARGETS)

- Status: **retired / LOW_VALUE**, stored as
  `target.source.tiflash-mpp-logical-core-starttimestamp.v1`; not a confirmed bug.
- Proof obligation to derive:
  - `P_check`: `splitTiFlashLogicalCoreCache` finds cached TiFlash MPP info for the same address
    and equal `StartTimestamp`.
  - `Q_claim`: equal address plus equal `StartTimestamp` proves the cached `LogicalCPUCount`
    still describes the current TiFlash process.
  - `F_effect`: current code reuses `mppServerInfo.LogicalCPUCount` and does not refresh hardware
    info if the token matches.
- Source evidence:
  - `pkg/planner/core/optimizer.go`: `mppServerInfo == nil || mppServerInfo.StartTimestamp != info.StartTimestamp`
    is the refresh gate; otherwise `minLogicalCores` uses cached `LogicalCPUCount`.
  - `pkg/domain/infosync/tiflash_manager.go`: TiFlash server info fills `StartTimestamp` from
    `tiflash.StartTime.Unix()`.
  - `pkg/store/copr/mpp_probe.go`: `GlobalMPPServerInfoManager` stores
    `Address`, `LogicalCPUCount`, and `StartTimestamp`.
- Required next proofs:
  - G3 schedule proof: show whether a TiFlash restart within the same second can reuse the address
    and expose a changed logical CPU count under product timing.
  - G4 effect proof: show a user-visible or planner-visible consequence, for example stale
    fine-grained shuffle stream count or an observable refresh miss.
- First target-analysis note:
  - G4 is plausible: `LogicalCPUCount` feeds `TiFlashFineGrainedShuffleStreamCount`, and existing
    planner tests already assert stream-count propagation into join/exchange nodes.
  - G3 is the blocker: a unit test can force equal `StartTimestamp`, but that would not prove a
    product-feasible TiFlash restart/config-change collision.
- Retirement decision:
  - Recorded in `/Users/bba/pc/ai-native-assets/source-targets-tiflash-mpp-cache-retire-analysis.jsonl`.
  - Decision key: `retired_invalid_schedule_effect_quality`.
  - Reason: the required schedule combines same-second TiFlash restart/re-registration, address
    reuse, and changed logical CPU count. The observable effect is only plan/performance quality.
- Stop rule: do not run a RED test on token equality alone. If G3 or G4 cannot be made concrete,
  retire as `INVALID(schedule-proof/effect-proof)` and keep it as selector calibration.

## P0: NT-DML `tx_read_ts` Split-Range Leak (id1230001, S23)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id1230001
  (current remote state after insert: `COUNT(*)=72`,
  `COUNT(DISTINCT root_cause_id)=50`).
- Proof obligation:
  - `P_check`: `HandleNonTransactionalDML` clears `SessionVars.ReadStaleness` because NT-DML is a
    write and should not be affected by stale reads.
  - `Q_claim`: the internal split-range SELECT is planned from the current rowset.
  - `D_dim`: stale-read state has sibling ingress channels. `ReadStaleness` is cleared, but
    `SET TRANSACTION READ ONLY AS OF TIMESTAMP` leaves `TxnReadTS`, and `tidb_snapshot` is a
    separate channel that is explicitly rejected.
  - `F_effect`: `buildShardJobs` calls `se.Execute(selectSQL)`, which lets the stale-read
    processor consume `TxnReadTS`; only stale-visible ranges are turned into jobs, and the split
    DML commits those ranges.
- Evidence on testbed `8220955`:
  - Control A: ordinary `UPDATE` after `SET TRANSACTION READ ONLY AS OF TIMESTAMP @ts` returns
    `ERROR 1105 only support read-only statement during read-only staleness transactions`.
  - Control B: `BATCH ON a LIMIT 1 UPDATE t SET b=b+100` without `tx_read_ts` reports two jobs and
    yields `1:110,2:120`.
  - Red cell: create `1:10`, capture `@ts`, insert `2:20` after `@ts`; `AS OF @ts` sees `1:10`.
    Then set transaction AS OF and run the same BATCH update. It reports one successful job and
    final current rowset is `1:110,2:20`.
  - `ADMIN CHECK TABLE` is green; the bug is semantic partial write, not storage corruption.
- Source:
  - `pkg/session/nontransactional.go:80-82`: clears `ReadStaleness` only.
  - `pkg/session/nontransactional.go:467-468`: split-range SELECT is executed through the normal
    session path.
  - `pkg/sessiontxn/staleread/processor.go:233-257`: `TxnReadTS` drives stale read when present.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-ntdml-tx-read-ts-stale-split-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id1230001-method-case.md`
- Method lesson: comments that say "clear/ignore X state" should trigger a sibling-input
  inventory. The fastest route was to ask "what else can make this internal SELECT stale?" and
  then build a three-cell oracle: ordinary-write reject, no-stale current rowset, stale split.
- Stop rule: do not enumerate BATCH syntax variants. Reopen only for another stale ingress
  channel, a stronger DELETE/INSERT-SELECT consequence, or fix validation.

## P0: RELEASE SAVEPOINT Stack-Semantics Split (id1200002, S21)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id1200002
  (current remote state after insert: `COUNT(*)=71`,
  `COUNT(DISTINCT root_cause_id)=49`).
- Proof obligation:
  - `P_check`: `RELEASE SAVEPOINT name` finds a named savepoint in the transaction context's
    ordered savepoint stack.
  - `Q_claim`: after release, only that named marker is removed; later markers remain valid.
  - `D_dim`: savepoint operations are adjacent but not equivalent. `ROLLBACK TO` restores an old
    transaction checkpoint and deletes later markers, while `RELEASE` only drops the named marker.
  - `F_effect`: TiDB's `ReleaseSavepoint` uses rollback-like slice truncation, so a later marker
    is silently removed and the next `ROLLBACK TO` cannot restore to it.
- Evidence on testbed `8192975`:
  - Red cell: `SAVEPOINT sp1; INSERT id=2; SAVEPOINT sp2; INSERT id=3; RELEASE SAVEPOINT sp1;
    ROLLBACK TO SAVEPOINT sp2` returns `ERROR 1305 SAVEPOINT sp2 does not exist`.
  - User-visible state: before release rows are `1:10,2:20,3:30`; after the failed rollback they
    are still `1:10,2:20,3:30`, so the write after `sp2` was not rolled back.
  - Green control: `ROLLBACK TO sp1` deletes later `sp2` by design.
  - Green control: `RELEASE SAVEPOINT sp2` still leaves `sp1` usable.
  - Reference contract: MySQL documents `RELEASE SAVEPOINT` as removing the named savepoint, while
    `ROLLBACK TO SAVEPOINT` removes later savepoints.
- Source:
  - `pkg/sessionctx/variable/session.go:529-535`: `ReleaseSavepoint` says it deletes later
    savepoints and implements `tc.Savepoints = tc.Savepoints[:i]`.
  - `pkg/sessionctx/variable/session.go:541-548`: `RollbackToSavepoint` is the operation that
    restores state and truncates later savepoints.
  - `pkg/executor/simple.go:680-685`: `executeReleaseSavepoint` directly trusts
    `TxnCtx.ReleaseSavepoint`.
  - `pkg/executor/test/txn/txn_test.go:309-315` and `:443-445`: existing tests encode the current
    behavior, so the oracle must come from the external/reference contract.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-release-savepoint-stack-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id1200002-method-case.md`
- Method lesson: txn has high-yield targets when a module maintains an ordered state stack and
  exposes several neighboring operations over it. List each operation's semantic effect first,
  then build a tiny stack matrix. Existing tests are especially suspect when they assert the same
  sibling semantics the source comments claim.
- Stop rule: do not enumerate savepoint name case, autocommit modes, or nested transaction
  variants. Reopen only for another transaction state-stack semantic split, a stronger consequence,
  or fix validation.

## P0: `join_key_type_cast` Semantic-Domain Rewrite (id30040, S20)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id30040
  (current remote state after insert: `COUNT(*)=69`,
  `COUNT(DISTINCT root_cause_id)=47`).
- Proof obligation:
  - `P_check`: the rule sees a DOUBLE equality produced from INT-vs-STRING casts and checks the
    string side with `CAST(CAST(varchar AS SIGNED) AS DOUBLE)=CAST(varchar AS DOUBLE)`.
  - `Q_claim`: after that guard, replacing the original mixed INT/VARCHAR comparison with INT
    equality preserves the join result.
  - `D_dim`: TiDB/MySQL numeric string comparison accepts scientific notation in the DOUBLE
    comparison domain, while signed integer cast parses only the integer prefix.
  - `F_effect`: the optimizer inserts a VARCHAR-side guard and changes the join key to INT-domain
    equality, so `s='1e1'` is filtered out before it can match `id=10`.
- Evidence on testbed `8192975`:
  - Scalar contract: `10='1e1'` is `1`; `CAST('1e1' AS DOUBLE)=10`; `CAST('1e1' AS SIGNED)=1`;
    the rule guard is `0`.
  - Trigger evidence: default `EXPLAIN FORMAT='brief'` shows `join_key_type_cast` rewrote the
    join to `equal:[eq(Column#1, Column#13)]` and inserted the signed-int round-trip guard.
  - Red cell: default query returns `1:1,2:2e0,10:10,10:10.0`.
  - References: CASE-wrapped equality and `opt_rule_blacklist` for `join_key_type_cast` both
    return `1:1,2:2e0,10:10,10:10.0,10:1e1`.
- Source:
  - `pkg/planner/core/rule/rule_join_key_type_cast.go:28-47`: the documented rewrite from DOUBLE
    comparison to INT equality plus guard.
  - `pkg/planner/core/rule/rule_join_key_type_cast.go:202-227`: appends `CAST(varchar AS SIGNED)`
    and inserts the guard.
  - `pkg/planner/core/rule/rule_join_key_type_cast.go:302-325`: classifies signed INT vs string
    without excluding non-canonical numeric strings.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-join-key-type-cast-scientific-notation-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id30040-method-case.md`
- Method lesson: S20 is the non-DDL version of the P/Q/F method at its cleanest. The target was
  not "weird strings"; it was a semantic-domain replacement where `D_old=DOUBLE/numeric string
  comparison` and `D_new=SIGNED integer equality`.
- Stop rule: do not enumerate numeric string spellings. Reopen only for another semantic-domain
  rewrite, a stronger consequence, or fix validation of `join_key_type_cast`.

## P0: EXCHANGE PARTITION DEFAULT Validation SQL (id630025, S19)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id630025
  (current remote state after insert: `COUNT(*)=68`,
  `COUNT(DISTINCT root_cause_id)=46`).
- Proof obligation:
  - `P_check`: `EXCHANGE PARTITION ... WITH VALIDATION` validates standalone rows by generating a
    restricted SQL predicate for the target partition.
  - `Q_claim`: the generated predicate is equivalent to TiDB's partition locator, including
    DEFAULT partition semantics.
  - `D_dim`: `LIST DEFAULT` is represented as the complement of all explicit `LIST` values; it is
    not stored as the current partition's ordinary `InValues`.
  - `F_effect`: for DEFAULT, the validation builder emits invalid SQL like `not () limit 1`, so a
    legal exchange fails before the metadata ID swap.
- Evidence on testbed `8192975`:
  - Direct target-state oracle: inserting value `3` into a `LIST(a)` table with `p1 IN (1)`,
    `p2 IN (2)`, `pdef DEFAULT` stores the row in `PARTITION(pdef)`.
  - Sibling validation control: a no-DEFAULT `LIST` table exchanging `p1` with standalone row `1`
    succeeds.
  - Red cell: `ALTER TABLE pt_default EXCHANGE PARTITION pdef WITH TABLE nt_default` with
    standalone row `3` returns `ERROR 1064 ... near ") limit 1"`; `pdef` remains empty and `nt`
    still contains `3`.
  - Boundary control: the same legal row swaps successfully with `WITHOUT VALIDATION`.
  - Sibling shape: `LIST COLUMNS(a,b) ... PARTITION pdef DEFAULT` hits the same syntax-error
    validation builder.
- Source:
  - `pkg/ddl/partition.go:4246-4388`: `checkExchangePartitionRecordValidation` builds and runs
    restricted SQL for validation.
  - `pkg/ddl/partition.go:4534-4550`: `buildCheckSQLConditionForListPartition` iterates only
    `pi.Definitions[index].InValues` and has a TODO for DEFAULT.
  - `pkg/ddl/partition.go:4554-4580`: `buildCheckSQLConditionForListColumnsPartition` has the
    same DEFAULT TODO.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-exchange-partition-default-validation-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630025-method-case.md`
- Method lesson: this came from the improved P4 high-risk lane, but the consequence is still
  honestly C1. The selector refinement is "internal validation SQL builder must preserve every
  semantic dimension of the relation it replaces."
- Stop rule: do not enumerate partition syntax. Reopen only for another validation SQL builder
  omitted dimension, wrong-acceptance/data-placement consequence, or fix validation.

## P0: `_utf8mb4` Default Collation Cached Literal Proof (id30037, S7 refinement)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id30037
  (insert-time remote state: `MAX(id)=630016,COUNT=56`; current latest is tracked in the handoff).
- Proof obligation:
  - `P_check`: prepared plan-cache key and clone rules say the cached expression plan is reusable.
  - `Q_claim`: cached underscore-charset literal collation remains valid after current-session
    default UTF8MB4 collation changes.
  - `D_dim`: expression rewrite reads `@@default_collation_for_utf8mb4`.
  - `F_effect`: cache hit reuses literal field-type collation metadata written under the old session
    value.
- Evidence on testbed `8192975`:
  - Direct contract: `default_collation_for_utf8mb4=utf8mb4_bin` gives
    `COLLATION(_utf8mb4'A')=utf8mb4_bin` and `_utf8mb4'A'=_utf8mb4'a'` is `0`; setting it to
    `utf8mb4_general_ci` gives `utf8mb4_general_ci` and equality `1`.
  - Prepared projection red cell: prepare/execute under `utf8mb4_bin` returned `utf8mb4_bin/0`;
    after switching to `utf8mb4_general_ci`, the second `EXECUTE` still returned `utf8mb4_bin/0`
    with `@@last_plan_from_cache=1`, while direct SQL returned `utf8mb4_general_ci/1`.
  - User-query red cell: `SELECT COUNT(*) FROM t WHERE _utf8mb4'A'=_utf8mb4'a'` returned `0` on
    cache hit after switching to `utf8mb4_general_ci`, while direct SQL returned `2`.
  - Flush reference: `ADMIN FLUSH SESSION PLAN_CACHE` made the same prepared statement return
    `2` rows with `@@last_plan_from_cache=0`.
  - Control: explicit `COLLATE utf8mb4_general_ci` stayed correct across the switch even on cache
    hit.
- Source:
  - `pkg/planner/core/expression_rewriter.go:1660-1663`: `adjustUTF8MB4Collation` writes
    `GetDefaultCollationForUTF8MB4()` into underscore-charset UTF8MB4 literal field types.
  - `pkg/sessionctx/variable/sysvar.go:1987-1995`: `default_collation_for_utf8mb4` is
    session/global and updates `SessionVars.DefaultCollationForUTF8MB4`.
  - `pkg/planner/core/plan_cache_utils.go:390-438`: prepared plan-cache key includes connection
    charset/collation, but not `default_collation_for_utf8mb4`.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-utf8mb4-default-collation-plan-cache-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id30037-method-case.md`
  - Getter scan ledger: `/Users/bba/pc/ai-native-s7-hidden-input-getter-scan.md`
- Method lesson: id30037 proves the getter-level scan is predictive. Adjacent key coverage does not
  imply semantic coverage: connection collation is key-covered, but default UTF8MB4 literal
  collation is a separate hidden input with a separate cached payload.
- Stop rule: do not enumerate charset introducers or comparison spellings. Reopen only for another
  hidden input, another literal/type payload owner, or fix validation.

## P0: ADD UNIQUE MVI Backfill Owner Mapping Proof (id30038, S1 refinement)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id30038
  (current remote state after insert: `COUNT(*)=66`,
  `COUNT(DISTINCT root_cause_id)=45`).
- Proof obligation:
  - `P_check`: add-index backfill flattens generated unique keys and uses the flattened key
    ordinal to recover the owning index.
  - `Q_claim`: `flattenedKeyOrdinal % len(indexes)` still identifies the correct index metadata
    for found-key duplicate classification.
  - `D_dim`: a multi-valued index can emit multiple keys for one `idxRecord`, while a sibling
    index can have a different index-column count and decode rule.
  - `F_effect`: if concurrent DML already wrote the new index key, backfill decodes that found key
    with the reconstructed owner and either skips it as same-handle or reports duplicate.
- Evidence on testbed `8192975`:
  - Red: with `tidb_enable_dist_task=OFF`, `tidb_ddl_enable_fast_reorg=OFF`,
    `mockBackfillSlow=return(true)`, and 100k rows split into 50 regions,
    `ALTER TABLE t ADD UNIQUE INDEX u_mvi((CAST(j AS SIGNED ARRAY))), ADD UNIQUE INDEX u_ab(a,b)`
    plus concurrent `UPDATE t SET b=b+7 WHERE a=90000` returned
    `ERROR 1062 Duplicate entry '90000' for key 't.u_mvi'`.
  - Job evidence: the add-index subjob rolled back at `row_count=179998`; only `PRIMARY` remained,
    `ADMIN CHECK TABLE` passed, and the user DML persisted.
  - Controls: add only `u_mvi` plus concurrent JSON update succeeded; add `u_mvi` plus one-column
    `u_b(b)` succeeded; add `u_mvi` plus two-column `u_ab(a,b)` plus concurrent JSON update stayed
    in write reorganization until cancel and returned `invalid encoded key`.
- Source:
  - `pkg/table/tables/index.go:663-670`: MVI indexes use a multi-value key iterator.
  - `pkg/table/index.go:177-203`: a multi-value iterator can emit multiple keys for one
    `idxRecord`.
  - `pkg/ddl/index.go:2606-2646`: `batchCheckUniqueKey` flattens generated keys but records only
    `recordIdx`.
  - `pkg/ddl/index.go:2661-2670`: found-key classification recovers the index owner with
    `w.indexes[i%len(w.indexes)]` using the flattened key ordinal.
  - `pkg/tablecodec/tablecodec.go:1008-1048`: `DecodeIndexHandle` depends on the index-column
    count, so using sibling `u_ab(a,b)` metadata on an MVI key can misdecode the handle.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-add-index-mvi-owner-mismatch-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id30038-method-case.md`
- Method lesson: S1 is not only rollback/restore reconstruction. A state-transforming path can
  also lose an owner bit by flattening per-owner artifacts and later reconstructing ownership from
  ordinal. MVI is the compact redflag because one owner emits multiple artifacts.
- Stop rule: do not enumerate MVI cast types or array element shapes. Reopen only for another
  flattened-artifact owner/type bit loss, silent corruption, or fix validation.

## P0: Persisted ANALYZE Options After EXCHANGE PARTITION (id30039, S4 blast-radius)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id30039. This reuses
  `root_cause_id=exchange-idswap-orphan`; it is a blast-radius surface, not a new distinct root
  (remote state after insert: `COUNT(*)=67`, `COUNT(DISTINCT root_cause_id)=45`).
- Proof obligation:
  - `P_check`: `EXCHANGE PARTITION` says the standalone table and partition can swap physical IDs.
  - `Q_claim`: side metadata created through a logical ANALYZE command remains attached to the
    correct logical owner, or is remapped/cleared.
  - `D_dim`: `mysql.analyze_options` is keyed by physical table ID, while users create it through
    `ANALYZE TABLE pt PARTITION p0 ...`.
  - `F_effect`: after the ID swap, future `ANALYZE TABLE nt` loads the inherited saved column list
    from old `pt.p0`.
- Evidence on testbed `8192975`:
  - `ANALYZE TABLE pt PARTITION p0 COLUMNS a WITH 1 TOPN,3 BUCKETS` saved
    `column_choice=LIST,column_ids=1` under old `p0` physical ID.
  - After `ALTER TABLE pt EXCHANGE PARTITION p0 WITH TABLE nt WITHOUT VALIDATION`, old `p0` ID
    became current `nt` table ID, and the analyze option row resolved to `nt`.
  - `ANALYZE TABLE nt WITH 2 TOPN,2 BUCKETS` analyzed only `a` and `PRIMARY`; `b/c` remained
    `stats_ver=0`.
  - No-exchange standalone control `ANALYZE TABLE ctrl WITH 2 TOPN,2 BUCKETS` analyzed
    `a/b/c/PRIMARY`.
- Source:
  - `pkg/ddl/partition.go`: `onExchangeTablePartition` swaps `partDef.ID, nt.ID`.
  - `pkg/statistics/handle/ddl/subscriber.go`: exchange handling updates global stats counts but
    does not remap or clear `mysql.analyze_options`.
  - `pkg/executor/analyze.go`: `AnalyzeExec.saveAnalyzeOptions` persists options by
    `opts.PhyTableID`.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-exchange-analyze-options-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id30039-method-case.md`
- Method lesson: O21's second tier can be a future behavior consumer, not only a cleanup command.
  Side-row survival becomes important when a current logical command consumes the stale row and
  changes behavior.
- Stop rule: do not enumerate stats/analyze_options variants. Reopen only for another owner with a
  new behavior round trip, a consequence escalation, or fix validation.

## P0: TTL Status Owner Mapping After EXCHANGE PARTITION (id630024, S4 quality calibration)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id630024
  (insert-time remote state: `MAX(id)=1020001,COUNT=65`).
- Proof obligation:
  - `P_check`: `EXCHANGE PARTITION` compatibility checks say the partitioned table and standalone
    table can swap physical IDs.
  - `Q_claim`: owner-sensitive side metadata remains coherent after the ID swap.
  - `D_dim`: TTL status and timers are keyed by physical table ID, while TTL ownership follows the
    current TTL table/partition.
  - `F_effect`: `onExchangeTablePartition` swaps `partDef.ID` and `nt.ID` without checking TTLInfo
    compatibility or reconciling TTL status/timer rows.
- Evidence on testbed `8192975`:
  - Before exchange, standalone TTL table `nt` had table_id `16104`; non-TTL partition `pt.p0` had
    partition_id `16101`.
  - A real TTL job ran on `nt`, deleted one expired row, and wrote
    `mysql.tidb_ttl_table_status.table_id=16104,parent_table_id=16104`.
  - After `ALTER TABLE pt EXCHANGE PARTITION p0 WITH TABLE nt WITHOUT VALIDATION`, `nt` became
    table_id `16101` and `pt.p0` became partition_id `16104`, but the old TTL status row remained
    on `16104`.
  - Timer sync created `/tidb/ttl/physical_table/16101/16101` for current `nt` and disabled old
    `/16104/16104`, leaving two visible status/history rows for `nt` across old and current IDs.
- Source:
  - `pkg/ddl/executor.go:3035-3118`: `checkTableDefCompatible` does not compare `TableInfo.TTLInfo`.
  - `pkg/ddl/executor.go:3119-3140`: `checkExchangePartition` does not check TTL ownership.
  - `pkg/ddl/partition.go:2765-3051`: `onExchangeTablePartition` swaps the IDs and updates
    placement/TiFlash-related metadata, but not TTL side metadata.
  - `pkg/ttl/ttlworker/timer_sync.go`: TTL timer/status lookup follows current physical IDs, which
    explains the new timer and disabled old timer after sync.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-exchange-partition-ttl-status-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630024-method-case.md`
- Method lesson: S4 remains productive, but not all S4 reds are equal. Split side-state ID-swap
  oracles into tier 1 (storage-vs-current-owner diff) and tier 2 (management/cleanup/active
  behavior failure). id630024 is tier 1 plus real job evidence; id630014 masking policy is tier 2.
- Stop rule: do not enumerate TTL options or all TTL system tables. Reopen only for active wrong
  scheduling/deletion, another side-state owner with tier-2 behavior failure, or fix validation.

## P0: `AVG()` Cached Decimal Scale Proof (id30036, S7 refinement)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id30036
  (`MAX(id)=630016,COUNT=55`; max id remains id630016 because id30036 is lower).
- Proof obligation:
  - `P_check`: prepared plan-cache key and clone rules say the cached aggregate plan is reusable.
  - `Q_claim`: cached `AVG()` return type/scale remains valid after current-session precision
    changes.
  - `D_dim`: `AVG()` type inference reads `@@div_precision_increment`.
  - `F_effect`: cache hit reuses the aggregate descriptor whose `RetTp.Decimal` was inferred under
    the old precision. Decimal division at execution reads the current precision, but final
    rounding/rendering uses the old cached scale.
- Evidence on testbed `8192975`:
  - Direct contract: `div_precision_increment=4` gives `AVG(x)=1.5000`; setting it to `8` gives
    `1.50000000`.
  - Prepared projection red cell: prepare/execute under precision 4 returned `1.5000`; after
    switching to precision 8, the second `EXECUTE` still returned `1.5000` with
    `@@last_plan_from_cache=1`, while direct SQL returned `1.50000000`.
  - User-query red cell: a derived-table predicate over `CAST(AVG(x) AS CHAR)='1.50000000'`
    returned `0` on cache hit after switching to precision 8, while direct SQL returned `1`.
  - Flush reference: `ADMIN FLUSH SESSION PLAN_CACHE` made the same prepared statement return
    precision-8 results with `@@last_plan_from_cache=0`.
- Source:
  - `pkg/expression/aggregation/base_func.go:127-139`: aggregate `TypeInfer` calls
    `typeInfer4Avg`.
  - `pkg/expression/aggregation/base_func.go:274-285`: `typeInfer4Avg` reads
    `ctx.GetDivPrecisionIncrement()` and sets return decimal/scale.
  - `pkg/expression/aggregation/avg.go:80-91`: decimal `AVG` divides by current
    `GetDivPrecisionIncrement()` and then rounds by `af.RetTp.GetDecimal()`.
  - `pkg/planner/core/plan_cache_utils.go:390-449`: prepared plan-cache key omits
    `div_precision_increment`.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-avg-div-precision-plan-cache-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id30036-method-case.md`
  - Getter scan ledger: `/Users/bba/pc/ai-native-s7-hidden-input-getter-scan.md`
- Method lesson: id30036 upgrades S7 from "implicit session input can stale folded constants" to
  "implicit session input can stale any cached payload class." The search unit is now
  `getter -> consumer -> cached payload class -> oracle`.
- Stop rule: do not enumerate AVG argument types. Reopen only for another hidden input, another
  cached payload class, or fix validation.

## P0: `div_precision_increment` Cached Division Constant Proof (id30035, S7 refinement)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id30035
  (`MAX(id)=630016,COUNT=54`; max id remains id630016 because id30035 is lower).
- Proof obligation:
  - `P_check`: prepared plan-cache key and clone rules say the cached plan is reusable.
  - `Q_claim`: folded decimal-division constants remain valid under current session precision.
  - `D_dim`: decimal division reads `@@div_precision_increment`.
  - `F_effect`: constant folding evaluates `1/7` under the old precision and stores a plain
    `Constant`; the later cache hit reuses it instead of rebuilding/re-evaluating under the new
    precision.
- Evidence on testbed `8192975`:
  - Scalar contract: `div_precision_increment=4` gives `1/7 = 0.1429`; setting it to `8` gives
    `0.14285714`.
  - Prepared projection red cell: prepare/execute under precision 4 returned `0.1429`; after
    switching to precision 8, the second `EXECUTE` still returned `0.1429` with
    `@@last_plan_from_cache=1`, while direct SQL returned `0.14285714`.
  - User-query red cell: `SELECT COUNT(*) FROM t WHERE CAST(1/7 AS CHAR)='0.142857142'` returned
    `0` on cache hit after switching to precision 8, while direct SQL returned `2`.
  - Flush reference: `ADMIN FLUSH SESSION PLAN_CACHE` made the same prepared statement return
    precision-8 results with `@@last_plan_from_cache=0`.
  - Control: `SELECT a/b FROM t` over decimal columns followed current precision even with
    `@@last_plan_from_cache=1`; the stale behavior is specific to folded constant payloads.
- Source:
  - `pkg/expression/builtin_arithmetic.go:745`: decimal division return type uses
    `ctx.GetEvalCtx().GetDivPrecisionIncrement()`.
  - `pkg/expression/builtin_arithmetic.go:810`: decimal division evaluation calls
    `types.DecimalDiv(..., ctx.GetDivPrecisionIncrement())`.
  - `pkg/planner/core/plan_cache_utils.go:360-455`: prepared plan-cache key omits
    `div_precision_increment`.
  - `pkg/expression/constant_fold.go:230-253`: all-constant scalar functions are folded into
    ordinary `Constant`s unless they contain parameter/deferred constants.
  - `pkg/expression/util.go:1720-1745`: the mutable-constant guard does not model hidden session
    inputs.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-div-precision-plan-cache-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id30035-method-case.md`
- Method lesson: id30035 confirms id30034's selector is not specific to dates. The next high-value
  scan should enumerate hidden `EvalContext` / `BuildContext` inputs, subtract plan-cache key and
  deferred-constant coverage, and then build tiny direct/cache-hit/flush matrices for the survivors.
- Stop rule: do not enumerate decimal literal spellings or arithmetic variants. Reopen only for
  another hidden session/config input family, another cache payload mechanism, or fix validation.

## P0: `WEEK()` default_week_format Cached Constant Proof (id30034, S7 refinement)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id30034
  (`MAX(id)=630016,COUNT=53`; max id remains id630016 because id30034 is lower).
- Proof obligation:
  - `P_check`: prepared plan-cache key and clone rules say the cached prepared plan is reusable.
  - `Q_claim`: cached expression payloads remain valid under the current session semantics.
  - `D_dim`: single-argument `WEEK(date)` has an implicit session input,
    `@@default_week_format`.
  - `F_effect`: constant folding evaluates `WEEK('2008-02-20')` under the old mode and stores a
    plain `Constant`; the later cache hit reuses it instead of re-evaluating under the new mode.
- Evidence on testbed `8192975`:
  - Scalar contract: `default_week_format=0` gives `WEEK('2008-02-20')=7`;
    `default_week_format=1` gives `8`.
  - Prepared projection red cell: prepare/execute under mode 0 returned `7`; after switching to
    mode 1, the second `EXECUTE` returned `7` with `@@last_plan_from_cache=1`, while direct SQL
    returned `8`.
  - User-query red cell: `SELECT COUNT(*) FROM t WHERE WEEK('2008-02-20') = 8` returned `0` on
    cache hit after switching to mode 1, while direct SQL returned `2`.
  - Flush reference: `ADMIN FLUSH SESSION PLAN_CACHE` made the same prepared statement return
    `8` / row count `2` with `@@last_plan_from_cache=0`.
  - Controls were green: `SELECT WEEK(d) FROM t` over a column followed the current mode even with
    `@@last_plan_from_cache=1`; explicit `WEEK('2008-02-20',1)` stayed correct across cache hit;
    `YEARWEEK(date)` without mode is a boundary sample because source fixes mode `0`.
- Source:
  - `pkg/expression/builtin_time.go:1493-1510`: `builtinWeekWithoutModeSig.evalInt` reads
    `ctx.GetDefaultWeekFormatMode()`.
  - `pkg/planner/core/plan_cache_utils.go:360-455`: prepared plan-cache key omits
    `default_week_format`.
  - `pkg/expression/constant_fold.go:230-253`: all-constant scalar functions are folded to a
    plain `Constant` unless their inputs are deferred/parameterized.
  - `pkg/expression/util.go:1720-1745`: the plan-cache mutable-constant guard checks only
    `ParamMarker` / `DeferredExpr`, not hidden session inputs.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-week-default-week-format-plan-cache-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id30034-method-case.md`
- Method lesson: cache-key completeness is not enough. A cached payload must be a pure function of
  explicit SQL inputs, key dimensions, and implicit semantic inputs read during build/rewrite/fold.
  Green controls from this pass show the boundary: `foreign_key_checks` prepared DML is protected by
  keying and no-clone FK payloads, while parameter-sensitive partial-index eligibility is uncached.
- Stop rule: do not enumerate every date function. Reopen only for another hidden session/config
  input that constant folding turns into a cached payload, or for fix validation.

## P0: cluster_log REGEXP_LIKE match_type Semantics Proof (id30033, S3 refinement)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id30033
  (`MAX(id)=630016,COUNT=52`; max id remains id630016 because id30033 is lower).
- Proof obligation:
  - `P_check`: `ClusterLogTableExtractor` sees `REGEXP_LIKE(message, const, ...)` and extracts
    a backend regexp pattern.
  - `Q_claim`: the backend regexp filter is equivalent to SQL-visible
    `REGEXP_LIKE(message, pattern[, match_type])`.
  - `D_dim`: `match_type` changes regexp semantics. On `utf8mb4_bin` `MESSAGE`,
    `match_type='i'` is case-insensitive, while `match_type='c'` is case-sensitive.
  - `F_effect`: the extractor keeps only the pattern string and removes the original scalar
    predicate, so the backend request cannot preserve `match_type='i'`.
- Evidence on testbed `8192975`:
  - Scalar contract check: `REGEXP_LIKE(_utf8mb4'gc_service.go' COLLATE utf8mb4_bin,
    'GC_SERVICE.GO','i')=1`, while the same expression with `match_type='c'` is `0`.
  - Fast arm over `information_schema.cluster_log` in the window
    `[2026-07-02 14:54:59, 2026-07-03 14:54:59)` returned `0` rows for
    `REGEXP_LIKE(message,'GC_SERVICE.GO','i')`.
  - CASE scalar reference over the same window returned `35742` rows, all with projected
    `REGEXP_LIKE(message,'GC_SERVICE.GO','i')=1`.
  - Controls were green: uppercase + `match_type='c'` returned `0/0`; lowercase +
    `match_type='c'` returned `35744/35744`.
  - `EXPLAIN FORMAT='brief'` for the fast arm showed `CLUSTER_LOG` `MemTableScan` with no
    remaining `Selection`; the CASE reference kept
    `Selection eq(case(regexp_like(Column#5,"GC_SERVICE.GO","i"),1,0),1)`.
- Source:
  - `pkg/planner/core/memtable_predicate_extractor.go:439-466`: `extractLikePattern` handles
    `ast.RegexpLike` by extracting only column+pattern and returning `datums[0].GetString()`.
  - `pkg/planner/core/memtable_predicate_extractor.go:182-231`: binary helper reads only the
    column side and one constant side, so the third `REGEXP_LIKE` argument is not preserved.
  - `pkg/planner/core/memtable_predicate_extractor.go:813-834`: cluster-log extractor stores the
    extracted patterns and returns the remaining predicates, dropping the scalar regexp predicate.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-clusterlog-regexp-like-match-type-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id30033-method-case.md`
- Method lesson: after id30030/id30031, the efficient next step was not enumerating more LIKE
  users. It was listing the next scalar operator's semantic inputs and checking whether the
  shortcut preserved all of them. `REGEXP_LIKE` proved the same selector can migrate to a new
  omitted input (`match_type`) with a tiny red/green matrix.
- Stop rule: do not enumerate regexp flags, pattern variants, or other cluster-log regexp
  spellings. Reopen only for a different replacement mechanism, a different omitted semantic
  input family, or fix validation.

## P0: Memtable LIKE ESCAPE Semantics Proof (id30030, S3 refinement)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id30030
  (`MAX(id)=630014,COUNT=47`; max id remains id630014 because id30030 is lower).
- Proof obligation:
  - `P_check`: the cluster-log predicate extractor sees `message LIKE const` and extracts a
    pattern string.
  - `Q_claim`: the extracted backend regexp is equivalent to SQL-visible
    `message LIKE pattern [ESCAPE x]`.
  - `D_dim`: custom `ESCAPE` changes the meaning of the same pattern. `%#_% ESCAPE '#'
    matches literal underscore; `%#_%` under the default escape searches for `#` plus any char.
  - `F_effect`: the extractor compiles the pattern with the default backslash escape and removes
    the original scalar predicate.
- Evidence on testbed `8192975`:
  - Fast arm
    `cluster_log WHERE message LIKE '%#_%' ESCAPE '#'` returned `0` rows.
  - CASE scalar reference over the same time window returned `130683` rows, all satisfying the
    custom-escape predicate.
  - `EXPLAIN FORMAT='brief'` for the fast arm showed `MemTableScan table:CLUSTER_LOG` with no
    remaining `Selection` for the message predicate; the CASE reference kept scalar
    `Selection eq(case(like(Column#5,"%#_%",35),1,0),1)`.
  - Default-escape control was green: fast and reference both returned `130759` rows for
    `message LIKE '%\_%'`.
  - Ordinary table contract control showed
    `'gc_service.go' LIKE '%#_%' ESCAPE '#' = 1`, while the no-ESCAPE form matched `abc#x`.
- Source:
  - `pkg/planner/core/memtable_predicate_extractor.go:439-466`: cluster-log `LIKE` extractor.
  - `pkg/planner/core/memtable_predicate_extractor.go:182-231`: helper extracts only column and
    pattern constant, not the third `ESCAPE` argument.
  - `pkg/planner/core/memtable_predicate_extractor.go:463`: calls
    `stringutil.CompileLike2Regexp(pattern)`.
  - `pkg/util/stringutil/string_util.go:260-263`: `CompileLike2Regexp` hardcodes the default
    backslash escape via `CompilePattern(str, '\\')`.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-clusterlog-like-escape-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id30030-method-case.md`
- Method lesson: S3 must enumerate the semantic arity of the scalar operator being replaced.
  For `LIKE`, the pattern string alone is insufficient; `ESCAPE` is part of the proof input.

## P0: InfoSchema LIKE ESCAPE Semantics Proof (id30031, S3 blast-radius)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id30031
  (`MAX(id)=630014,COUNT=48`; max id remains id630014 because id30031 is lower).
- Proof obligation:
  - `P_check`: `InfoSchemaBaseExtractor` extracts `TABLE_NAME LIKE const` into a regexp prefilter.
  - `Q_claim`: the regexp prefilter is equivalent to SQL scalar evaluation of
    `TABLE_NAME LIKE pattern [ESCAPE x]`.
  - `D_dim`: custom `ESCAPE` flips the witnesses for `%#_%`: `abc_def` is true under
    `ESCAPE '#'`, while `abc#x` is true under the default escape.
  - `F_effect`: the InfoSchema scan filters rows by the default-escape regexp and removes scalar
    recheck.
- Evidence on testbed `8192975`:
  - Setup tables: `abc_def`, `abc#x`, `plain` in database `ai_show_like_escape_0703`.
  - Fast arm
    `information_schema.tables WHERE table_schema=DATABASE() AND table_name LIKE '%#_%' ESCAPE '#'`
    returned `abc#x` with projected `self_true=0`.
  - CASE scalar reference returned `abc_def` with projected `self_true=1`.
  - Default-escape control was green: fast/reference both returned `abc#x`.
  - `SHOW TABLES LIKE ... ESCAPE` was syntax-invalid and classified as source-screen INVALID, not
    a bug.
- Source:
  - `pkg/planner/core/memtable_infoschema_extractor.go:215-235`: extracts and compiles InfoSchema
    `LIKE` regexps, then removes the predicate.
  - `pkg/planner/core/memtable_infoschema_extractor.go:277-285`: filters rows by compiled regexp.
  - `pkg/planner/core/memtable_predicate_extractor.go:439-465`: extracts only the pattern string.
  - `pkg/util/stringutil/string_util.go:260-263`: `CompileLike2Regexp` hardcodes default escape.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-infoschema-like-escape-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id30031-method-case.md`
- Method lesson: this is a representative cross-owner blast-radius case for id30030. It proves
  the omitted-`ESCAPE` proof gap is not isolated to `cluster_log`, but it also closes the family
  for now: do not enumerate every InfoSchema table using the same helper.

## P0: EXCHANGE PARTITION Masking-Policy Orphan (id630014, S4)

- Status: **ISSUE-FILED**, inserted into remote `found_bug` as id630014
  (`MAX(id)=630014,COUNT=46`).
- GitHub issue: https://github.com/pingcap/tidb/issues/69754
- Proof obligation:
  - `P_check`: `checkExchangePartition` validates table shape and rejects a few known unsupported
    owner states, but does not inspect masking-policy side metadata.
  - `Q_claim`: after the table/partition ID swap, every masking-policy row still resolves to the
    logical owner named by its public DDL surface, or the exchange was blocked.
  - `D_dim`: masking policies store both logical names and physical IDs; `EXCHANGE PARTITION`
    swaps the standalone table ID with a partition physical ID.
  - `F_effect`: masking-policy management DDL looks up the current logical table ID, so a row left
    on the old ID becomes unreachable even though it still says `table_name=nt`.
- Evidence on testbed `8192975`:
  - `CREATE MASKING POLICY mp_nt ON nt(a) AS a ENABLE` created an operable policy.
  - Before exchange, `ALTER TABLE nt DISABLE/ENABLE MASKING POLICY mp_nt` worked.
  - After `ALTER TABLE pt EXCHANGE PARTITION p0 WITH TABLE nt`, the policy row kept
    `table_name=nt` but its `table_id` matched `pt.p0`'s `tidb_partition_id`, not the current
    `nt` `tidb_table_id`.
  - `ALTER TABLE nt DISABLE MASKING POLICY mp_nt` and
    `ALTER TABLE nt DROP MASKING POLICY mp_nt` both failed with
    `ERROR 1105: masking policy mp_nt doesn't exist`; `ALTER TABLE pt ...` also failed.
  - Recreating `mp_nt` on `nt` created a second same-name row on the new `nt` table ID; disabling
    it changed only the new row and left the old row `ENABLED`.
- Source:
  - `pkg/ddl/executor.go:3119-3136`: `checkExchangePartition` does not reject masking policies.
  - `pkg/ddl/executor.go:3035-3110`: table-definition compatibility does not compare
    masking-policy side state.
  - `pkg/ddl/partition.go:2766-3055`: `onExchangeTablePartition` swaps `partDef.ID` and `nt.ID`.
  - `pkg/ddl/table.go:566-568`: truncate has `updateMaskingPolicyTableIDAfterTruncate`.
  - `pkg/ddl/table.go:830-839,881-889`: rename paths update masking-policy names.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-exchange-partition-masking-policy-orphan-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630014-method-case.md`
- Method lesson: S4 is strongest when a basic owner matrix is green but source shows a sibling
  move/rekey path that changes the same ID/name binding without calling the owner-specific helper.
  The oracle must prove operability before the move and management behavior after the move; a
  surviving sys-table row is not enough.

## P0: MODIFY COLUMN Reorg CHECK Bypass (id630013, S17)

- Status: **ISSUE-FILED**, inserted into remote `found_bug` as id630013
  (`MAX(id)=630013,COUNT=45`).
- GitHub issue: https://github.com/pingcap/tidb/issues/69649
- Proof obligation:
  - `P_check`: existing rows satisfied the CHECK constraint under the old column type, and
    `CastColumnValue` succeeds during MODIFY COLUMN backfill.
  - `Q_claim`: converted rows still satisfy writable CHECK constraints under the new column type.
  - `D_dim`: lossy-but-successful type conversion can change predicate truth value, for example
    `DECIMAL(10,2)` value `0.40` or `DOUBLE`/`VARCHAR` value `0.4` becomes `INT` value `0`.
  - `F_effect`: the MODIFY COLUMN reorg path encodes the converted row and writes it directly with
    `txn.Set`, bypassing the row constraint checks used by DML.
- Evidence on testbed `8192975`:
  - `CREATE TABLE t(a DECIMAL(10,2), CONSTRAINT c CHECK (a > 0)); INSERT (0.4),(1.2);`
    produced rows where `a > 0` was true before ALTER.
  - `ALTER TABLE t MODIFY a INT` succeeded and `SHOW WARNINGS` was empty.
  - After ALTER, `SELECT a, a > 0 AS ok FROM t` returned `0,0` and `1,1`, while
    `SHOW CREATE TABLE t` still showed `CHECK ((a > 0))`.
  - The same witness reproduced from `VARCHAR('0.4')` and `DOUBLE(0.4)` to `INT`.
  - Direct reference `ALTER TABLE ref ADD CONSTRAINT c_ref CHECK (a > 0)` on an `INT` table
    containing `0` rejected with `ERROR 3819`; ordinary `INSERT INTO t VALUES (0)` also rejected
    with `ERROR 3819`.
  - `ADMIN CHECK TABLE t` returned success, so this is outside the normal index/record checker.
- Source:
  - `pkg/ddl/constraint.go:354-389`: `ADD CHECK` scans existing rows via
    `verifyRemainRecordsForCheckConstraint`.
  - `pkg/table/tables/tables.go:508-510`: ordinary `UpdateRecord` calls CHECK validation.
  - `pkg/table/tables/tables.go:888`: ordinary `AddRecord` calls CHECK validation.
  - `pkg/ddl/column.go:754-815`: `updateColumnWorker.getRowRecord` casts the old value to the new
    column type and encodes the new row.
  - `pkg/ddl/column.go:847-863`: the reorg transaction writes converted row bytes with `txn.Set`.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-check-constraint-modify-column-reorg-bypass-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630013-method-case.md`
- Method lesson: S17 extends the proof-obligation frame from schema validators to data reorg
  writers. Any DDL backfill that decodes rows, transforms values, and writes through a raw path
  must re-prove row invariants on the post-conversion row. The efficient seed is not "all type
  conversions"; it is "old predicate true, converted predicate false, conversion succeeds".

## P0: FK Signedness Target-State Ordering Gap (id630012, S16)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id630012
  (`MAX(id)=630012,COUNT=44`).
- Proof obligation:
  - `P_check`: `checkModifyColumnWithForeignKeyConstraint` sees unchanged type, flen, and decimal,
    then returns nil.
  - `Q_claim`: the final modified FK column remains compatible with the related FK column.
  - `D_dim`: integer signedness is part of FK compatibility and cascade write safety, but it is not
    included in the type/flen/decimal early-return predicate.
  - `F_effect`: MODIFY COLUMN publishes a target schema that direct CREATE/ADD FK rejects.
- Evidence on testbed `8192975`:
  - Direct parent `INT` / child `INT UNSIGNED` FK with `ON UPDATE CASCADE` rejected with
    `ERROR 3780`.
  - Valid signed/signed FK control cascaded parent update `1 -> -1` successfully; both parent and
    child rows became `-1`.
  - Valid signed/signed FK, then `ALTER TABLE c_red MODIFY COLUMN a INT UNSIGNED`, succeeded with
    no warning.
  - `SHOW CREATE TABLE c_red` showed `a int unsigned` while the FK still referenced signed parent
    `p_red(a)`.
  - Parent `UPDATE p_red SET a=-1 WHERE a=1` after the red ALTER failed with `ERROR 1264 Out of
    range value for column 'a'`.
  - After `DROP FOREIGN KEY`, adding the same FK back failed with `ERROR 3780`, proving the final
    metadata is rejected by the target-state validator.
- Source:
  - `pkg/ddl/foreign_key.go:284-288`: CREATE/ADD FK compatibility checks type, unsigned flag,
    charset, and collation.
  - `pkg/ddl/foreign_key.go:301-304`: MODIFY FK check returns early on unchanged type/flen/decimal.
  - `pkg/ddl/modify_column.go:1912`: the FK check runs before later target-state effects are fully
    validated against FK compatibility.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-fk-signedness-modify-unsigned-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630012-method-case.md`
- Method lesson: S16 is not just "options applied after validation". Its sharper form is
  `P_coarse -> Q_rich`: code checks a coarse tuple and treats it as proof of a richer target-state
  invariant. The audit must list omitted dimensions and then prove whether a later validator covers
  each dimension. In this turn, signedness was red, while collation and primary-key NULL were green
  because later validators covered them.

## P0: FK SET NULL Target-State Ordering Gap (id630011, S16)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id630011
  (`MAX(id)=630011,COUNT=43`).
- Proof obligation:
  - `P_check`: `checkModifyColumnWithForeignKeyConstraint` sees unchanged type, flen, and decimal,
    then returns nil.
  - `Q_claim`: the final modified child column remains compatible with existing FK actions.
  - `D_dim`: `NOT NULL` is applied after that FK check, and nullability is required by
    `ON DELETE SET NULL` / `ON UPDATE SET NULL`.
  - `F_effect`: MODIFY COLUMN publishes a target schema that direct CREATE/ADD FK rejects.
- Evidence on testbed `8192975`:
  - Direct `pid INT NOT NULL` child FK with `ON DELETE SET NULL` or `ON UPDATE SET NULL` rejected
    with `ERROR 1830`.
  - Valid nullable child FK with `ON DELETE SET NULL`, then
    `ALTER TABLE c_del MODIFY COLUMN pid INT NOT NULL`, succeeded with no warning.
  - Same for `ON UPDATE SET NULL`.
  - Parent `DELETE`/`UPDATE` after the red ALTER failed with `ERROR 1048 Column 'pid' cannot be
    null`.
  - `ON DELETE RESTRICT` control accepted nullable->NOT NULL, proving the red cell depends on the
    SET NULL action.
- Source:
  - `pkg/ddl/foreign_key.go:301-304`: FK modify check returns early on unchanged type/flen/decimal.
  - `pkg/ddl/modify_column.go:1912`: FK check runs before column options are processed.
  - `pkg/ddl/modify_column.go:1924` and `:2318-2320`: `ProcessModifyColumnOptions` later applies
    `ColumnOptionNotNull`.
  - `pkg/ddl/executor.go:5329-5330`: CREATE/ADD FK path already rejects NOT NULL child columns
    when the referential action is SET NULL.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-fk-set-null-modify-not-null-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630011-method-case.md`
- Method lesson: transition validators must run on complete target state. If a validator consumes
  a partially built object, every later mutation is a missing proof dimension until compared
  against a sibling target-state validator and a behavior oracle.

## P0: ADD IF NOT EXISTS Table-Element List Flag Loss (id630010, S15)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id630010
  (`MAX(id)=630010,COUNT=42`).
- Proof obligation:
  - `P_check`: parser accepts `ALTER TABLE ... ADD IF NOT EXISTS (...)` and records the flag on the
    parent `AlterTableSpec`.
  - `Q_claim`: duplicate table elements produced from that accepted list should use idempotent
    semantics, or unsupported flagged constraint forms should be rejected before execution.
  - `D_dim`: the flag must survive representation changes: table-element-list parsing,
    `ResolveAlterTableSpec` splitting, and child constraint execution.
  - `F_effect`: constraints split from `NewConstraints` become `AlterTableAddConstraint` jobs, but
    index execution reads only `constr.IfNotExists` and CHECK execution never checks
    `spec.IfNotExists`.
- Evidence on testbed `8192975`:
  - `ALTER TABLE idx_outer ADD IF NOT EXISTS (KEY idx_a(a))` succeeded the first time; the same
    statement failed on retry with `ERROR 1061 Duplicate key name 'idx_a'`.
  - `ALTER TABLE ck_outer ADD IF NOT EXISTS (CONSTRAINT ck_a CHECK (a > 0))` succeeded the first
    time; the same statement failed on retry with `ERROR 3822 Duplicate check constraint name
    'ck_a'`.
  - Green controls:
    `ALTER TABLE col_outer ADD IF NOT EXISTS (b INT)` retried successfully with `Note 1060`, and
    `ALTER TABLE idx_inner ADD IF NOT EXISTS (KEY IF NOT EXISTS idx_a(a))` retried successfully with
    `Note 1061`.
  - Schema-count controls showed one index and one CHECK constraint after the red cells, so this is
    wrong-error rather than duplicate metadata insertion.
- Source:
  - `pkg/parser/parser.y`: `ADD ColumnKeywordOpt IfNotExists '(' TableElementList ')'` stores the
    outer flag as `spec.IfNotExists`.
  - `pkg/ddl/executor.go`: `resolveAlterTableAddColumns` splits `NewConstraints` into
    `AlterTableAddConstraint`.
  - `pkg/ddl/add_column.go`: duplicate column checks honor `spec.IfNotExists`.
  - `pkg/ddl/executor.go`: the index constraint branch passes only `constr.IfNotExists`, and the
    CHECK branch does not consult `spec.IfNotExists`.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-add-if-not-exists-table-element-list-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630010-method-case.md`
- Method lesson: S15 must audit parser/spec splitting and AST rewrites, not only final executor
  sibling dispatch. If a parent DDL node owns a flag, every child job created from that node needs a
  deliberate flag-ownership rule.

## P0: DROP PARTITION IF EXISTS Count Precheck Ordering (id630015, S15)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id630015
  (`MAX(id)=630015,COUNT=50`).
- Proof obligation:
  - `P_check`: `CheckDropTablePartition` checks
    `len(current partitions) <= len(requested names)` before checking name existence.
  - `Q_claim`: if requested-name count reaches current partition count, the DDL would remove every
    existing partition.
  - `D_dim`: under `IF EXISTS`, missing names should not count as existing partitions to remove.
  - `F_effect`: `ErrDropLastPartition` is returned before executor can convert
    `ErrDropPartitionNonExistent` into a note for `spec.IfExists`.
- Evidence on testbed `8192975`:
  - One-partition table plus `ALTER TABLE onep DROP PARTITION IF EXISTS px` returned
    `ERROR 1508 Cannot remove all partitions`, even though `px` is missing.
  - Two-partition table plus the same missing-name statement returned `Note 1507` and left `p0,p1`
    unchanged.
  - Real last-partition control stayed green:
    `ALTER TABLE onep DROP PARTITION IF EXISTS p0` returned `ERROR 1508`.
  - Real existing partition control stayed green:
    `ALTER TABLE twop DROP PARTITION IF EXISTS p0` removed `p0` and left `p1`.
  - Boundary siblings `px,py` and `p0,px` on a two-partition table returned `ERROR 1508`, matching
    the raw requested-name count root cause.
- Source:
  - `pkg/parser/parser.y`: `DROP PARTITION IfExists PartitionNameList` stores `spec.IfExists`.
  - `pkg/ddl/executor.go:2956-2961`: executor catches only `ErrDropPartitionNonExistent` after
    the shared checker returns.
  - `pkg/ddl/partition.go:2027-2043`: count precheck returns `ErrDropLastPartition` before the
    existence loop.
  - `pkg/ddl/db_change_test.go:1508-1528`: current IF EXISTS coverage drops existing `p1` from a
    three-partition table; it does not cover missing names or count boundaries.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-drop-partition-if-exists-count-precheck-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630015-method-case.md`
- Method lesson: S15 is not only "flag lost while dispatching." A flag can survive but still be
  too late if it only catches a later existence error. Idempotence audits must inspect earlier
  prechecks that reason over raw requested names/counts before resolving which objects exist.

## P0: ADD PARTITION IF NOT EXISTS DEFAULT Gate Ordering (id630016, S15)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id630016
  (`MAX(id)=630016,COUNT=51`).
- Proof obligation:
  - `P_check`: `AddTablePartitions` checks whether the current LIST table already has a DEFAULT
    partition.
  - `Q_claim`: if the table has DEFAULT, every ADD LIST PARTITION request must fail and use
    `REORGANIZE PARTITION`.
  - `D_dim`: `IF NOT EXISTS` changes the existence/duplicate dimension. If the requested partition
    name already exists, the operation should be classified as idempotent before capability checks
    for genuinely new partitions decide whether ADD is supported.
  - `F_effect`: the executor returns ERROR 8200 before combining old/new definitions and before
    reaching the `ErrSameNamePartition && spec.IfNotExists` note path.
- Evidence on testbed `8192975`:
  - LIST table without DEFAULT, duplicate `ADD PARTITION IF NOT EXISTS p0`: Note 1517, old
    partitions `p0,p1` remained.
  - LIST table with DEFAULT, duplicate `ADD PARTITION IF NOT EXISTS p0`: ERROR 8200
    `Unsupported ADD List partition, already contains DEFAULT partition`.
  - LIST table with DEFAULT, new `ADD PARTITION IF NOT EXISTS p1`: ERROR 8200, expected
    capability-control.
  - LIST table without DEFAULT, new `ADD PARTITION IF NOT EXISTS p1`: success.
- Source:
  - `pkg/ddl/executor.go:2300-2304`: LIST DEFAULT capability gate returns ERROR 8200.
  - `pkg/ddl/executor.go:2307-2318`: duplicate-name classification happens later and is the only
    place that converts `ErrSameNamePartition` to an IF NOT EXISTS note.
  - `pkg/ddl/partition.go:5215-5219`: combined partition definition checker detects duplicate
    names first, but it is unreachable when the earlier DEFAULT gate fires.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-add-partition-if-not-exists-default-precheck-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630016-method-case.md`
- Method lesson: S15 now includes capability gates before existence classification. The audit
  question is: before the duplicate/missing-object catch, does any precheck reject the operation
  for a reason that is irrelevant if the requested object already exists?

## P0: DROP INDEX IF EXISTS PRIMARY Special-Name Ordering (id630017, S15)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id630017
  (`MAX(id)=630017,COUNT=57`).
- Proof obligation:
  - `P_check`: `dropIndex` calls `CheckIsDropPrimaryKey` when the requested index name is
    `PRIMARY`.
  - `Q_claim`: this request should be handled by primary-key drop rules.
  - `D_dim`: on a table without a primary key, the requested `PRIMARY` index/key is absent, and
    `IF EXISTS` should classify it as an idempotent no-op.
  - `F_effect`: `CheckIsDropPrimaryKey` returns `ErrCantDropFieldOrKey` before the generic
    `indexInfo == nil && ifExist` note path.
- Evidence on testbed `8192975`:
  - `ALTER TABLE no_pk DROP INDEX IF EXISTS missing_i` returned `Note 1091` and no-op.
  - ``ALTER TABLE no_pk DROP INDEX IF EXISTS `PRIMARY``` returned
    `ERROR 1091 Can't DROP 'PRIMARY'`.
  - ``DROP INDEX IF EXISTS `PRIMARY` ON no_pk`` returned the same `ERROR 1091`.
  - ``ALTER TABLE pk_nc DROP INDEX IF EXISTS `PRIMARY``` on a real `PRIMARY KEY NONCLUSTERED`
    succeeded and removed the index.
- Source:
  - `pkg/parser/parser.y:2832-2838`: ALTER TABLE DROP INDEX stores `spec.IfExists`.
  - `pkg/parser/parser.y:5718-5729`: top-level DROP INDEX stores `stmt.IfExists`.
  - `pkg/ddl/executor.go:5518-5521`: `CheckIsDropPrimaryKey` error is returned before
    missing-index handling.
  - `pkg/ddl/executor.go:5527-5533`: generic missing-index handler would convert to a note when
    `ifExist` is set, but is bypassed.
  - `pkg/ddl/executor.go:5571-5582`: missing `PRIMARY` triggers `ErrCantDropFieldOrKey`.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-drop-index-primary-if-exists-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630017-method-case.md`
- Method lesson: S15 now includes special-name classifiers before existence classification. The
  audit should look for helpers that interpret names such as `PRIMARY` before the generic
  idempotence safe path has classified whether the object exists.

## P0: CREATE TABLE IF NOT EXISTS Candidate Prevalidation (id630018, S15)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id630018
  (`MAX(id)=630018,COUNT=58`).
- Proof obligation:
  - `P_check`: candidate source/metadata for `CREATE TABLE` validates successfully.
  - `Q_claim`: TiDB may proceed to create the table.
  - `D_dim`: with `IF NOT EXISTS` and an already-existing target table, the candidate definition
    is discarded and should not affect the no-op.
  - `F_effect`: source resolution, `BuildTableInfo`, and partition/index validation return before
    `createTableWithInfoJob` reaches the target-exists `OnExistIgnore` note path.
- Evidence on testbed `8192975`:
  - Existing target plus `CREATE TABLE IF NOT EXISTS t(b BIGINT,c VARCHAR(60))`: Note 1050 and
    target unchanged.
  - Existing target plus `CREATE TABLE IF NOT EXISTS t LIKE src`: Note 1050 and target unchanged.
  - Existing target plus `CREATE TABLE IF NOT EXISTS t LIKE missing_src`: ERROR 1146.
  - Existing target plus `CREATE TABLE IF NOT EXISTS t(a INT, INDEX idx_b(b))`: ERROR 1072.
  - Existing target plus `CREATE TABLE IF NOT EXISTS t(a INT) PARTITION BY RANGE(b) ...`:
    ERROR 1054.
  - Target-absent invalid source/index controls returned the same hard errors.
  - Existing target plus duplicate-column candidate returned Note 1050, calibrating that only
    validators before the target-exists check are suspect.
- Source:
  - `pkg/ddl/executor.go:1015-1024`: `CREATE TABLE ... LIKE` source table is resolved before
    target existence is checked.
  - `pkg/ddl/executor.go:1032-1041`: candidate `TableInfo` is built before target existence is
    checked.
  - `pkg/ddl/executor.go:1044-1069`: partition/split/FK candidate validations run before
    `OnExistIgnore`.
  - `pkg/ddl/executor.go:1072-1077`: `OnExistIgnore` is set only after those validations.
  - `pkg/ddl/executor.go:1100-1113`: target-exists no-op appends Note 1050 inside
    `createTableWithInfoJob`.
  - `pkg/ddl/db_integration_test.go:59-84`: existing test covers valid duplicate
    `CREATE TABLE IF NOT EXISTS ... LIKE` and ordinary duplicate create, but not missing-source or
    invalid-candidate prechecks.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-create-table-if-not-exists-prevalidation-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630018-method-case.md`
- Method lesson: for create-like idempotence, the existence classifier is target existence, not
  child object existence. Any candidate-source or candidate-metadata validation before target
  existence can be a wrong-error selector.

## P0: CREATE SEQUENCE IF NOT EXISTS Candidate Prevalidation (id630019, S15)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id630019
  (`MAX(id)=630019,COUNT=59`).
- Proof obligation:
  - `P_check`: candidate sequence options validate successfully.
  - `Q_claim`: TiDB may proceed to create the sequence.
  - `D_dim`: with `IF NOT EXISTS` and an already-existing target sequence, candidate options are
    discarded and should not affect the no-op.
  - `F_effect`: `buildSequenceInfo` returns sequence option/table option errors before
    `createTableWithInfoJob` reaches the target-exists `OnExistIgnore` note path.
- Evidence on testbed `8192975`:
  - Existing sequence plus valid duplicate options: Note 1050 and old definition unchanged.
  - Existing sequence plus `INCREMENT 0`: ERROR 4136.
  - Existing sequence plus `MAXVALUE 1 START WITH 2`: ERROR 4136.
  - Existing sequence plus `CHARSET=utf8`: ERROR 8227.
  - Target-absent invalid option controls returned the same hard errors.
  - After failed duplicate attempts, existing `seq` remained unchanged and `new_seq_bad*` objects
    were absent.
- Source:
  - `pkg/ddl/executor.go:6067-6071`: `CreateSequence` calls `buildSequenceInfo` before
    `OnExistIgnore`.
  - `pkg/ddl/executor.go:6080-6085`: `OnExistIgnore` is set only after candidate sequence info is
    built.
  - `pkg/ddl/executor.go:1100-1113`: target-exists no-op appends Note 1050 inside
    `createTableWithInfoJob`.
  - `pkg/ddl/sequence.go:142-160`: sequence option validation rejects invalid candidate values.
  - `pkg/ddl/sequence.go:171-184`: unsupported table options and invalid sequence options return
    before target existence is checked.
  - `pkg/ddl/sequence_test.go:32-61`: existing tests cover invalid new sequence definitions, but
    not target-exists plus invalid candidate options.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-create-sequence-if-not-exists-prevalidation-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630019-method-case.md`
- Method lesson: id630019 validates id630018's create-like selector across a second owner. The
  target-exists classifier is shared in `CreateTableWithInfo`, while the candidate builder is
  owner-specific.

## P0: CREATE RESOURCE GROUP IF NOT EXISTS Candidate Builder Prevalidation (id630020, S15)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id630020
  (`MAX(id)=630020,COUNT=60`).
- Proof obligation:
  - `P_check`: candidate resource-group options are built successfully.
  - `Q_claim`: TiDB may proceed to create the resource group.
  - `D_dim`: with `IF NOT EXISTS` and an already-existing target resource group, candidate options
    are discarded and should not affect the no-op.
  - `F_effect`: `buildResourceGroup` rejects `BACKGROUND` for non-default resource groups before
    `AddResourceGroup` reaches the target-exists `IF NOT EXISTS` Note 8248 path.
- Evidence on testbed `8192975`:
  - `tidb_enable_resource_control=ON`.
  - Existing resource group `ai_rg_s15 RU_PER_SEC=1000`.
  - Existing target plus valid duplicate
    `CREATE RESOURCE GROUP IF NOT EXISTS ai_rg_s15 RU_PER_SEC=2000`: Note 8248 and old definition
    unchanged.
  - Existing target plus `CREATE RESOURCE GROUP IF NOT EXISTS ai_rg_s15 BACKGROUND=()`:
    ERROR 1105 `unsupported operation. Currently, only the default resource group support change
    background settings`.
  - Target-absent control `CREATE RESOURCE GROUP IF NOT EXISTS ai_rg_s15_absent BACKGROUND=()`:
    same ERROR 1105 and no new group.
- Source:
  - `pkg/ddl/executor.go:6350-6355`: `AddResourceGroup` calls `buildResourceGroup` before
    target existence is checked.
  - `pkg/ddl/executor.go:6358-6364`: the `IF NOT EXISTS` duplicate Note 8248 path is later.
  - `pkg/ddl/resource_group.go:185-197`: `buildResourceGroup` iterates candidate options.
  - `pkg/ddl/resource_group.go:244-248`: `BACKGROUND` is rejected for any non-default resource
    group.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-create-resource-group-if-not-exists-background-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630020-method-case.md`
- Method lesson: candidate builders and option setters can be semantic validators. For S15,
  audit every resolver/builder/setter before the target-exists classifier, not only functions named
  `validate*`.
- Negative calibration:
  - `CREATE VIEW IF NOT EXISTS` is parser-unsupported in this build.
  - `CREATE PLACEMENT POLICY IF NOT EXISTS` checks existence before `checkPolicyValidation`, so the
    obvious invalid-option shape is a green control.

## P0: CREATE MASKING POLICY IF NOT EXISTS Candidate Expression Prevalidation (id630021, S15)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id630021
  (`MAX(id)=630021,COUNT=61`).
- Proof obligation:
  - `P_check`: candidate masking-policy expression references only the target column.
  - `Q_claim`: TiDB may proceed to create the masking policy.
  - `D_dim`: with `IF NOT EXISTS` and the same policy already existing on the same table column,
    the candidate expression is discarded and should not affect the no-op.
  - `F_effect`: `buildMaskingPolicyInfo` validates the candidate expression before
    `CreateMaskingPolicy` selects `OnExistIgnore` and before `createMaskingPolicyWithInfo` reaches
    the duplicate-policy note path.
- Evidence on testbed `8192975`:
  - Existing policy: `CREATE MASKING POLICY p_mp ON t(a) AS a ENABLE`.
  - Existing same policy/table/column plus valid candidate
    `CREATE MASKING POLICY IF NOT EXISTS p_mp ON t(a) AS concat(a,'_x') DISABLE`: Note 1105 and
    old expression/status unchanged.
  - Existing same policy/table/column plus invalid candidate
    `CREATE MASKING POLICY IF NOT EXISTS p_mp ON t(a) AS b DISABLE`: ERROR 8275 `masking policy
    expression can only reference the target column 'a'`.
  - Target-absent control `CREATE MASKING POLICY IF NOT EXISTS p_absent ON t(a) AS b DISABLE`:
    same ERROR 8275 and no new policy row.
- Source:
  - `pkg/ddl/executor.go:6477-6507`: `CreateMaskingPolicy` resolves table and calls
    `buildMaskingPolicyInfo` before selecting `OnExistIgnore`.
  - `pkg/ddl/executor.go:6509-6518`: `OnExistIgnore` is selected only after candidate policy info
    is built.
  - `pkg/ddl/masking_policy.go:344-370`: `buildMaskingPolicyInfo` finds column and validates the
    candidate expression.
  - `pkg/ddl/masking_policy.go:409-420`: `validateMaskingPolicyExpression` rejects non-target
    column references with ERROR 8275.
  - `pkg/ddl/masking_policy_test.go:71-81`: existing test covers concurrent valid
    `IF NOT EXISTS`, but not an existing target with invalid unused expression.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-create-masking-policy-if-not-exists-expression-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630021-method-case.md`
- Method lesson: id630021 proves expression validators inside metadata builders belong in the S15
  pre-classifier audit. For policy-like owners, first pin identity to the same name/table/column;
  then change only the discarded candidate payload.

## P0: CREATE SPATIAL INDEX IF NOT EXISTS Capability Gate Ordering (id630022, S15)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id630022
  (`MAX(id)=630022,COUNT=62`).
- Proof obligation:
  - `P_check`: requested index type is supported.
  - `Q_claim`: TiDB may proceed to create the index.
  - `D_dim`: with `IF NOT EXISTS` and an already-existing same-name index on the same table, the
    candidate index type is discarded and should not affect the no-op.
  - `F_effect`: `createIndex` rejects `SPATIAL` before `checkIndexNameAndColumns` can classify the
    duplicate index name and append Note 1061.
- Evidence on testbed `8192975`:
  - Existing index: `CREATE INDEX idx_a ON t(a)`.
  - Existing target plus valid duplicate candidate `CREATE INDEX IF NOT EXISTS idx_a ON t(b)`:
    Note 1061 and `SHOW INDEX` still has only `idx_a` on column `a`.
  - Existing target plus unsupported candidate type
    `CREATE SPATIAL INDEX IF NOT EXISTS idx_a ON t(a)`: ERROR 8200 `SPATIAL index is not
    supported`.
  - Target-absent control `CREATE SPATIAL INDEX IF NOT EXISTS idx_sp_absent ON t(a)`: same
    ERROR 8200 and no new index.
- Source:
  - `pkg/parser/parser_test.go:3469`: parser accepts `CREATE SPATIAL INDEX IF NOT EXISTS`.
  - `pkg/ddl/executor.go:5065-5070`: `createIndex` checks `keyType` and returns unsupported
    SPATIAL error before table/index duplicate handling.
  - `pkg/ddl/executor.go:5085-5091`: `checkIndexNameAndColumns` is the later duplicate-name
    no-op path for `IF NOT EXISTS`.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-create-spatial-index-if-not-exists-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630022-method-case.md`
- Method lesson: early capability `switch` blocks are part of the S15 pre-classifier audit. The
  ordinary duplicate control proves same-table same-name index identity dominates candidate column
  list, so the SPATIAL red cell is not a different-object ambiguity.
- Negative calibration:
  - `CREATE DATABASE IF NOT EXISTS` is green by source order: schema existence is checked before
    charset/collation and placement validation.
  - Do not enumerate other index types from this hit.

## P0: CREATE USER IF NOT EXISTS Account Attribute Prevalidation (id1020001, S15)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id1020001
  (`MAX(id)=1020001,COUNT=63`).
- Proof obligation:
  - `P_check`: `PASSWORD EXPIRE` is invalid for anonymous users.
  - `Q_claim`: TiDB may reject the statement before creating or modifying a user.
  - `D_dim`: with `IF NOT EXISTS` and the exact same username+host already present, candidate
    account attributes are discarded and should not affect the no-op.
  - `F_effect`: `executeCreateUser` checks `len(username)==0 && passwordExpired=="Y"` before
    calling `userExists`, so the duplicate user classifier and Note 3163 path are unreachable.
- Evidence on testbed `8192975`:
  - Existing account: `CREATE USER ''@'ai_s15_host'`.
  - Existing target plus valid duplicate candidate `CREATE USER IF NOT EXISTS ''@'ai_s15_host'`:
    Note 3163 and `mysql.user.Password_expired=N,Account_locked=N`.
  - Existing target plus unused invalid account attribute
    `CREATE USER IF NOT EXISTS ''@'ai_s15_host' PASSWORD EXPIRE`: ERROR 3016 `The password for
    anonymous user cannot be expired`.
  - Target-absent control `CREATE USER IF NOT EXISTS ''@'ai_s15_host' PASSWORD EXPIRE`: same
    ERROR 3016 and no `mysql.user` row.
- Source:
  - `pkg/executor/simple.go:1099`: `plOptions.loadOptions` records `PASSWORD EXPIRE`.
  - `pkg/executor/simple.go:1176-1177`: anonymous-user password-expire validation returns
    `ErrPasswordExpireAnonymousUser`.
  - `pkg/executor/simple.go:1185-1198`: the later `userExists` / `IF NOT EXISTS` duplicate no-op
    path appends Note 3163.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-create-user-if-not-exists-password-expire-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id1020001-method-case.md`
- Method lesson: account DDL can use S15 only after identity pinning. Here identity is
  username+host; only the candidate account attribute changes. This prevents confusing an invalid
  target identity with an unused candidate payload.
- Negative calibration:
  - `ALTER SEQUENCE IF EXISTS` is green by source order: target existence is checked before option
    validation.
  - `ALTER RESOURCE GROUP IF EXISTS` is green by source order: group existence is checked before
    option build/validation.
  - Do not enumerate account options from this hit.

## P0: Partial-Index Condition Metadata-Only False Rejection (id630009, S11)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id630009
  (`MAX(id)=630009,COUNT=41`).
- Proof obligation:
  - `P_check`: `checkColumnReferencedByPartialCondition` detects that column `b` appears in a
    partial-index condition's `AffectColumn` set.
  - `Q_claim`: any `MODIFY COLUMN b` can invalidate that partial-index condition.
  - `D_dim`: metadata-only changes such as `COMMENT` and safe `DEFAULT` do not rename the column,
    change its type/collation/nullability, or change the condition expression or row membership.
  - `F_effect`: common `MODIFY COLUMN` rejects before distinguishing metadata-only changes from
    semantic changes.
- Evidence on testbed `8192975`:
  - Direct target `CREATE TABLE direct_comment(a INT, b INT COMMENT 'new-comment', c INT,
    INDEX idx_a(a) WHERE b > 0)` succeeded, returned 2 matching rows through the partial index,
    and `ADMIN CHECK TABLE` passed.
  - Direct target `b INT DEFAULT 5` with the same partial index succeeded; default insert returned
    `b=5`, and `ADMIN CHECK TABLE` passed.
  - Existing table `CREATE TABLE t_comment(a INT,b INT,c INT,INDEX idx_a(a) WHERE b > 0)` failed on
    `ALTER TABLE t_comment MODIFY COLUMN b INT COMMENT 'new-comment'` with `ERROR 8272`.
  - Existing table `t_default` failed on `ALTER TABLE t_default MODIFY COLUMN b INT DEFAULT 5` with
    the same error.
  - Green controls: modifying non-condition column `c` succeeded; dropping `idx_a` before modifying
    `b` succeeded.
- Source:
  - `pkg/ddl/index.go`: partial-index validation stores condition columns in `idx.AffectColumn`.
  - `pkg/ddl/executor.go`: `checkColumnReferencedByPartialCondition` returns
    `ErrModifyColumnReferencedByPartialCondition` whenever the column appears in `idx.AffectColumn`.
  - `pkg/ddl/modify_column.go`: MODIFY calls that checker before operation semantics distinguish
    COMMENT/DEFAULT from rename/type/collation/nullability changes.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-partial-index-metadata-modify-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630009-method-case.md`
- Method lesson: S11 generalizes beyond generated/functional-index hidden-column dependency gates.
  The useful selector is "dependency existence is used as semantic-change proof"; the matrix should
  stay isomorphic: direct target, metadata-only red cell, dependency-absent green, dependency-removed
  green.

## P0: ADD FOREIGN KEY IF NOT EXISTS Idempotence Flag Drop (id630008, S15)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id630008 (`MAX(id)=630008,COUNT=40`).
- Proof obligation:
  - `P_check`: parser accepts `IF NOT EXISTS` on `ADD FOREIGN KEY` and stores it on the
    constraint AST.
  - `Q_claim`: duplicate existing FK name should take the idempotent success/note path when the
    flag is present.
  - `D_dim`: sibling DDL branches pass the same idempotence flag into their execution helper.
  - `F_effect`: the FK branch drops `constr.IfNotExists`, calls `CreateForeignKey`, and reaches
    `checkFKDupName`, so the duplicate uses the hard-error path.
- Evidence on testbed `8192975`:
  - First `ALTER TABLE c ADD CONSTRAINT fk_pid FOREIGN KEY IF NOT EXISTS (pid) REFERENCES p(id)`
    succeeded.
  - Re-running the same statement failed with
    `ERROR 1826 (HY000): Duplicate foreign key constraint name 'fk_pid'`.
  - `information_schema.referential_constraints` showed exactly one FK row, so this is a
    wrong-error/idempotence failure rather than duplicate metadata insertion.
  - Plain duplicate `ADD FOREIGN KEY` without `IF NOT EXISTS` also failed with 1826, which is the
    expected hard-error control.
  - Sibling `ALTER TABLE idx_t ADD INDEX IF NOT EXISTS idx_a(a)` returned `Note 1061 Duplicate key
    name 'idx_a'` and preserved one index.
- Source:
  - `pkg/ddl/executor.go`: index and columnar index branches pass `constr.IfNotExists`; FK branch
    has a comment saying the IF NOT EXISTS check is ignored and calls `CreateForeignKey` directly.
  - `pkg/ddl/foreign_key.go`: `checkAddForeignKeyValidInOwner` calls `checkFKDupName`, which returns
    `ErrFkDupName`.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-fk-if-not-exists-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630008-method-case.md`
- Method lesson: DDL grammar flags are proof obligations. If a parser bit is implemented in one
  sibling owner and dropped in another, a tiny idempotence matrix can expose wrong-error bugs.

## P0 Companion: Expression-Index Dependency Metadata-Only False Rejection (id630007, S11)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id630007 (`MAX(id)=630007,COUNT=39`).
  Companion/blast-radius case for id630004; same common MODIFY root cause, distinct user-facing
  owner.
- Proof obligation:
  - `P_check`: `checkModifyColumnWithGeneratedColumnsConstraint` detects that a hidden generated
    column for an expression index references the base column.
  - `Q_claim`: any `MODIFY COLUMN` on that base column can invalidate the expression index.
  - `D_dim`: metadata-only changes such as `COMMENT` and safe `DEFAULT` do not rename the base
    column, change the expression, or change the base-column type/value domain.
  - `F_effect`: common `MODIFY COLUMN` rejects every non-nil dependency error after type checks,
    even when the direct target expression-index schema is valid.
- Evidence on testbed `8192975`:
  - Direct target `CREATE TABLE direct_comment(a INT COMMENT 'new-comment', INDEX idx_expr ((a+1)))`
    succeeded; inserted rows queried correctly and `ADMIN CHECK TABLE` passed.
  - `ALTER TABLE t_comment MODIFY COLUMN a INT COMMENT 'new-comment'` failed with
    `ERROR 3106` wrapping `ddl:3837`, even though the operation is not a drop or rename.
  - Direct target `a INT DEFAULT 5, INDEX idx_expr ((a+1))` succeeded; default insert returned
    `a=5, expr=6`.
  - `ALTER TABLE t_default MODIFY COLUMN a INT DEFAULT 5` failed with the same expression-index
    dependency error.
  - Green controls:
    non-dependent column comment change succeeded; dropping the expression index before modifying
    the base-column comment succeeded; true base-column type change remained rejected.
- Source:
  - `pkg/ddl/modify_column.go`: hidden generated columns cause
    `ErrDependentByFunctionalIndex`.
  - `pkg/ddl/modify_column.go`: rename uses the dependency error only when the column name changes.
  - `pkg/ddl/modify_column.go`: later unconditional `if errG != nil` rejects metadata-only changes.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-functional-index-metadata-modify-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630007-method-case.md`
- Method lesson: S11 generalized to a second dependency owner with the same tiny matrix. Count this
  honestly as owner/blast-radius validation, not a new root-cause family.

## P0: FLASHBACK TABLE Duplicate CHECK Constraint Namespace (id630006, S14)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id630006 (`MAX(id)=630006,COUNT=38`).
- Proof obligation:
  - `P_check`: `onRecoverTable` verifies the target table name and table ID are free.
  - `Q_claim`: the recovered `TableInfo` can be safely published into the current schema.
  - `D_dim`: CHECK constraint names live in a schema-level namespace, not a table-local namespace.
  - `F_effect`: `RecoverTable` publishes old CHECK metadata without running the
    `checkConstraintNamesNotExists` validator used by `CREATE TABLE` and `ADD CHECK`.
- Evidence on testbed `8192975`:
  - Normal duplicate control:
    `CREATE TABLE dup_explicit(a INT, CONSTRAINT base_chk_1 CHECK (a > 1))` failed with
    `ERROR 3822` when `base_chk_1` already existed.
  - Red cell:
    `CREATE TABLE f(a INT, CHECK(a > 0)); DROP TABLE f; CREATE TABLE f(a INT, CHECK(a > 1));
    FLASHBACK TABLE f TO f_old` succeeded.
  - Metadata evidence:
    `SHOW CREATE TABLE f` and `SHOW CREATE TABLE f_old` both showed
    `CONSTRAINT f_chk_1`; `information_schema.check_constraints` listed two `f_chk_1` rows with
    different clauses.
  - Runtime symptom:
    violating inserts into both `f` and `f_old` failed with
    `Check constraint 'f_chk_1' is violated`.
  - Sibling reconstruction control:
    `CREATE TABLE like_copy LIKE f` produced `like_copy_chk_1`.
- Source:
  - `pkg/executor/ddl.go`: `executeFlashbackTable` sets only `tblInfo.Name` for
    `FLASHBACK TABLE ... TO new_name`.
  - `pkg/ddl/table.go`: `onRecoverTable` checks table name and table ID but does not call
    `checkConstraintNamesNotExists`.
  - `pkg/ddl/create_table.go` and `pkg/ddl/constraint.go`: normal create/add paths call the
    schema-level CHECK-name uniqueness helper.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-flashback-check-duplicate-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630006-method-case.md`
- Method lesson: recovery DDL must re-prove current-schema namespace/reference invariants, not
  just object identity. Restored metadata was valid in the old schema snapshot; that does not prove
  it is still valid after other tables/constraints have been recreated.

## P0: CREATE TABLE LIKE Source CHECK Constraint Metadata Mutation (id630005, S13)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id630005 (`MAX(id)=630005,COUNT=37`).
- Proof obligation:
  - `P_check`: `BuildTableInfoWithLike` copies the source `TableInfo` to construct target table
    metadata.
  - `Q_claim`: target-only metadata normalization, such as regenerating CHECK constraint names,
    cannot affect the source table.
  - `D_dim`: `Constraints` is a slice of `*ConstraintInfo`; a top-level struct copy keeps nested
    pointer ownership shared.
  - `F_effect`: `renameCheckConstraint(&tblInfo)` mutates the shared `ConstraintInfo` objects while
    preparing the target table, so source `SHOW CREATE TABLE` can display the target constraint
    name.
- Evidence on testbed `8192975`:
  - Direct sibling control:
    `CREATE TABLE d1(a INT, CHECK(a > 0))` and `CREATE TABLE d2(a INT, CHECK(a > 0))` produced
    independent `d1_chk_1` and `d2_chk_1` names.
  - Red cell:
    `CREATE TABLE src_auto(a INT, CHECK(a > 0))` initially showed `src_auto_chk_1`; after
    `CREATE TABLE dst_auto LIKE src_auto`, a new connection showed `SHOW CREATE TABLE src_auto`
    with `CONSTRAINT dst_auto_chk_1`.
  - Runtime symptom:
    `INSERT INTO src_auto VALUES (-1)` failed with `Check constraint 'dst_auto_chk_1' is violated`.
  - Metadata inconsistency:
    `information_schema.check_constraints` still listed both `src_auto_chk_1` and `dst_auto_chk_1`.
- Source:
  - `pkg/ddl/create_table.go`: `BuildTableInfoWithLike` starts with `tblInfo := *referTblInfo`.
  - `pkg/ddl/create_table.go`: `renameCheckConstraint` mutates `cons.Name` and `cons.Table`.
  - `pkg/meta/model/table.go`: `ConstraintInfo.Clone` exists but is not used on the LIKE path's
    CHECK constraints before target rename.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-create-like-check-source-mutation-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630005-method-case.md`
- Method lesson: target reconstruction DDL must prove nested metadata ownership before mutating
  target-only fields. A top-level struct copy is not a source/target isolation proof when the
  object contains pointer-backed slices or maps.

## P0: CREATE TABLE LIKE READ ONLY Lock Clone (id1200001, S13 refinement)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id1200001
  (`COUNT(*)=70`, `COUNT(DISTINCT root_cause_id)=48`).
- Proof obligation:
  - `P_check`: `BuildTableInfoWithLike` shallow-copies source `TableInfo` and resets selected
    target-only fields.
  - `Q_claim`: any source field left on the target is schema definition that is safe for a new
    table.
  - `D_dim`: `TableInfo.Lock` is runtime table-lock state, not schema definition.
  - `F_effect`: the new target table is published with copied `READ ONLY` lock metadata, and
    `checkTableLocked` rejects writes to the target.
- Evidence on testbed `8192975`:
  - Red cell:
    `ALTER TABLE src READ ONLY; CREATE TABLE dst LIKE src; INSERT INTO dst VALUES (2)` returned
    `ERROR 8020 Table 'dst' was locked in READ ONLY ...`.
  - Isolation control:
    `ALTER TABLE dst READ WRITE; INSERT INTO dst VALUES (3)` succeeded, while
    `INSERT INTO src VALUES (3)` still returned `ERROR 8020`.
  - Cleanup control:
    cleaning only `dst` made `dst` writable while `src` remained read-only.
- Source:
  - `pkg/ddl/create_table.go:1249`: the LIKE path starts with `tblInfo := *referTblInfo`.
  - `pkg/ddl/create_table.go:1263-1298`: the path resets several fields but not `Lock`.
  - `pkg/ddl/executor.go:1786-1803`: `ALTER TABLE ... READ ONLY` maps to
    `TableLockReadOnly`.
  - `pkg/ddl/table_lock.go:145-167`: locked table metadata is trusted by write checks.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-create-like-readonly-lock-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id1200001-method-case.md`
- Method lesson: S13 should include target runtime-state cloning, not only source metadata
  mutation. The efficient selector is "shallow copy + selective reset list + leftover field that is
  runtime state + target behavior oracle".

## P0: Generated-Column Dependency Metadata-Only False Rejection (id630004, S11)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id630004 (`MAX(id)=630004,COUNT=36`).
- Proof obligation:
  - `P_check`: `checkModifyColumnWithGeneratedColumnsConstraint` detects that a generated column
    references the base column.
  - `Q_claim`: any `MODIFY COLUMN` on that base column can invalidate the generated column.
  - `D_dim`: metadata-only changes such as `COMMENT` and safe `DEFAULT` do not change the generated
    expression, dependency name, or base-column type.
  - `F_effect`: `GetModifiableColumnJob` rejects the DDL unconditionally when the dependency exists,
    after using the same check more precisely for rename and after `checkModifyTypes` has accepted
    the target.
- Evidence on testbed `8192975`:
  - Direct target `CREATE TABLE direct_comment(a int COMMENT 'new-comment', b int GENERATED ALWAYS
    AS (a + 1) STORED)` succeeded; inserting `a=1` returned `b=2`.
  - `ALTER TABLE t_comment MODIFY COLUMN a int COMMENT 'new-comment'` failed with `ERROR 3106/3108`.
  - Direct target `a int DEFAULT 5, b as (a+1)` succeeded; default insert returned `a=5,b=6`.
  - `ALTER TABLE t_default MODIFY COLUMN a int DEFAULT 5` failed with the same generated-column
    dependency error.
  - Green controls:
    non-dependent column comment change succeeded; generated column's own comment change with the
    same expression succeeded; true dependent base-column type change remained rejected.
- Source:
  - `pkg/ddl/modify_column.go`: dependency existence is computed at `errG`.
  - `pkg/ddl/modify_column.go`: rename uses `errG` only when the column name changes.
  - `pkg/ddl/modify_column.go`: later unconditional `if errG != nil` rejects all modify operations.
  - `pkg/ddl/tests/fail/fail_db_test.go`: current coverage checks type-change rejection, not
    metadata-only changes.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-generated-column-metadata-modify-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630004-method-case.md`
- Method lesson: dependency existence is not a sufficient proof that every DDL touching the
  depended-on object is unsafe. Split dependency checks by operation semantics: rename/type/value
  changes need guards; metadata-only changes need target-schema and behavior references.

## P0: Partition Column NULL To NOT NULL False Rejection (id630023, S10)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id630023
  (`MAX(id)=1020001,COUNT=64`; max id remains id1020001).
- Proof obligation:
  - `P_check`: partition-column modify validation says the target flags are unsafe.
  - `Q_claim`: changing a partition column from nullable to `NOT NULL` would require repartition
    or cannot be proven safe.
  - `D_dim`: nullability is a row-data invariant; for non-NULL rows it does not change partition
    placement.
  - `F_effect`: the partition flag allowlist rejects before the generic NULL data check can prove
    the transition safe.
- Evidence on testbed `8192975`:
  - Direct target references:
    `CREATE TABLE direct_range(a INT NOT NULL, b INT) PARTITION BY RANGE(a) ...` succeeded and
    accepted non-NULL rows. `RANGE(TO_DAYS(datetime_col))` with `DATETIME NOT NULL` also succeeded.
  - Non-partitioned reference:
    `CREATE TABLE nonpart(a INT NULL,b INT); INSERT (1),(11); ALTER TABLE nonpart MODIFY a INT NOT NULL`
    succeeded and `SHOW CREATE TABLE` showed `a int NOT NULL`.
  - DDL red cells:
    `RANGE(a)`, `LIST COLUMNS(a)`, `KEY(a)`, and `RANGE(TO_DAYS(a))` partition tables with only
    non-NULL rows all returned `ERROR 8200 Unsupported modify column: can't change the partitioning
    column, since it would require reorganize all partitions`.
  - Unsafe-data control:
    non-partitioned `NULL -> NOT NULL` with an actual NULL row rejected with `ERROR 1265`, proving
    the generic data-fit check exists.
- Source:
  - `pkg/ddl/modify_column.go:1481-1492`: `checkPartitionColumnModifiable` rejects when
    `isAllowedPartitionColumnFlagChange` returns false.
  - `pkg/ddl/modify_column.go:1538-1547`: `isAllowedPartitionColumnFlagChange` allows
    `NOT NULL -> NULL`, but not `NULL -> NOT NULL`.
  - `pkg/ddl/modify_column.go:1968-1972`: generic `checkForNullValue` can validate the
    `NULL -> NOT NULL` data condition, but the partition flag gate runs earlier.
  - `pkg/ddl/tests/partition/modify_column_test.go`: existing tests expect `NULL -> NOT NULL`
    rejection for partition columns; they do not compare against direct target schemas or the
    non-partitioned data-check reference.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-partition-column-not-null-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630023-method-case.md`
- Method lesson: S10 generalizes from metric mismatch to dimension mismatch. A transition
  allowlist must distinguish structural partition-placement dimensions from row-data invariants
  that already have precise validators.
- Stop rule: do not enumerate partition-column flags/type variants. Reopen only for another
  validation dimension with a stronger direct target or data-fit reference, silent
  wrong-acceptance, or fix validation.

## P0: Partition Column VARCHAR Shrink False Rejection (id630003, S10)


- Status: **CONFIRMED**, inserted into remote `found_bug` as id630003 (`MAX(id)=630003,COUNT=35`).
- Proof obligation:
  - `P_check`: `checkPartitionColumnTypeChangeAllowlist` only allows string length extension for
    `KEY`, `RANGE COLUMNS`, and `LIST COLUMNS` partition columns.
  - `Q_claim`: any `VARCHAR` shrink of a partition column requires repartition and is unsafe.
  - `D_dim`: target partition definitions and existing rows can fit the shorter `VARCHAR`, and
    generic non-partition `MODIFY COLUMN` can prove the same value-fit contract.
  - `F_effect`: `checkPartitionColumnModifiable` rejects the DDL before the later target
    partition-definition validation and before the generic data-fit check can accept the transition.
- Evidence on testbed `8192975`:
  - Direct target references succeeded for `varchar(5)` partition columns with fitting literals/data:
    `LIST COLUMNS(a)` with `abc`/`xyz`, `RANGE COLUMNS(a)` with bound `'m'`, and `KEY(a)`.
  - Non-partitioned control succeeded:
    `varchar(6)->varchar(5)` with rows `abc` and `xyz`, `MAX(CHAR_LENGTH(a))=3`.
  - Red cells:
    `LIST COLUMNS`, `RANGE COLUMNS`, and `KEY` partition columns all failed
    `varchar(6)->varchar(5)` with `ERROR 8200`.
  - Green control:
    partition-column `varchar(6)->varchar(7)` succeeded.
  - KEY guard:
    direct `varchar(6)` and `varchar(5)` KEY partition tables had identical sampled partition
    membership for values `a`, `bb`, `ccc`, `dddd`, `中`, and `中中`.
- Source:
  - `pkg/ddl/modify_column.go`: `checkPartitionColumnModifiable` calls
    `checkPartitionColumnTypeChangeAllowlist` before target partition-definition validation.
  - `pkg/ddl/modify_column.go`: `isStringLengthExtension` requires `newCol.GetFlen() > col.GetFlen()`.
  - `pkg/ddl/tests/partition/modify_column_test.go`: existing shrink reject coverage uses
    literals/data of length 6 when shrinking to length 5, but does not cover safe shrink where
    literals/data fit.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-partition-column-varchar-shrink-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630003-method-case.md`
- Method lesson: S10 should look for a coarse transition allowlist that runs before a stronger
  target-state validator. If direct target partition definitions and the generic data-fit contract
  accept the final state, the transition allowlist needs a precise reason to reject.

## P0: FK MODIFY COLUMN VARCHAR Length False Rejection (id630002, S10)


- Status: **CONFIRMED**, inserted into remote `found_bug` as id630002 (`MAX(id)=630002,COUNT=34`).
- Proof obligation:
  - `P_check`: `isAcceptableForeignKeyColumnChange` requires `newCol.GetFlen() >= relatedCol.GetFlen()` and `newCol.GetFlen() >= originalCol.GetFlen()` for non-integer FK column changes.
  - `Q_claim`: if either inequality fails, the target FK column pair is not safe for `MODIFY COLUMN`.
  - `D_dim`: transition length inequalities vs TiDB's target-state FK compatibility contract.
  - `F_effect`: `checkModifyColumnWithForeignKeyConstraint` rejects the DDL before normal data-fit checking and before comparing against direct target-state compatibility.
- Evidence on testbed `8192975`:
  - Direct target references succeeded:
    parent `varchar(10)` / child `varchar(10)`,
    parent `varchar(10)` / child `varchar(15)`,
    parent `varchar(15)` / child `varchar(20)`.
  - Child red cells:
    parent `varchar(10)`, child `varchar(20)` with `MAX(CHAR_LENGTH(child))=10`;
    `ALTER TABLE child MODIFY COLUMN b varchar(10)` failed with `ERROR 1832`.
    The `varchar(20) -> varchar(15)` transition also failed with `ERROR 1832`.
  - Parent red cell:
    parent `varchar(10)` referenced by child `varchar(20)`;
    `ALTER TABLE parent MODIFY COLUMN a varchar(15)` failed with `ERROR 1833`, even though the direct target FK pair is valid.
  - Green controls:
    child `varchar(20) -> varchar(25)` succeeded;
    parent `varchar(10) -> varchar(20)` succeeded.
- Source:
  - `pkg/ddl/foreign_key.go`: `checkTableForeignKey` accepts FK creation when type, unsigned flag, charset, and collation match; it does not require equal string lengths.
  - `pkg/ddl/tests/fk/foreign_key_test.go`: parent `varchar(10)` / child `varchar(20)` is an explicit passing FK creation case.
  - `pkg/ddl/foreign_key.go`: `checkModifyColumnWithForeignKeyConstraint` and `isAcceptableForeignKeyColumnChange` impose stricter length inequalities during modify.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-fk-modify-column-length-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630002-method-case.md`
- Method lesson: S10 generalizes beyond data-fit prechecks. For target-state DDL validators, compare the transition validator against the sibling create/add validator for the exact target state. Hidden inequalities over old metadata or related metadata are suspect when the product accepts the same final schema directly.

## P0: MODIFY COLUMN Multibyte CHAR/VARCHAR Shrink False Rejection (id630001, S10)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id630001 (`MAX(id)=630001,COUNT=33`).
- Proof obligation:
  - `P_check`: `buildCheckSQLFromModifyColumn` runs a restricted SQL precheck using `LENGTH(old_col) > newFlen` for non-integer range checks.
  - `Q_claim`: if no row is returned, every existing value fits the target column definition.
  - `D_dim`: byte length vs character length for non-binary `CHAR`/`VARCHAR`.
  - `F_effect`: `doModifyColumnWithCheck` finishes the `MODIFY COLUMN` path without row reorg after the precheck passes, or rejects the DDL when the precheck finds a row.
- Evidence on testbed `8192975`:
  - Direct target references:
    `CREATE TABLE ref_v3(a varchar(3) charset utf8mb4 collate utf8mb4_bin); INSERT INTO ref_v3 VALUES (_utf8mb4'中中中');`
    succeeded with `LENGTH=9, CHAR_LENGTH=3`.
    The same direct insert into `char(3)` also succeeded.
  - DDL arm:
    `CREATE TABLE t(a varchar(4) charset utf8mb4 collate utf8mb4_bin); INSERT INTO t VALUES (_utf8mb4'中中中'); ALTER TABLE t MODIFY COLUMN a varchar(3) ...;`
    returned `ERROR 1265 Data truncated for column 'a', value is '中中中'`, and `SHOW CREATE TABLE` remained `varchar(4)`.
  - `char(4) -> char(3)` reproduced the same false rejection.
  - ASCII control `abc` from `varchar(4) -> varchar(3)` succeeded.
- Source:
  - `pkg/ddl/modify_column.go`: `getModifyColumnType` selects no-reorg-with-check for shrink cases.
  - `pkg/ddl/modify_column.go`: `doModifyColumnWithCheck` runs `checkModifyColumnData` then publishes metadata.
  - `pkg/ddl/modify_column.go`: `buildCheckSQLFromModifyColumn` uses `LENGTH(col) > newFlen`.
  - `pkg/ddl/modify_column_test.go`: current string shrink tests cover ASCII length and trailing spaces, not multibyte character length.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-modify-column-multibyte-shrink-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id630001-method-case.md`
- Method lesson: S10 adds DDL precheck metric mismatch. For any metadata-only/no-reorg DDL validation, explicitly compare the metric used by the precheck with the target contract's unit: bytes, characters, display width, encoded key bytes, collation weight, restored-data form, and SQL type domain are not interchangeable.

## P0 Candidate: Prepared DDL VARCHAR Auto-Conversion Frozen Across Strict sql_mode (id30029, S8)

- Status: **CANDIDATE**, inserted into remote `found_bug` as id30029 (`MAX(id)=30029,COUNT=31`). Contract needs owner/product ruling.
- Proof obligation:
  - `P_check`: PREPARE runs `Preprocess`; `checkColumn` detects overlong `VARCHAR`; `hasAutoConvertWarning` reads `SQLMode.HasStrictMode()` and, in non-strict mode, mutates the AST from `VARCHAR` to `TEXT/BLOB`.
  - `Q_claim`: the mutated prepared DDL AST remains valid for later `EXECUTE`.
  - `D_dim`: `sql_mode` strictness and prepare-time AST mutation.
  - `F_effect`: `EXECUTE` uses the already-mutated AST under the later strict session and does not re-run the direct strict validation.
- Evidence on testbed `8192975`:
  - Direct strict:
    `SET sql_mode='STRICT_TRANS_TABLES'; CREATE TABLE t_direct_strict(c VARCHAR(70000) CHARACTER SET utf8mb4);`
    returned error 1074.
  - Direct non-strict:
    `SET sql_mode=''; CREATE TABLE ... VARCHAR(70000) ...` succeeded with warning 1246 and created `mediumtext`.
  - Prepared non-strict -> strict:
    `PREPARE s FROM 'CREATE TABLE ... VARCHAR(70000) ...'` emitted warning 1246; after switching to `STRICT_TRANS_TABLES`, `EXECUTE s` succeeded and `SHOW CREATE TABLE` showed `c mediumtext`.
  - Reverse control: PREPARE under strict failed with error 1074, so no prepared statement existed.
  - Boundary: `ALTER TABLE ... ADD COLUMN` same shape did not reproduce; PREPARE under non-strict failed with 1074 on the tested build.
- Source:
  - `pkg/planner/core/preprocess.go`: `checkColumn` and `hasAutoConvertWarning`.
  - `pkg/planner/core/plan_cache_utils.go`: prepare-time preprocessing.
  - `pkg/planner/core/plan_cache.go`: execute-time preprocessing only on schema-version change.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-prepared-ddl-varchar-strict-freeze-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id30029-method-case.md`
- Method lesson: S8 now has a candidate-only sub-shape: prepared/preprocess semantic freeze via AST mutation. This is stronger source evidence but more contract-sensitive than id30028. Treat direct-vs-prepared differences as confirmed only when the current-session contract is clear; otherwise record as candidate and stop expanding.

## P0: Prepared Noop-Functions Preprocessor Semantic Freeze (id30028, S8)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id30028 (`MAX(id)=30028,COUNT=30`).
- Proof obligation:
  - `P_check`: `GeneratePlanCacheStmtWithAST` runs `Preprocess(..., InPrepare, ...)` during `PREPARE`; `checkSelectNoopFuncs` and `checkGroupBy` read `SessionVars.NoopFuncsMode`.
  - `Q_claim`: the prepared AST and resolve context remain semantically valid for later `EXECUTE`.
  - `D_dim`: current-session semantic switch `tidb_enable_noop_functions`.
  - `F_effect`: `EXECUTE` optimizes the stored AST and `planCachePreprocess` only re-runs `Preprocess` when schema version changes. Flushing prepared plan cache rebuilds the physical plan but not the prepare-time noop validation.
- Evidence on testbed `8192975`:
  - Direct reference under `SET tidb_enable_noop_functions=OFF`:
    `SELECT SQL_CALC_FOUND_ROWS a FROM t ORDER BY a` returned error 1235.
  - Prepared under `ON`, then switched to `OFF`:
    `EXECUTE s` returned rows `1,2` with `@@warning_count=0`.
  - After `ADMIN FLUSH SESSION PLAN_CACHE`, the same `EXECUTE s` still returned rows with `@@last_plan_from_cache=0`.
  - Sibling syntax: direct `SELECT a FROM t GROUP BY a DESC` under `OFF` returned error 1235, but prepared under `ON` then executed under `OFF` returned rows before and after plan-cache flush.
  - Internal control: a prepared statement created under `sql_mode=''` and executed after switching to `ONLY_FULL_GROUP_BY` returns error 1055 in the existing session-state test and on the testbed, showing changed session semantics can affect prepared execution in nearby code.
- Source:
  - `pkg/planner/core/plan_cache_utils.go`: `GeneratePlanCacheStmtWithAST` runs preprocessor at prepare time.
  - `pkg/planner/core/preprocess.go`: `checkSelectNoopFuncs` and `checkGroupBy` enforce noop-function mode.
  - `pkg/planner/core/plan_cache.go`: `planCachePreprocess` only re-preprocesses on schema version change.
  - `pkg/planner/core/plan_cache.go`: `generateNewPlan` optimizes the already-preprocessed AST.
  - `pkg/executor/prepared.go`: `PrepareExec.Next` stores the prepared statement.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-prepared-noop-functions-freeze-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id30028-method-case.md`
- Method lesson: S8 adds prepared/preprocess semantic freeze. When PREPARE-time validation consumes a session semantic switch, use direct current-session SQL as the reference and add plan-cache flush/off-cache controls to separate AST/preprocessor freeze from physical plan cache reuse.

## P0: Inspection Result Cluster Config Cache Key Granularity (id30027, S3 refinement)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id30027 (`MAX(id)=30027,COUNT=29`).
- Proof obligation:
  - `P_check`: `InspectionTableCache` treats `cluster_config` as cacheable and keys cached rows by memtable name.
  - `Q_claim`: a cached `cluster_config` snapshot is equivalent to rerunning later internal `cluster_config` queries during inspection.
  - `D_dim`: later queries may depend on extractor-consumed dimensions, especially `type` mapped to `node_types`, that are not part of the table-name cache key and no longer exist as scalar predicates.
  - `F_effect`: `MemTableReaderExec.Next` returns cached full rows and skips the normal `fetchClusterConfig` server filtering path.
- Evidence on testbed `8192975`:
  - Mock cluster config servers supplied deterministic rows: `tikv-a -> foo-test=tikv-a`, `tikv-b -> foo-test=tikv-b`, `tidb-a -> foo-test=tidb-a`.
  - Direct reference:
    `cluster_config WHERE type='tikv' AND key='foo-test'` returned `tikv-a=tikv-a,tikv-b=tikv-b`.
  - `inspection_result WHERE rule='config' AND item='foo-test' AND type='tikv'` returned `value='inconsistent'` and details containing `tidb-a config value is tidb-a`.
  - `@@warning_count` was 0.
  - Trigger evidence: `EXPLAIN FORMAT='brief' SELECT value,instance FROM cluster_config WHERE type='tikv' AND key='foo-test'` showed `MemTableScan table:CLUSTER_CONFIG node_types:["tikv"]` and only `key='foo-test'` remained in scalar `Selection`.
- Source:
  - `pkg/executor/inspection_result.go`: `inspectionResultRetriever.retrieve` initializes `SessionVars.InspectionTableCache`.
  - `pkg/executor/inspection_result.go`: `configInspection.inspectDiffConfig` first scans and caches broad `cluster_config` rows.
  - `pkg/executor/inspection_result.go`: `configInspection.generateDetail` later issues the type/key detail query.
  - `pkg/executor/memtable_reader.go`: cache hit is by table name and returns cached rows fully.
  - `pkg/planner/core/memtable_predicate_extractor.go`: `ClusterTableExtractor` consumes `type` into `node_types`.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-inspection-result-cluster-config-cache-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id30027-method-case.md`
- Method lesson: S3 adds cache snapshot key granularity. A shortcut cache must prove its key includes every extractor-consumed dimension, or the cache hit path must reapply those dimensions before returning SQL-visible rows.

## P0: TIKV_REGION_PEERS Numeric Extractor Type-Domain Conversion (id30026, S3 refinement)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id30026 (`MAX(id)=30026,COUNT=28`).
- Proof obligation:
  - `P_check`: `extractCol` recognizes `region_id` / `store_id` `EQ` or `IN` predicates as extractable.
  - `Q_claim`: converting extracted values to `uint64` request IDs preserves the SQL predicate semantics.
  - `D_dim`: SQL `bigint` predicates and backend uint64 point-lookup domains differ for negative values and invalid strings.
  - `F_effect`: `TikvRegionPeersExtractor` removes the scalar Selection, then `parseUint64` silently ignores conversion failures.
- Evidence on testbed `8192975`:
  - `SELECT COUNT(*) FROM information_schema.tikv_region_peers` returned 269.
  - `WHERE region_id = -1` returned 269, while `WHERE CASE WHEN region_id = -1 THEN TRUE ELSE FALSE END` returned 0.
  - Returned rows projected `region_id = -1` as `0`.
  - `WHERE store_id = -1` also returned 269, while the CASE oracle returned 0.
  - `WHERE region_id IN (-1)` returned 269, while the CASE oracle returned 0.
  - Green controls: `peer_id=-1` returned 0/0 because `peer_id` is not extracted by this extractor; `region_id IN (-1, valid_region_id)` matched the valid rows only.
- Source:
  - `pkg/planner/core/memtable_predicate_extractor.go`: `extractCol` drops extracted predicates.
  - `pkg/planner/core/memtable_predicate_extractor.go`: `parseUint64` ignores parse failures.
  - `pkg/planner/core/memtable_predicate_extractor.go`: `TikvRegionPeersExtractor.Extract` uses `parseUint64` for `region_id` and `store_id`.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-tikv-region-peers-negative-id-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id30026-method-case.md`
- Method lesson: S3 adds type-domain conversion. If a shortcut converts SQL values into a narrower backend request domain, conversion success/failure/empty semantics are part of the proof; the original predicate cannot be dropped before that proof is complete.

## P0: Prepared Plan Cache Timezone-Folded UNIX_TIMESTAMP (id30025, S7 refinement)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id30025 (`MAX(id)=30025,COUNT=27`).
- Proof obligation:
  - `P_check`: prepared plan cache key represents `time_zone` by current offset.
  - `Q_claim`: same current offset is enough for cached timezone-dependent semantics.
  - `D_dim`: named zones can share today's offset but differ for the literal's historical date.
  - `F_effect`: `UNIX_TIMESTAMP(datetime literal)` is folded into the cached object and not rebuilt on cache hit.
- Evidence on testbed `8192975`:
  - Johannesburg -> Amsterdam: direct Amsterdam result for `2025-01-15 12:00:00` is `1736938800`, but cached execute after toggle returned Johannesburg's `1736935200` with `@@last_plan_from_cache=1`; after `ADMIN FLUSH SESSION PLAN_CACHE`, the same prepared statement returned `1736938800`.
  - Amsterdam -> Johannesburg: cached hit stayed `1736938800`; flush reference returned `1736935200`.
  - Green control: `2025-07-15 12:00:00` has the same historical offset in both zones, so cached/direct/flush all returned `1752573600`.
- Source:
  - `pkg/planner/core/plan_cache_utils.go`: `NewPlanCacheKey` hashes `time.Now().In(vars.TimeZone).Zone()`, i.e. current offset.
  - `pkg/planner/core/expression_rewriter.go`: `UNIX_TIMESTAMP` with arguments is handled as a normal expression under plan cache, not a deferred expression.
  - `pkg/expression/scalar_function.go`: `NewFunction` folds constants.
  - `pkg/expression/builtin_time.go`: `UNIX_TIMESTAMP` with an argument converts the DATETIME using the session location.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-plan-cache-timezone-unix-timestamp-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id30025-method-case.md`
- Method lesson: S7 adds coarse-key sufficiency. A coarse key dimension can be safe for a semantic boundary rebuilt after hit, but unsafe for a folded value that depends on omitted details.

## P0: Prepared Plan Cache Sysdate Semantic Switch (id30024, S7 refinement)

- Status: **CONFIRMED**, inserted into remote `found_bug` as id30024 (`MAX(id)=30024,COUNT=26`).
- Proof obligation:
  - `P_check`: prepared plan cache key covers SQL/schema/user/session dimensions listed in `NewPlanCacheKey`.
  - `Q_claim`: cached plan remains semantically equivalent after session state changes.
  - `D_dim`: `tidb_sysdate_is_now` changes expression construction: `sysdate()` may become `now()`.
  - `F_effect`: cache hit reuses the old scalar-function tree and skips re-optimization.
- Evidence on testbed `8192975`:
  - OFF -> ON: after `@@last_plan_from_cache=1`, changing `tidb_sysdate_is_now` to `1` still returned `sysdate(6)=now(6) => 0`; after `ADMIN FLUSH SESSION PLAN_CACHE`, the same prepared statement returned `1`.
  - ON -> OFF: cached hit stayed `1`; after flush, the same prepared statement returned `0`.
- Source:
  - `pkg/expression/scalar_function.go`: `sysdate` is rewritten to `now` when `GetSysdateIsNow()`.
  - `pkg/planner/core/plan_cache_utils.go`: `NewPlanCacheKey` omits `SysdateIsNow`.
- Artifacts:
  - Draft: `/Users/bba/pc/ai-native-plan-cache-sysdate-toggle-draft.md`
  - Method case: `/Users/bba/pc/ai-native-id30024-method-case.md`
- Method lesson: S7 now has three proofs: key completeness, payload purity, and semantic-switch
  coverage. Observability variables must stay outside the cached query, otherwise they make the
  statement uncacheable and invalidate the matrix.

## P0: DDL Restore Reference Revalidation
Source anchors:
- `/Users/bba/pc/tidb/pkg/ddl/executor.go`: `RecoverTable`
- `/Users/bba/pc/tidb/pkg/ddl/table.go`: `onRecoverTable`, `recoverTable`
- `/Users/bba/pc/tidb/pkg/ddl/create_table.go`: normal create-table FK validation
- `/Users/bba/pc/tidb/pkg/ddl/foreign_key.go`: `checkTableForeignKeyValidInOwner`

P/Q/F:
- **P**: recover/flashback checks target schema/name/ID and GC safe point, then treats historical `TableInfo` as recoverable.
- **Q**: the historical metadata is valid as current schema metadata, including every owned reference.
- **F**: restore publishes cloned metadata directly and skips create/alter-time reference validators.

Known result:
- `found_bug id30016`: `DROP child; DROP parent; FLASHBACK child` restores a child table whose `SHOW CREATE TABLE` and `information_schema.key_column_usage` still declare `REFERENCES p`, while parent `p` is absent. During that missing-parent window, `INSERT INTO c VALUES (2,999)` succeeds with `foreign_key_checks=ON`; after recreating `p`, new bad inserts fail again, but the orphan row remains.
- Probe: `/Users/bba/pc/ai_native_ddl_fk_flashback_missing_parent_probe.py`
- Draft: `/Users/bba/pc/ai-native-fk-flashback-missing-parent-draft.md`

Counterexample families:
- Restore one object while a referenced sibling object has been dropped.
- Restore a container/object whose normal create path has an explicit validator, but the recover path clones historical metadata.
- ID/name-binding changes where metadata remains display-visible but behavior depends on current object lookup.

Oracle:
- Metadata: `SHOW CREATE TABLE` and `information_schema.key_column_usage` expose the recovered reference.
- Behavior: invalid child insert must fail if FK metadata is published and checks are on.
- Trigger evidence: `EXPLAIN INSERT` should contain `Foreign_Key_Check` when enforcement is active; absence of that node while metadata claims an FK is a strong red signal.
- Controls: ordinary create with missing parent must fail, and `FLASHBACK DATABASE` restoring parent+child together should remain green.

Selector update:
- S6 is now sharpened from "restore fields may be stale" to:
  ```text
  restore path re-materializes historical metadata
  + ordinary create/alter path has an explicit validator
  + recover path skips that validator
  + post-recover behavior has a low-noise oracle
  ```
- Boundary samples matter: TTL recover is green because TTL scheduling is explicitly disabled; cached table recover is blocked by drop-table semantics; TiFlash recover is static-high-signal but runtime-blocked on a no-TiFlash testbed.
- Negative selector calibration: `/Users/bba/pc/ai_native_ddl_sequence_recover_boundary_probe.py` shows that sequence-default recover is not a clean S6 validator gap. `FLASHBACK TABLE` can restore a default pointing at a missing sequence, but ordinary `CREATE TABLE ... DEFAULT NEXT VALUE FOR missing_seq` also succeeds and fails only at insert time. That behavior belongs to the existing sequence-default lazy-name-resolution family, not a new recover-only proof violation.
- 2026-07-03 calibration: broad ordinary DDL owner matrices are green on the current testbed (28 column/reference cells, 17 object/reference cells). Do not widen rename/drop/partition happy paths blindly. Masking-policy recover is static-asymmetric but lacks a behavior oracle because masking policy is DDL-consumed only; TTL×FK is symmetric because later TTL parent creation over a dangling child FK still fails with `8152`.

- 2026-07-12 identity-drift extension: `FLASHBACK TABLE` with a same-name empty parent is a new high candidate (`id1500002`), not just another missing-parent row. The current parent exists and future plans contain `Foreign_Key_Check`, but the recovered existing row is orphaned. The smallest id30016 fix (reject absent parent) would not catch this cell; the new proof obligation is current-parent row membership or historical referenced-object identity.

## P0: DDL Side-State ID-Swap Ownership
Source anchors:
- `/Users/bba/pc/tidb/pkg/executor/lockstats/lock_stats_executor.go`: `LOCK STATS` resolves current table/partition IDs.
- `/Users/bba/pc/tidb/pkg/statistics/handle/lockstats/lock_stats.go`: `mysql.stats_table_locked` is keyed by `table_id`.
- `/Users/bba/pc/tidb/pkg/executor/show_stats.go`: `SHOW STATS_LOCKED` maps locked IDs through current InfoSchema.
- `/Users/bba/pc/tidb/pkg/ddl/partition.go`: `EXCHANGE PARTITION` swaps standalone table ID and partition ID.
- `/Users/bba/pc/tidb/pkg/statistics/handle/ddl/subscriber.go`: exchange updates stats counts but does not rewrite stats-lock ownership rows.

P/Q/F:
- **P**: a side-state row exists for the physical ID that was current when the user issued the command.
- **Q**: after DDL rekeys or swaps physical IDs, the side state still has a coherent SQL-visible owner and can be cleaned up by the matching SQL command.
- **F**: later SHOW/cleanup paths resolve current InfoSchema IDs and trust the side table as if object ownership did not change.

Known result:
- `found_bug id30017`: `LOCK STATS t` on a partitioned table reports `t/global,t/p0,t/p1`; after `ALTER TABLE t EXCHANGE PARTITION p0 WITH TABLE t1`, `SHOW STATS_LOCKED` reports `t/global,t1,t/p1`; after `UNLOCK STATS t`, `t1` remains locked.
- Probe: `/Users/bba/pc/ai_native_ddl_stats_lock_exchange_partition_probe.py`
- Draft: `/Users/bba/pc/ai-native-stats-lock-exchange-partition-draft.md`
- Method case: `/Users/bba/pc/ai-native-id30017-method-case.md`

Counterexample families:
- Side tables keyed by physical object ID while DDL swaps or reallocates IDs.
- Session/sys-table side state that stores both an object ID and a container/owner key.
- Existing tests that assert only side-row count after DDL.

Oracle:
- Weak calibration: side-table row count.
- Ownership oracle: `SHOW` or information_schema mapping of IDs back to current SQL objects.
- Strong behavior oracle: a matching cleanup/round-trip command, such as `LOCK STATS t` followed by `UNLOCK STATS t`, must not leave an unrelated object locked.

Selector update:
- S4 now covers both owner/container rekey and pure ID swap.
- Do not count "row survived" as a pass. The pass condition is "the user-visible owner is still the one the product contract promises, and cleanup reaches it."

## P0: Partial-Index Predicate Implication
Source anchors:
- `/Users/bba/pc/tidb/pkg/planner/core/operator/logicalop/logical_datasource.go`: `DataSource.CheckPartialIndexes`
- `/Users/bba/pc/tidb/pkg/planner/core/partidx/check_constraint.go`: `CheckConstraints`, `implCompareExpr`, `implIsNotNull`, `AlwaysMeetConstraints`

P/Q/F:
- **P**: pushed-down query filters are checked against the partial-index condition by exact match or ranger-based implication.
- **Q**: every row satisfying the query filter also satisfies the partial-index predicate.
- **F**: planner keeps or forces a partial-index access path; plan cache may also mark some partial-index scans cacheable.

Known result:
- `found_bug id30001`: `INDEX pi(b) WHERE a < 3` + query `a >= 0` can use `pi` and silently miss rows.
- Semantic-matrix expansion after the pause gate did not reveal a separate root cause, but it expanded the blast radius into 280 row-set mismatches across 1200 cells. Hits clustered in:
  `upper_bound/lower_overlap` (138), `upper_bound/wide_range` (72),
  `upper_bound/boundary_range` (52), `excluded_point/boundary_range` (12),
  and `upper_bound/or_widening` (6).
- No-hint/stats-pressure expansion confirmed the same root cause can become user-visible without hints: with fresh pseudo stats and `ORDER BY b LIMIT`, TiDB chose `IndexFullScan ... index:pi(b) keep order:true` and returned only the partial subset.

Counterexample families:
- Partial-overlap ranges: partial `a < c` or `a <= c`, query `a >= low`.
- Excluded point: partial `a != c`, query range contains `c`.
- NULL leakage: `IS NOT NULL`, `<=> NULL`, `OR a IS NULL`, functions that collapse to NULL.
- OR widening: one branch implies the partial predicate, another branch does not.
- Type/collation boundary: string/date comparisons, casts, unsigned/signed constants.
- Plan-cache parameter drift: first parameter implies partial predicate, later parameter does not.

Oracle:
- Fast path: `USE INDEX(pi)` / `FORCE INDEX(pi)` / no-hint with ORDER BY or stats pressure.
- Safe path: `IGNORE INDEX(pi)`.
- Required equality: row set, order under deterministic `ORDER BY`, and error/warning state.
- `ADMIN CHECK TABLE` is storage sanity only; passing does not prove planner correctness.

Next improvement:
- The first semantic matrix is in `/Users/bba/pc/ai_native_partial_index_semantic_matrix_20260630_184141.csv`.
- Convert the remaining hand-written matrix into a parameterized interval/excluded-point generator and downweight proven-low-yield cells:
  lower-bound partial predicates, point predicates, NULL-only predicates, and most `IN`/OR point-set forms.
- The no-hint/stats-pressure pass found a hit after changing the query shape from `ORDER BY b,id` to unique-`b` data plus `ORDER BY b LIMIT`. Keep this as a generator rule: determinism should come from data construction when adding a tie-breaker would disable the target fast path.
- Do not keep expanding this proof family until issue/fix verification needs it; the method-validation value has already been extracted.

## P0: Partition-Pruning Boundary Proof
Source anchors:
- `/Users/bba/pc/tidb/pkg/planner/core/rule/rule_partition_processor.go`: range/list pruning comparators such as `minCmp`, `maxCmp`, `multiColumnRangeColumnsPruner`
- SQL toggle evidence: `tidb_partition_prune_mode = static|dynamic` is widely used in integration tests.

P/Q/F:
- **P**: the partition pruner maps query predicates to a set of partitions using boundary comparison rules.
- **Q**: every row that can satisfy the query lives in the retained partition set.
- **F**: executor scans only pruned partitions.

Why it is a strong next target:
- It has an immediate safe-path differential: static vs dynamic partition-prune modes should return the same rows.
- Boundary code explicitly handles NULL, prefix columns, `MAXVALUE`, unsigned/signed minimums, datetime zero, empty strings, and non-constant expressions.
- This is the same shape as id30001: a range proof decides whether data can be skipped.

Counterexample families:
- Multi-column prefix predicates where only the first partition columns are constrained.
- `NULL` in nullable partition columns versus `NOT NULL` minimum-value shortcuts.
- Unsigned `0`, signed lower bound, zero datetime, empty string.
- `MAXVALUE` partitions and off-by-one inclusive/exclusive ranges.
- Static vs dynamic pruning with `IN`, `BETWEEN`, `OR`, casts, and collation-sensitive strings.

Oracle:
- Run the same deterministic query across three paths:
  ```sql
  -- reference path: same schema/data without partitioning
  SELECT ... FROM unpartitioned_ref WHERE ... ORDER BY primary_key;

  SET @@tidb_partition_prune_mode = 'static';
  SELECT ... FROM partitioned_table WHERE ... ORDER BY primary_key;

  SET @@tidb_partition_prune_mode = 'dynamic';
  SELECT ... FROM partitioned_table WHERE ... ORDER BY primary_key;
  ```
- Required equality: unpartitioned reference rows = static-prune rows = dynamic-prune rows.
- Add `EXPLAIN` partition evidence only after row-set mismatch.

Next improvement:
- Build a small semantic generator for `PARTITION BY RANGE COLUMNS(...)` and `PARTITION BY RANGE(expr)` with boundary rows placed exactly on both sides of every partition edge.

Current smoke result:
- `/Users/bba/pc/ai_native_partition_prune_probe.py` implements the three-way oracle.
- First run covered `RANGE COLUMNS(int)`, multi-column range columns, and date range columns; findings = 0.
- Second run upgraded from hand-written examples to boundary-derived predicates and covered six target families:
  `RANGE COLUMNS(int)`, `RANGE COLUMNS(a,b)`, `RANGE COLUMNS(date)`,
  unsigned multi-column range, string range columns, and `floor(unix_timestamp(ts))` expression partition; findings = 0.
- Methodology takeaway: static/dynamic-only equality is too weak because both optimized paths could be wrong together; the unpartitioned reference table is now mandatory.
- Tooling takeaway: the probe now has progress output plus `--max-predicates`, so future semantic expansion can stay observable and cheap.

## P0: Plan-Cache Safety Under Parameter Drift
Source anchors:
- `/Users/bba/pc/tidb/pkg/planner/core/plan_cache.go`: `IsSafeToReusePointGetExecutor`
- `/Users/bba/pc/tidb/pkg/planner/util/null_misc.go`: plan-cache-sensitive folding guarded by `MaybeOverOptimized4PlanCache`
- `/Users/bba/pc/tidb/pkg/planner/core/operator/logicalop/logical_datasource.go`: partial-index `AlwaysMeetConstraints` plan-cache special case

P/Q/F:
- **P**: plan-cache qualification and special-case checks decide a cached plan is reusable.
- **Q**: changing parameter values cannot invalidate access-path correctness, pruning, null-reject proof, or partial-index applicability.
- **F**: TiDB reuses a previous plan instead of replanning with current parameters.

Why it is a strong next target:
- It naturally attacks stale proof obligations: a proof true for parameter value `p1` may be false for `p2`.
- The SQL oracle is simple and low-noise.

Counterexample families:
- First execution parameter implies a partial-index predicate; second parameter widens outside it.
- First execution prunes to one partition; second should scan another partition.
- First execution makes `IS NOT NULL` / null-reject / simplification true; second uses `NULL`.
- LIMIT/order/index choices where parameterized bounds change range shape.

Oracle:
- Enable prepared plan cache and compare prepared executions against direct no-cache executions:
  ```sql
  SET tidb_enable_prepared_plan_cache = 1;
  PREPARE stmt FROM 'SELECT ... WHERE a > ? ORDER BY id';
  SET @p = ...; EXECUTE stmt USING @p; SELECT @@last_plan_from_cache;
  SET @p = ...; EXECUTE stmt USING @p; SELECT @@last_plan_from_cache;
  ```
- Compare each prepared result with the same query run directly using literals or with cache disabled.
- Record `@@last_plan_from_cache` as evidence, not as the oracle itself.

Next improvement:
- Make the partial-index probe's plan-cache checks generic: parameter schedules should be generated from the proof target's counterexample families.
- Use the same three-way shape where possible: cached execution vs direct literal execution vs cache-disabled prepared execution.

Current smoke result:
- `/Users/bba/pc/ai_native_plan_cache_drift_probe.py` implements the three-way oracle:
  prepared cached execution vs prepared cache-disabled execution vs direct literal execution.
- The first implementation had a measurement bug: a separate marker query after `EXECUTE` overwrote `@@last_plan_from_cache`. It now reads cache evidence immediately with `SELECT 'LAST', key, @@last_plan_from_cache`.
- Baselines now prove the probe can observe cache hits:
  `point_get_cache_baseline` and `normal_index_range_cache_baseline` hit cache on later parameters and matched direct execution.
- Initial proof targets produced no mismatch:
  partial-index threshold, partition range boundary, and predicate-simplification NULL/IN cases all matched direct execution.
- Some partial-index shapes remained uncacheable, which is useful signal: plan-cache drift fuzzing must first filter for proof cases that actually enter cache.
- Code reading refined the filter: comparison-based partial indexes are marked noncacheable by `DataSource.CheckPartialIndexes`; only the single `IS NOT NULL` partial-index special case can pass through `AlwaysMeetConstraints` for plan cache. Plan-cache drift should therefore focus on NULL-reject proofs and partition pruning, not generic partial-index comparison predicates.
- 2026-06-30 follow-up: the probe now supports multi-parameter schedules. It confirmed `LIMIT ?` is a negative/control because TiDB puts limit parameters into the plan-cache key, and LIST/default partition cases can hit cache without row-set drift. No new plan-cache bug was found in this pass.

## P0: InfoSchema Predicate-Extractor Collation Proof
Source anchors:
- `/Users/bba/pc/tidb/pkg/planner/core/memtable_infoschema_extractor.go`: `InfoSchemaBaseExtractor.Extract`, `extractLikePatternCol`, `InfoSchemaTablesExtractor.HasTableName`
- `/Users/bba/pc/tidb/pkg/planner/core/memtable_infoschema_extractor.go`: object-name columns call `extractCol(..., valueToLower=true)` and `filter()` can lowercase row values before checking pushed-down scalar functions.
- `/Users/bba/pc/tidb/pkg/planner/core/memtable_predicate_extractor.go`: `extractCol` can push down equality over `LOWER(col)` / `UPPER(col)` through `setColumnPushedDownFn`.
- `/Users/bba/pc/tidb/pkg/executor/infoschema_reader.go`: `setDataFromTables`, `HasTableName(t.TableName.L)`, `HasTableSchema(t.DBName.L)`

P/Q/F:
- **P**: the InfoSchema extractor converts predicates such as `TABLE_SCHEMA = ...` and `TABLE_NAME LIKE ...` into an internal schema/table prefilter.
- **Q**: the prefilter is no wider than the original SQL predicate under the column's SQL-visible collation and expression semantics.
- **F**: the executor enumerates only the extracted objects and the original predicate may be removed from `remained`, so rows admitted by the extractor can bypass the normal scalar filter.

Known result:
- `found_bug id30010`: `information_schema.tables.TABLE_NAME` and `information_schema.columns.TABLE_NAME` are `utf8mb4_bin`, but `table_name LIKE 'a_%'` is extracted as a case-insensitive/lowercased regexp prefilter. A mixed-case table `Acase` is returned by the ordinary `WHERE` query even though scalar SQL evaluation says `Acase LIKE 'a_%'` is false.
- Minimal drafts: `/Users/bba/pc/ai-native-infoschema-like-case-sensitive-draft.md` and `/Users/bba/pc/ai-native-id30010-method-case.md`.
- Bug-library state: written to `found_bug` as id30010 with `confirmed=1,status=confirmed`.
- `found_bug id30018`: `LOWER(table_name) = 'ACASE'` and `UPPER(table_name) = 'acase'` return `Acase`, while the projected self predicate is `0` and the CASE-wrapped reference returns no rows. This is scalar pushdown plus value-normalization, not the LIKE mechanism.
- Minimal drafts: `/Users/bba/pc/ai-native-infoschema-scalar-pushdown-case-draft.md` and `/Users/bba/pc/ai-native-id30018-method-case.md`.

Counterexample families:
- Binary-collation object names: mixed-case schema/table/column names against lowercase LIKE patterns.
- LIKE/REGEXP/escape variants where the extractor rewrites pattern semantics.
- Scalar-function pushdown over object names, especially `LOWER/UPPER(col)=const` combined with lowercased key normalization.
- Predicate forms where wrapping prevents extraction: explicit re-check, CASE-wrapped predicate, or projection of the scalar predicate.
- Other system-table extractors that lower-case keys, compile case-insensitive regexps, or drop the original predicate after prefiltering.

Oracle:
- Compare the shortcut path with an explicit SQL-visible re-check:
  ```sql
  SELECT ... FROM information_schema.tables
  WHERE table_schema = 'aiis_000' AND table_name LIKE 'a_%';

  SELECT ... FROM information_schema.tables
  WHERE table_schema = 'aiis_000'
    AND table_name LIKE 'a_%'
    AND (table_name LIKE 'a_%') = 1;

  SELECT ... FROM information_schema.tables
  WHERE table_schema = 'aiis_000'
    AND CASE WHEN table_name LIKE 'a_%' THEN 1 ELSE 0 END = 1;
  ```
- Required equality: row set and projected object names. Projection evidence such as `table_name LIKE 'a_%' AS scalar_pred` is triage evidence.
- For scalar-pushdown cases, every returned row must satisfy the projected self predicate (`LOWER/UPPER(table_name)=const AS self_ok`), and at least one matching-constant green control must remain green.
- Do not rely on plan-only evidence. This is a row-return contract for a SQL-visible system table.

Current result:
- Red surfaces: `information_schema.tables` and `information_schema.columns` both return `Acase` on the extracted path, while CASE-wrapped/explicit re-check returns only `a%b,a_b`.
- New red surface: `information_schema.tables` object-name scalar pushdown returns `Acase` for wrong-case `LOWER/UPPER` constants even though the returned row evaluates the predicate to false.
- Negative calibration: plain partition pruning, global-index partition pruning, and plan-cache drift probes found no mismatches before this selector hit. That is useful evidence that the efficiency came from switching to a better proof obligation, not from broader random SQL.

Next improvement:
- Pause this family for bug quality and fix-direction discussion; do not expand more `tables/columns` LIKE or `LOWER/UPPER` variants unless a new mechanism appears.
- If continuing non-DDL search, reuse the selector:
  `virtual/system table or cache/shortcut path + string/time/collation/session semantic dimension + custom extractor/key/reuse + CASE-wrapped/no-shortcut/self-predicate oracle`.

## P1: Null-Reject Proof for Outer-Join Simplification
Source anchors:
- `/Users/bba/pc/tidb/pkg/planner/util/null_misc.go`: `IsNullRejected`, `proveNullRejected`
- `/Users/bba/pc/tidb/pkg/planner/core/rule/rule_outer_join_to_semi_join.go`: `joinCondNullRejectsInnerCol`, anti-semi join conversion

P/Q/F:
- **P**: after replacing inner-side columns with SQL NULL, a predicate cannot evaluate TRUE.
- **Q**: outer-join null-extended rows cannot survive the filter.
- **F**: planner can convert an outer join to inner/anti-semi style plans.

Counterexample families:
- Builtins that are not strictly NULL-preserving but fold after nullification: `COALESCE`, `IF`, `IFNULL`, JSON functions, string functions.
- Three-valued logic traps: `NOT`, `OR`, `IS TRUE`, `IS FALSE`, `<=>`.
- Deferred constants / parameter markers under plan cache.
- Projection between selection and join where column identity may be disguised.

Oracle:
- Preferred: compare TiDB with a reference evaluation that preserves the explicit `LEFT JOIN` semantics, or compare with a rule-disabled internal path if available.
- Avoid plan-only assertions. The oracle must be row-set equality over stable user tables.

Why P1, not P0:
- The code is already heavily documented and has focused tests.
- A clean SQL-level toggle for this exact rule is less obvious than partition pruning or plan cache, so triage noise is higher.

## P0: Predicate-Simplification Collation Proof
Source anchors:
- `/Users/bba/pc/tidb/pkg/planner/core/rule/rule_predicate_simplification.go`: `updateInPredicate`, `mergeInAndNotEQLists`, `unsatisfiable`, `isNullInListContradiction`, predicate type reducers
- `/Users/bba/pc/tidb/pkg/planner/core/operator/logicalop/logical_datasource.go`: predicate simplification applied to pushed-down conditions

P/Q/F:
- **P**: predicates over the same column are classified as contradictory, redundant, or reducible.
- **Q**: removing or replacing predicates preserves SQL three-valued semantics, type coercion, and collation/coercibility semantics.
- **F**: planner simplifies filters, can turn branches into false, and can alter access conditions/pushed-down filters.

Known result:
- `found_bug candidate id30002`: with `s VARCHAR(...) COLLATE utf8mb4_general_ci`, TiDB returns both `a` and `A` for
  `s IN ('a','A') AND s != _utf8mb4'A' COLLATE utf8mb4_bin`, while direct expression evaluation and the CASE-wrapped oracle show only `a` should survive.
- Minimal draft: `/Users/bba/pc/ai-native-predicate-simplification-collation-draft.md`.
- Root model: `IN`/`!=` merge can shrink the IN list and remove the `!=` predicate, but the remaining `IN ('a')` is still evaluated under the column's case-insensitive collation and can match `A`.

Counterexample families:
- String collation compatibility and connection collation changes.
- `IN` lists with `NULL`, mutable constants, parameter markers.
- Signed/unsigned, decimal/float, date/time casts.
- OR branch pruning where one side has an unsafe contradiction proof.
- Equivalent-column propagation across joins.

Oracle:
- Primary low-noise oracle:
  ```sql
  SELECT ... FROM t WHERE <predicate>;
  SELECT ... FROM t WHERE CASE WHEN (<predicate>) THEN 1 ELSE 0 END = 1;
  ```
- Projection evidence is useful for triage: select each sub-predicate and the combined predicate as scalar columns to show SQL evaluation itself is correct.
- Plan evidence is supporting only. In id30002, `EXPLAIN` showed the pushed selection reduced to `in(s, "a")`.

Current smoke result:
- `/Users/bba/pc/ai_native_predicate_simplification_probe.py` implements the CASE-wrapped oracle over small integer/NULL/string/collation domains.
- First pass covered 800 generated predicates. It found 2 symmetric mismatches, both the same root:
  `(s != _utf8mb4'A' COLLATE utf8mb4_bin) AND (s IN ('a','A'))` and the reversed order.
- The first 600+ integer/NULL scalar/OR and `IN`/`!=` cases matched the CASE oracle, which is useful negative evidence for the next generator weights.

## P0: Memtable Time-Extractor Timezone Proof (id30012, selector S3 second hit)
Source anchors:
- `pkg/planner/core/memtable_predicate_extractor.go:816` `ClusterLogTableExtractor.Extract` → `extractTimeRange(..., "time", time.Local)`.
- Siblings using session tz: `SlowQueryExtractor` :1334, `MetricTableExtractor` :1048, `StatementsSummaryExtractor` :1626.
- `extractTimeRange` :558-626 converts literal with the passed zone and does NOT re-append matched GT/GE/LT/LE/EQ predicates to `remained`.

P/Q/F:
- **P**: cluster_log's extractor converts `time <op> literal` into an absolute [start,end] window using `time.Local`.
- **Q**: the extracted window equals the SQL-visible predicate under the session time zone.
- **F**: executor sends only that window to remote log search; the original predicate is dropped, so no scalar recheck.

Known result:
- `found_bug id30012` (confirmed): under `time_zone='+14:00'`, the same literal window returns the identical 415-row set as `+00:00` (rows violating WHERE), while the tz-respecting `+14:00` literal returns 0 (drops rows satisfying WHERE). Extractor ignores session tz entirely.
- Oracle: absolute-instant equivalence (same literal window under two session zones must select different absolute instants). Probe `/Users/bba/pc/ai_native_clusterlog_timezone_probe.py`; draft `/Users/bba/pc/ai-native-clusterlog-timezone-draft.md`; method case `/Users/bba/pc/ai-native-id30012-method-case.md`.
- Fix: `time.Local` → `ctx.GetSessionVars().StmtCtx.TimeZone()` at :816.

Selector lesson: S3 (shortcut/extractor lossy prefilter) is now 2/2. Sibling-asymmetry ("N extractors pass session tz, one passes server-local tz") is a reusable source-level red flag; found by reading one function, confirmed by differential. Pause gate: do not expand timezone variants.

## P0: Memtable Time-Extractor Precision Proof (id30015, selector S3 refinement)
Source anchors:
- `pkg/planner/core/memtable_predicate_extractor.go:576-593` converts time literals to DATETIME(6)-derived nanosecond timestamps.
- `pkg/planner/core/memtable_predicate_extractor.go:595-602` turns `EQ` into a start=end range and drops the matched predicate from `remained`.
- `pkg/planner/core/memtable_predicate_extractor.go:816-819` truncates `cluster_log` start/end to milliseconds before backend search.
- `pkg/executor/memtable_reader.go:460-465` sends only the millisecond window to `SearchLogRequest`.

P/Q/F:
- **P**: `cluster_log` extractor converts `time = literal` into a backend log-search window.
- **Q**: the millisecond backend window is exactly equivalent to the SQL-visible equality predicate.
- **F**: executor sends only the window; the original equality predicate is dropped, so there is no scalar recheck.

Known result:
- `found_bug id30015` (confirmed, inserted): `WHERE time = '2026/07/02 22:00:45.416500' AND message LIKE '%'` returned two rows at `2026/07/02 22:00:45.416`; each row evaluated `time = '...416500'` as `0`. CASE-wrapped scalar recheck over `[.416,.417)` returned 0.
- Oracle: same-millisecond safe window plus CASE-wrapped equality recheck and self-predicate projection on returned rows. Probe `/Users/bba/pc/ai_native_clusterlog_subms_precision_probe.py`; draft `/Users/bba/pc/ai-native-clusterlog-subms-precision-draft.md`.
- Fix: preserve scalar recheck for non-millisecond-aligned literals or make extractor/request precision-preserving.

Selector lesson: S3 needs a precision-lowering guard. A shortcut that maps a high-precision SQL predicate to a lower-precision backend request must keep a safe scalar recheck. This is distinct from id30012's time-zone context bug.

## P0: InfoSchema Scalar-Pushdown Normalization Proof (id30018, selector S3 refinement)
Source anchors:
- `pkg/planner/core/memtable_infoschema_extractor.go:190-209` calls `extractCol(..., valueToLower=true)` for InfoSchema object-name columns.
- `pkg/planner/core/memtable_predicate_extractor.go:321-327` allows equality extraction for `LOWER(col)` / `UPPER(col)` and records the pushed-down scalar function.
- `pkg/planner/core/memtable_predicate_extractor.go:333-349` applies value normalization to extracted constants.
- `pkg/planner/core/memtable_infoschema_extractor.go:292-300` lowercases row values first and returns before consulting pushed-down scalar functions.

P/Q/F:
- **P**: `LOWER/UPPER(TABLE_NAME)=const` can be extracted as an InfoSchema object-name prefilter.
- **Q**: lowercasing the object key and the constant preserves the SQL-visible scalar predicate.
- **F**: the scalar predicate is removed from `remained`; returned rows bypass normal scalar evaluation.

Known result:
- `found_bug id30018` (confirmed, inserted): with table `Acase`, `LOWER(table_name)='ACASE'` returns `Acase`, but projected `LOWER(table_name)='ACASE'` is `0`; the CASE-wrapped reference returns no rows. The symmetric `UPPER(table_name)='acase'` cell is also red. Matching constants (`'acase'` for LOWER and `'ACASE'` for UPPER) stay green.
- Oracle: row self-predicate plus CASE-wrapped reference. Probe `/Users/bba/pc/ai_native_infoschema_scalar_pushdown_case_probe.py`; draft `/Users/bba/pc/ai-native-infoschema-scalar-pushdown-case-draft.md`; method case `/Users/bba/pc/ai-native-id30018-method-case.md`.
- Fix: keep the original scalar predicate in `remained`, or make scalar-function pushdown compare the unmodified scalar result to an unmodified constant under exact SQL semantics.

Selector lesson: S3 now has a "composed shortcut" rule. A custom extractor is highest risk when it composes scalar-function pushdown with value/key normalization and then drops the original predicate. The strong oracle is not plan shape; it is a returned row whose projected predicate is false.

## P0: Metrics Summary Name Normalization Proof (id30019, S3 representative blast-radius)
Source anchors:
- `pkg/planner/core/memtable_predicate_extractor.go:1141-1145`: `MetricSummaryTableExtractor.Extract` calls `extractCol(..., "metrics_name", true)`.
- `pkg/planner/core/memtable_predicate_extractor.go:274-349`: `extractCol` / `merge` can lowercase extracted constants and remove the matched predicate from `remained`.
- `pkg/executor/metrics_reader.go:211-217`: `MetricsSummaryRetriever` filters `infoschema.MetricTableMap` by extracted `MetricsNames`.

P/Q/F:
- **P**: `METRICS_NAME = const` can be used as a metric-name prefilter for `information_schema.metrics_summary`.
- **Q**: lowercasing the metric key and the constant preserves the SQL-visible predicate on a `utf8mb4_bin` column.
- **F**: the original predicate is removed; retriever enumerates the lower-case metric key without scalar recheck.

Known result:
- `found_bug id30019` (confirmed, inserted): `METRICS_NAME` is `utf8mb4_bin`, but `WHERE metrics_name='TIDB_QPS'` returns `tidb_qps`; the projected self predicate `metrics_name='TIDB_QPS'` is `0`, and the CASE-wrapped reference returns no rows. Matching-case control `metrics_name='tidb_qps'` returns `self_ok=1`.
- `LOWER(metrics_name)='TIDB_QPS'` has the same symptom: fast path returns `tidb_qps`, while the projected predicate is `0` and the CASE reference is empty.
- Probe: `/Users/bba/pc/ai_native_metrics_summary_name_case_probe.py`; draft: `/Users/bba/pc/ai-native-metrics-summary-name-case-draft.md`; method case: `/Users/bba/pc/ai-native-id30019-method-case.md`.

Selector lesson:
- id30019 is not a new broad target selector. It is the representative cross-owner blast-radius proof for the same generic helper issue as id30018: `extractCol(..., valueToLower=true)` plus predicate removal.
- New stop rule:
  ```text
  if a generic helper bug is proven across a second owner:
    record one representative blast-radius case
    stop enumerating all users of the helper
    move to a different shortcut mechanism or return to DDL selectors
  ```

## P0: Hot Regions History Timezone Render Proof (id30023, S3 refinement)
Source anchors:
- `/Users/bba/pc/tidb/pkg/planner/core/memtable_predicate_extractor.go:950-959`: `HotRegionsHistoryTableExtractor` extracts `update_time` with `ctx.GetSessionVars().StmtCtx.TimeZone()` and drops the matched predicates.
- `/Users/bba/pc/tidb/pkg/planner/core/memtable_predicate_extractor.go:960-966`: backend request timestamps are lowered to millisecond values and `SkipRequest` is decided before scalar evaluation.
- `/Users/bba/pc/tidb/pkg/executor/memtable_reader.go:863-867`: `getHotRegionRowWithSchemaInfo` calls `updateTimestamp.In(tz)` but does not assign the returned `time.Time`.
- `/Users/bba/pc/tidb/pkg/infoschema/tables.go:1007-1024`: `UPDATE_TIME` is a SQL-visible timestamp column of `TIDB_HOT_REGIONS_HISTORY`.

P/Q/F:
- **P**: `UPDATE_TIME` predicates can be translated to a backend millisecond range using the session timezone.
- **Q**: returned rows are materialized under the same SQL-visible timezone and therefore satisfy the original predicate.
- **F**: the original `UPDATE_TIME` predicates are removed; returned rows bypass scalar recheck.

Known result:
- `found_bug id30023` (confirmed, inserted): under `time_zone='+14:00'`, a query for the absolute window `2026-07-03 13:40:41..13:40:42` returned 69 rows displayed as `2026-07-02 23:40:41`; the projected predicate sum was 0. The same range plus CASE self-recheck returned 0 rows. UTC control over the equivalent window returned 69 rows whose self-predicate sum was 69.
- Probe: `/Users/bba/pc/ai_native_hot_regions_history_timezone_probe.py`; draft: `/Users/bba/pc/ai-native-hot-regions-history-timezone-draft.md`; method case: `/Users/bba/pc/ai-native-id30023-method-case.md`.

Counterexample families:
- Backend request context and SQL-visible row construction context can diverge.
- Time conversion helpers that return a new value are called for side effect and ignored.
- System-table retrievers that drop predicates after request-side filtering but construct result datums manually.

Oracle:
- Trigger evidence: `EXPLAIN` shows `TIDB_HOT_REGIONS_HISTORY start_time...end_time`.
- Fast arm: plain `UPDATE_TIME` range under a non-UTC session, with projected predicate sum.
- Reference: same simple range plus `CASE WHEN <same predicate> THEN 1 ELSE 0 END = 1`, so the backend request is still bounded but scalar recheck is forced.
- Green control: UTC equivalent absolute window returns rows whose projected predicate is true.

Selector lesson:
- This is S3, but novelty is medium because the time-zone D_dim already existed in id30012. The new reusable shape is request context vs row-render context, not "more timezone columns." Stop further time-column enumeration unless source shows a distinct request/render context split.

## P0: TiKV Region Peers Backend Not-Found Proof (id30022, S3 refinement)
Source anchors:
- `/Users/bba/pc/tidb/pkg/infoschema/tables.go:1100`: `REGION_ID` is a SQL-visible BIGINT column of `TIKV_REGION_PEERS`.
- `/Users/bba/pc/tidb/pkg/planner/core/memtable_predicate_extractor.go:1673-1678`: the extractor removes original `region_id` / `store_id` predicates into `RegionIDs` / `StoreIDs`.
- `/Users/bba/pc/tidb/pkg/planner/core/memtable_predicate_extractor.go:1691-1696`: `EXPLAIN` emits `region_ids:[...]`.
- `/Users/bba/pc/tidb/pkg/executor/memtable_reader.go:962-971`: executor calls `pdCli.GetRegionByID(ctx, regionID)` and returns errors directly.
- `/Users/bba/pc/tidb/pkg/executor/memtable_reader.go:941-959`: `store_id` sibling uses `GetRegionsByStoreID` and pack/filter path, returning empty rows for missing store ids.

P/Q/F:
- **P**: `region_id = const` or an `IN` list can be delegated to PD point lookups.
- **Q**: the backend point lookup is semantically equivalent to a SQL filter over the SQL-visible `REGION_ID` column.
- **F**: the original scalar predicate is removed; backend not-found errors are returned directly to the user.

Known result:
- `found_bug id30022` (confirmed, inserted): `SELECT COUNT(*) FROM information_schema.tikv_region_peers WHERE region_id=0` errors with PD `400 Bad Request`, while the CASE-wrapped reference returns 0 rows. `region_id IN (0,2)` also errors, while the CASE reference returns the 3 valid rows for region 2. Existing region `2` is green, and sibling `store_id=0` returns 0 rows without error.
- Probe: `/Users/bba/pc/ai_native_tikv_region_peers_region_id_not_found_probe.py`; draft: `/Users/bba/pc/ai-native-tikv-region-peers-region-id-not-found-draft.md`; method case: `/Users/bba/pc/ai-native-id30022-method-case.md`.

Counterexample families:
- External point lookups for SQL filters where backend object-not-found is an API error.
- IN-list point lookups where one missing object should not abort valid ids.
- Sibling lookup paths with different error contracts (`region_id` point lookup vs `store_id` collection lookup).

Oracle:
- Trigger evidence: `EXPLAIN` shows `region_ids:[...]`.
- Reference: CASE-wrap the same `region_id` predicate so the extractor cannot consume it.
- Green controls: existing region id fast/reference equal; sibling `store_id=0` returns empty without error.

Selector lesson:
- This is S3, but it is not lossy value normalization, time precision, interval skip, or cache purity. It adds a new D_dim: backend error domain. A shortcut that turns a SQL predicate into an external point lookup must prove backend object-not-found maps to SQL empty rowset, and that mixed `IN` lists keep valid ids.

## P0: Statements Summary Coarse Interval Skip Proof (id30021, S3 refinement)
Source anchors:
- `pkg/planner/core/memtable_predicate_extractor.go:1556-1561`: `StatementsSummaryExtractor` documents a coarse time-range predicate for statement-summary tables.
- `pkg/planner/core/memtable_predicate_extractor.go:1580-1588`: if the derived coarse range has `StartTime > EndTime`, it sets `SkipRequest=true` and returns no rows.
- `pkg/planner/core/memtable_predicate_extractor.go:1619-1628`: `findCoarseTimeRange` derives `endTime` from `summary_begin_time` predicates and `startTime` from `summary_end_time` predicates.
- `pkg/infoschema/tables.go:1322-1324`: `summary_begin_time` and `summary_end_time` are SQL-visible timestamp columns.
- `pkg/util/stmtsummary/statement_summary.go:333-341`: statement-summary rows naturally cover nonzero refresh windows.

P/Q/F:
- **P**: `summary_begin_time <= A` gives a coarse upper bound A and `summary_end_time >= B` gives a coarse lower bound B.
- **Q**: if `B > A`, no statement-summary row can satisfy the query.
- **F**: MemTableScan sets `skip_request:true`, so the retained scalar predicates are never evaluated.

Known result:
- `found_bug id30021` (confirmed, inserted): with a real statement-summary window `[2026-07-02 23:00:00, 2026-07-02 23:30:00]`, choose `A=23:10:00` and `B=23:20:00`. The ordinary query `summary_begin_time <= A AND summary_end_time >= B` returns 0 rows and `EXPLAIN` shows `skip_request:true`. The CASE-wrapped reference returns satisfiable rows, and every reference row projects both predicates as true.
- Probe: `/Users/bba/pc/ai_native_statements_summary_coarse_range_probe.py`; draft: `/Users/bba/pc/ai-native-statements-summary-coarse-range-draft.md`; method case: `/Users/bba/pc/ai-native-id30021-method-case.md`.

Counterexample families:
- Rows that represent intervals/windows rather than points.
- Coarse skip logic that treats `start > end` as contradiction without proving the original SQL predicate is unsatisfiable.
- Any shortcut that keeps original predicates for scalar recheck in normal cases but has a `SkipRequest` fast arm that bypasses that recheck.

Oracle:
- Trigger evidence: `EXPLAIN` must show `skip_request:true` for the fast arm.
- Reference: CASE-wrap each original predicate so the extractor cannot build the coarse skip.
- Self-predicate evidence: reference rows must project both original predicates as true.
- Green control: a non-reversed overlap predicate over the same live window should not skip and should return rows.

Selector lesson:
- This is S3, but it is not the id30018/id30019 normalization helper. It adds a new D_dim: interval semantics. A point/range abstraction can be empty while the original interval-containment predicate is satisfiable.

## P0: Apply Cache Payload Purity Proof (id30020, selector S7)
Source anchors:
- `pkg/planner/core/exhaust_physical_plans.go:2278-2288`: `LogicalApply` enables Apply cache from correlated-column NDV and `tidb_mem_quota_apply_cache`.
- `pkg/executor/parallel_apply.go:631-647`: `ParallelNestedLoopApplyExec.fetchAllInners` builds the cache key from correlated outer-column values only.
- `pkg/executor/parallel_apply.go:650-653`: cache hit returns cached `innerList` without reopening/re-evaluating the inner executor.
- `pkg/executor/parallel_apply.go:657-714`: cache miss evaluates the inner executor and stores the resulting `chunk.List`.
- `pkg/executor/internal/applycache/apply_cache.go:26-28`: Apply cache is intended to reuse inner rows when the outer row value is the same.
- `pkg/expression/builtin_miscellaneous.go:1524-1530`: `UUID()` generates a new UUID during evaluation.
- `pkg/expression/function_traits.go:49-55` and `pkg/expression/util.go:1502-1515`: non-foldable/non-deterministic function metadata already exists for functions including UUID.

P/Q/F:
- **P**: repeated correlated outer values make Apply cache profitable; the cache key encodes those values.
- **Q**: the inner subquery result is a pure function of the correlated values.
- **F**: on cache hit, executor reuses cached `chunk.List` and skips inner re-evaluation.

Known result:
- `found_bug id30020` (confirmed, inserted): with `tidb_enable_parallel_apply=1`, `tidb_executor_concurrency=1`, and Apply cache enabled, a correlated scalar subquery `(SELECT UUID() FROM inner_t WHERE inner_t.a = outer_t.a LIMIT 1)` returns only one distinct UUID for 24 duplicate `a=1` rows and one for 16 duplicate `a=2` rows. With `tidb_mem_quota_apply_cache=0`, the same groups return 24 and 16 distinct UUIDs.
- Deterministic control: `(SELECT CONCAT('v', inner_t.a) ...)` stays `distinct_v=1` per key in both cache modes.
- Probe: `/Users/bba/pc/ai_native_apply_cache_volatile_probe.py`; draft: `/Users/bba/pc/ai-native-apply-cache-volatile-subquery-draft.md`; method case: `/Users/bba/pc/ai-native-id30020-method-case.md`.

Counterexample families:
- Volatile inner projections: `UUID()`, `RAND()`, `RANDOM_BYTES()`, sequence/session-variable functions where SQL semantics require re-evaluation.
- Inner filters that depend on volatile expressions rather than only correlated keys.
- Result-cache/reuse mechanisms whose key is complete for stable inputs but whose cached value stores evaluated expressions.

Oracle:
- Trigger evidence: `EXPLAIN ANALYZE` must show Apply `cache:ON` for the fast arm and `cache:OFF` for the reference arm.
- Red arm: cache ON collapses `COUNT(DISTINCT UUID())` below the duplicate outer-row count.
- Reference arm: cache OFF restores one UUID per outer-row subquery execution.
- Green control: deterministic inner expression stays stable per key in both cache modes.

Selector lesson:
- S7: key completeness is only half of a cache proof. The cached payload must also be pure with respect to that key.
- This is a deliberate post-id30019 pivot to a different shortcut mechanism. It validates the proof-obligation workflow outside S3 without reopening random executor fuzzing.

## P0: ANALYZE Sub-Job Lifecycle Coverage (id30014 / perf-30003, PS1 refined)
Source anchors:
- `pkg/executor/analyze.go`: `analyzeWorker` calls `StartAnalyzeJob` before partition work; `handleResultsErrorWithConcurrency` checks `SQLKiller.HandleSignal()` before receiving/sending results and can return after closing `saveResultsCh`.
- `pkg/executor/analyze.go`: failpoint `analyzeBeforeSendToSaveResults` is immediately before `saveResultsCh <- results`.
- `pkg/executor/analyze_worker.go`: save worker calls `finishJobWithLog` only for results received from `saveResultsCh`; drain mode can finish queued results, not an already-started result that was never handed off.

P/Q/F:
- **P**: every partition/global ANALYZE sub-job is inserted and started in `mysql.analyze_jobs`.
- **Q**: once the parent `ANALYZE TABLE` returns, every sub-job started by that statement is terminal (`finished` or `failed`), and no `running` row references a dead SQL process.
- **F**: `SHOW ANALYZE STATUS` and `mysql.analyze_jobs` expose non-terminal rows directly to users/operators.

Known result:
- `found_bug id30014` (confirmed, inserted): with `analyzeBeforeSendToSaveResults=2*off->pause`, interrupting a 4-partition `ANALYZE TABLE` leaves one partition job in `running` with a `process_id` absent from `information_schema.processlist`. Clean rerun appends finished rows but does not immediately clear the stale row.
- Oracle: processlist liveness + `mysql.analyze_jobs` terminal-state check + `SHOW ANALYZE STATUS`. Probe `/Users/bba/pc/ai_native_perf_pf6_analyze_interrupt.py`; draft `/Users/bba/pc/ai-native-perf-analyze-interrupt-running-job-draft.md`.

Selector lesson: PS1 is no longer only a checkpoint/rework selector. For background multi-task pipelines, a strong proof obligation is "every visible sub-job that reaches Start must reach Finish on parent exit." This is a deliberate non-DDL/perf pivot; it should not reopen broad executor fuzzing.

## P0: Restore-Path Reference Revalidation (id30011, methodology-v2 pilot)
Source anchors:
- `pkg/ddl/schema.go` `onRecoverSchema`: DBInfo restored verbatim (`schemaInfo.Clone()` → `CreateDatabase`), no placement-ref sanitization or validation.
- `pkg/ddl/table.go:291` `recoverTable` → `clearTablePlacementAndBundles`: sibling object path deliberately drops table/partition placement refs.

P/Q/F:
- **P**: FLASHBACK DATABASE reconstructs the dropped DBInfo from the history snapshot.
- **Q**: restored metadata must not reference objects the live catalog cannot resolve (the sibling table path states the intended semantics: recovered objects drop placement refs).
- **F**: SHOW CREATE DATABASE and CREATE TABLE inheritance trust the restored DB-level ref.

Known result:
- `found_bug id30011` (confirmed): drop policy between DROP DATABASE and FLASHBACK DATABASE → dangling DB-level ref; every CREATE TABLE in the recovered db fails `8239`. Same-name policy recreation heals by name (relevant to fix semantics). Bonus asymmetry: with the policy alive, the flashbacked DB keeps its ref while restored tables lose theirs.
- Probe: `/Users/bba/pc/ai_native_ddl_flashback_placement_probe.py` (6 cells, findings=2, greens trigger-evidenced). Draft: `/Users/bba/pc/ai-native-flashback-db-placement-draft.md`. Method case: `/Users/bba/pc/ai-native-id30011-method-case.md`.

Pause gate: do not expand restore-path variants (BR/IMPORT restore, `FLASHBACK TABLE TO`, recover × TiFlash replica/other owners are queued under selector S6 in `/Users/bba/pc/ai-native-selector-ledger.md`) (GUARDED: reopen only on a new sibling path or D_dim).

## Current Priority
1. **Hot-regions-history timezone render result**: id30023 is confirmed and inserted. Treat it as an S3 refinement with medium novelty: request timezone and row-render timezone split. Next work should be bug quality/fix direction, or another request/render context split with a distinct source root; do not enumerate more time columns.
2. **TiKV region-peers backend not-found result**: id30022 is confirmed and inserted. Treat it as an S3 refinement: backend object-not-found is not automatically a SQL execution error. Next work should be bug quality/fix direction, another backend point-lookup owner with a distinct error-domain oracle, or a return to DDL selectors; do not enumerate more `tikv_region_peers` region-id variants.
3. **Statements-summary coarse interval skip result**: id30021 is confirmed and inserted. Treat it as an S3 refinement: interval rows break point/range-style `start > end => empty` skip proofs. Next work should be bug quality/fix direction, or another shortcut only with a new D_dim; do not enumerate more statement-summary predicate permutations.
4. **Apply-cache payload purity result**: id30020 is confirmed and inserted. Treat it as the first S7 hit: cache key equality does not prove cached payload purity. Next work should be bug quality/fix direction or a distinct cache/reuse owner only if it has a new D_dim; do not randomly fuzz executor queries.
5. **Metrics-summary helper blast-radius result**: id30019 is confirmed and inserted. Treat it as the representative cross-owner proof for `extractCol(..., valueToLower=true)` plus predicate removal. Do not enumerate more `valueToLower=true` users; next work is fix direction / bug quality, a different shortcut mechanism, or a return to DDL selectors.
6. **InfoSchema scalar-pushdown result**: id30018 is confirmed and inserted. Pause expansion of `information_schema.tables/columns` `LOWER/UPPER` variants; the method value is the refined S3 rule, not more object-name permutations.
7. **ANALYZE lifecycle result**: id30014/perf-30003 is a confirmed PS1 selector-validation case. Pause expansion of ANALYZE variants; next work is bug quality/fix direction, not more kill timings.
8. **InfoSchema extractor collation result**: id30010 is a confirmed non-DDL selector-validation case. Pause expansion of `information_schema.tables/columns` LIKE variants; reuse the shortcut/extractor selector on another module only via a deliberate pivot.
9. **DDL-only reference ownership matrix**: still the default mainline after the 2026-07-01 scope correction. Work from `/Users/bba/pc/ai-native-ddl-reference-matrix.md`, `/Users/bba/pc/ai_native_ddl_reference_matrix_probe.py`, `/Users/bba/pc/ai_native_ddl_object_reference_probe.py`, and `/Users/bba/pc/ai_native_ddl_stateful_object_probe.py`.
8. **Object-reference DDL matrix**: extend the same rewrite/block proof obligation beyond columns to table/index/partition references only when a concrete owner/path pair has not already reached a pause gate.
9. **Add-index/global-index rollback delete-range result**: id30009 is confirmed; `/Users/bba/pc/ai-native-add-index-global-rollback-delete-range-draft.md` and `/Users/bba/pc/ai-native-id30009-method-case.md` document the red cell. Do not expand add-index happy paths before fix-direction validation.
8. **Stats side-metadata result**: `/Users/bba/pc/ai_native_ddl_stats_reference_probe.py` found one root family; `/Users/bba/pc/ai-native-stats-column-rename-draft.md` is now issue-discussion quality. Do not expand more stats cells while GUARDED (reopen only on a new state dimension or owner/container surface).
9. **Privilege side-metadata selector result**: `/Users/bba/pc/ai_native_ddl_privilege_reference_probe.py` covered 3 cells and showed grant rows are name-bound policy, not object-identity references. Treat it as a negative selector example, not a bug target.
10. **Table-cache side-metadata result**: `/Users/bba/pc/ai_native_ddl_table_cache_reference_probe.py` found one root family; `/Users/bba/pc/ai-native-table-cache-drop-database-draft.md` is issue-discussion ready. Do not expand table-cache variants while GUARDED (reopen only on a new state dimension or owner/container surface).
11. **Sequence-default reference result**: `/Users/bba/pc/ai_native_ddl_sequence_default_reference_probe.py` found id30005; `/Users/bba/pc/ai-native-sequence-default-reference-draft.md` is issue-discussion ready. Do not expand sequence variants (GUARDED: reopen only on a new sibling path or D_dim).
12. **Affinity negative selector result**: `/Users/bba/pc/ai_native_ddl_affinity_reference_probe.py` covered 6 cells and found no stale visible affinity reference. Treat affinity as a downweighted external-state owner unless a deterministic stale PD-state oracle appears.
13. **Functional-index hidden-column boundary result**: `/Users/bba/pc/ai_native_ddl_functional_index_reference_probe.py` covered 5 cells and found no dependency loss for rename/change/drop dependency preservation. Treat that stale-reference slice as green, but do not treat all expression-index MODIFY paths as green: id630007 shows metadata-only MODIFY is falsely rejected by the same common dependency gate.
14. **DB-level placement negative selector result**: `/Users/bba/pc/ai_native_ddl_db_placement_reference_probe.py` covered 6 cells and found no dangling DB/table policy reference. Treat ordinary placement as covered across DB/table/partition unless a new state dimension or container bypass appears.
15. **View reference selector result**: `/Users/bba/pc/ai_native_ddl_view_reference_probe.py` covered 5 cells and confirmed view refs are name-bound SQL text. Treat view invalidation after base-object DDL as out of the rewrite/block owner contract.
16. **Resource-group SWITCH_GROUP selector result**: `/Users/bba/pc/ai_native_ddl_resource_group_reference_probe.py` covered 3 cells and confirmed `SWITCH_GROUP` currently stores an unvalidated target name. Treat it as a negative selector unless create/alter semantics start validating target existence.
17. **Hypo-index side-metadata result**: `/Users/bba/pc/ai_native_ddl_hypo_index_reference_probe.py` found id30006; `/Users/bba/pc/ai-native-hypo-index-reference-draft.md` is issue-discussion ready. Do not expand hypo-index variants (GUARDED: reopen only on a new sibling path or D_dim).
18. **Reorganize-partition global-index result**: `/Users/bba/pc/ai_native_ddl_reorg_global_index_reference_probe.py` found id30007; `/Users/bba/pc/ai-native-reorg-global-index-reference-draft.md` is issue-discussion ready and now includes the fix-validation contract. Do not expand more reorg/global-index variants (GUARDED: reopen only on a new sibling path or D_dim).
19. **Table-lock owner-key rewrite result**: id30008 is reproduced on testbed `8192975` after enabling `enable-table-lock=true`; `/Users/bba/pc/ai-native-table-lock-cross-schema-rename-draft.md` is issue-discussion ready and `/Users/bba/pc/ai-native-id30008-method-case.md` records the selector. Do not expand table-lock variants (GUARDED: reopen only on a new sibling path or D_dim).
20. **Pause optimizer/executor proof families**: id30001/id30002 remain method evidence. Partition-pruning and plan-cache probes are green calibration unless the user explicitly reopens that lane or a new proof obligation supplies a stronger selector.

Current DDL smoke result:
- `ai_native_ddl_reference_matrix_probe.py` covered 28 high-value cells on the `fp-tidb` TiKV testbed.
- Unexpected findings: 0.
- Known controls reproduced: CHECK + CHANGE silent loss, CHECK + multi-schema CHANGE silent loss, partial-index predicate + RENAME misleading 1054.
- The useful new method lesson is that block oracles must verify the error family, not just nonzero return code.
- `ai_native_ddl_object_reference_probe.py` covered 17 placement/global-index object-reference cells on the `5c9198e948` TiKV testbed.
- Unexpected findings: 0; skipped: 0.
- Placement ordinary paths are green: in-use drop blocks, table/partition placement rewrite releases old policy and protects new policy, remove partitioning releases partition policy but preserves table policy, drop partition releases dropped partition policy, truncate partition preserves the policy on the still-existing partition, and `ALTER PLACEMENT POLICY` keeps table/partition dependents in-use.
- Global/local index ordinary paths are green: missing global blocks, `UPDATE INDEXES` rewrites, remove partitioning clears global/partition metadata, exchange partition blocks global source index, drop/truncate partition keeps visible rowsets consistent, and mixed placement+global remove-partitioning rewrites both owner families.
- `ai_native_ddl_stateful_object_probe.py` covered 14 stateful cells on failpoint-enabled `fp-tidb`.
- Unexpected findings: 0; skipped: 0.
- Stateful partition-reorg rollback layer is green: `PARTITION BY ... UPDATE INDEXES` rollback restored non-partitioned table and released added partition policy; `REMOVE PARTITIONING` rollback restored partition metadata, table/partition policy refs, original global marker, and rowsets, across `reorgPartRollback2/3/4`.
- Stateful partition-reorg retry layer is green: one-shot `reorgPartFail4/5` retried successfully for both `PARTITION BY ... UPDATE INDEXES` and `REMOVE PARTITIONING`, preserving/releasing placement refs and global/local markers as expected.
- Stateful truncate layer is green: `truncatePartCancel1` preserved original metadata/policy/global/rowsets; one-shot `truncatePartFail1/2/3` retried successfully and removed only the truncated partition rows.
- `ai_native_ddl_delete_range_probe.py` covered 2 delete-range metadata cells.
- Unexpected findings: 0; skipped: 0.
- Delete-range enqueue layer is green: `REMOVE PARTITIONING` records the old global-index range, and `DROP GLOBAL INDEX` records only the logical index range without partition/table range leakage.
- `ai_native_ddl_placement_bundle_failure_probe.py` covered 5 placement-bundle failure cells with `putRuleBundlesError`.
- Unexpected findings: 0; skipped: 0.
- Placement-bundle failure layer is green: persistent failure leaves table/partition/policy metadata unchanged; retryable failure succeeds and preserves dependencies.
- `ai_native_ddl_fk_object_reference_probe.py` covered 10 FK table/index object-reference cells.
- Unexpected findings: 0; skipped: 0.
- FK table/index layer is green: table rename rewrites/preserves FK targets, drop/truncate parent and drop supporting indexes block with FK-owner errors, rename/drop redundant supporting index preserves enforcement.
- `ai_native_ddl_masking_policy_reference_probe.py` covered 13 masking-policy side-metadata cells.
- Unexpected findings: 0; skipped: 0.
- Masking-policy layer is green: table/cross-DB/multi-table rename rewrites policy table refs; column rename and multi-schema change rewrite `column_name` plus expression; unsupported modify blocks with policy intact; drop column/table/database clean policy rows; truncate rewrites `table_id` and leaves the policy operable.
- `ai_native_ddl_stats_reference_probe.py` covered 7 stats side-metadata cells.
- Unexpected findings: 2; skipped: 0.
- Stats layer mixed result: table rename, add/remove partitioning global stats ID rewrite, truncate table, and truncate partition are green; column rename and change-column rename after `ANALYZE TABLE` leave `SHOW STATS_HISTOGRAMS` displaying the old column name until re-analyze. Treat these two red cells as one root family.
- Minimal candidate draft: `/Users/bba/pc/ai-native-stats-column-rename-draft.md`.
- `ai_native_ddl_privilege_reference_probe.py` covered 3 privilege grant side-metadata screening cells.
- Unexpected findings: 0; skipped: 0.
- Privilege grants are downweighted as a DDL object-reference owner: table grants stay on textual names across rename/drop/recreate, and column grant metadata stays on the textual column name across rename/replacement. This is name-bound policy behavior, so "no rewrite" is not a red cell by itself.
- `ai_native_ddl_table_cache_reference_probe.py` covered 3 table-cache side-metadata cells.
- Unexpected findings: 1; skipped: 0.
- Table-cache layer mixed result: `CACHE`/`NOCACHE` lifecycle is green, and cached table direct DDL paths block rename/drop/truncate/index/partition changes; `DROP DATABASE` succeeds but leaves the dropped table ID in `mysql.table_cache_meta`. Treat this as one root family.
- Minimal candidate draft: `/Users/bba/pc/ai-native-table-cache-drop-database-draft.md`.
- `ai_native_ddl_region_split_policy_probe.py` covered 5 region-split policy cells.
- Unexpected findings: 0; skipped: 0.
- Region-split policy is a negative selector example: SQL-visible split policy is stored in object-local `TableInfo`/`IndexInfo` metadata, so rename/drop/change-column paths naturally preserve or remove it without independent side-row cleanup.
- `ai_native_ddl_sequence_default_reference_probe.py` covered 5 sequence-default reference cells.
- Unexpected findings: 3; skipped: 0.
- Sequence-default layer mixed result: live defaults and `CHANGE COLUMN ... DEFAULT NEXT VALUE FOR seq` are green, but `DROP SEQUENCE`, `RENAME TABLE seq TO seq2`, and cross-DB `DROP DATABASE` can leave a live table default pointing at a missing sequence. Treat these three red cells as one root family.
- Minimal candidate draft: `/Users/bba/pc/ai-native-sequence-default-reference-draft.md`.
- `ai_native_ddl_affinity_reference_probe.py` covered 6 affinity reference-owner screening cells.
- Unexpected findings: 0; skipped: 0.
- Affinity is a negative selector example: `SHOW AFFINITY` rows are enumerated from live InfoSchema, PD group state is an external annotation, table/partition DDL paths either cleanup or block, and the 6-cell probe found no stale visible reference across rename/truncate/drop/drop database and partition block paths.
- `ai_native_ddl_functional_index_reference_probe.py` covered 5 functional-index hidden-column cells.
- Unexpected findings: 0; skipped: 0.
- Functional-index hidden-column stale-reference layer is green: referenced-column rename/change/drop block with `3837`, `DROP INDEX` releases the dependency for a later column rename, and one-statement multi-schema `DROP INDEX + RENAME/DROP COLUMN` blocks in both orders while preserving schema. Metadata-only `MODIFY COLUMN` is no longer green: id630007 proves COMMENT/DEFAULT changes are over-rejected by the common dependency gate.
- `ai_native_ddl_db_placement_reference_probe.py` covered 6 DB-level placement reference cells.
- Unexpected findings: 0; skipped: 0.
- DB-level placement layer is green: database placement is visible, in-use policy drop blocks with `8241`, database policy rewrite releases the old policy and protects the new one, `DEFAULT` and `DROP DATABASE` release the DB ref, and the table inheritance boundary is explicit.
- `ai_native_ddl_view_reference_probe.py` covered 5 view reference screening cells.
- Unexpected findings: 0; skipped: 0.
- View references are name-bound SQL text: base table/column rename, base table drop, and cross-DB base database drop are allowed, and the view keeps old SELECT text and becomes invalid. Do not classify this as a sequence-default style dangling reference.
- Delete-range GC worker consumption/redo is currently downweighted because no low-cost SQL/HTTP trigger was found; `tidb_gc_run_interval` remains minimum 10 minutes and `ignoreDeleteRangeFailed` is not a failure injector.

Current non-DDL shortcut/extractor result:
- `ai_native_partition_prune_probe.py` and `ai_native_partition_global_index_prune_probe.py` both returned `findings=0`; keep their three-way row-set oracles as green calibration.
- `ai_native_plan_cache_drift_probe.py` returned `findings=0` while proving cache-hit evidence works for baseline cases.
- InfoSchema predicate extraction produced id30010 with a stronger selector: a custom system-table extractor drops the original predicate, while SQL-visible collation requires case-sensitive LIKE behavior.
- InfoSchema scalar pushdown produced id30018 with the same selector but a new sub-shape: the extractor composes `LOWER/UPPER` pushdown with value normalization, drops the original predicate, and returns rows whose projected predicate is false.
- Metrics-summary name filtering produced id30019 with the same generic helper in a second owner: `extractCol(..., valueToLower=true)` lowercases a `utf8mb4_bin` equality constant, drops the original predicate, and returns rows whose projected predicate is false. This is representative blast-radius evidence, not permission to enumerate every helper user.

## Next Concrete Experiment
The latest positive hit is id630005: `CREATE TABLE ... LIKE` can mutate the source table's CHECK
constraint names while constructing the target table. The red cells have reached the pause-gate
state: repro, source chain, method case, oracle, selector, and bug-library entry are documented.
Do not enumerate LIKE options or CHECK expression syntax (GUARDED: reopen only on another
pointer-backed metadata owner, a behavior-changing source mutation, or fix validation).

Current target card:

```text
Goal:
  Close the CREATE TABLE LIKE shallow-copy source-mutation case at id630005,
  then choose a different DDL proof obligation or deliberately enter fix-validation mode.

Question:
  Can the loop stay in DDL while finding clone/rebuild bugs without enumerating LIKE syntax?

Expected output:
  1. issue-ready repro and source chain for id630005 (done)
  2. fix-semantics recommendation (done):
     deep-clone pointer-backed CHECK constraint metadata before target-only rename
  3. source/target metadata isolation oracle O16 (done)
  4. selector update S13 for shallow-copy target mutation (done)
  5. explicit stop rule so LIKE proof does not become option/expression enumeration (done)
  6. choose another DDL proof obligation with a different P/Q relation,
     or validate the fix across constraints, indexes, TTL, affinity, placement, and partition metadata
```

Method case:

```text
/Users/bba/pc/ai-native-id630005-method-case.md
```

id30009 remains the newest DDL add-index positive hit and pause gate. Its minimized red cell is failed/canceled partition `ADD INDEX ... GLOBAL` rollback registering delete-range cleanup under partition IDs instead of the table ID. Do not expand add-index variants (GUARDED: reopen only on a new sibling path or D_dim); the repair model and raw-KV evidence are documented in `/Users/bba/pc/ai-native-add-index-global-rollback-delete-range-draft.md` and `/Users/bba/pc/ai-native-id30009-method-case.md`.

id30008 remains a separate positive hit and pause gate. Its minimized red cell is cross-schema `RENAME TABLE` leaving a table-lock stale after `UNLOCK TABLES`. Do not expand table-lock variants (GUARDED: reopen only on a new sibling path or D_dim); the source chain, same-schema green neighbor, repair options, and testbed reproduction are documented in `/Users/bba/pc/ai-native-table-lock-cross-schema-rename-draft.md` and `/Users/bba/pc/ai-native-id30008-method-case.md`.

id30007 remains a separate positive hit and pause gate. Its minimized red cell is `REORGANIZE PARTITION` leaving a replacement global index incomplete for later non-touched partitions. Do not expand reorg/global-index variants (GUARDED: reopen only on a new sibling path or D_dim); the repair contract and fix-validation matrix are documented in `/Users/bba/pc/ai-native-reorg-global-index-reference-draft.md` and `/Users/bba/pc/ai-native-id30007-method-case.md`.

id600001 is a separate `REORGANIZE PARTITION` positive hit and pause gate. Its minimized red cell is nonclustered reorg after `EXCHANGE PARTITION ... WITHOUT VALIDATION`, where two old partitions contain identical `(row bytes, _tidb_rowid)` rows and the backfill fast path treats raw-byte equality as identity, dropping one visible row. Do not expand reorg syntax variants (GUARDED: reopen only for a different equality-as-identity fast path or fix validation); the repair contract and guard matrix are documented in `/Users/bba/pc/ai-native-reorg-duplicate-rowid-drop-draft.md` and `/Users/bba/pc/ai-native-id600001-method-case.md`.

id630001 is a separate `MODIFY COLUMN` positive hit and pause gate. Its minimized red cells are `varchar(4)->varchar(3)` and `char(4)->char(3)` on utf8mb4 value `中中中`: the target types directly accept the value by character length, but the DDL precheck rejects it because it uses byte `LENGTH`. Do not enumerate charsets/string variants (GUARDED: reopen only for a different precheck metric, a silent acceptance bug, or fix validation across binary/indexed boundaries); the repair contract and guard matrix are documented in `/Users/bba/pc/ai-native-modify-column-multibyte-shrink-draft.md` and `/Users/bba/pc/ai-native-id630001-method-case.md`.

id630002 is a second S10 `MODIFY COLUMN` positive hit and pause gate. Its minimized red cells are FK `varchar` length transitions whose target schemas are directly accepted: child `varchar(20)->varchar(10/15)` with parent `varchar(10)`, and parent `varchar(10)->varchar(15)` with child `varchar(20)`. Do not enumerate FK type-pair variants (GUARDED: reopen only for a different validation metric, a silent invalid-metadata acceptance, or fix validation across parent/child directions and binary/decimal boundaries); the repair contract and guard matrix are documented in `/Users/bba/pc/ai-native-fk-modify-column-length-draft.md` and `/Users/bba/pc/ai-native-id630002-method-case.md`.

id30006 remains a separate positive hit and pause gate. Its minimized red cells are column/table/database DDL leaving `SessionVars.HypoIndexes` stale or resurrected in `SHOW CREATE TABLE`; do not expand hypo-index variants (GUARDED: reopen only on a new sibling path or D_dim).

Narrow source validation currently favors session-map cleanup/rekey semantics: hypo indexes are advisory session metadata, so real column/table DDL should not generally be blocked by them. Column rename/change/drop can drop affected hypo indexes, or rewrite them if all referenced fields can be proven safe. Table/database drop should clean `SessionVars.HypoIndexes`; table rename can rekey or drop. `SHOW CREATE TABLE` should defensively avoid printing hypo indexes whose column references no longer match the current `TableInfo`, but this is only a backstop because stale map entries are what cause same-name resurrection.

Sequence-default remains a separate positive hit and pause gate. Its minimized red cells are `DROP SEQUENCE`, `RENAME TABLE seq TO seq2`, and cross-DB `DROP DATABASE`; do not expand sequence variants (GUARDED: reopen only on a new sibling path or D_dim).

## Latest positive hit: ADD PARTITION IF NOT EXISTS DEFAULT gate ordering (id630016)

```text
P_check:
  LIST table already has a DEFAULT partition.

Q_claim:
  ADD LIST PARTITION is unsupported on that table shape.

D_dims:
  requested partition already exists vs genuinely new partition; IF NOT EXISTS duplicate no-op
  vs capability rejection for new objects.

F_effect:
  executor returns ERROR 8200 before the duplicate-name IF NOT EXISTS handler can run.

O_oracle:
  O18 idempotent DDL flag oracle: duplicate/new x DEFAULT/no-DEFAULT four-cell matrix.
```

Evidence:

- `l_no_default(p0,p1)`: duplicate `ADD PARTITION IF NOT EXISTS p0` returned Note 1517 and kept
  `p0,p1`.
- `l_default_dup(p0,pdef DEFAULT)`: duplicate `ADD PARTITION IF NOT EXISTS p0` returned ERROR
  8200.
- `l_default_new(p0,pdef DEFAULT)`: new `p1` returned ERROR 8200 as the green capability-control.
- `l_no_default_new(p0)`: new `p1` succeeded.

Status: **CONFIRMED**, inserted into remote `found_bug` as id630016
(`MAX(id)=630016,COUNT=51`).

Selector lesson: S15's "precheck ordering" branch is broader than raw requested-name counts.
Even when the flag survives and the duplicate catch exists, an earlier capability/default gate can
make the idempotence catch unreachable. Reopen this sub-shape only for another capability gate
before existence classification or fix validation.

Artifacts:

- `/Users/bba/pc/ai-native-add-partition-if-not-exists-default-precheck-draft.md`
- `/Users/bba/pc/ai-native-id630016-method-case.md`

## Previous positive hit: ADD COLUMN inline CHECK owner handoff (id30032)

```text
P_check:
  ADD COLUMN validates and builds the column metadata from ColumnDef.

Q_claim:
  A successful ADD COLUMN publishes the requested column definition, including inline CHECK
  constraints accepted by the parser.

D_dims:
  column metadata vs constraint metadata; parent job vs child constraint job; direct target schema
  vs transition schema; warning/error visibility; enforcement on future writes.

F_effect:
  CreateNewColumn discards the constraints returned by buildColumnAndConstraint, then AddColumn
  submits only ActionAddColumn. The CHECK owner never runs.

O_oracle:
  O23 target_schema_constraint_reference: direct CREATE and sequential ADD CHECK as safe
  references; SHOW CREATE/check_constraints metadata; violating INSERT b=0.
```

Evidence:

- Direct reference: `CREATE TABLE t(a INT, b INT DEFAULT 1 CHECK(b > 0))` publishes
  `CONSTRAINT t_chk_1 CHECK ((b > 0))`; `INSERT b=0` fails with ERROR 3819.
- Sequential reference: `ALTER TABLE t ADD COLUMN b INT DEFAULT 1; ALTER TABLE t ADD CONSTRAINT
  ck CHECK(b > 0)` publishes `ck`; `INSERT b=0` fails with ERROR 3819.
- Red transition: `ALTER TABLE t ADD COLUMN b INT DEFAULT 1 CHECK(b > 0)` succeeds with
  `@@warning_count=0`; SHOW CREATE contains no CHECK; `information_schema.check_constraints`
  contains no row; `INSERT b=0` succeeds and stores `3:0:0`.
- Named inline CHECK is the same red root: `CONSTRAINT ck_inline CHECK(b > 0)` is also absent and
  `b=0` succeeds.

Status: **CONFIRMED**, inserted into remote `found_bug` as id30032
(`MAX(id)=630014,COUNT=49`).

Selector lesson: S18 embedded constraint owner loss. When a DDL syntax embeds a child obligation
inside a parent owner, the proof is not complete until the child obligation is transferred to its
real owner and verified by a target-schema reference. Do not enumerate column options; look for
different embedded child owners or fix validation.

Boundary checks after id30032:

- `pid INT REFERENCES p(id)` is not an ALTER-specific red cell because direct `CREATE TABLE` also
  ignores the column-level `REFERENCES` clause, publishes no FK metadata, and accepts bad parent
  keys. Without a direct target reference, this is a product/compatibility boundary, not S18.
- `ALTER TABLE t ADD COLUMN b INT, ADD INDEX idx_b(b)` and sibling `ADD CHECK` / generated-column
  dependency forms fail because later specs validate against the pre-execution schema. TiDB's
  public ALTER TABLE compatibility notes explicitly document that multi-schema ALTER validates
  against the table schema before execution and can reject references to columns added earlier in
  the same statement. Treat these as boundary/owner-ruling cases, not confirmed bugs.

Artifacts:

- `/Users/bba/pc/ai-native-add-column-inline-check-loss-draft.md`
- `/Users/bba/pc/ai-native-id30032-method-case.md`

Post-pause next-owner scan:

```text
/Users/bba/pc/ai-native-ddl-next-owner-scan.md
```

Current scan result:
- `ATTRIBUTES` / PD label rules are structurally high-signal but already have strong coverage for rename, truncate, drop, recover, flashback, drop database, partition drop/truncate, and exchange partition. Treat this as a green-control owner and coverage-gate example, not the next live bug target.
- TTL job-state tables are ID-keyed side metadata, but cleanup belongs to the TTL worker and background GC. Do not call immediate post-DDL TTL rows a bug unless a deterministic cleanup trigger makes the oracle low-noise.
- stats lock/analyze-options/column-usage remain paused because id30003 already hit the stats side-metadata owner family.
- region split policy is now a documented negative screen: 5 cells found no stale reference, and the source shape is object-local `TableInfo`/`IndexInfo` metadata rather than independent side state.
- affinity is now a documented negative screen: 6 cells found no stale visible reference, and the source shape is live InfoSchema rows plus optional PD group state rather than a public independent side row.
- sequence-default reference is now a documented positive hit: 5 cells found 3 dangling-reference red cells. Terminal state CLOSED-FIXABLE (fix semantics documented); GUARDED against blast-radius expansion.
- functional-index hidden-column dependency now has a split result: stale-reference/owner-removal cells found no dependency loss and multi-schema owner-removal plus referenced-column change blocks against original metadata, while metadata-only `MODIFY COLUMN` is id630007's S11 companion false-rejection case.
- DB-level placement is now a documented negative screen: 6 cells found no stale DB/table policy reference, and source/test evidence shows the policy in-use scan covers DB, table, and partition refs.
- view references are now a documented negative screen: 5 cells showed create-time validated SQL text is not a maintained object-identity dependency.
- resource-group `SWITCH_GROUP` is now a documented negative screen: 3 cells showed create/alter allows a missing switch target, so the field is an unvalidated name parameter rather than a maintained DDL reference edge.
- hypo-index session metadata is now a documented positive hit: 7 cells found 6 stale/resurrected side-metadata red cells after column/table/database DDL. Terminal state CLOSED-FIXABLE (fix semantics documented); GUARDED against blast-radius expansion.
- hypo TiFlash replica session metadata is now a documented sibling negative: it is session-local and table-name keyed, but the only known use is planner/`EXPLAIN`; it is not merged into `SHOW CREATE TABLE`, `INFORMATION_SCHEMA`, or another current-schema DDL surface.
- SQL binding metadata is now a documented policy-text negative: `CREATE BINDING` validates the SQL text, but existing behavior and tests intentionally keep bindings after index drop/rename, so stale `USE INDEX(old)` text is not a maintained object-identity edge.
- local temporary table session metadata is now a documented design-boundary negative: source comments explicitly say local temporary tables have a loose database relationship and may survive database drop.
- reorganize-partition replacement global index is now a documented positive hit: 2 cells found 1 red global-index rowset inconsistency and 1 green placement-policy control. Terminal state CLOSED-FIXABLE (fix direction documented); GUARDED against blast-radius expansion; the validation contract should prove the non-touched set semantics rather than only replaying the current repro.
- reorganize-partition duplicate rowid repair is now a documented positive hit: 4 cells found 1 red data-loss case and 3 green guard controls. Terminal state CLOSED-FIXABLE (fix direction documented); GUARDED against reorg syntax expansion. The reusable selector is equality-as-identity with missing source/container ID, not "more partition DDL."
- table-lock session metadata is now a documented positive hit: same-schema locked-table rename is green, but cross-schema rename keeps `TableID` while changing `SchemaID`, so `UNLOCK TABLES` can trust a stale session owner key and leave the new-schema table locked. Terminal state CLOSED-FIXABLE (fix direction documented); GUARDED against blast-radius expansion.
- masking-policy side metadata has a new ID-swap positive hit: basic rename/column/drop/truncate paths were green, but `EXCHANGE PARTITION` swaps table/partition IDs without the masking-policy remap helper. id630014 is confirmed and guarded; reopen only for fix validation or a different owner-changing DDL entrypoint.
- add-index/global-index rollback delete-range is now a documented positive hit: success path uses tableID and local rollback uses partitionID, but failed/canceled partition `ADD INDEX ... GLOBAL` rebuilds cleanup args as local and misses tableID global-index KV. Terminal state CLOSED-FIXABLE (fix direction documented); GUARDED against blast-radius expansion.
- InfoSchema / metrics predicate extraction is now a documented non-DDL positive family: shortcut prefiltering for `TABLE_NAME LIKE` violates `utf8mb4_bin` scalar semantics (id30010), scalar-function pushdown plus value normalization can return rows that fail `LOWER/UPPER(TABLE_NAME)` (id30018), and the same `valueToLower=true` helper can return `metrics_summary` rows that fail `METRICS_NAME='TIDB_QPS'` (id30019). Terminal state CLOSED-FIXABLE (fix direction documented); GUARDED against helper-user enumeration, then reuse the shortcut/extractor selector only on a different mechanism.

The next live experiment should choose one of two lanes deliberately:
- DDL lane: use the refined DDL-owner selector below.
- Non-DDL lane: use the id30010 shortcut/extractor selector, not broad optimizer/executor fuzz.

DDL target cells:
- owners backed by a sys table or side metadata where DDL must keep object IDs and object names synchronized.
- session-local or cache-local side metadata created by DDL syntax and merged into public DDL/API output.
- executable schema expressions that reference a separate DDL object, especially when create/alter checks existence but drop/rename/drop-database paths have no reverse dependency scan.
- owners where object-identity binding is proven; reject pure name-bound policies before building a rewrite/block matrix.
- reject create-time validated SQL text such as views unless product semantics promise schema-bound rewrite/block behavior.
- reject fields that merely store another object's name when create/alter does not validate that the target exists.
- reject object-local properties that naturally move/drop with `TableInfo` or `IndexInfo` unless a separate cache, async record, or invalidation layer is found.
- reject external-state owners whose public SQL rows are driven by live InfoSchema unless a separate stale public state surface is proven.
- reject session/cache metadata whose only observable surface is optimizer or `EXPLAIN` behavior; keep executor/planner effects as consequence oracles, not as the DDL owner target.
- reject public surfaces that are intentionally historical, advisory, or saved user policy text, even if create-time validation exists.
- reject local/session objects with explicitly documented lifecycle boundaries that differ from normal database/table ownership.
- reject "sequential succeeds but single multi-schema blocks" as a red signal unless intra-statement dependency elimination is an explicit product contract.
- reject ordinary placement policy paths unless a new owner or state dimension bypasses the already-covered DB/table/partition in-use scan.
- owners whose storage is ID-keyed but whose public `SHOW`/API surface exposes names.
- owners where sibling DDL paths already have explicit block/cleanup rules but a broader container path may bypass those rules.
- DDL entrypoints where one path calls an owner-specific helper and another path appears to bypass it.
- DDL paths that update storage rows but may not advance the version/invalidation signal used to refresh cached display metadata.
- multi-schema or stateful variants that select a different preparation/completion path before the same owner check.
- sibling DDL paths where the common paths are green but a less-common path has a separate multi-stage iterator, especially if the code says it must visit every remaining partition/index/ref.
- DDL repair fast paths that use target-key existence or payload equality to prove an object was already handled; require the source/owner/container identity dimension to be present in the proof.
- session/cache/sys-table side state where object ID is stable but owner/container key can change; the cleanup path must not trust the old owner key after move/rekey DDL.
- `TRUNCATE PARTITION` and `DROP PARTITION` with global index after forced delete-range consumption/redo failure only if a low-cost trigger becomes available.
- only revisit placement policy bundle failure if a new multi-owner path is found; the obvious `putRuleBundlesError` cells are now green.
- only revisit basic FK table/index owner paths if a new state dimension or newer code path is found; the basic object-reference matrix is now green.
- only revisit masking policy for id630014 fix validation or another genuinely new owner-changing entrypoint; the basic rewrite/cleanup cells are green, and `EXCHANGE PARTITION` is already minimized as the ID-swap red cell.
- only revisit privilege grants if a product semantic claim says grants should follow object identity; the current evidence says they are name policy.
- only revisit affinity if a deterministic stale PD-state oracle is found; the current SQL-visible reference surface is green and InfoSchema-driven.
- only revisit functional indexes if a new owner path bypasses hidden-column dependency checks; the base column-alter/drop-index paths are now green.
- only revisit resource-group `SWITCH_GROUP` if product semantics change to validate switch targets, or if a separate runtime correctness oracle appears.
- only revisit hypo TiFlash replica metadata if a current-schema public surface is found; `EXPLAIN`-only behavior is outside this DDL-owner lane.
- only revisit SQL binding if product semantics change to auto-disable/rewrite object hints after DDL, or if a separate schema-current surface is introduced.
- only revisit local temporary tables if the documented loose database lifecycle changes.
- only revisit hypo-index variants after owner feedback or fix-direction validation; the current red cells are already minimized to one session-map invalidation/rekey root cause.
- only revisit reorg/global-index variants after fix-direction validation; the current red cell is already minimized to later non-touched partitions missing from the replacement global index.
- only revisit reorg duplicate-rowid repair after fix-direction validation; the current red cell is already minimized to raw-row equality being used as identity while source partition identity is omitted.
- only revisit table-cache after owner feedback or fix-direction validation; the current red cell is already minimized to `DROP DATABASE` with one cached table.
- only revisit sequence-default after fix semantics are agreed; the current red cells are already minimized and share one root family.
- only revisit add-index/global-index rollback after fix-direction validation; the current red cell is already minimized to a lost `IsGlobal`/tableID cleanup bit during rollback argument reconstruction.

## Negative Calibration: Optimistic Retry Replays Writes but Not Read-Only Session State

2026-07-12:

- Source proof: pkg/session/tidb.go adds retry-safe writes to StmtHistory,
  while pkg/session/session.go:retry rebuilds and replays that history. A
  read-only SELECT @v := ... is not replayed.
- Strong local probe: after SELECT @retry_value := v read 10, a retry hook
  changed the source row to 20; the retried UPDATE ... SET v=@retry_value
  committed 10. The retry path therefore exposes a real stale session-state
  result, not a harness assertion failure.
- Contract gate: the official optimistic transaction documentation explicitly
  describes write-only replay and warns that query-derived writes may violate
  Repeatable Read. It also deprecates automatic optimistic retry from v8.0.
- Classification: known/documented-semantic-boundary, not a new bug and not
  inserted into the bug library.
- Testbed note: the attempted SQL lift was INVALID(failpoint-lifecycle);
  the dirty test binary left an empty HTTP failpoint action after DELETE and
  then panicked at a val.(bool) assertion. It is not product evidence.
- Asset: docs/method-cases/ai-native-txn-retry-user-variable-known-boundary.md.

Method gate added: a strong wrong-result oracle must still be followed by a
release/default/contract check. A candidate that reproduces a documented,
opt-in semantic limitation is a reusable negative calibration, not a severe
bug.

Non-DDL shortcut/extractor target cells:
- virtual/system table, cache, or reuse path that replaces normal scalar evaluation with a custom prefilter.
- string/time/collation/session-variable semantics where the SQL-visible predicate can be rechecked independently.
- code that lowercases, normalizes, pushes down scalar functions, compiles regexps, hashes keys, or reuses cached plans/results and then drops the original predicate.
- low-noise oracle available as CASE-wrapped predicate, explicit re-check, row self-predicate, cache-disabled/direct path, or no-shortcut reference path.
- reject plan-only differences; require row-set/error/warning equality under a deterministic query.
- reject same-helper blast-radius enumeration once the helper has been proven across a second owner; keep one representative case and move to a different mechanism.

Non-DDL semantic-domain rewrite target cells:
- optimizer/executor rule replaces original scalar comparison/evaluation with a typed-key, narrowed-domain, normalized, rounded, or parser-specific equivalent.
- source can name both domains: original SQL-visible domain `D_old` and replacement domain `D_new`.
- a local guard exists, but it proves only a subset or adjacent property, not full `D_old == D_new`.
- low-noise oracle available as CASE-wrapped original predicate, no-shortcut/rule-disabled path, or scalar self-predicate.
- compact redflag values come from domain boundaries: scientific notation vs integer prefix, overflow/rounding, unsigned/signed, collation/coercibility, NULL/three-valued logic, or warnings-as-values.
- stop after one representative red cell per rewrite; do not enumerate every value spelling.

Execution note:
- The current testbed is `8192975`, namespace `testbed-tps-8192975-1-14`, using failpoint-enabled `fp-tidb` as the only TiDB owner; managed TiDB has been scaled to 0.
- Use `tcctl testbed get -p 8192975`, then `KUBECONFIG=/Users/bba/pc/kubeconfig.yml` and port-forward `pod/fp-tidb 14000:4000 18080:10080`.
- Record the environment capability/version fingerprint before classifying a probe result. On this testbed `SELECT VERSION()` returns `8.0.11-TiDB-v8.4.0-this-is-a-placeholder`, while `@@tidb_version` is unavailable.
- id30007 reproduced here with `SUMMARY total=2 findings=1 skipped=0`, making it a cross-testbed signal rather than a master-only observation.
- The rollback/cancel and forced-failure cells need `/fail/`, for example `afterRunOneJobStep`, `reorgPartRollback1..4`, `truncatePartFail1..3`, or `truncatePartCancel1`.
- Existing green probes are now baseline/regression checks. A new source scan should create another small matrix only if the owner/shortcut has a concrete proof obligation and a low-noise oracle.

Stop condition:

```text
if any new unexpected cell appears:
  stop expansion
  minimize SQL
  map it back to the object owner/path
  update the matrix before adding more cases
```

Default boundary remains DDL-owner focused: executor/query rowsets are allowed as consequence oracles after DDL has changed metadata. A deliberate non-DDL pivot is acceptable only under the id30010-style shortcut/extractor protocol above.

## id1620002 - TTL scan/delete time-zone context drift

- Target: `target.ttl.datetime-global-timezone-drift-deletes-refreshed-row.v1`.
- Selector: `SCAN_DELETE_CONTEXT_STABILITY`.
- **P**: scan and delete carry the same expiration epoch `E`.
- **Q**: both phases therefore enforce the same `DATETIME` wall-clock cutoff.
- **F**: every TTL statement independently resets to current global `time_zone`, then evaluates
  `FROM_UNIXTIME(E)`; validation does not pin or compare time zone.
- C3 oracle: after scan under UTC, refresh the selected row to cutoff plus four hours, switch global
  time zone to `+08:00`, and release the actual delete worker. The row is current under scan
  semantics but is silently deleted, and the job completes successfully.
- Controls: unchanged UTC context preserves the row; #41043's pre-job time-zone-change regression
  remains GREEN on current source.
- Status: **CONFIRMED**, remote `found_bug id1620002`, high severity. Remote state after insert:
  `COUNT(*)=93`, `COUNT(DISTINCT root_cause_id)=70`.
- Assets: `docs/bug-drafts/ai-native-ttl-midjob-timezone-drift-refreshed-row-draft.md`,
  `docs/method-cases/ai-native-ttl-context-stability-method-case.md`, and
  `assets/store/ttl-midjob-timezone-drift-results.jsonl`.
- Pause gate: do not enumerate offsets, TTL intervals, or DATE variants. Reopen only for fix
  validation or another cross-phase context owner.

## id1650002 - BR abort suppresses the live heartbeat it observes

- Target: `target.br.restore-abort-self-suppresses-live-heartbeat.v1`.
- Selector: `OBSERVATION_LOCK_SUPPRESSES_LIVENESS_SIGNAL`.
- **P**: `FOR UPDATE` stabilizes the matching registry row for the abort decision.
- **Q**: heartbeat reads still independently reveal whether the restore owner is alive.
- **F**: the heartbeat writer updates that same row through another session and conflicts with the
  observer's retained pessimistic lock.
- C3 oracle: prove pre-lock heartbeat progress, then require abort to return zero and retain the row.
  Current source instead emits `kv:9007`, declares stale, returns the task ID, and deletes the row.
- Controls: the unlocked liveness phase classifies the same owner active; a genuinely stale task is
  deleted under the same compressed clock.
- Status: **CONFIRMED**, remote `found_bug id1650002`, high severity. Remote state after insert:
  `COUNT(*)=94`, `COUNT(DISTINCT root_cause_id)=71`.
- Assets: `docs/bug-drafts/ai-native-br-abort-lock-suppresses-live-heartbeat-draft.md`,
  `docs/method-cases/ai-native-br-observation-lock-liveness-method-case.md`, and
  `assets/store/br-abort-live-heartbeat-results.jsonl`.
- Pause gate: do not enumerate restore filters, running/resetting status, or heartbeat intervals.

## id1680003 - BR scheduler-removal failure can publish false backup success

- Target: `target.br.scheduler-removal-error-false-success.v1`.
- Selector: `CHECKED_ERROR_MUST_DOMINATE_TERMINAL_RESULT`.
- **P**: scheduler-removal error `e` is explicitly checked.
- **Q**: taking that branch guarantees a nonzero BR terminal result.
- **F**: five top-level paths return stale outer `err`, normally nil, before backup/restore action.
- C3 oracle: process exits 0, internal summary says failed, and no `backupmeta` exists.
- Controls: no-fault command writes a 285-byte `backupmeta`; returning `e` under the same fault exits 1.
- Status: **CONFIRMED**, remote `found_bug id1680003`, high severity.
- Assets: `ai-native-br-scheduler-removal-false-success-draft.md`,
  `ai-native-id1680003-br-terminal-success-method-case.md`, and
  `assets/store/br-scheduler-removal-false-success-results.jsonl`.
- Pause gate: five source sites are one root. Do not execute each sibling.

## id1710003 - cancelled ALTER RESOURCE GROUP leaves runtime state changed

- Target: `target.ddl.resource-group-external-effect-survives-cancel.v1`.
- Selector: `EXTERNAL_EFFECT_PRECOMMIT_ROLLBACK_COHERENCE`.
- **P**: resource-group metadata and job publication are staged in the DDL worker transaction.
- **Q**: cancellation means neither SQL metadata nor runtime resource control changed.
- **F**: PD mutation commits first and generic rollback has no compensation.
- C3 oracle: ALTER and history are cancelled; SHOW CREATE is old; PD-backed runtime view is new.
- Controls: normal real-PD ALTER aligns both views; no precommit external effect makes drift impossible.
- Status: **CONFIRMED**, remote `found_bug id1710003`, high severity.
- Assets: `ai-native-resource-group-cancel-external-drift-draft.md`,
  `ai-native-id1710003-external-effect-precommit-method-case.md`, and
  `assets/store/resource-group-cancel-external-drift-results.jsonl`.
- Pause gate: do not enumerate resource-group option values under this ordering root.

## Retired negative - BR ignored GC read error

- Target: `target.br.gc-safepoint-read-error-allows-unprotected-backup.v1`.
- Selector: `GC_PROTECTION_ACK_DOMINATES_HISTORICAL_READ`.
- P/Q/F: BR ignores `GetGCSafePoint` errors and global service-safepoint writes can return nil
  despite an effective boundary newer than backupTS.
- C3 oracle: after physical GC, success plus a wrong restored rowset.
- Result: **GREEN/RETIRED**. Normal BR rejected at the front guard. With only the read failure
  injected, TiKV's snapshot owner rejected with 9006; exit 1 and no backupmeta.
- Method gain: continue dominance analysis through every downstream independent owner.
- Assets: `ai-native-br-gc-protection-layered-rejection-method-case.md` and
  `assets/store/br-gc-protection-layered-rejection-results.jsonl`.
