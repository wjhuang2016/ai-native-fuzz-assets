# AI-Native Cross-Layer Transaction Campaign
> Prepared: 2026-07-13. Status: methodology and asset design complete; no exact bug target admitted yet.

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

### Remaining work order

1. Build a current-source owner graph for client-go's `undeterminedErr`: every write, clear, read,
   and terminal conversion through TiDB. **Completed for the initial path.**
2. Compare every cleanup/rollback request's lock identity with the identity TiKV checks before
   deletion, especially `forUpdateTS` and pessimistic-primary handling.
3. Close the exact mode-at-send/current-mode proof debt above by tracing mixed lock cleanup and
   status semantics at TiKV.
4. Admit exactly one target with a complete card. Do not place all three families in the active
   queue at once.
5. Execute locally first. The testbed remains closed until an independently admitted local RED.

The default first source pass is commit-outcome terminal truth because it has the cleanest severe
user promise and the strongest cross-layer oracle. If source ownership proves complete, record the
negative and move to lock generation identity rather than forcing an artificial RED.

## Stop Rules

- Stop after one root per terminal owner and protocol transition.
- Region counts, key counts, error strings, SQL syntax, and retry counts are blast radius unless
  they change ownership or the minimum proof obligation.
- Do not file a client-go/TiKV internal invariant without a TiDB or KV-client user consequence.
- Do not use a known PR finding to rescue an unproductive source pass.
- Do not touch the testbed before local RED and exact owner attribution.
- On GREEN, name the gate that held and store it as a negative screen.
