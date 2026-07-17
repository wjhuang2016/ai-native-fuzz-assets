# AI-Native Cross-Layer Transaction Campaign
> Prepared: 2026-07-13; updated: 2026-07-17. Status: the first severe cross-layer root
> (`id2250003`) is execution-verified; the adjacent 1PC ambiguity candidate is retired after its
> provisional local RED became GREEN under the real TiKV idempotency owner. A historical
> `DefaultNotFound` replay now also validates the live-resource registration selector and is GREEN
> on current master.

## Mission

Find severe transaction correctness bugs that only appear when TiDB, client-go, and TiKV disagree
about transaction identity, lock ownership, protocol mode, or terminal outcome. A hit must validate
the AI-native discovery loop, not reproduce a PR-review finding.

This campaign is quality assurance on authorized repositories and test environments. It excludes
partition-related work and does not use security-attack framing.

## Provenance And Admission

The campaign runs in `COLD_SOURCE` mode:

```text
candidate seed before local RED:
  current TiDB source
  the TiDB-pinned client-go revision
  current TiKV source
  executable public transaction semantics

closed before local RED:
  known issues
  PR review findings
  fixes and blame history
  historical bug descriptions
```

An exact target enters the queue only when all are true:

1. The cross-layer `P/Q/F` claim is named from current source.
2. Every owner and durable boundary is mapped.
3. The product can reach the edge, or the injection is only a locator with a credible live lift.
4. A highest-consumer oracle can prove data loss, atomicity violation, false terminal truth, or
   serious transaction unavailability.
5. A compact matrix and one-variable counterfactual exist before expensive execution.
6. The root is distinct from already terminal selectors such as statement retry residue.

No target is admitted merely because a field is omitted, a comment is strong, or an RPC can fail.

## Pinned Source Surface

Snapshot used to prepare this campaign:

```text
TiDB:      /Users/bba/pc/tidb       @ 13282a8bd06bd33324a4dbfd3c1c03685f3cd9aa
client-go: /Users/bba/pc/client-go  @ 078462f59f8acc5d3154525bac2515e4e81ec07c
TiKV:      /Users/bba/pc/tikv       @ d2d7ee3fe465523c14b87cb8fb72df9b9ba89ff5
TiDB pins: github.com/tikv/client-go/v2 @ 661db4f5f4e8
```

The local client-go checkout has unrelated pre-existing example `go.mod` changes. Do not modify or
revert them. Before executing a cross-repo probe, either use an isolated worktree at TiDB's pinned
revision or explicitly verify that the local checkout matches that revision.

Primary ownership surfaces:

| Layer | Initial source owners | Truth owned |
| --- | --- | --- |
| TiDB | `pkg/session`, `pkg/sessiontxn`, `pkg/store/driver/txn` | SQL terminal result, retry policy, statement/transaction state, user-visible commit timestamp |
| client-go | `txnkv/transaction`, `txnkv/txnlock` | 2PC state machine, primary/secondary sets, Async Commit/1PC mode, undetermined errors, TTL, lock resolution |
| TiKV | `src/storage/txn/commands`, `src/storage/txn/actions`, `src/storage/mvcc` | lock generation, commit/rollback records, CheckTxnStatus, ResolveLock, minCommitTS, atomic write application |
| PD/topology | timestamps, region routing, GC boundaries | ordering and reachability assumptions consumed by the other layers |

## Cross-Layer Audit Card

The ordinary P/Q/D/F card gains an owner-transfer graph:

```text
U_promise: what must the SQL client be able to conclude?
P_check:   which layer checked which evidence?
Q_claim:   which transaction fact is inferred?
D_dims:    startTS, forUpdateTS, primary, key set, mode, minCommitTS, epoch, attempt, terminal state
Owners:    who creates, mutates, persists, observes, and publishes each dimension?
Boundaries:which write/RPC response makes each fact durable or merely observable?
F_effect:  retry, fallback, cleanup, resolve, return success/error, or publish commitTS
O_oracle:  public result + fresh-session data + MVCC/transaction truth
R_redflag: one temporal or semantic dimension that makes the evidence stale or incomplete
S_selector: reusable cross-layer source shape
```

Instrumentation is designed only after this card. A failpoint without an owner-transfer claim is
not a candidate generator.

## Priority 1: Commit Outcome Terminal Truth

### Proof obligation

When TiDB reports transaction success or failure, that result must be coherent with the durable
status of the primary and the fresh-session visibility of every mutation. A transport failure after
TiKV applies a request creates uncertainty, not permission to blindly retry or return a definite
opposite result.

### Source selector S48

Search current source for paths where:

- an RPC may be applied before its response is lost;
- client-go stores, clears, wraps, or downgrades `undeterminedErr`;
- a later retry, cleanup, or `CheckTxnStatus` converts uncertainty into committed/rolled back;
- TiDB publishes success, an ordinary retryable error, or `LastCommitTS` from that conclusion.

### Small matrix

| Cell | Apply point | Response | Status check | Required observation |
| --- | --- | --- | --- | --- |
| G1 | before primary commit | lost | primary absent | error and no durable rows |
| R1 | after primary commit apply | lost | committed | no duplicate replay; terminal truth matches durable rows |
| G2 | after secondary commit apply | lost | primary committed | success/uncertainty resolves to one committed row image |
| C1 | no fault | delivered | not needed | normal success and exact row image |

The first pass should prefer one primary plus two secondaries in distinct regions. A single-key
transaction cannot expose incomplete secondary ownership.

### Oracle O56

Jointly observe:

1. the SQL client's exact terminal result;
2. fresh-session row and unique-index state;
3. primary transaction status and commitTS;
4. whether a second execution duplicated or inverted the logical operation.

Logs alone are not the oracle. MVCC status alone is also insufficient because the user-visible
terminal result is part of the contract.

## Priority 2: Lock Generation Identity

### Proof obligation

A rollback, TTL expiration, deadlock cleanup, or lock resolver may remove only the lock generation
it proved it owns. Equality on key or startTS is insufficient when `forUpdateTS`, lock type, primary,
or protocol state distinguishes an older attempt from a newer lock.

### Source selector S49

Search for owner narrowing between layers:

```text
request proves: key + startTS + forUpdateTS + lock kind + primary/mode
cleanup carries: key + startTS or another strict subset
TiKV action: delete, rollback, wake waiters, or write rollback record
```

High-value paths include pessimistic rollback, resumed lock acquisition, TTL manager reset, deadlock
cleanup, `CheckTxnStatus` on pessimistic primary locks, and ResolveLock fallback.

### Small matrix

| Cell | Old generation | New generation | Delayed action | Required observation |
| --- | --- | --- | --- | --- |
| G1 | lock at F1 | none | rollback F1 | lock removed |
| R1 | lock at F1 | same txn reacquires at F2 | delayed rollback F1 | F2 lock and later commit survive |
| G2 | lock at F1 | different txn owns key | delayed rollback F1 | different owner survives |
| C1 | no delayed action | F2 commits | none | expected row and no leaked lock |

### Oracle O57

Observe lock identity before and after the delayed action, then require a later writer or commit to
consume that lock. A raw lock delta without a behavioral consumer is diagnostic only.

## Priority 3: Commit-Mode Fallback Atomicity

### Proof obligation

If 1PC or Async Commit is attempted and later falls back, all layers must agree on the final mode,
key set, minCommitTS, primary, and cleanup responsibility. A mode bit changing locally is not enough
if TiKV has already persisted mode-specific locks or committed a prefix.

### Source selector S50

Compare the state represented at these boundaries:

```text
client-go before prewrite
client-go after each region response
client-go fallback/cleanup state
TiKV lock or write record per region
CheckTxnStatus / CheckSecondaryLocks interpretation
TiDB terminal publication
```

Rank branches that reset `useAsyncCommit`, `isOnePC`, secondaries, `onePCCommitTS`, `minCommitTS`, or
primary ownership after some region has already accepted a prewrite.

### Small matrix

Use at least three keys split across two or three regions:

| Cell | Initial mode | Fault altitude | Final requirement |
| --- | --- | --- | --- |
| G1 | 2PC | none | all keys visible at one logical commit |
| G2 | 1PC | prewrite rejects before apply | clean 2PC fallback or clean failure |
| R1 | 1PC | one region applies, response/order changes | no mixed 1PC/2PC ownership or partial commit |
| R2 | Async Commit | primary/secondary response split | status recovery reaches one atomic outcome |
| C1 | same keys, one region | none | establishes protocol selection and baseline |

### Oracle O58

The terminal oracle is an atomic keyset differential:

- every key is absent, or every key is visible under one logical commit outcome;
- unique/index views and table rows agree;
- primary and secondaries resolve to the same transaction status;
- retrying the client operation does not duplicate a non-idempotent logical action.

## Fault Altitudes

Do not inject generic `timeout` at arbitrary RPC call sites. Classify each point by whether the
server could already have made progress:

```text
A0 before request send
A1 after send, before TiKV apply
A2 after TiKV apply, before response delivery
A3 after client receives response, before local state publication
A4 between region batches
A5 during status recovery / lock resolution
A6 after terminal result, before TiDB session publication
```

For ambiguity bugs, A2 and A3 are the valuable pair. A0 is usually only a control. Every injected
cell needs an event witness containing transaction identity, region/key batch, protocol mode, and
whether the server-side write was applied.

## Observer Stack

Use the lowest observer needed to prove the owner, then close at the highest consumer:

1. **SQL observer:** exact error/success, affected rows, fresh-session rowset, unique/index checks.
2. **client-go observer:** startTS/commitTS, primary, mode, batch, retry, undetermined/status decision.
3. **TiKV observer:** lock/write generation, CheckTxnStatus action, ResolveLock result, apply witness.
4. **topology observer:** region/leader/epoch at each request, only when topology is the selected
   dimension.

Instrumentation must be correlation-safe. Use one transaction-scoped ID and structured events; do
not rely on timestamp adjacency across three processes.

## Execution Ladder

```text
L0 source proof
  build owner graph and reject weak consumers/unreachable edges

L1 deterministic layer test
  preserve the real state machine on both sides of the selected boundary

L2 TiDB + pinned client-go + real local TiKV
  prove cross-layer semantics without cluster topology as a hidden variable

L3 authorized testbed
  add real region layout, process restart, leader transfer, or response loss

L4 issue-quality closure
  exact counterfactual, natural or production-faithful reachability, post-RED dedup
```

A mock that implements Commit but omits undetermined status recovery, TTL, backoff, or lock resolver
semantics is not valid for L1. Move the boundary to a fake transport around the real client-go state
machine, or use a real TiKV.

## First Source Pass Checkpoint

The first `undeterminedErr` owner map was read from TiDB's exact pinned client-go module
`661db4f5f4e8`, not from the older local checkout.

Confirmed owner flow:

```text
client-go 2PC primary Commit RPC error
  -> set undeterminedErr
  -> do not run ordinary cleanup
  -> return ErrResultUndetermined unless a processed primary response proved committed

client-go Async Commit / 1PC prewrite RPC error
  -> set undeterminedErr while that protocol is active
  -> do not clean up an outcome that may already be committed by prewrite
  -> return ErrResultUndetermined

TiDB driver
  -> map client-go unknown outcome to global ErrResultUndetermined

TiDB server
  -> do not send an ordinary retryable SQL error
  -> close the connection immediately
```

This retires one attractive but incorrect direction: TiDB does not automatically replay an
undetermined commit in the same session. An application may still retry after connection loss, but
the server has deliberately refused to claim success or failure; that is an application-level
idempotency contract, not a TiDB replay bug by itself.

One current-source proof debt remains, but is **not admitted as a target**. Each prewrite request is
built with a fixed `UseAsyncCommit/TryOnePc` mode, while the request-completion handler decides
whether an RPC error is undetermined by rereading the committer's mutable current mode. A concurrent
region response can turn Async Commit off before the errored handler drops:

```text
mode at request send:       async
another batch response:    fallback -> current mode sync
lost-response batch drop:  current mode says sync -> no undeterminedErr
```

This source shape satisfies S50's information-loss question, but not yet its consequence gate. The
missing proof is whether the fallback response independently guarantees that no persisted async
lock set can be considered committed and that ordinary cleanup safely resolves every mixed lock.
The next legal analysis follows that exact state through TiKV prewrite, rollback/cleanup,
`CheckTxnStatus`, and ResolveLock. Only if an after-apply response-loss schedule can escape those
owners does it become an active S48/S50 target.

Recorded negative asset:
`negative.txn-undetermined-same-session-replay.v1`.

### Current-source screen checkpoint

The next pass applied the same owner-transfer discipline to pipelined DML, shared locks, resumed
pessimistic locking, and TiKV async apply. No severe bug was admitted. The useful result is a sharper
screen, not a forced target:

- Pipelined DML broadcasts the commit timestamp captured before a `CommitTsExpired` retry, but the
  ordinary TiKV consumers inspected use the cache entry only as committed/ongoing membership. No
  current TiDB wrong-result or atomicity consumer was found, so the field mismatch is retired for
  this campaign rather than promoted from source shape alone.
- TiKV returns multi-owner shared-lock envelopes, but current client-go prewrite, pessimistic-lock,
  and GC paths expand the embedded owners before making owner-specific decisions. Ordinary reads
  intentionally ignore shared locks. The apparent top-level zero owner is therefore not itself a
  correctness bug.
- TiKV's async-apply prewrite response occurs after Raft commit, while scheduler latches remain held
  through apply. A same-key commit cannot overtake prewrite apply; an apply failure after the early
  response takes the process-preserving panic path rather than publishing a false terminal result.
- Aggressive/fair locking plus shared locks and resumed shared locks are rejected at the client-go
  entry gate. Source-only exploration below that gate cannot produce a user-reachable schedule.

This pass adds two admission gates ahead of expensive schedule design:

1. **Highest-consumer gate:** name the public invariant that consumes the suspicious state before
   building a fault schedule. A field difference that is logged or reduced to a boolean is not yet
   a target.
2. **Entry/capability gate:** prove that the exact product configuration reaches the lower-layer
   path. Unsupported mode combinations and stale component binaries are environment results, not
   bugs.

The bounded AI source scout also exposed a budgeting error: limiting shell-command count did not
limit source volume or reasoning cost. Future scouts need simultaneous command, token, wall-clock,
and per-read-region budgets, and must return only owner-anchored claims.

### Shared-lock highest-consumer matrix

The strongest shared-lock concern was compressed into a parent/child FK matrix. Two pessimistic
child inserts hold compatible shared locks on the same parent. A concurrent parent `DELETE` waits;
after the first holder rolls back, it must still wait for the second owner. The only changed cell is
whether the second owner rolls back or commits:

| Cell | Second holder | Required result |
| --- | --- | --- |
| G1 | rollback | parent DELETE succeeds; no child remains |
| G2 | commit | DELETE retries/revalidates, returns FK error; parent and committed child remain |

The first run used a cached `tiup nightly` TiKV at commit `2d4737d`, built on 2026-01-19. Even the
repository's existing `TestSharedLockBlockExclusiveLock` capability baseline blocked, so that run
was `INVALID(environment)` and never became bug evidence. After removing and reinstalling the TiKV
nightly component, TiKV was commit `7ecce12`, built on 2026-07-13. The existing baseline passed, and
the new matrix passed both cells. No testbed mutation was performed.

The holding guard is statement revalidation: the committed shared-lock owner makes the waiting
parent DELETE conflict with a newer commit; TiDB's pessimistic DML loop advances `forUpdateTS`,
rebuilds the executor, and reruns the FK check. This is stored as
`negative.txn-shared-lock-parent-delete-revalidation.v1`, with the executable matrix in
`scaffolds/tidb-tests/txn_shared_lock_parent_delete_revalidation_test.go`.

Environment methodology is now two-lane:

- **refreshed nightly:** fast capability and current-head screening; always record the actual commit
  and do not claim exact source-pin coverage;
- **exact-SHA local binary:** required when a RED must be attributed to the pinned TiKV source.

`txnlab` now automates both lanes, refuses to reuse an unknown PD on port 2379, and cleans up the
playground it starts. A capability baseline is mandatory before every feature-specific candidate.

### Tooling checkpoint

`tools/txnlab` now makes an admitted transaction card executable without weakening the campaign
gates. It pins the exact TiDB/client-go/TiKV commits in isolated worktrees, verifies exact failpoint
images, double-gates testbed mutations, controls HTTP failpoints and run-labeled Chaos objects,
captures structured evidence, and automatically removes faults and restores images. O56, O57, and
O58 are executable and deliberately return `INVALID` when the critical ordering witness is absent.

Read-only preflight passed on testbed 8220955 with one TiDB, three TiKV, and one PD. Both exact
failpoint images are available. The official `PingCAP-QE/artifacts` generator was also exercised for
the pinned TiDB and TiKV commits; its generated scripts contain `make failpoint-enable` and
`make fail_release` respectively. A new source hook still requires the image-builder runtime and
registry push credentials, but the current-source campaign can use the existing pinned images.

This does **not** admit the current mode-fallback shape, authorize a testbed fault run, or prove a
bug. The shared-lock FK matrix is GREEN. The remaining work stays at proof: close one owner graph,
admit one P/Q/F card, and produce the transaction-scoped oracle input locally.

### Bounded source-packet and owner-close checkpoint

The remaining lock-status and lock-generation paths were closed from current source without a
testbed mutation:

- `getTxnStatus` caches only determined TiKV responses. `LockNotExistDoNothing` and pessimistic
  rollback actions are not classified as durable rollback, and primary mismatch falls back to
  owner-specific lock cleanup.
- A lost force-lock response can hide a larger `LockedWithConflictTS`; the resulting rollback may
  miss that pessimistic lock. Heartbeat termination and TTL resolution bound the result to residual
  lock availability, with no data-loss, atomicity, or false-terminal consumer found.
- Pipelined DML records the greatest flushed key as an exclusive range end, so a Region beginning at
  exactly that key can be omitted from eager terminal cleanup. Ordinary read lock resolution and
  GC's mandatory resolve pass remain recovery owners; this does not meet the severe gate.
- Fair-lock retry cleanup captures client-go's previous committer `forUpdateTS`. TiDB's newly
  allocated retry timestamp first updates transaction context and snapshot state, and reaches the
  committer only in the later `LockKeys`. TiKV deletes only `lock.for_update_ts <= rollback_ts`, so
  a later lock generation survives.
- Region relocation rebuilds commit batches from the mutation subset and re-identifies the primary
  by key. The old batch's `isPrimary` bit is not copied into split children.

Unbounded full-repository scouts were not useful: three runs consumed roughly 61k-82k tokens and
returned no JSON. `txnlab source-packet-scout` now receives only explicit numbered ranges, runs in an
isolated directory, validates at most three owner-anchored candidates, and kills its whole process
group at the wall limit. Calibration tightened the hard packet cap to 32 KiB: 47 KiB timed out at
75 seconds; 25 KiB completed in about 45 seconds. Token counts are observed, not treated as a hard
provider control.

The first packet-only adversarial pass also demonstrated why parent verification remains mandatory.
It proposed a three-attempt fair-lock deletion schedule, but conflated TiDB transaction-context
publication with client-go committer mutation. Direct owner tracing retired the schedule. The
method is therefore `parent selects owners -> child proposes counterexample -> parent verifies
transfers -> only then admit`.

### Remaining work order

1. Build a current-source owner graph for client-go's `undeterminedErr`: every write, clear, read,
   and terminal conversion through TiDB. **Completed for the initial path.**
2. Compare every cleanup/rollback request's lock identity with the identity TiKV checks before
   deletion, especially `forUpdateTS` and pessimistic-primary handling. **Completed as negative.**
3. Close the exact mode-at-send/current-mode proof debt above by tracing mixed lock cleanup and
   status semantics at TiKV. **Completed as negative: durable mixed-mode marker forces sync.**
4. Trace `ASYNC_SECONDARY_SET_COMPLETENESS`: every accepted async-prewrite key must be represented by
   the primary's recovery set after filtering, batching, Region relocation, fallback, and dedup.
   **Completed as negative after client-go/TiKV owner closure.**
5. Admit exactly one target with a complete card, then execute locally first. **Completed for
   id2220003 at moderate severity; the severe cross-layer gate remains open.**

The default first source pass is commit-outcome terminal truth because it has the cleanest severe
user promise and the strongest cross-layer oracle. If source ownership proves complete, record the
negative and move to lock generation identity rather than forcing an artificial RED.

### Async closure and mutable-value checkpoint

The async-secondary owner graph closed negative. client-go walks every accepted mutation, excludes
only primary and `CheckNotExists`, carries the full secondary set only on the primary request, and
rebuilds primary identity by key after Region relocation. TiKV stores that set on the primary and
forces synchronous recovery when a secondary is not async. Mixed fallback therefore did not yield
an accepted key omitted from recovery ownership. The bounded scout returned zero candidates, and
the parent added predecessor/filter/lifetime owners before accepting the result.

The adjacent savepoint owner pass did produce `id2220003`. `TemporaryTables` correctly survives as
a container, but a mutable dirty-size field inside each value is neither restored nor monotonic.
The minimal matrix proved empty rows plus a one-byte error 1114, and restoring only that field made
the test GREEN. Testbed 8220955 reproduced the SQL-only RED on exact TiDB commit `5c9198e9484d`.

This changes the next campaign compiler: packet generation must close predecessor and lifetime
owners, and restoration differentials must traverse mutable values rather than stopping at field or
container classification. It does not lower the severe admission bar; id2220003 remains moderate.

### First severe cross-layer hit: late age error after async proof

The next bounded client-go pass found a severe terminal-truth root without using issue or PR seeds.
`twoPhaseCommitter.execute` completes async prewrite and selects nonzero `minCommitTS`, then runs the
generic 24-hour transaction-age check and can return ordinary `txn takes too much time`. The error
defer starts best-effort cleanup, but a complete async lock set is already sufficient for a later
LockResolver to choose commit.

The compressed matrix made only the age predicate true and made cleanup unavailable. Local
integration returned the ordinary error, then point gets recovered both keys as committed. Moving
the check before prewrite kept both keys absent. The original RED was then lifted into
`sdkserver-0` on testbed 8220955 against one PD and three real TiKV nodes at commit `bf73df27`; logs
show nonzero commitTS and `ResolveLock action=commit` for both keys. The probe removed its dedicated
raw keys afterward.

This promotes `POST_PROOF_FALLIBLE_EPILOGUE`: mark the earliest prefix where an independent owner
can finish the outcome, then audit every remaining error edge. Deferred cleanup does not justify an
ordinary abort after that frontier unless completion is synchronously proven. The required oracle
is terminal response versus final MVCC truth after invoking the independent owner.

Remote `found_bug id2250003` records the root. Its consequence is high, while natural frequency is
bounded by the exact trigger: a transaction older than 24 hours plus unavailable cleanup. Do not
inflate frequency or enumerate TTL/key-count variants.

### 1PC ambiguity negative: real TiKV dominates the local mock

The next current-source candidate asked whether a committed TryOnePc response could be lost, then
an EpochNotMatch regroup could clear client-go's current 1PC mode and suppress explicit
undetermined. The embedded store produced exactly that terminal mismatch: ordinary write conflict,
both keys visible, and no undetermined error. A request-scoped safety counterfactual converted it to
explicit undetermined.

The real-TiKV lift retired the candidate. TiKV's repeated-prewrite path recognizes the transaction's
committed record and returns the prior `one_pc_commit_ts`; after a real external Region split,
client-go returned success and both keys matched. The local RED is stored as `INVALID(semantic-gap)`
and was not added to the bug database.

This adds two campaign gates: close the exact downstream retry/idempotency owner before promotion,
and run topology actors in a separate process from process-wide failpoints. The next source packet
must not revisit this 1PC shape; it should cover common pipelined/fast-commit proof horizons and
their highest terminal consumers.

### Second severe cross-layer hit: 1PC schema validation ends before atomic apply

The bounded post-primary packet first returned zero candidates. Ordinary 2PC's apparent fallible
post-primary worker could not return a production error, secondary commit errors had only transient
lock consumers, pipelined post-primary work was background recovery, and explicit undetermined
covered lost primary responses. Instead of widening the packet, the parent moved one proof stage
earlier: validations performed before a fast path's irreversible apply.

That pass produced `id2280003`. When MDL is off, TiDB's delta `SchemaChecker` is installed before
commit. client-go calls it in `calculateMaxCommitTS`, then reaches `beforePrewrite`. A successful 1PC
prewrite is already an atomic TiKV commit, and client-go returns without the actual-commitTS schema
check used by 2PC. The compressed matrix paused only that interval and ran related DDL.

Real-TiKV `ADD INDEX` evidence on testbed 8220955 was direct: 1PC `commit_ts` was later than DDL
`FinishedTS`, the table scan returned `1:10`, the new index returned no row, and `ADMIN CHECK TABLE`
failed. The same schedule with 2PC retried and returned `1:10` through both paths with ADMIN CHECK
green. Async commit remained enabled in the strongest 1PC RED. A TRUNCATE sibling returned success
with a later commitTS while the replacement table was empty. Disabling 1PC only when
`needCheckSchemaByDelta` is true made the local oracle fully GREEN.

This adds `VALIDATION_HORIZON_COVERS_IRREVERSIBLE_APPLY`: compute facts consumed at atomic apply that
can change after validation, subtract downstream lock/version/revalidation owners, and compare fast
and safe paths. For overlapping actors, wall-clock return order is an anti-oracle; the matrix must
first prove logical order with commitTS/FinishedTS or an equivalent token.

Post-RED dedup found no exact TiDB or client-go issue. Closed TiDB issue #24009 covers an unstable
skipped test and explicitly says no production impact. Existing `id1440001` is the async-commit
false-abort sibling; this root is 1PC false success with persistent wrong data. Stop DDL-shape and
delay-value expansion. The next transaction pass should reuse the validation-horizon selector on a
different semantic owner or return to a different common commit/retry terminal owner.

### MDL-on retry capability hit

The next pass explicitly restored `tidb_enable_metadata_lock=DEFAULT` and verified `1` before any
experiment. It did not use DDL concurrency. Reusing the current-source S45 owner graph, the pass
ranked state outside statement KV and `StatementContext` by highest independent consumer.
`GET_LOCK` exposed a distinct external capability owner: `session.advisoryLocks` plus a dedicated
internal pessimistic transaction.

A natural unique-key conflict forced one transparent RC retry after row-dependent lock acquisition.
The retry saw a gate and completed with zero rows, but `IS_USED_LOCK` named the successful
statement's connection and a competitor returned 0. Owner cleanup changed the competitor to 1. A
run beginning from the same final state returned `NULL/1`. Local unistore and SQL-only real TiKV on
testbed 8220955 agreed; the live slow log recorded retry count one and success.

This produced `id2310003` and upstream #69820. It extends retry closure from values to external
capabilities and shows why a competitor oracle is stronger than internal map inspection. Stop this
root family. The next pass may reuse the capability-owner selector only on a different lock, lease,
registration, or handle owner; it must not enumerate advisory-lock syntax or timing variants.

### Historical DefaultNotFound replay: the missing owner was registration

An exact 2024-12-26 stack reproduced `KV:Storage:DefaultNotFound` with one TiDB, three TiKV nodes,
MDL enabled, and no partition features. The product path was an autocommit external stale read.
Old TiDB exposed an empty processlist `TxnStart`, so `ReportMinStartTS` omitted the active stale TS
and GC could advance past it. With 20,000 long-value rows and point readers active during Write CF
compaction, the run observed 117,855 successful reads followed by 13 exact `DefaultNotFound`
errors.

The testbed build `d573e284da773c820c1c313105b73d587378381b` was GREEN at the active-statement owner boundary:
processlist and etcd both reported the exact external TS, and a 61-second query completed. The
source counterfactual falls back from `TxnCtx.StartTS` to `TxnCtx.StaleReadTs` in both process-info
publication paths. Post-RED history maps the root to #61325/#61329, so this is a historical replay,
not a new cold-source finding.

The reusable campaign rule is `LIVE_RESOURCE_REGISTRATION_COMPLETENESS`: before testing GC,
cleanup, timeout, or failover, enumerate every mode that creates a live resource and compare its
identity with the registry consumed by the collector. The oracle ladder is registration, aggregate
minimum, collection frontier, intermediate owner state, and highest consumer. Do not spend ten
minutes reaching GC until the seconds-level registration cell is RED.

The full matrix, exact stack, production trigger, negative screens, and scaffold are recorded in
`docs/method-cases/ai-native-default-not-found-stale-read-gc-method-case.md`.

### Adjacent master hit: stale snapshot identity is lost at lazy-cursor handoff

The next pass reused the historical obligation but changed owner phase instead of enumerating stale
read syntax. Current master fixed `active statement -> processlist`; it did not fix `active statement
-> detached cursor`. `TryDetach` registers `TxnCtx.StartTS`, which is zero for an autocommit stale
read, while `ReportMinStartTS` later consumes only the cursor state's `StartTS`.

The seconds-level owner probe reported a nonzero stale TS, cursor TS zero, and an aggregate frontier
from a later transaction. The real protocol lift used one TiDB, three TiKV nodes, MDL ON, a 64-Region
table, default DistSQL scan concurrency, and no extra SQL after cursor open. Process info naturally
became Sleep; TiDB-reported minStartTS crossed the live snapshot; PD accepted exactly that legal
GCV2 upper bound; the first normal fetch returned error 9006. No compaction or data rewrite was
needed. Falling back to `TxnCtx.StaleReadTs` during cursor registration made the new owner test and
the existing ordinary cursor test GREEN.

This is `id2760003/moderate`, not a severe campaign hit: lazy cursor fetch is OFF by default and the
observed consequence is a clear query failure. It validates the new selector
`LIVE_RESOURCE_IDENTITY_ACROSS_OWNER_HANDOFF`: for each registry proof, enumerate semantic identity
across every owner transition, then gate expensive collection on a RED handoff cell. Assets are in
`docs/method-cases/ai-native-lazy-cursor-stale-read-gc-method-case.md` and
`assets/store/txn-lazy-cursor-stale-read-gc-results.jsonl`.

## Stop Rules

- Stop after one root per terminal owner and protocol transition.
- Region counts, key counts, error strings, SQL syntax, and retry counts are blast radius unless
  they change ownership or the minimum proof obligation.
- Do not file a client-go/TiKV internal invariant without a TiDB or KV-client user consequence.
- Do not use a known PR finding to rescue an unproductive source pass.
- Do not touch the testbed before local RED and exact owner attribution.
- On GREEN, name the gate that held and store it as a negative screen.

### Statement rollback and retry owner-set checkpoint (2026-07-17)

Two new C3-directed matrices were GREEN on TiDB master `94b834d94b60` with real TiKV
`c27c66202dcd`, MDL enabled, and ordinary transaction settings.

The statement-checkpoint pass tested whether a failed statement can leave non-value MemDB state that
poisons an earlier successful mutation. The final matrix used NOT NULL failure after revisiting row,
unique, ordinary, and generated-index owners. Five shapes under optimistic and pessimistic explicit
transactions all matched the control with the failed statement removed; `ADMIN CHECK` and every
forced index scan remained consistent. Two earlier runs are retained as `INVALID(trigger)`: duplicate
checking occurred only at COMMIT, and the chosen CHECK predicate was not enforced in that runtime.
The family is closed until source finds a production-reachable flag setter and a correctness-bearing
consumer.

The retry pass added a deterministic pause immediately before `Txn.Commit`, then used another session
to create a real TiKV write conflict. A same-cascade-set baseline proved replay capability. The
stronger cell changed owner-set membership: attempt one staged parent-child-grandchild deletion, but
the blocker moved the child to another parent before retry. The successful retry dropped the stale
cascade set and preserved the moved child and grandchild. This closes FK cascade attempt leakage and
promotes `RETRY_ATTEMPT_OWNER_SET_REBUILD` for other implicit work producers.

The next cell uses the same replay schedule on FK validation rather than cascade mutation: plain
autocommit child INSERT first validates an existing parent; the blocker deletes that parent before
prewrite; retry must revalidate and return an FK error. Success plus an orphan child is a direct
persistent-corruption RED. This is distinct from RC `INSERT IGNORE`: plain INSERT, optimistic
autocommit transaction replay, and default MDL are required.

That FK validation cell is now GREEN. The lock-only parent mutation caused a real retry, and the
successful replay did not reuse the first positive check: it returned error 1452. Fresh parent and
child rows were empty, the FK anti-join was zero, and physical checks passed. Close ordinary FK
existence across autocommit retry; the next validation candidate must have a proof or conflict owner
outside executor/transaction rebuild.
