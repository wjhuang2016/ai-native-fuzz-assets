# AI-Native Bug Discovery Retrospective
> Snapshot: 2026-07-13. This is a methodology retrospective, not a chronological bug list.

## The Question We Were Actually Answering

The project was never just trying to maximize bug count. A new bug is useful when it proves that AI
can repeatedly turn code understanding into a high-quality, independently verified product finding.
The real output is therefore a learning system:

```text
current source -> proof debt -> compact experiment -> strong evidence
               -> selector/oracle update -> cheaper and sharper next round
```

The bug is evidence that the loop works. The selector, oracle, scenario, negative screen, and
execution scaffold are what let the next round start above zero.

## How The Work Evolved

### Phase 1: broad semantic exploration

The early rounds proved that an AI can read a large codebase, identify suspicious checks, and turn
them into small SQL cases quickly. This produced real findings across DDL, extractors, plan cache,
recovery, and transaction state.

It also exposed the first structural failure: target selection drifted with whatever source path was
interesting. The work could start from DDL and end in executor behavior without an explicit campaign
decision. Broad pattern scans also overproduced wrong-error and compatibility findings. Recall rose,
but severity and explanatory quality were uneven.

The correction was to make campaign scope and consequence admission explicit before execution.

### Phase 2: proof obligations and small matrices

The strongest DDL rounds did not come from trying more syntax. They came from finding a statement in
the implementation that had to be true:

```text
code checked P
therefore the system assumed Q
and skipped, delayed, reused, or published F
```

Adding `D_dims` made the method productive. The AI stopped treating `Q` as one boolean and asked
which semantic dimensions the claim had to preserve: object identity, owner generation, current
namespace, external side state, session context, rollback state, or a derived key domain.

The matrix then changed one of those dimensions while holding the rest fixed. A strong reference
path, current owner, or durable state supplied the oracle. This is why two or three cells often
outperformed a large generated workload.

### Phase 3: highest-consumer and severity discipline

Source anomalies are common; severe user-visible bugs are not. The next improvement was to follow a
candidate to its highest consumer before spending on a live environment.

Examples:

| Method increment | Representative evidence | What it taught |
| --- | --- | --- |
| External effect before local publication | id1710003, id1800003, id1830003 | Cancellation is only honest if metadata and the external runtime owner agree. |
| Restore dependency closure | id1980003 | Restoring a capability bit without its mandatory runtime side owner is incomplete. |
| Retry payload atomicity | id2040003 | Error propagation alone is insufficient when a failed attempt has already appended publishable work. |
| Clone alias identity | id2070003 | Equal cloned values are not equivalent when later mutation requires shared identity. |
| Attempt side-effect rollback closure | id2100003 | KV rollback does not restore every value consumed by statement re-entry. |
| Deferred return-slot ownership | id2130003 | Executing Commit and assigning its error does not prove that the caller receives that error. |
| Retry terminal publication | id2190003 | A zero-work successful retry can publish residue that only the failed attempt produced. |

This stage also produced an important severity rule: a selector does not transfer the severity of a
previous hit. Every candidate must independently reach durable data, terminal truth, availability,
or a mandatory control-plane consumer.

### Phase 4: asset-backed incremental discovery

The loop became meaningfully incremental when positives and negatives both became structured assets.
A validated hit now contributes:

- a selector that predicts a source shape;
- an obligation that states the product contract;
- an oracle that distinguishes RED, GREEN, and INVALID;
- a scenario and schedule that pin the critical boundary;
- fault points or observers used only when the boundary cannot be reached naturally;
- a stop rule and root-cause identity that prevent same-root replay.

Negatives contribute just as much when they retire a tempting class early. The BR GC screen showed
that a downstream TiKV owner can dominate an apparently missing upper-layer guard. The partial-index
screen showed that a pushdown suspicion is not a bug without a semantic disagreement. The retry
reset pass retired `planHint` because its highest consumers were diagnostic, and retired a plausible
`SET_VAR` leak because the product does not reach that explicit-transaction retry edge.

Before registering the next campaign, the local store contained 316 asset revisions and 146
execution records (`RED=68`, `GREEN=65`, `INVALID=12`, `INFO=1`). The transaction campaign added 15
hypothesis assets plus one current-source negative screen, bringing revisions to 332 without
changing any execution count. These counts are not a quality score. Their value is that they
preserve what was tried, what the result proved, and what must not be repeated.

### Phase 5: current-source-only incremental mining

Using a PR review or known fix as a candidate seed can validate a harness, but it does not prove that
the discovery method found the bug. The severe discovery lane now uses a strict provenance mode:

```text
before independent local RED:
  current source, executable contracts, existing public semantics

after independent local RED:
  issues, PRs, fixes, and history for deduplication and blast-radius accounting only
```

The recent sequence from id2070003 through id2190003 is the useful calibration. Each target was
generated from current source, reduced to an owner/consumer graph, proved locally with an exact
counterfactual, and only then lifted to a real environment and checked against upstream history.

## What Consistently Worked

### 1. Semantic compression beat input expansion

The AI advantage was not typing SQL faster. It was compressing a large implementation into one
falsifiable claim, then choosing the smallest value or schedule that separated the claim from the
implementation.

### 2. The oracle was chosen before the fault

Good experiments started by deciding what durable truth would distinguish correct from incorrect.
Fault injection, logging, and source patches were then used to reach or observe that truth. When the
fault came first, the result was often an artificial error with no product meaning.

### 3. Exact counterfactuals established ownership

A RED proves that something is wrong. Changing only the suspected owner and making the same schedule
GREEN proves why. This turned plausible reports into issue-quality findings.

### 4. Natural reachability upgraded bug quality

Injected faults located boundaries efficiently. Replacing them with a real competing transaction,
real PD/TiKV behavior, or a user-executable schedule showed that the product could reach the same
state. Natural reachability is an admission gate for severe findings, not an optional polish step.

### 5. Zero-work and same-final-state controls were unusually powerful

Many stale-state bugs are masked because a successful retry overwrites the residue. A zero-work
retry exposes publication debt. A direct run that begins from the same final database state
separates failed-attempt residue from ordinary statement semantics.

### 6. Negative screens improved throughput

Ownership proof, reachability proof, downstream dominance, and highest-consumer classification can
retire many candidates without compiling or touching a cluster. High-quality negatives are part of
the discovery engine, not failed bug hunts.

## What Did Not Work

1. **Campaign drift.** Following an interesting call graph across module boundaries without an
   explicit scope decision made the work hard to evaluate and easy to repeat.
2. **Syntax blast radius.** Enumerating sibling SQL after one root cause increased surface count but
   added little method knowledge.
3. **Source smell as verdict.** An omitted reset, unchecked-looking error, or comment mismatch is a
   candidate until consumer and reachability are proved.
4. **Mock-induced semantics.** A local mock can remove client-go backoff, TiKV transaction status,
   lock TTL, or topology behavior and manufacture a false failure.
5. **Configured injection as evidence.** A failpoint expression is not a witness that the edge ran.
   The experiment must record a hit or a boundary-specific event.
6. **Internal-state-only oracles.** A stale field or counter is not severe until a correctness-bearing
   consumer changes durable state, terminal result, or mandatory control-plane behavior.
7. **Inherited severity.** Reusing a high-value selector does not make every new owner high severity.
8. **Context replay.** Long sessions repeatedly revisited old targets after compaction. The resume
   gate now records the last terminal target, forbidden replay family, and only legal next action.

## The Improved Discovery Loop

```text
0. Declare campaign scope, provenance mode, and severity objective.
1. Read current source and name a user-visible contract.
2. Build an owner graph across mutation, durable boundaries, retry, and publication.
3. Write P/Q/D/F and identify the highest consumer.
4. Reject on duplicate root, weak consequence, unreachable edge, or dominant safe owner.
5. Choose the strong oracle before choosing injection.
6. Compress to a matrix that changes one semantic or temporal dimension.
7. Run the cheapest faithful environment and require a boundary witness.
8. On RED, change only the suspected owner and require GREEN.
9. Replace synthetic reachability with a natural or production-faithful owner when possible.
10. Only now deduplicate upstream, file, and integrate selector/oracle/negative assets.
11. Stop the root family and let the new assets generate the next target.
```

## Why Transaction Is The Right Next Step

The method has become strongest at boundaries where one owner observes or publishes another owner's
state. Distributed transactions concentrate exactly those obligations:

- TiDB owns SQL semantics and the public result;
- client-go owns retries, primary selection, lock resolution, and protocol choice;
- TiKV owns MVCC locks, transaction status, commit records, and atomic write application;
- PD timestamps, GC, region topology, and transport failures change what can be known at each step.

This is a harder test of the methodology than another single-process DDL variant. It requires the AI
to design observability and injection across repositories while preserving real client-go and TiKV
safety behavior. The dedicated campaign is in `ai-native-transaction-cross-layer-campaign.md`.
