# AI-Native Bug Hunting Methodology v2
> Last updated: 2026-07-13. This document records the improved proof-obligation methodology for using AI to find bugs efficiently. It is intentionally separate from per-bug method-case notes.

## One-Sentence Version

AI should not "try more cases". AI should find where code is making a semantic proof on behalf of the system, then break that proof with a tiny counterexample matrix and a strong oracle.

```text
Find a proof obligation in current source or an executable product contract
→ write a P/Q/D/F/O/R/S audit card
→ compress it into a small matrix
→ validate red cells with a strong oracle
→ stop on hit, minimize and store
→ reverse-engineer the reusable selector
→ stop same-root blast-radius once it has taught the selector
→ score every selector in a ledger by what it actually predicts
→ use the ledger to choose the next target
```

For the independent severe-bug lane, issues, PRs, fixes, and historical bug descriptions remain
closed until an independent local RED and exact-owner counterfactual exist. A declared held-out or
historical-replay experiment is a separate methodology-evaluation mode and must not be counted as
cold discovery.

## What The Discovery Journey Changed

The full retrospective is `ai-native-discovery-retrospective.md`. Its durable corrections are:

1. **Campaign scope is an input.** Declare module boundary, provenance mode, and consequence target
   before reading candidates. Crossing a boundary is allowed only as an explicit campaign decision.
2. **Source smells are proof debt, not bugs.** An omitted field, strong comment, early return, or
   unchecked-looking RPC is admitted only after consumer, reachability, and owner proof.
3. **Choose the highest-consumer oracle first.** Injection and logging are ways to reach or observe
   the contract; they are not candidate seeds.
4. **Build an owner graph.** Track who mutates, rolls back, retries, persists, and publishes every
   semantic dimension across durable boundaries.
5. **Use one-dimensional matrices.** Fault altitude, owner generation, protocol mode, context, or
   final state changes one at a time while the oracle remains fixed.
6. **Require an exact counterfactual.** The same schedule becomes GREEN after changing only the
   suspected owner, return slot, reset edge, alias, or compensation edge.
7. **Natural reachability and severity are separate gates.** A deterministic internal failure can
   validate a selector without qualifying as a severe product bug.
8. **Store negative screens.** Downstream dominance, unreachable retries, diagnostic-only consumers,
   and faithful GREEN matrices prevent future rounds from restarting the same dead ends.
9. **Calibrate the runtime before the candidate.** Record component commits and run a positive
   feature baseline. A cached nightly that cannot execute the selected path makes the cell
   `INVALID(environment)`, not RED or GREEN. Refreshed nightly is a capability lane; exact-source
   attribution requires a matching binary.
10. **Budget AI exploration by volume, not tool count.** A bounded scout needs simultaneous command,
    source-region, token, and wall-clock ceilings. It must return owner-anchored claims and the
    highest consumer, rather than a large source digest that merely used few shell commands.

The practical AI speedup comes from semantic compression, not test-volume expansion.

## Core Model

The old model was:

```text
Code checks P
System believes Q
So it takes a fast path or skips a safe path
```

The improved model makes the hidden parts explicit:

```text
P_check:    What did the code actually check?
Q_claim:    What does the system believe after that check?
D_dims:     Which semantic dimensions must Q preserve?
F_effect:   Which fast path is taken, which safe path is skipped, or which recorded state is trusted later?
O_oracle:   Which strong oracle proves fast path == safe path?
R_redflag:  Which input shape makes P true but Q false?
S_selector: What reusable target shape did this hit reveal?
```

The most important upgrade is `D_dims`. Many bugs are not in the visible branch condition; they are in an unstated semantic dependency of `Q_claim`: collation, NULL semantics, time zone, session variables, schema version, ID/name binding, async cleanup state, plan-cache parameters, SQL type-domain conversion, cache snapshot key granularity, extractor-consumed dimensions, prepared/preprocessor semantic freshness, or rollback-only metadata bits.

### Error identity needs terminal-action observers

The `ERROR_IDENTITY_PRESERVATION` lane exposed a second upgrade. For cleanup and writer paths, it is not enough to ask whether the final error still contains the injected root cause.

The stronger card is:

```text
P_check:  code observes or returns a root error from terminal owner A
Q_claim:  returning that root error is enough to represent failure
D_dims:   sibling terminal owners still need Close/Flush/Commit/Abort semantics
F_effect: code returns early, or lets a later terminal action commit/complete partial state
O_oracle: root error identity + terminal action observer for every owned resource
```

This is why the `dxf/importinto` hit worked. `chunkWorker.Close` did preserve the data-writer root error, so an error-text oracle would have passed. The bug was that `indexWriter.Close` never ran. The useful oracle counted the sibling close action and checked the logs for index flush/close.

New selector shape:

```text
multi-owner terminal action:
  one Close/Flush/Commit returns root error
  the same higher-level operation still owns sibling terminal resources
  a final error-only oracle is too weak
  require root-error preservation plus state/action observer
```

The next `ingestctrl/sstIter` hit refined the selector. The source shape alone is not executable yet; first prove ownership:

```text
owner/finalizer gate:
  same owner object/function owns terminal resource A and B
  no defer cleanup already closes B on A's error path
  no outer caller owns a stronger abort/cleanup finalizer
  B has a direct action observer, not just an inferred absence
```

This gate retired several tempting false positives. `ImportSelectedRows` and `simplesst.flushSortedKVs` were covered by deferred cleanup. `dxf/importinto.onFinished` had a local early return, but its caller `RunSubtask` owns an outer `CleanupAllLocalEngines` error finalizer. The high-quality positive was `sstIter.Close`: the same object owns `iter` and `reader`, there is no deferred or outer owner, and a real `objstorage.Readable` wrapper can count whether `reader.Close` happened. That is the practical rule: execute only after source shape plus owner/finalizer proof plus direct action oracle.

## Bug-Class Scope

This methodology is tuned for one family: semantic bugs where the system silently does the wrong thing and a behavioral reference path exists. Other bug classes need different obligations and oracles. The audit-card skeleton transfers further than the oracle discipline does.

```text
class                      obligation shape                           oracle shape                  fit
wrong result               fast path == spec/SQL semantics            behavioral equality           core
state/metadata corruption  references rewritten, blocked, or cleaned  reference liveness,           core
                                                                      post-cleanup behavior,
                                                                      storage-vs-schema diff
interleaving/concurrency   invariant holds in every substate          pinned-window differential    core, requires the
                                                                                                    temporal dimensions
liveness/hang              this wait always terminates                bounded-time watchdog         adapted: oracle is a
                                                                                                    timeout, not equality
crash/panic                this input cannot reach this assert        process death, sanitizers     random fuzzing is often
                                                                                                    cheaper; use proof
                                                                                                    obligations only for
                                                                                                    targeted asserts
performance                this path does bounded work                quantitative differential     sibling loop; see below
```

### Retry probes need typed effects and an edge witness

Direct closure assignments are only the first layer of retry analysis. A callback can look clean
while `e.fetch()` advances a receiver cursor, appends a batch, or increments a publishable count.
For direct captured receiver calls, bind the receiver type and expand one level of method effects:

```text
retry callback
  -> direct captured receiver call
  -> concrete receiver method
  -> field writes / increments / appends / mutable field calls
  -> post-mutation retryable edge
  -> next-attempt or terminal consumer
```

Do not admit on mutation alone. `id2160003` and its negative siblings produced three gates:

1. **type binding:** do not join methods only by names such as `Update` or `Close`;
2. **post-mutation edge reachability:** an error before the mutation cannot replay its residue;
3. **edge witness:** logs/counters must prove the retry actually occurred.

The witness is essential for failpoint-driven tests. A configured failpoint, compiled test tag, or
expected code location is not evidence that the branch ran. The first cleanup-index oracle passed
only because failpoint source conversion was absent; it became a valid RED only after the output
showed `RunInNewTxn` accepting a retryable error.

Severity remains a separate gate. A deterministic panic or wrong repair count validates the
selector, but it does not become severe without wrong durable data, terminal semantic inversion, or
control-plane publication at the highest consumer.

`id2190003` adds a second consumer class. Do not search only for state read during re-entry. Search
also for failed-attempt state published after re-entry returns:

```text
retry reset omits value + valid/set/dirty flag
  -> successful attempt performs zero work and never overwrites the pair
  -> statement/task completion publishes the failed-attempt value
  -> a next operation persists or acts on it
```

Add a **zero-work successful attempt** cell to retry matrices. Change concurrent state so the
rebuilt executor skips the setter, then compare publication state with a run that starts directly
from the same final state. This cell found `LastInsertID/LastInsertIDSet`: neighboring statement
outputs and counters are reset, but these fields survived. Observing `LAST_INSERT_ID()=99` was not
the highest oracle; inserting it into a sink table proved wrong durable data. Follow every retained
`Set`, `Valid`, `Dirty`, `Changed`, or `Present` flag through terminal publication and one downstream
consumer.

### Liveness lanes need a persistence gate

For retry-classifier and DXF-style liveness families, a single red timeout is not
enough. Add a tiny gate before you generalize the selector:

```text
gate A: one-shot infra/generic fault      -> should usually recover (green control)
gate B: persistent infra/generic fault    -> if it stays running, this is the red lane
gate C: semantic/runtime SQL error        -> should fail fast / rollback, not hang
```

This gate matters because it separates three very different stories:

- normal resilience to a transient blip
- a real liveness bug where an impossible-to-complete task remains `running`
- a normal user-visible rollback on semantic data/SQL errors

In practice, do not call the family "proved" until you have at least one green
control from A or C. The useful selector is often not "runtime error", but the
much narrower shape:

```text
persistent unknown/generic post-check or post-import error
  enters retry classifier as retryable
  task remains running instead of failing/reverting
  removing the external condition lets it immediately continue
```

That is the difference between a noisy timeout probe and a reusable liveness
selector.

### Source safety comments and skipped expected-success tests are proof obligations

Do not treat an explicit source comment like "this makes async commit and 1PC
safe" as soft background text. It is a first-class `Q_claim`, especially when a
nearby test already encodes the same expectation but is currently skipped or
unstable.

The practical loop is:

```text
source/runtime contract says path P is safe
→ find the smallest natural schedule that should still satisfy P
→ vary only the sibling guard/protection path (for example MDL OFF vs MDL ON)
→ require a strong post-success oracle on the green arm
```

This produces a different class of high-value hit from failpoint injection. The
bug is no longer "an injected error was mishandled"; it is "the product contract
that source and tests already claim is safe no longer holds on current runtime."
id1440001 is the calibration point: `delayForAsyncCommit` plus skipped
`TestAsyncCommitWithSchemaChange` / `Test1PCWithSchemaChange` became an
executable proof obligation, and the winning matrix was the natural
`metadata_lock=OFF` red arm versus `metadata_lock=ON` green sibling on the same
DDL/txn shape.

There is one more split inside gate B that matters a lot in practice:

```text
P_check:  error is classified as retryable
Q_claim:  retryable means "safe to keep the subtask/job running until success"
F_effect: outer layer provides no retry budget, no escalation, no terminal state
O_oracle: one-shot GREEN, persistent RED, repeated same-step rerun count,
          post-removal recovery, and ideally a bounded-retry contrast in a nearby layer
```

This separates two different bug classes that look identical if you only stare
at a stuck job: `S25` ("a runtime fundamental never should have entered retry")
versus `S26` ("the timeout can begin life as retryable, but retryable does not
mean retry forever"). Once the card reaches this shape, source review should
explicitly ask where the retry budget lives and what turns budget exhaustion
into a user-visible terminal action.

### Runtime asset loss after progress persistence is also a proof obligation

For stateful modules like `DXF / ingest / txn`, another high-value question is
not "was the error retryable", but:

```text
the system has already persisted or scheduled meaningful progress
-> the next phase assumes some runtime asset still exists
-> if that asset disappears, does the system fail gracefully,
   retry with a budget, or kill the serving/owner process?
```

The useful card shape is:

```text
P_check:  code has already persisted state, chosen an owner, or built a local runtime asset
Q_claim:  the next phase can safely consume that asset as if it still exists
D_dims:   process ownership, local file/object lifetime, failover visibility, task history
F_effect: import / commit / replay path touches the asset without a recoverable boundary
O_oracle: front symptom + process liveness + owner failover + task/subtask evolution + end-state
```

The practical move is very specific:

```text
find a natural pause after progress is durable but before the asset is consumed
→ perturb the smallest owned runtime asset (file, dir, local engine, checkpoint, temp state)
→ observe both user-facing symptom and process/owner aftermath
```

The asset name must be resolved before the fault is injected. A single directory tree
can contain several semantically different assets:

```text
raw ingest input:        <engine>.sst/<file>.sst
open local engine DB:    <engine>/000004.sst + MANIFEST/CURRENT
checkpoint/ownership:    durable task state in KV
```

Do not treat them as interchangeable because they are adjacent on disk. The current
`ADD INDEX` calibration makes the distinction executable: deleting the raw input SST
is a GREEN retry/rebuild control, while deleting the internal Pebble DB SST reaches a
fatal missing-file path and kills the executor. The selector must name the exact
asset-to-consumer edge (`P -> Q`) and every RED must have the neighboring asset-type
control, otherwise a broad “local file loss” result is not attributable to a product
contract.

This is different from ordinary chaos. The point is not to make the system
"unstable"; it is to test a precise hidden proof: after progress has been made,
is the remaining runtime asset treated as a guaranteed fact?

The distributed `ADD INDEX` local-engine-loss crash candidate is the current
calibration point. The winning loop was:

```text
natural pause after SetTSBeforeImportEngine
→ delete the local engine directory
→ require:
   - client-side failure signal
   - actual process-life proof, not only pod health
   - owner failover observation
   - eventual task history and end-state
```

One practical trap matters a lot here: container health can mask process death.
If PID1 is only a sleeper or shell wrapper, a TiDB process can exit while the
pod remains `Running`. For this lane, "process is gone" must be proven from the
actual process/port surface, not inferred from pod restart counters.

For a candidate that self-heals after failover, severity is still judged by the
serving-process interval: client disconnect, process disappearance, owner handoff,
and delayed completion are separate observables. A green final table does not erase
the availability RED, but it does keep it distinct from wrong-result or permanent
liveness roots.

### Strong REDs need an upstream history/issue dedup gate

A strong live RED is evidence of a behavior, not automatically evidence of a new
root. Before filing or incrementing the severe-bug count, normalize the finding into
an exact tuple:

```text
operation + lifecycle action + phase + asset/consumer edge + fatal log signature
```

Then search, in order:

1. upstream issues using the exact log and user action;
2. upstream PRs and regression tests using the asset path and cleanup/ownership terms;
3. local history and existing bug roots using the same failure boundary and fix locus.

Classify it as a known-root rediscovery when the existing report has the same trigger,
the same user-visible failure boundary, and the same intended repair location. Keep the
new run as an asset when it adds a smaller deterministic trigger, an adjacent GREEN
control, a stronger process oracle, or a fix-validation matrix. Do not file a second
issue or count a second root merely because the new harness reached the same fatal
library path through a different fault injection.

```text
strong RED
-> normalize exact tuple
-> issue / PR / history dedup
-> same trigger + boundary + fix locus?
   yes -> known-duplicate rediscovery asset
   no  -> contract review, then new-root accounting
```

The `id1530002` calibration is the model: the current-master probe deleted an
internal Pebble engine SST and proved process exit, while upstream #65958 already
covered the natural `ADMIN CANCEL DDL JOBS -> tmp_ddl cleanup -> missing SST` race.
The probe remains valuable because raw-input SST loss was a GREEN retry control and
the process/owner/task oracle was made executable.

### Performance obligations are a sibling loop, not the same loop

The card structure transfers. Optimizers, caches, and pruners constantly make cost claims: "this predicate prunes the scan", "this cached plan is still optimal", "this path reads O(k) rows". Those are proof obligations too, and `R_redflag` thinking applies directly: stale statistics, data skew, parameter drift, and cardinality boundaries are the inputs that let a cost proof pass while the cost claim fails.

What does not transfer is the oracle discipline:

- Behavioral equality is exact; cost oracles are noisy. Never use absolute wall-clock time as the oracle.
- Prefer counting oracles over timing oracles: rows read, bytes scanned, RPCs, cache hits, pages or keys visited. Deterministic counters are the performance analog of row-set equality.
- Make the oracle relative: plan A vs plan B on identical data, the same query at N vs 2N rows (growth shape), shortcut-on vs shortcut-off, same counters across versions on pinned data. A cost claim is falsified by the wrong growth curve or the wrong counter ratio, not by one slow run.

Do not mix the two loops in one campaign. A performance red cell has its own pause gate (minimize, map back to the cost claim, record counter evidence), and its selectors and battery entries belong in a separate performance ledger: the target shapes that predict wrong results do not generally predict bad plans.

## Audit Card Template

Use this before writing a probe:

```text
Target:

Source anchors:

T_tests:
  Existing tests cover ... ; battery dimensions they never exercise: ...

P_check:
  Code actually checks ...

Q_claim:
  The system then assumes ...

D_dims:
  Q depends on ...
  (walk the battery in the appendix; mark every family relevant or irrelevant)

F_effect:
  Because Q is trusted, the system ...

O_oracle:
  Fast path / shortcut:
  Safe path / reference:
  Required equality:

R_redflag:
  Inputs most likely to make P pass but Q fail:

S_selector:
  If this hits, the reusable target shape is:

Score:
  density / precision / oracle / cost / teaching (1-3 each)

Stop rule:
  On first unexpected red cell, stop expansion and minimize.
```

## Target Sourcing

Selection rules evaluate a candidate; this section is where candidates come from. Maintain these as standing queues, not one-off searches, so the next target is pulled from a queue instead of re-derived from inspiration.

Every campaign declares one provenance mode before it fills those queues:

```text
COLD_SOURCE:
  current source + executable contracts only before RED
  history/issues/PRs/fixes open only for post-RED dedup and root accounting

HELDOUT_REPLAY:
  historical cases are intentionally hidden/evaluated to measure method recall
  findings validate selectors or harnesses, not independent discovery

REVIEW_ASSISTED:
  diffs or review findings are explicit seeds
  useful for regression design, but never mixed into cold-discovery yield
```

The active severe-bug campaign uses `COLD_SOURCE`. Items 4 and 5 below are available only in a
declared replay/review lane, or after an independent RED.

1. **Prover name patterns.** Sweep the codebase for functions whose boolean answer gates a path/skip/block decision: `Check*`, `CanUse*`, `Imply*`, `Prune*`, `Derive*`, `Rewrite*`, `IsSafe*`, `Need*`, and fast-path guards. Every match is a candidate `P_check`. Enumerating them once gives the queue a denominator: audited / total.
2. **Semantic replacement patterns.** Code that replaces general evaluation with a cheaper form: prefilters that drop the original predicate, custom extractors, normalization (lowercasing, regex compilation, key hashing), conversion into a narrower backend request domain, caches that reuse earlier results, prepared ASTs that reuse earlier validation, or table/object-name snapshots that stand in for later filtered queries. The obligation is always "the replacement is neither wider nor narrower than the original semantics." For caches, also ask whether the cache key includes every semantic dimension consumed by the extractor/shortcut, or whether the hit path rechecks the missing dimensions. For prepared statements, ask whether a session variable consumed by preprocessor/validator at `PREPARE` time is revalidated under the current session at `EXECUTE` time, or whether direct current-session SQL provides a stronger reference.

   **Operator semantic arity checklist.** When the replacement is a scalar operator, do not call
   the target "covered" after seeing only the obvious operands. First list every semantic input
   the SQL operator consumes, then compare that list with the inputs the shortcut actually records.
   Typical hidden inputs are collation, escape character, regexp `match_type`, timezone, precision,
   type-domain conversion, NULL mode, session switches, and backend error handling. id30030 and
   id30031 came from `LIKE` recording only `pattern` while SQL also has `ESCAPE`; id30033 came
   from `REGEXP_LIKE` recording only `expr+pattern` while SQL also has `match_type`. The red cell
   is usually the smallest value where the omitted input flips scalar truth value, and the green
   controls are the same operator with that input set to the backend's default semantics.

   **Semantic-domain rewrite checklist.** When a rule replaces one comparison/evaluation domain
   with another, name both domains before writing cases. The proof is not "the guard seems
   related"; it is `D_old == D_new for every value the guard accepts`. id30040 came from this
   shape: `join_key_type_cast` replaced DOUBLE-domain mixed INT/VARCHAR comparison with signed-INT
   equality and a signed-int round-trip guard. The smallest red cell was `'1e1'`: true in the
   original numeric-string comparison (`10='1e1'`), but false under the guard because
   `CAST('1e1' AS SIGNED)=1` while `CAST('1e1' AS DOUBLE)=10`. This is domain-difference
   enumeration, not random string fuzzing: parser grammar, overflow, rounding, collation, NULL,
   and error/warning behavior are all candidate `D_dims` when a general domain is narrowed.

   **State-stack operation checklist.** When a module has several SQL operations mutating the
   same ordered state container, do not audit the container generically. List each operation's
   contract separately, then compare the implementation's mutation primitive with that contract.
   id1200002 came from this exact split in transaction savepoints: `RELEASE SAVEPOINT` should
   remove only the named marker, while `ROLLBACK TO SAVEPOINT` should restore state and discard
   later markers. TiDB used rollback-like truncation for release. The red matrix needed only two
   markers (`sp1`, `sp2`) and one consumer (`ROLLBACK TO sp2`). Existing tests can encode product
   drift, so use an external/reference contract when compatibility is expected.

   **State-owner handoff checklist.** When code borrows an internal/session-pool object, name the
   owner of every field it reads or writes. The proof obligation is not just "this SQL is internal";
   it is "no state owned by a previous borrower or by the internal default can become user-visible
   output for the current statement." The red matrix is usually cross-actor: make A write metadata,
   make B perform a partial update that leaves the row visible, then check whether the actor/state
   field is A, B, empty, or stale. The 2026-07-10 grant/revoke hit came from this shape: GRANT
   copied the outer user into a pooled sys session, REVOKE wrote `mysql.tables_priv.Grantor` from
   the internal session user, and a cross-user partial revoke left `Grantor` empty. Treat this as a
   correctness/metadata oracle unless authorization behavior itself changes; do not inflate it into
   a security claim.
3. **Sibling-path asymmetry.** One path records state, a sibling path reconstructs it: success vs rollback/cancel/retry, primary entrypoint vs bypass entrypoint, common shape vs a rarer shape with its own iterator. The obligation is "the sibling reconstructs equivalent state."
   A strong subcase is "green helper sibling": common DDL paths are green precisely because source
   has owner-specific rewrite/cleanup/remap helpers, while a less-common DDL path changes the same
   ownership dimension without calling those helpers. id630014 came from this shape: masking-policy
   rename/drop/truncate paths were green, but `EXCHANGE PARTITION` swapped table/partition IDs
   without remapping policy rows.
   Another strong subcase is "flattened artifact owner bit": one path creates per-owner artifacts,
   a sibling path flattens them, and a later consumer reconstructs owner/type from ordinal instead
   of carrying it explicitly. id30038 came from this shape: add-index backfill flattened multiple
   keys emitted by one multi-valued index, then used `flattenedKeyOrdinal % len(indexes)` to pick
   index metadata for duplicate checking. The redflag is any owner that can emit N artifacts while
   a sibling owner emits one or has a different decode/type shape.
   Another strong subcase is "error-domain retry bridge": recovery logic assumes a foreign
   transient error will cross into the local retry classifier without losing its retryable/fatal
   meaning. id1290001 came from this shape: fast-reorg `ADD INDEX` needed PD TSO recovery, but
   `PD:client:ErrClientCreateTSOStream(... retry timeout)` entered the `*terror.Error` branch,
   `terror.ToSQLError` mapped unknown RFC class `PD` to a generic MySQL code, and
   `isRetryableError` therefore treated the transient fault as fatal. The audit question is
   "after normalization/classification, what fact still proves this foreign error is retryable?"
   The smallest red matrix is usually one live transient-fault cell plus one sibling green control
   and a terminal-state oracle (`err_count=1` vs a real retry budget).
   A useful negative screen from 2026-07-11: if a synthetic foreign family turns both siblings
   RED at an altitude where the family is not yet proven product-feasible, record domain-mismatch
   calibration rather than a hit. Injecting `KV/Ingest` leader-change shapes into the common txn
   backfill worker made both current-master `ADD INDEX` and `MODIFY COLUMN` roll back; without a
   same-altitude GREEN control or natural-reachability proof, that teaches no new selector.
   Another strong subcase is "embedded owner handoff": a parent DDL syntax accepts a child semantic
   obligation, but the real owner is a different job or validator. id30032 came from this shape:
   `ADD COLUMN` accepted a column definition containing inline `CHECK`, but submitted only the
   column job and discarded the extracted CHECK constraint. The audit question is "who owns the
   child obligation after the parent spec is split?" Direct target-schema and sequential safe-path
   references are the cheapest oracles.

   Side-state ID-swap findings now need a two-tier oracle. Tier 1 is the storage-vs-current-owner
   diff: prove the system table/cache/timer row still points at an ID whose logical owner changed.
   Tier 2 is the behavior gate: prove a management DDL, cleanup round trip, active scheduler, or
   data oracle is wrong because of that stale row. id630024 showed why this matters. `EXCHANGE
   PARTITION` left stale TTL status/timer rows after swapping a TTL table ID with a non-TTL
   partition ID, but the timer syncer created the current-ID timer and disabled the old one. That is
   still a confirmed metadata bug, but it is low severity; id630014 masking policy was higher
   quality because ordinary `DISABLE/DROP MASKING POLICY` could no longer reach the stale policy.
4. **Diff-directed intake.** Recently merged changes that touch a known prover, add a shortcut, or fix a bug (incomplete-fix neighborhoods). New code in a weakly tested area is the highest target-density signal available; this source keeps the queue alive continuously.
5. **Historical bug clustering.** A curated bug library is the empirical prior of the whole method: it says which `D_dims` actually break in this codebase, which subsystems have density, and it supplies seeds — regression tests are known-fragile configurations worth perturbing. Every mined cluster should emit battery entries and candidate cards, and every new hit flows back into the library.

Restore/flashback paths need an extra filter. A broad rule like "restore copies old metadata" creates too many low-value variants. Score a restore candidate higher only when the ordinary create/alter path has an explicit validator, the recover path appears to skip that validator, no sibling entrypoint already provides a symmetric validator/sanitizer, and a post-recover behavior oracle can prove the validator still matters. Score it lower when source deliberately strips/disables the field on recover, the operation is blocked before recovery, the environment cannot instantiate the referenced resource, or the only evidence is a metadata/sys-table delta with no user-facing behavior. Also reject lazy-name-resolution references where the ordinary create path is already permissive: a restored sequence default pointing at a missing sequence is not a recover-specific validator gap if `CREATE TABLE ... DEFAULT NEXT VALUE FOR missing_seq` also succeeds. This "asymmetric validator gap" filter is what separated id30016 (FK parent-reference validator skipped on `FLASHBACK TABLE`) from green/boundary screens such as TTL recovery, cached-table recovery, TiFlash-on-no-TiFlash testbed, sequence-default recovery, masking-policy recover, and TTL parent creation after a dangling child FK.

When broad DDL owner matrices come back green, treat them as a coverage map rather than an invitation to keep widening. The next candidate must show a sharper reason: an entrypoint pair with non-symmetric validation, rollback/restore reconstruction that can lose an owner bit, or an independent sibling iterator/finalizer. Otherwise record the screen as calibration and move the selector queue forward.

For every sourced candidate, check existing test coverage before writing the card (`T_tests`): read the target's tests and list which battery dimensions they exercise. Dimensions absent from the tests are the cheapest `R_redflag` candidates available.

## Target Selection Rules

High-value targets have all of these:

- `P/Q/F` can be stated precisely from source or historical behavior.
- `D_dims` contains fragile semantics such as collation, NULL, time zone, session vars, prepared/preprocessor freshness, async state, ID/name binding, rollback state, or cache reuse.
- There is a strong oracle before we write the case.
- The target can be covered by a small adversarial matrix rather than broad random SQL.
- A hit can be turned into a reusable selector, not just a one-off bug.
- The card is admitted to the campaign's severity objective: either a direct consequence-3
  user failure is predicted, or a consequence-2 symptom has a concrete, executable oracle that
  could prove a named consequence-3 escalation. A cheap C1 symptom is not enough.

Downweight targets when:

- The only evidence is a different plan.
- There is no safe path or reference path.
- The case needs large random fuzzing to have a chance.
- The product contract is intentionally name-bound, historical, advisory, or loose-lifecycle.
- A hit would not teach us how to pick the next target.

When more than a handful of cards are queued, order them with an explicit score instead of gut feeling:

```text
density:     how likely this code is under-tested or recently complicated
precision:   how exactly P/Q can be stated
oracle:      how strong and low-noise the reference path is
cost:        how quickly the matrix runs end to end
teaching:    how much a hit or a green would sharpen the selector ledger
consequence: the user-visible severity class if this target hits (scale below)
```

Score coarsely (1-3 per axis). The point is a defensible queue order, not false precision.

The `consequence` axis exists because the first five axes systematically over-reward the
cheapest bug class. Static-precheck targets (wrong-error, idempotence) are high on density,
precision, oracle, and cost — cheap and clean — so a purely additive score floats them to the
top even though a hit changes almost nothing for a user. Score consequence explicitly:

```text
1  wrong-error / non-idempotent: valid SQL fails, or a migration is not idempotent, but no bad
   data is produced and the user sees an explicit error.
2  wrong-result / wrong-acceptance: a silent wrong answer, or DDL publishes an invalid state
   that only fails later DML.
3  data loss / corruption / constraint bypass / cross-session leakage / concurrency-invariant
   violation: silent, durable, and hard to detect.
```

Three rules govern the queue with this axis:

- **Order by consequence first, then the composite of the other five.** A cheap, precise,
  easy-oracle consequence-1 target must not outrank a harder consequence-3 target. The first
  five axes break ties within a consequence class, they do not cross it.
- **Wrong-error cap.** After two confirmed consequence-1 bugs in one selector family, further
  consequence-1-only variants are blast-radius by default (see Blast-Radius Stop Rule). They
  reopen only by adding a new sub-shape or escalating the consequence class — not by naming
  another owner of the same shape.
- **Standing high-consequence lane.** Every confirmed consequence-3 hit to date — reorg
  row-skip (id600001), MODIFY-reorg CHECK bypass (id630013), EXCHANGE id-swap orphan (id630014)
  — lives in one family: *a state-transforming DDL (reorg, backfill, id-swap, restore, or a
  pinned concurrent substate) bypasses an invariant the normal path enforces.* Source and
  schedule this lane, including the interleaving dimension (pinned substate × concurrent op)
  that the battery documents but campaigns have barely run, ahead of static-precheck targets —
  not after them. This is where severity has actually been, and it is the least-exercised lane.

### Severity admission gate

The `consequence` score orders candidates; it does not by itself make a candidate eligible for
the main discovery loop. Record one explicit `admission` field on every audit card:

```text
C3_DIRECT:       expected user-visible data/invariant/liveness failure and its state-observing
                 oracle are named before execution.
C2_WITH_LIFT:    the first symptom may be wrong-result or wrong-acceptance, but the card names
                 a plausible C3 escalation and the oracle that can prove or disprove it.
NOT_ADMITTED:    C1 only, metadata-only, leak-only, or no credible C3 oracle.
```

Only `C3_DIRECT` and `C2_WITH_LIFT` can enter `MINE_BUG`. A `C2_WITH_LIFT` probe ends when its
escalation oracle is decided: RED promotes the actual C3 consequence; GREEN is a methodology
calibration or negative result, not a public high-severity finding. In particular, injected
`Close`/cleanup errors need a follow-up proof of residual data, a failed retry, a blocked DDL, or
another user-visible consequence. A skipped terminal action by itself is not a serious bug.

Run a **user-promise calibration before fault injection**, not after a convenient RED. Trace the
state owner to its highest actual consumer, then state the current official product promise in
plain user terms. Names such as placement, policy, affinity, checkpoint, and replica do not carry a
severity class by themselves. Ask what directly changes: row correctness, durability, constraint
enforcement, cross-session isolation, required-path availability, replica safety, or only an
experimental/advisory performance optimization. `C3_DIRECT` requires the first group plus an
observer. `C2_WITH_LIFT` requires an executable oracle that could reach it. A metadata/runtime split
whose highest promised consumer is performance-only is `NOT_ADMITTED`, even when S35 proves a real
rollback-coherence bug. Record that RED as a lower-severity method asset and stop before a costly
testbed lift. The TRUNCATE-affinity cancel screen is the calibration case: exact owner split, but an
experimental Region-colocation promise rather than data or availability failure.

## Strong Oracle Patterns

Prefer oracles that compare behavior, not implementation detail:

```text
fast path vs safe path
normal WHERE vs CASE-wrapped predicate re-check
shortcut/extractor path vs explicit scalar re-check
cached execution vs cache-disabled/direct execution
direct current-session SQL vs existing prepared statement after a semantic switch
index path vs table scan
DDL-visible metadata vs live schema
cleanup done vs real post-cleanup behavior
side-state owner ID/name mapping vs management round trip
rollback path vs successful sibling path
direct target schema vs sequential safe path vs compact transition path
pinned-substate window + concurrent operation vs no-concurrency baseline
reference-implementation differential (e.g. MySQL) for contract-ambiguous semantics
```

Plan evidence is useful for triage, but it should not be the oracle by itself.

When a cell is `info(contract-ambiguous)`, a reference implementation is the sharpest way to split it. Run the same minimal semantics on the reference (for a MySQL-compatible system, MySQL itself; otherwise the spec or a sibling internal path). The differential separates two things the ambiguity was hiding: the **general contract that the reference settles** (e.g. an `utf8mb4_bin` column's `=` is case-sensitive — MySQL returns 0 where the shortcut returns rows), which is now a firm violation, from the **product-specific exemption only the owner can grant** (e.g. whether diagnostic tables intentionally override that contract). The reference cannot rule on the exemption, but it removes the part that was never actually ambiguous. Isolate the minimal semantic claim first — reference implementations rarely have the same virtual tables, so compare the underlying rule (collation, NULL, coercion) on an ordinary table, not the product-specific surface.

## Small Matrix Discipline

Do not start with a broad fuzzer. Build a tiny matrix around the dimensions most likely to break `Q_claim`.

Example dimensions:

```text
collation: binary / case-insensitive / explicit COLLATE
name shape: lowercase / mixed-case / escaped wildcard / special chars
NULL: NULL / non-NULL / <=> / IS TRUE / OR widening
time: current offset / historical offset / timezone switch
type-domain: signed / unsigned / negative / zero / nonnumeric string / overflow
comparison domain: original scalar equality / rewritten typed-key equality /
                   parser grammar boundary / rounding or overflow boundary /
                   warning-or-error boundary
cache snapshot: full-table snapshot / type-filtered snapshot / address-filtered snapshot /
                scalar-rechecked hit / extractor-dimension-only hit
cache: first parameter safe / second parameter unsafe
prepared semantic switch: prepare ON / execute OFF / execute WARN / direct OFF /
                          flush plan cache / plan-cache OFF / schema-change reprepare
DDL state: success path / rollback path / cancel path / retry path
ownership: object ID stable / owner key changed / name changed
embedded owner handoff: direct target schema / sequential child-owner path /
                        compact parent syntax / named vs anonymous child object /
                        warning_count / behavior after transition
side owner remap: pre-DDL operable / post-DDL ID maps to current owner /
                  post-DDL management round trip / recreate leaves no stale row
interleaving: pinned substate × concurrent operation / no-concurrency baseline
```

The interleaving dimension is not optional decoration. In systems with multi-step state changes, a large share of historical bugs are temporal: an invariant that holds before and after an operation breaks inside a specific substate window. If the harness can pin substates deterministically (injection points, pause hooks), `D_dims` must cover "when", not just "what", and each temporal cell compares the pinned-window run against a no-concurrency baseline.

Each cell must have:

```text
setup
trigger
trigger evidence: proof that the fast/shortcut path actually fired
fast/shortcut result
safe/reference result
expected equality
classification: red / green(triggered) / invalid(untriggered) / skipped(capability) / info(contract-ambiguous)
```

An `info` cell documents behavior that is neither a proven violation nor a proven pass — typically a product-contract ambiguity (inconsistent sibling semantics, name-vs-ID healing, historical-by-design surfaces). Info cells are evidence for the fix-semantics discussion, not findings; do not count them as red, and do not let them expand the matrix.

A green cell without trigger evidence is `invalid`, not green: if the shortcut never fired, the cell proved nothing about the proof obligation. Invalid cells must not feed calibration, downweighting, or the selector ledger. Trigger evidence is cheap — a plan that shows the shortcut, a cache-hit flag, an injection-point counter. This is the second legitimate use of plan evidence besides triage.

## Pause Gate

When a red cell appears:

```text
1. Stop expanding the matrix.
2. Minimize the SQL or operation sequence.
3. Prove the user-visible symptom.
4. Map the symptom back to P/Q/D/F.
5. Record source anchors and fix direction.
6. Write the bug into the bug library.
7. Reverse-engineer S_selector and open its ledger entry.
8. Drive the family to a terminal state (see Family Resolution); do not leave it "paused awaiting owner."
9. Run the severity admission check before any external filing: a root without a reproduced C3
   consequence stays in the internal method/bug library as calibration or a lower-severity sample.

This pause gate is essential. The goal is not to count many similar red cells; the goal is to improve target selection.

## Blast-Radius Stop Rule

A confirmed bug often exposes a root cause with obvious siblings. One or two sibling checks can be useful: they tell whether the root cause is a local one-off or a generic helper / framework-level mistake. After that, more sibling enumeration usually lowers novelty and turns the method back into case grinding.

Use this rule:

```text
if a red cell proves a root cause in one owner:
  optionally test one high-value sibling owner to learn whether the root is generic

if the same generic helper/mechanism is proven across a second owner:
  record one representative blast-radius case
  update the selector and fix direction at the helper/mechanism level
  stop enumerating users of that helper
  move to a different mechanism, a different D_dim, or another lane
```

id30019 is the model case. id30018 showed that `extractCol(..., valueToLower=true)` plus predicate removal can violate SQL-visible semantics for InfoSchema object names. id30019 proved the same generic helper across a second owner, `information_schema.metrics_summary`. That was enough to upgrade the selector and repair model; a third/fourth `valueToLower=true` user would be low-novelty blast-radius enumeration unless it introduced a new mechanism or D_dim.

### Reopen test: what counts as a new root cause

"A different owner" is not a new root cause, and the prose reopen conditions in the selector
ledger ("another create-like owner", "another dependency checker") silently license endless
enumeration. Apply this discriminable test, in order. A hit opens a new root — a new found_bug
root, a new selector entry, a new main report — only if it passes:

```text
1. Fix-locus test.  Would the fix for an already-recorded sibling also fix this hit?
   YES -> SAME root cause. Record it as a surface under that root (a matrix row in the
          root's report), not a new root. A different owner reached by the same fix is one
          root with a wider blast radius, not two bugs.

2. New-reasoning test.  Does finding this hit require a checklist step the selector did not
   already have?  NO -> it is blast-radius. After the SECOND owner has proven the mechanism
   is generic, STOP and record "affects N owners" on the single root. "Another owner of the
   same shape" is not a new step.

3. Consequence-escalation test.  Is the user-visible class strictly worse than every recorded
   sibling (e.g. wrong-error -> silent corruption)?  YES -> it may warrant its own report, but
   under the SAME selector, flagged as an escalation, not a new selector.
```

Only tests 2 or 3 open a new root. A different fix locus alone (test 1 = NO) does not — it is a
blast-radius surface. Worked example of the failure this prevents: the S15
"candidate-validation-before-target-exists" sub-shape was a legitimate new root at id630018
(CREATE TABLE). But id630019 (SEQUENCE), id630020 (RESOURCE GROUP), id630021 (MASKING POLICY),
and id630022 (SPATIAL INDEX) add no checklist step (same audit: "find the target-exists
classifier, audit every candidate builder before it") and stay consequence-1. They are four
blast-radius surfaces of one root — recorded as "affects 5 create-like owners" — not four
roots. Counting them as four bugs is the metric inflation this test exists to stop.

## Family Resolution (no external dependency)

A pause is a transition, not a terminal state. This method treats the LLM's own judgment as final — there is no external owner sign-off in the loop. So a family must never park in "paused, awaiting owner feedback": that condition never fires and turns the pause gate into a permanent freeze, silently converting a discovery buffer into a graveyard. Every paused family must be driven, in the same session, to one of three terminal states the LLM can decide on its own:

```text
CLOSED-FIXABLE:
  The LLM can state a concrete fix direction AND a fix-validation contract —
  what a correct patch must satisfy across the whole D_dims set, not just the repro.
  Archive as resolved-pending-patch. Stop probing this family.

RULED-CANDIDATE:
  A contract-ambiguous cell. The LLM issues the ruling itself, using a
  reference-implementation differential to split the settled general contract
  from the product exemption. Record as candidate with the ruling written down.
  Close — do not wait for an owner.

GUARDED:
  The only terminal state that keeps "do not expand." It exists solely to block
  same-root blast-radius variants. It carries an explicit reopen trigger the LLM
  can detect without external input — a new sibling path, a new D_dim, a new
  owner/container surface — NOT "owner feedback".
```

Rule: no family stays "paused" across sessions. If none of the three terminal states can be reached, that gap is itself the finding — the symptom is not yet proven, the fix is not yet understood, or the oracle is too noisy. Resolve that, do not park it. "Awaiting owner feedback" is not a legal state; the closest legal state is CLOSED-FIXABLE (we already know the fix) or GUARDED (we do not, and we say what would change that).

## Green Gate

The pause gate handles red cells. All-green matrices need their own stop rule, because grinding a dry target is the quiet way this method degrades back into fuzzing.

```text
1. Check trigger evidence for every green cell; reclassify unproven cells as invalid.
2. If the matrix is genuinely green(triggered), record the family as calibration and downweight it.
3. Every downweight must name its reopen condition: the new path, owner, or state
   dimension that would turn this green family back into a live target.
4. Do not build a second matrix for the same target without a new D_dims hypothesis.
5. Charge the outcome to the selector that nominated the target.
```

A reopen condition that later becomes true is an automatic re-nomination: green calibrations are not dead records, they are tripwires.

A matrix full of invalid cells measured nothing: fix the trigger, or abandon the target honestly.

## Selector Ledger

A selector extracted from one hit is a hypothesis, not a fact. Without a ledger, selectors are only descriptions of past bugs; with one, they become tested predictors.

Each selector carries a record:

```text
selector:     the reusable target shape, one sentence
born from:    the hit that produced it
predictions:  targets it nominated → red / green(triggered) / invalid
status:       active / narrowed / retired
```

Rules:

- Selectors compose. An intersection nomination (two selectors jointly pointing at one target) is often sharper than either alone; record composed nominations against every contributing selector.
- Every nomination gets an outcome. Invalid (untriggered) outcomes count neither for nor against; they mean the probe was broken, not the selector.
- Retire or narrow a selector after several consecutive green(triggered) nominations, and record why.
- Narrow a selector after a second-owner same-root hit proves a generic helper/mechanism; record one representative blast-radius case, then stop enumerating helper users.
- Rank the target queue by the ledger: selectors with real hit records outrank plausible-sounding new ones.
- The ledger answers the method's own health question: are hits coming because the selectors predict, or because we keep looking everywhere?

## Oracle Mining and the Oracle Library

Selectors answer "where is the next bug"; oracles answer "how do we know it is wrong". Both are first-class mining products, not fixed assets. The original leverage — mine an oracle once, cover a wide generation space (a single `ADMIN CHECK` + rowset diff covered dozens of add-index triggers) — only holds if oracles are actively mined, kept, and reused the way selectors are. Treating the oracle suite as a fixed list is the quiet failure mode: it caps what the method can *see* regardless of how well it aims.

Every audit card already contains an oracle-mining step. `O_oracle` is derived from `Q_claim`: "the system believes Q" directly implies "detect a state where Q is false". When no existing oracle can express that detection, `O_oracle` is not "pick one from the list" — it is a mining subtask whose output is a new, reusable oracle.

Oracle sourcing (parallel to Target Sourcing):
- **Q_claim of the current target** — the primary source; the negation of Q is the oracle.
- **Held-out false negatives** — an FN is a bug class with no firing oracle. It is an oracle-mining ticket, not a manual patch.
- **Symptom-class coverage gaps** — a class in the battery with no matched oracle.
- **Historical `violated_invariant`** — each library bug's invariant is an oracle requirement.

The oracle library (parallel to the selector ledger) records each oracle:

```text
oracle:      name
obligation:  the claim it verifies (some Q)
form:        the differential/equality it computes
catches:     symptom classes it fires on
blind to:    classes it structurally cannot see
sensitivity: TP evidence from held-out (fires on the real injected bug?)
specificity: FP evidence from held-out (silent on controls?)
noise:       known false-positive sources
```

Rules:
- An oracle is not "trusted" until held-out shows BOTH sensitivity (fires on the injected bug) AND specificity (silent on controls). An unverified oracle is a hypothesis, exactly like an unproven selector.
- Match the oracle to the symptom class. A suite tuned for one class is blind to others (held-out run #2: data-inconsistency oracles are blind to predicate-simplification wrong-results). Never read a low FN from one suite against a mixed set as coverage.
- When held-out surfaces an FN, mine the oracle from the missed bug's `Q_claim`, add it to the library, and re-run held-out to confirm sensitivity and specificity before counting the blind spot closed. The derivation should come from the proof obligation, not from a human noticing the miss.

### LLM self-verification as an automatic mining step

Held-out execution is independent of the reasoning, but it needs an injectable bug — and many oracles cannot be cheaply injected. The LLM can verify an oracle directly, and this belongs INSIDE the mining loop as an automatic step, not a manual afterthought. For every newly mined oracle, run an adversarial self-verification (before and alongside held-out):

```text
skeptic task (refute, do not confirm):
  given the firing rule F and the obligation Q it claims to detect,
  find inputs where F fires but Q is NOT violated    -> false positive / noise source
  find inputs where Q is violated but F does NOT fire -> blind spot / false negative
  emit each as an EXECUTABLE counterexample, not an opinion
```

Two rules keep this from collapsing back into self-grading:

- **Adversarial, not confirmatory.** The verifier's job is to break the oracle. A confirmatory pass ("looks right to me") shares the miner's blind spots — same-source bias, the exact mechanism that makes self-grading blind to false negatives. Run the skeptic as a separate role (ideally a separate agent) prompted to refute.
- **Land every doubt in execution.** An LLM counterexample is a hypothesis, not evidence. It becomes evidence only when its SQL runs. So LLM verification does not replace held-out — it GENERATES held-out tickets. Pipeline: skeptic (broad, cheap, biased) → executable counterexample → held-out (narrow, independent) → confirm/deny → update the oracle library's `blind to` / `noise` fields.

Evidence tiers: `REFUTED` (a reproduced FP/FN — unsound, replace it) — `HYPOTHESIS` < `LLM-VERIFIED` (survived adversarial review, counterexamples not yet run) < `TRUSTED` (adversarial review + held-out sensitivity/specificity). An oracle that only survived LLM review is stronger than untested but weaker than one whose counterexamples were actually executed — because same-source bias means the review can miss what execution would catch.

This is not hypothetical. Oracle O9 (`metadata_sync_check`) passed a held-out run 3/3 with 0 false positives and was marked TRUSTED — then an adversarial skeptic broke it the same day: a durable false negative (drop + re-add a column with the same name hides stale stats behind a matching name set), a false positive (drop-only column fires on benign GC lag), and a demonstration that the "0 FP" was a timing artifact. A single held-out injection shape gave false confidence; the adversarial review — broad but same-source — caught what one execution shape could not. The ordering rule follows: never let a single-shape held-out promote an oracle to TRUSTED without an adversarial pass, and never let an adversarial pass alone promote one without executing its surviving counterexamples.

But the deeper lesson came from chasing the refutation one step further. The counterexamples were real, yet the root problem was not "the form is unsound" — execution showed that after a rename the stats VALUE is correct (column ID unchanged) and only the displayed NAME is stale, whereas the drop+re-add case corrupts the VALUE. Those are two different obligations (display-consistency vs value-correctness) that one oracle was being asked to cover at once. The fix was not to swap O9's form but to SPLIT the obligation: keep O9 as a display oracle and mine O9' as a separate value oracle. General rule: **when a refutation produces both an FN and an FP on the same oracle, suspect a conflated obligation before condemning the form.** An oracle that tries to prove two claims at once is unsound for both; the repair is one oracle per obligation. This is the oracle-side echo of keeping `Q_claim` singular in the audit card.

A second oracle, O2 (`index_vs_table_rowset`), showed the other repair mode. Its obligation was singular and correct (both access paths return the same rows), but the FORM was too weak, and the skeptic reproduced three failures: a structural FN (for multi-valued indexes, `USE INDEX` is not honored, so both arms take a byte-identical full scan and the differential can never fire), a weak-projection FN (`COUNT(*)` is not injective over row sets — under `LIMIT`, disjoint rows gave equal counts), and a concurrency FP (the two arms ran on separate snapshots). None of these is an obligation conflation; the fix is to HARDEN the form: require trigger evidence that the two arms actually take different plans, compare an ordered row hash instead of a count, run both arms in one snapshot, and demand a deterministic query.

A third oracle, O1 (`admin_check_consistency`), showed the last mode. The skeptic could NOT break its actual claim: 120 concurrent checks under live DML gave zero spurious failures, and every tricky type stayed clean — the `8223 ⟺ index-vs-record mismatch` biconditional held. What it found instead was that O1 is trusted for a WIDER obligation than it proves: a row placed in the wrong partition by `EXCHANGE PARTITION ... WITHOUT VALIDATION` passes ADMIN CHECK (index==record holds inside the partition) yet is silently lost by partition pruning, and columnar indexes are skipped by the checker outright. O1 is sound; it is just narrower than "table t is healthy." The repair is neither split nor harden — it is to WRITE DOWN the true scope and mine complement oracles for the obligations O1 never covered (partition-placement, columnar-index consistency).

So refutations sort into three repair modes, and naming which one you are in prevents the wrong fix:

```text
FN + FP together, two different claims   -> obligation conflation -> split, one oracle per Q  (O9)
FN/FP within one claim, form has holes   -> form too weak         -> harden the form           (O2)
oracle sound but trusted too broadly     -> scope overreach       -> record true scope +
                                                                     mine complement oracles    (O1)
```

The trap the three share: a green result read as broader assurance than the oracle actually provides. Held-out execution cannot catch this on its own — it grades an oracle against the shape you injected, so an untested shape (O9's name collision, O2's multi-valued index, O1's misplaced partition row) stays invisible until an adversary goes looking. That is why adversarial verification is a mandatory, automatic step, not a courtesy pass.

The structural FN in O2 also surfaces a principle that generalizes: **an oracle needs trigger evidence, exactly like a matrix cell.** A differential that reports "equal" proves nothing unless the two sides actually exercised different code — if `USE INDEX` was silently ignored, "equal" is vacuous, and the cell is INVALID, not a pass. The trigger-evidence discipline that guards green cells applies one level up, to the oracle itself.

And it is recursive. Executing the hardened O2' to confirm the fix exposed a vacuous pass in the *verifier*: the harness wrapped both arms in `START TRANSACTION READ ONLY`, which TiDB rejects as a noop-error, so both arms failed, returned nothing, and compared "equal" — a false GREEN on a real bug. The LLM derivation of O2' did not foresee this; execution caught it in one run. The repair added a clause to O2' (a failed or empty arm is INVALID, never "equal") and a rule for every harness: trigger-evidence goes all the way down — the code that checks an oracle must itself prove its arms ran, or it will certify what it never tested. This is the whole thesis in miniature: reasoning proposes, execution disposes.

## Example: id30010 InfoSchema LIKE Extractor

```text
P_check:
  InfoSchema extractor recognizes TABLE_NAME LIKE 'a_%'
  and converts it into a prefilter.

Q_claim:
  Every table name accepted by the prefilter satisfies the SQL-visible LIKE predicate.

D_dims:
  collation, case sensitivity, LIKE wildcard semantics,
  whether the original predicate remains as a scalar filter.

F_effect:
  executor enumerates objects from the extractor result,
  while the normal scalar predicate may be removed from remained.

O_oracle:
  ordinary WHERE result must equal explicit re-check / CASE-wrapped result.

R_redflag:
  TABLE_NAME has utf8mb4_bin collation,
  object name is mixed-case,
  extractor lowercases or compiles a case-insensitive regexp.

S_selector:
  custom system-table extractor/cache/shortcut
  + string/collation semantics
  + original predicate bypassed
  + CASE-wrapped or no-shortcut oracle.
```

The lesson is not "fuzz more information_schema LIKE patterns". The lesson is that shortcut paths that prefilter SQL-visible rows must preserve every semantic dimension of the predicate, or keep a safe scalar re-check.

## Example: DDL Owner / Rollback Path

For DDL, the same structure applies:

```text
P_check:
  Success path records enough metadata to clean a global index.

Q_claim:
  Rollback path reconstructs equivalent cleanup metadata.

D_dims:
  table ID vs partition ID, global vs local index bit,
  temp/origin index IDs, rollback/cancel state.

F_effect:
  delete-range cleanup trusts reconstructed metadata.

O_oracle:
  schema says index is gone,
  but raw/index rowset and delete-range coverage prove whether KV was actually cleaned.

R_redflag:
  common success path is green,
  sibling rollback path rebuilds args and can drop owner/type bits.

S_selector:
  common path green + sibling rollback/cancel path reconstructs state
  + cleanup later trusts reconstructed bit
  + cheap consequence oracle exists.
```

This is why id30009 is a high-quality DDL signal: the bug is in a sibling path, not in the obvious happy path.

## Lane Rules

Default lane remains DDL-owner focused:

```text
DDL changes an object
→ all maintained references must be rewritten, blocked, or cleaned
→ consequence oracle verifies the visible result
```

A non-DDL pivot is allowed only when it follows the same proof-obligation discipline:

```text
custom extractor/cache/shortcut
→ identify its SQL-visible semantic obligation
→ compare shortcut path with CASE-wrapped/no-shortcut/safe path
→ stop on first red cell and extract selector
```

Do not drift into broad optimizer/executor fuzzing merely because an interesting non-DDL bug appears.

## Practical Checklist

Before a probe:

- Can I write P/Q/D/F in one screen?
- Which battery families are relevant, and which do the existing tests never exercise?
- Which hidden semantic dimension is most likely to break Q?
- What exact safe path or reference path exists?
- Is the oracle behavioral and low-noise?
- Can the first matrix stay under roughly 5-30 cells?
- If it hits, what selector would it teach?

After a probe:

- Did it find a red cell, or did it calibrate a target as green?
- Does every green cell carry trigger evidence, or are some cells invalid?
- Did any skipped cell reflect capability/version, not product behavior?
- Did the nominating selector's ledger entry get an outcome?
- Should this family pause for fix direction instead of expanding?
- If this is a helper/mechanism-level bug, has a second-owner representative already proven the blast radius?

## Final Rule

```text
A good AI bug-hunting campaign should produce either:
  1. a confirmed bug,
  2. a stronger selector,
  3. or a justified downweighting of a target family.

If it produces only more random cases, the method has drifted.
A green calibration counts only when trigger evidence shows
the fast path actually fired.
```

## Running This Unattended

Methodology-v2 defines the moves; running them as an unattended, self-iterating loop is specified in `ai-native-autonomous-loop.md`. Its one load-bearing idea: **judgment health is scheduled before discovery** — a loop that keeps mining with a REFUTED or unverified oracle just manufactures wrong verdicts faster, so the controller fixes/verifies instruments (P0-P3) before mining bugs (P4). The whole oracle-verification apparatus in this document is what makes an unattended verdict believable; it is the prerequisite for automation, not a detour.

## Appendix: D_dims Battery

Walk this battery for every card and mark each family relevant or irrelevant; an unmarked family is the most common source of missed red flags. The battery is a living asset: every hit must append the dimension that made it possible, with the bug id as provenance. Seed entries below come from real hits and negative screens.

Value semantics:

- collation, case sensitivity, unicode normalization
- NULL and three-valued logic: `NOT` / `OR` / `IS TRUE` / null-safe equality
- type coercion, signed/unsigned boundaries, overflow, float vs decimal
- time: time zones, DST, zero or invalid dates, historical offsets
- request context vs row-render context: if a shortcut requests backend rows using a session context, the SQL-visible rows it constructs must use the same context before dropping scalar recheck; non-mutating conversion helpers whose return value is ignored are a high-signal red flag (id30023)
- precision-lowering: SQL predicate has higher precision than the shortcut/backend request (for example DATETIME(6) equality lowered to millisecond log search); safe only if scalar recheck remains (id30015)
- string edges: empty string vs NULL, escaping, wildcards, trailing spaces

Binding semantics:

- sibling asymmetry: N sibling code paths pass one context/flag, a lone sibling passes another (e.g. session zone vs server-local zone, validated vs unvalidated) — a high-signal source-level red flag for extractor/shortcut families (id30012)
- ID-bound vs name-bound references
- owner/container keys that can change while the object ID stays stable (move, rename, cross-container transfer)
- same-name recreation inheriting stale side state
- identifier case sensitivity differing across lookup paths

State and lifecycle:

- success path vs rollback/cancel/retry siblings
- recorded metadata vs reconstructed metadata (bits lost during reconstruction)
- async or deferred cleanup vs immediately visible state
- cache/version invalidation ordering vs storage update
- session-local state merged into public output surfaces

Reuse:

- cache payload purity: a complete cache key proves only stable inputs; cached results must also be pure with respect to that key. If the value stores evaluated expressions, include volatility/session/time side effects in `D_dims` and require a cache-disabled oracle (id30020)
- semantic-switch coverage: if a session/config variable is read while building a cached object
  (expression construction, rewrite, plan generation), it must be part of the cache key or force a
  rebuild. id30024 showed `tidb_sysdate_is_now` changing `sysdate()` into `now()` during expression
  construction while the prepared plan cache key omitted that switch.
- coarse-key sufficiency: if the cache key stores only an approximation of a semantic dimension,
  prove that the missing detail cannot affect any cached/folded value. id30025 showed that using
  only the current timezone offset is not enough for `UNIX_TIMESTAMP(datetime literal)`: two zones
  can share today's offset but differ for the literal's historical date.
- green calibration rule for cache keys: an omitted or coarse key dimension is not a bug by itself.
  If the cache-hit path rebuilds the relevant semantic boundary under current context, the cell is
  GREEN. The timezone/DST plan-cache probe hit cache across same-offset zones but rebuilt the
  TIMESTAMP range correctly; the follow-up `UNIX_TIMESTAMP` literal case was RED because the
  timezone-dependent value was folded into the cached object.
- plan/result cache parameter drift: proof true for the first parameter, false for a later one
- prepared/preprocess semantic freeze: if `PREPARE` runs a validator/preprocessor that consumes a
  session variable, `EXECUTE` must either revalidate affected AST semantics under the current
  session or the product contract must explicitly freeze those semantics at prepare time.
  Use direct current-session SQL as the reference, then add plan-cache flush/off-cache controls.
  id30028 showed `tidb_enable_noop_functions=OFF` rejecting direct SQL while prepared statements
  created under ON continued to execute under OFF even after `ADMIN FLUSH SESSION PLAN_CACHE`.
  id30029 adds a candidate-only sub-shape: preprocessor validation can mutate the stored AST
  itself, such as non-strict VARCHAR auto-conversion to TEXT before later strict EXECUTE. Treat
  AST-mutation cases as contract-sensitive unless direct current-session semantics are clearly
  authoritative.
- prepared statements, connection state, pooled session leakage

Backend/error domains:

- backend object-not-found vs SQL predicate empty rowset: delegated lookup APIs may return errors for missing objects, but SQL filters usually treat missing ids as no matching rows; an `IN` list with one missing id must not abort valid ids (id30022)

Temporal ("when", not just "what"):

- interval rows vs point ranges: if a row represents `[begin,end]`, a shortcut range with `start > end` may still correspond to a satisfiable containment predicate (`begin <= A AND end >= B` with `A < B`); prove original predicate unsatisfiability before using skip (id30021)
- intermediate substates pinned via injection points
- concurrent operations inside a pinned window vs a no-concurrency baseline
- ordering across multi-step schema or state changes
- partial failure before vs after the commit/visibility point

Environment:

- capability/version differences (classify as skipped, never as red)
- config toggles and feature flags that select a different code path

## Latest Calibration: Equality Is Not Identity

id600001 adds a compact rule to the "P implies Q" audit:

```text
P_check:  target key exists, payload/raw bytes are equal
Q_claim:  this must be the same logical object/row, already handled
fast path: skip repair/write
missing D: source owner/container/partition identity
oracle:   row-multiset or cardinality preservation
```

The important move was to attack the proof relation, not the syntax surface. Source already named
the hard case: duplicate `_tidb_rowid` can exist across partitions after `EXCHANGE PARTITION`.
The code's repair path handled "same target key, different bytes" by regenerating rowid, but
treated "same target key, same bytes" as idempotent. The adversarial matrix was therefore only:

```text
same rowid / different row bytes -> should repair
different rowid / same row bytes -> should preserve
same rowid / same row bytes from different source partitions -> should preserve
```

The last cell was red: `REORGANIZE PARTITION` changed `COUNT(*)` from 2 to 1.

Method improvement:

- When code uses equality as a proof of identity, explicitly list the dimensions that define
  identity in the product model.
- If any owner/container/source dimension is omitted, build the smallest matrix that holds all
  checked dimensions constant while changing only the omitted dimension.
- Use a consequence oracle that sees duplicates. For row-level DDL, always include `COUNT(*)`
  because row projection alone can hide duplicate values.
- After one red cell and two guard cells, stop. Further syntax enumeration is lower value than
  extracting the reusable selector and finding a different equality-as-identity fast path.

## Latest Calibration: Validation Metric Must Match The Contract

id630001, id630002, and id630003 add a second compact DDL rule:

```text
P_check:  DDL validator says no existing row / target-state transition violates the contract
Q_claim:  the target type or target metadata relation is valid
fast path: publish metadata, or reject before safe conversion/target-state validation
missing D: the validator's metric differs from the target contract's metric
oracle:   direct target-type / target-schema acceptance reference
```

The source clue was `LENGTH(col) > newFlen` in the `MODIFY COLUMN` no-reorg-with-check path. The
critical question was not "which string type should we fuzz"; it was "what unit does this proof
measure?" For utf8mb4 `CHAR`/`VARCHAR`, `newFlen` is a character-count limit, while `LENGTH` is a
byte-count function.

The matrix was therefore tiny:

```text
direct varchar(3)/char(3) accepts `中中中` -> target contract says valid
varchar(4)/char(4) containing `中中中` -> MODIFY to length 3 should be valid
ASCII `abc` -> control where bytes == characters
```

The multibyte cells were red: direct target insert succeeded, but `ALTER TABLE ... MODIFY COLUMN`
failed with `ERROR 1265`.

id630002 then generalized the rule from value-fit checks to target-state validators. FK creation
accepts parent/child `VARCHAR` columns with different lengths when type, charset, and collation
match. But FK modify validation used stricter transition inequalities:

```text
newFlen >= originalFlen
newFlen >= relatedFlen
```

That made valid target schemas unreachable by `ALTER TABLE ... MODIFY COLUMN`: child
`varchar(20)->varchar(10/15)` referencing parent `varchar(10)`, and parent
`varchar(10)->varchar(15)` referenced by child `varchar(20)`.

id630003 kept S10 inside DDL but moved to a different validator owner. Partition-column modify
validation had a target-state validator (`buildPartitionDefinitionsInfo` against the new column),
but an earlier allowlist only admitted string length extension:

```text
newFlen > oldFlen
```

That made safe shrink unreachable even when the direct target partitioned schema was valid and
all rows/literals fit the target `varchar(5)`. The useful matrix was again small:

```text
direct LIST/RANGE/KEY partition schema with varchar(5) -> GREEN
non-partition varchar(6)->varchar(5), max CHAR_LENGTH=3 -> GREEN
partition varchar(6)->varchar(5), literals/data length 3 -> RED
partition varchar(6)->varchar(7) -> GREEN checker-aligned control
```

id630023 used the same selector but changed the dimension from length to nullability. Source
inspection showed that `checkPartitionColumnModifiable` rejects flag changes before the generic
`checkForNullValue` path, and `isAllowedPartitionColumnFlagChange` allows `NOT NULL -> NULL` but
not `NULL -> NOT NULL`. The small matrix was:

```text
direct partition schema with a NOT NULL partition column -> GREEN
non-partition NULL -> NOT NULL, no NULL rows -> GREEN
partition NULL -> NOT NULL, no NULL rows -> RED, ERROR 8200
non-partition NULL -> NOT NULL, NULL row present -> GREEN unsafe-data reject control
```

This is a useful improvement because the checked dimension changed:

```text
old S10: value length metric vs target type contract
new S10: column flag allowlist vs target state plus data-fit contract
```

The lesson is that a transition allowlist may accidentally classify a data invariant as a
structural/placement invariant. For `NULL -> NOT NULL`, the safe proof is not "partition columns
must not change flags"; it is "no existing row has NULL", which the non-partitioned path already
knows how to check.

Method improvement:

- For every DDL precheck, write down both the checked metric and the target contract metric.
- Treat unit conversions as first-class `D_dims`: bytes vs characters, display width vs range,
  encoded key bytes vs SQL value, collation weight vs visible equality, restored-data form vs raw
  storage bytes.
- For target-state validators, compare transition validation against the sibling create/add
  validator for the exact final schema. If direct `CREATE TABLE` / `ADD FOREIGN KEY` accepts the
  target relation, `MODIFY` needs an explicit data-safety reason to reject the transition.
- For partition-column validators, add a third comparison point: the direct target partition
  definition and the generic data-fit contract. A blanket transition allowlist is suspect when
  the final partition literals and existing rows fit the shorter target type, or when a flag
  change such as `NULL -> NOT NULL` has an existing row-data validator.
- Prefer target-type acceptance, target-schema acceptance, or safe-path conversion as the oracle.
  If the target schema itself accepts a value or metadata relation, a pure fit-check should not
  reject it unless the product has an explicit stricter DDL contract.
- Stop after one red family plus controls. More charset, FK type-pair, or partition/string variant
  enumeration is lower value than searching for a different validation metric mismatch.

## Latest Calibration: Idempotence Requires Early Existence Classification

id630015 and id630016 sharpen the DDL idempotence selector. The important question is not only
whether the parser accepts `IF EXISTS` / `IF NOT EXISTS`, or whether the final executor branch
reads the flag. The question is:

```text
before the duplicate/missing-object catch runs,
can any earlier precheck return an error that would be irrelevant if the object is missing or
already present?
```

id630015 was the raw-count version:

```text
DROP PARTITION IF EXISTS px
P_check:  requested-name count >= current partition count
missing D: requested names may not exist and should be classified before counting removals
RED:      one-partition table, missing px -> ERROR 1508 instead of note/no-op
```

id630016 is the capability-gate version:

```text
ADD PARTITION IF NOT EXISTS p0
P_check:  LIST table already has DEFAULT partition, so ADD LIST PARTITION is unsupported
missing D: p0 may already exist, so the ADD is an idempotent duplicate rather than a real ADD
RED:      LIST table with DEFAULT, duplicate p0 -> ERROR 8200 instead of Note 1517
```

The efficient matrix is deliberately small:

```text
requested object: duplicate / new
capability gate:  absent / present
```

Method improvement:

- For idempotent DDL, draw the validation order before running SQL.
- Mark the first point where the code classifies requested objects as existing, missing, or
  duplicate.
- Every precheck before that point must be audited: is it still meaningful when the requested
  object is absent or already present?
- Controls must include both sides of the gate: duplicate with no gate, duplicate with gate, new
  object with gate, and new object without gate.
- Stop after a new ordering sub-shape is proven. More partition syntax is less valuable than
  searching for another owner where existence classification is delayed behind an unrelated gate.

id630017 adds the special-name version:

```text
DROP INDEX IF EXISTS `PRIMARY`
P_check:  indexName is PRIMARY, so run the primary-key helper
missing D: on a table with no primary key, the requested index is absent and IF EXISTS should
           classify it as a no-op
RED:      no-primary-key table, missing `PRIMARY` -> ERROR 1091 instead of Note 1091
```

This improved the selector again:

- For `IF EXISTS`, search not only raw-count and capability gates, but also special-name helpers
  such as `PRIMARY` that run before the generic missing-object catch.
- The compact matrix is: ordinary missing object, special missing object, and real special object.
  This distinguishes "special object cannot be dropped" from "special missing object bypasses the
  idempotence safe path."

id630018 adds the create-target version:

```text
CREATE TABLE IF NOT EXISTS t LIKE missing_src
P_check:  source/candidate table definition must be valid before a create-table job is built
missing D: if target t already exists, the candidate definition/source will be discarded
RED:      target exists + missing LIKE source / invalid candidate index / invalid partition
          expression -> hard error instead of Note 1050
```

The improved S15 audit now has two classification points:

- For `IF EXISTS` / `IF NOT EXISTS` over existing child objects, mark the first duplicate/missing
  object classifier.
- For `CREATE ... IF NOT EXISTS`, mark the first target-exists classifier. Candidate-source and
  candidate-metadata validation before that point is suspect because the candidate may never be
  used.

The compact matrix is: target exists plus valid candidate, target exists plus invalid candidate,
and target absent plus invalid candidate.

id630019 validates that this is not a CREATE TABLE-specific issue:

```text
CREATE SEQUENCE IF NOT EXISTS seq INCREMENT 0
P_check:  candidate sequence options must validate before a create-sequence job is built
missing D: if target seq already exists, candidate options are discarded
RED:      target sequence exists + invalid increment/range/table option -> hard error instead of
          Note 1050
```

So the improved create-like selector is:

```text
find CREATE-like owner
locate first target-exists classifier
audit every candidate builder / source resolver / option validator before that classifier
matrix = target-exists valid candidate / target-exists invalid candidate / target-absent invalid
```

id630020 extends the same selector beyond the shared table/sequence create path:

```text
CREATE RESOURCE GROUP IF NOT EXISTS ai_rg_s15 BACKGROUND=()
P_check:  candidate resource-group options are built before a create-resource-group job is built
missing D: if target ai_rg_s15 already exists, candidate options are discarded
RED:      target resource group exists + BACKGROUND=() on non-default group -> ERROR 1105 instead
          of Note 8248
```

The key refinement is that a candidate builder can be a semantic validator:

```text
buildResourceGroup(...)
  -> SetDirectResourceGroupSettings(BACKGROUND)
  -> returns unsupported-operation error
ResourceGroupByName(...)
  -> target-exists classifier, reached too late
```

So the improved create-like selector is now:

```text
find DDL owner with IF NOT EXISTS
confirm grammar/AST really promises idempotence
locate first target-exists classifier
audit every candidate resolver / builder / option setter / semantic validator before it
matrix = target-exists valid candidate / target-exists invalid candidate / target-absent invalid
```

Negative calibration from this pass matters:

- `CREATE VIEW IF NOT EXISTS` is not a bug in this build because the grammar does not accept that
  syntax.
- `CREATE PLACEMENT POLICY IF NOT EXISTS` is a green control for the obvious invalid-option shape:
  the target existence check happens before `checkPolicyValidation`.

id630021 extends the same selector to a policy owner with expression validation:

```text
CREATE MASKING POLICY IF NOT EXISTS p_mp ON t(a) AS b
P_check:  candidate masking expression references only the target column
missing D: if p_mp already exists on the same t(a), candidate expression is discarded
RED:      existing p_mp on t(a) + AS b -> ERROR 8275 instead of Note/no-op
```

This adds one more audit detail:

```text
for policy-like owners, define object identity before probing
same name + same table + same column + only candidate payload changes
```

Without that identity pin, an invalid candidate may be a different target rather than an unused
definition. With it, the three-cell matrix stays low-noise:

```text
existing same target + valid payload
existing same target + invalid payload
absent target + same invalid payload
```

id630022 adds the common top-level index owner:

```text
CREATE SPATIAL INDEX IF NOT EXISTS idx_a ON t(a)
P_check:  requested index type is supported
missing D: if idx_a already exists on t, candidate index type is discarded
RED:      existing idx_a + SPATIAL -> ERROR 8200 instead of Note 1061
```

This is the same capability-gate shape as id630016, but on a different owner and with a much
shorter source pattern:

```text
createIndex
  switch keyType
    SPATIAL -> unsupported
  checkIndexNameAndColumns
    FindIndexByName
    IF NOT EXISTS -> note/no-op
```

The useful method refinement is to inspect early `switch`/capability gates before assuming the
interesting validators are deeper helper calls.

Negative calibration:

- `CREATE DATABASE IF NOT EXISTS` is green in source order because `CreateSchemaWithInfo` checks
  schema existence before charset/collation and placement validation.
- Do not enumerate `FULLTEXT`, `VECTOR`, columnar, or index options from id630022; the method
  result is the owner/path ordering, not index-type blast radius.

id1020001 extends S15 to account DDL, but only under a stricter identity-pinning rule:

```text
CREATE USER IF NOT EXISTS ''@'ai_s15_host' PASSWORD EXPIRE
P_check:  PASSWORD EXPIRE is invalid for anonymous users
missing D: the exact same user identity (username+host) already exists, so candidate account
           attributes should be discarded
RED:      existing ''@'ai_s15_host' + PASSWORD EXPIRE -> ERROR 3016 instead of Note 3163
```

Source pattern:

```text
executeCreateUser
  plOptions.loadOptions
  if empty username && passwordExpired == "Y" -> ErrPasswordExpireAnonymousUser
  userExists
  IF NOT EXISTS -> note/no-op
```

The method refinement is that account or policy owners need explicit identity pinning before the
candidate-invalid cell is meaningful. For masking policy that meant same name/table/column; for
`CREATE USER` it means same username+host. Without that pin, an invalid candidate might simply be a
different object or an invalid target identity.

Negative calibration:

- `ALTER SEQUENCE IF EXISTS` is green because it checks target sequence existence before
  `alterSequenceOptions`.
- `ALTER RESOURCE GROUP IF EXISTS` is green because it checks group existence before
  `buildResourceGroup` and `checkResourceGroupValidation`.
- Do not enumerate account options from id1020001; reopen only if another account-DDL path has the
  same identity-pinned ordering proof.

## Latest Calibration: Dependency Existence Is Not A Semantic-Change Proof

id630004 adds a third compact DDL rule:

```text
P_check:  object A is referenced by object B
Q_claim:  every DDL operation touching A can invalidate B
fast path: reject before classifying whether the operation changes B's semantic dependency
missing D: metadata-only changes do not affect the dependency expression, column name, or value type
oracle:   direct target-schema acceptance plus dependency behavior reference
```

The source clue was not a length metric. It was a dependency error computed once and used in two
different ways:

```text
rename path:
  if name changes and generated column depends on old name -> reject

common modify path:
  if generated column depends on the column -> reject every MODIFY
```

The matrix was tiny:

```text
direct target: a int COMMENT 'x', b generated as (a+1) -> GREEN
ALTER target:  a int -> a int COMMENT 'x' with same generated b -> RED
direct target: a int DEFAULT 5, b generated as (a+1), insert default -> 5,6 GREEN
ALTER target:  a int -> a int DEFAULT 5 with same generated b -> RED
non-dependent column comment -> GREEN
generated column's own comment -> GREEN
true base-column type change -> GREEN reject
```

Method improvement:

- Treat dependency checks as two-stage proofs: first "does a dependency exist?", then "does this
  operation change anything the dependency relies on?"
- For false-reject hunting, hold the dependency graph constant and vary only operation semantics:
  comment/default/nullability vs rename/type/expression/value rewrite.
- The oracle must exercise the dependency, not only inspect `SHOW CREATE TABLE`. For generated
  columns, insert/select the generated value in the direct target reference.
- Stop after the first metadata-only family plus controls. Further generated expression syntax is
  lower value than finding another overbroad dependency gate.

## Latest Calibration: Target Reconstruction Needs Ownership And Runtime-State Proof

id630005 and id1200001 add a fourth compact DDL rule:

```text
P_check:  source metadata is copied to build target metadata
Q_claim:  target-only normalization and copied target fields are safe for a new object
fast path: shallow-copy a top-level metadata struct, then selectively mutate/reset fields
missing D1: nested slices/maps/pointers may still be shared with the source object
missing D2: remaining copied fields may be runtime state, not schema definition
oracle:   source/target isolation, plus target-only behavior cleanup when runtime state is copied
```

The source clue was not a validator predicate. It was a construction pattern:

```text
tblInfo := *referTblInfo
renameCheckConstraint(&tblInfo)
```

That reads like a copy, but it is only a top-level copy. The CHECK constraints live behind a slice
of pointers, so target-only renaming mutates source-owned `ConstraintInfo` objects too.

id1200001 adds the sibling target-state version:

```text
tblInfo := *referTblInfo
... reset ForeignKeys/cache/TiFlash/TTL/affinity ...
// no tblInfo.Lock reset
```

The reset list is itself a proof artifact: the code knows some copied fields are invalid for the
new target. The high-yield question is then, "which remaining fields are not schema definition?"
`TableInfo.Lock` is runtime state, so `CREATE TABLE dst LIKE src` from a `READ ONLY` source can
publish a new `dst` that is also read-only. The strong oracle is target-only cleanup:
`ALTER TABLE dst READ WRITE` makes `dst` writable while `src` remains read-only.

The useful matrix stayed small:

```text
direct d1/d2 with anonymous CHECK constraints -> GREEN, independent names
src before LIKE -> src_auto_chk_1
CREATE TABLE dst_auto LIKE src_auto
src after LIKE, same and new connection -> RED, dst_auto_chk_1
runtime CHECK violation on src -> RED, error names dst_auto_chk_1
information_schema cross-check -> RED, surfaces disagree
```

Method improvement:

- Add "nested ownership proof" as a first-class DDL proof obligation for clone/rebuild paths.
- When source metadata is used to build target metadata, list every pointer-backed nested owner:
  constraints, indexes, foreign keys, TTL, affinity, placement, partition definitions, and cached
  side metadata.
- The oracle must inspect the **source** object after creating the target. Looking only at the
  target proves construction, not isolation.
- Include a direct sibling-create control so normal target renaming is not mistaken for source
  mutation.
- Stop after one owner with a source mutation. More LIKE options or CHECK expression syntax is less
  valuable than finding another clone/rebuild path with a different nested owner.

## Latest Calibration: Recovery Must Re-Prove Current Namespace Invariants

id630006 adds a fifth compact DDL rule:

```text
P_check:  recovered table name and table ID are free
Q_claim:  restored TableInfo can be published in the current schema
fast path: re-materialize old metadata after only container/object identity checks
missing D: schema-level namespaces/references may have changed since the drop snapshot
oracle:   schema-level namespace uniqueness plus normal create/add validator controls
```

The source clue came from comparing sibling entrypoints:

```text
CREATE TABLE:
  check table name
  check CHECK constraint names in schema

ADD CHECK:
  check CHECK constraint name in schema

RECOVER / FLASHBACK TABLE:
  check table name
  check table ID
  publish recovered TableInfo
```

That immediately made the test matrix tiny:

```text
normal CREATE duplicate CHECK name -> GREEN reject, ERROR 3822
create f(a CHECK a>0), drop f, recreate f(a CHECK a>1)
FLASHBACK TABLE f TO f_old -> RED, two public f_chk_1 constraints
violating inserts on both tables -> RED, both errors name f_chk_1
CREATE TABLE like_copy LIKE f -> GREEN, like_copy_chk_1
```

Method improvement:

- For every recovery/flashback/import path, list the validators used by the normal create/add path
  for the same metadata owner.
- Do not treat "valid in the old snapshot" as "valid in the current schema". Namespaces,
  references, side tables, and external rules can be occupied or deleted between drop and recover.
- The oracle should inspect the **current schema-level surface**, not just the recovered table.
  For CHECK constraints, `information_schema.check_constraints` duplicate-name grouping is stronger
  than a single-table `SHOW CREATE`.
- Use normal create/add rejection as the green control. If the sibling path rejects duplicates but
  recovery publishes them, the proof gap is real.
- Stop after one namespace owner plus controls. More flashback fields are lower value than finding
  another create/add validator that recovery skips.

### Identity drift is a separate recovery dimension

The next recovery probe reused the `FLASHBACK TABLE` foreign-key oracle but changed only one hidden
input: the referenced parent went from **absent** to **a different empty object with the same name**.
That produced a stronger consequence than the missing-parent case: the recovered child already
contained a row that was valid in the old snapshot but was orphaned against the current parent.

The reusable matrix is:

```text
old child + parent absent                 -> future FK check may be skipped
old child + same-name parent, same row    -> GREEN reference continuity control
old child + same-name parent, empty       -> RED existing orphan after recovery
old child + same-name parent, bad schema  -> RED invalid target schema publication
FLASHBACK DATABASE with both objects       -> GREEN container-level control
```

This adds an explicit identity-drift gate to the recovery selector:

```text
P_check:  historical object A referenced object B
Q_claim:  restoring A against current name B preserves the old reference contract
D_dims:   current object identity, schema, columns, indexes, and current rowset
F_effect: old metadata and rows are published after only name/ID availability checks
O_oracle: existing-row reference differential + future-DML control + ADMIN CHECK
```

Method rules:

- Reuse a strong prior oracle, then mutate one semantic identity dimension at a time; this is
  asset reuse, not restarting a broad FK fuzz campaign.
- Check existing recovered rows before checking only future writes. A future-DML green result can
  coexist with a historical orphan already published by recovery.
- Treat `same name` as a candidate identity collision, not proof of object continuity. If the
  metadata model stores names but not referenced object identity, require either an explicit
  revalidation/reconciliation step or a recovery refusal.
- Account the result as a new root only when the fix locus or product contract is independent.
  Otherwise record it as a stronger identity-drift surface under the existing recovery selector.

Consequence escalation rule:

- Once an identity-drift RED proves that a recovered reference is rebound, run one normal
  downstream action owned by the current referenced object. For a foreign key, an
  `ON DELETE CASCADE` parent delete is a stronger oracle than another `LEFT JOIN`: it distinguishes
  a visible orphan from direct loss of recovered child data. Keep the action under the same root
  unless the fix locus changes; consequence escalation improves severity evidence, not root count.

## Latest Calibration: Cross-Owner Hits Need Root-Cause Accounting

id630007 reuses S11 on expression indexes:

```text
P_check:  base column is referenced by a hidden generated column backing an expression index
Q_claim:  every MODIFY COLUMN on the base column is unsafe for the expression index
fast path: common MODIFY path rejects any dependency error after type checks
missing D: COMMENT/DEFAULT do not rename the column, change the expression, or change the type
oracle:   direct target expression-index schema + indexed query / ADMIN CHECK
```

The useful matrix was the same shape as id630004:

```text
direct expression-index schema with a COMMENT on the base column -> GREEN
ALTER base column COMMENT under existing expression index -> RED
direct expression-index schema with DEFAULT 5 -> GREEN, default insert gives 5/6
ALTER base column DEFAULT 5 under existing expression index -> RED
non-dependent column COMMENT -> GREEN
DROP INDEX then base-column COMMENT -> GREEN
true base-column type change -> GREEN reject
```

Method improvement:

- A selector can be useful even when the second hit shares the first hit's root cause. That proves
  blast radius and feature-surface impact, but it should not be counted as a fresh root-cause
  family.
- For cross-owner S11 probes, keep the matrix isomorphic: direct target reference, metadata-only
  ALTER red cell, dependency-absent green, dependency-removed green, and true semantic-change
  reject.
- The oracle must exercise the owner. For expression indexes, `ADMIN CHECK TABLE` or an indexed
  expression query is stronger than merely showing the table DDL.
- After a second owner with the same code gate, stop. The next valuable S11 target is a different
  dependency code path or a silent wrong-acceptance, not more expression-index syntax.

id630009 reuses S11 on a different dependency checker: partial-index condition columns.

```text
P_check:  column b is referenced by a partial-index condition
Q_claim:  every MODIFY COLUMN on b is unsafe for the partial index
fast path: common MODIFY path rejects any partial-condition dependency before classifying the change
missing D: COMMENT/DEFAULT do not rename b, change its type/collation/nullability, or change the
           condition expression
oracle:   direct target partial-index schema + behavior query + ADMIN CHECK
```

This hit changes the accounting: S11 is no longer just "generated-column dependency checker is too
broad". It is a broader proof mistake that can occur in independent dependency gates. The improved
selector should therefore look for code that answers "is referenced" and immediately feeds a
blanket DROP/CHANGE/MODIFY rejection, then use metadata-only DDL as the first counterexample.

New stop rule:

- After id630009, do not enumerate partial-index predicate syntax. The valuable next S11 target is
  a silent wrong-acceptance, a different dependency checker, or fix validation across COMMENT,
  DEFAULT, rename, drop, type, collation, and nullability.

## Latest Calibration: Parser Flags Are Proof Obligations

id630008 adds an idempotence-flag rule:

```text
P_check:  parser accepts IF NOT EXISTS on ADD FOREIGN KEY
Q_claim:  duplicate existing FK should be treated through idempotent semantics
fast path: sibling dispatch drops the flag before CreateForeignKey
missing D: FK branch must distinguish "duplicate with flag" from "duplicate without flag"
oracle:   flagged duplicate vs unflagged duplicate plus sibling ADD INDEX IF NOT EXISTS
```

The source clue was a sibling branch comparison:

```text
ADD INDEX:
  createIndex(..., constr.IfNotExists)

ADD COLUMNAR INDEX:
  createColumnarIndex(..., constr.IfNotExists)

ADD FOREIGN KEY:
  comment says IF NOT EXISTS is ignored
  CreateForeignKey(...)
```

The useful matrix was tiny:

```text
first ADD FOREIGN KEY IF NOT EXISTS -> GREEN, FK row exists once
second same ADD FOREIGN KEY IF NOT EXISTS -> RED, ERROR 1826
plain duplicate ADD FOREIGN KEY -> GREEN reject
ADD INDEX IF NOT EXISTS duplicate -> GREEN note/no-op
schema count after red -> one FK row, so wrong-error not duplicate-write
```

Method improvement:

- Treat grammar flags as first-class proof obligations. If the parser stores a bit, every sibling
  execution owner must either honor it or explicitly reject the syntax.
- For idempotence bugs, pair the flagged duplicate with an unflagged duplicate control. The bug is
  not "duplicate rejected"; it is "duplicate rejected despite the idempotence flag".
- Include a schema-count oracle after failure/success. This separates wrong-error from the more
  serious silent duplicate-write class.
- Negative calibration matters: FK names are table-local enough that cross-table duplicate FK names
  are allowed here, so FK-name recovery is not a schema-namespace bug; `DROP FOREIGN KEY IF EXISTS`
  is not parser-accepted here, so it is outside this proof.

id630010 sharpens the same rule from "executor branch forgot a flag" to "flag ownership can be lost
during parser/spec splitting":

```text
P_check:  parser accepts ADD IF NOT EXISTS (...) and records spec.IfNotExists
Q_claim:  duplicate table elements produced by that accepted list should use idempotent semantics
fast path: ResolveAlterTableSpec splits NewConstraints into AlterTableAddConstraint children
missing D: the child owner reads constr.IfNotExists, while the parent flag remains on spec
oracle:   outer KEY red vs outer column green vs inner KEY IF NOT EXISTS green + schema counts
```

Method improvement:

- After finding a parser/AST flag, trace every representation change before execution:
  parse node -> resolved spec -> split child specs/jobs -> executor helper args.
- For parent-level flags, write down which child owns the flag after splitting. A copied struct is
  not enough if the child branch later reads a different field.
- Add an "inner spelling" control when available. In id630010, `KEY IF NOT EXISTS` proved the index
  owner can implement idempotence; the bug was specifically the outer spec flag not reaching it.
- Do not overclaim unsupported surfaces. If a constraint kind has no product decision for outer
  `IF NOT EXISTS`, the fix can be either note/no-op or early syntax/resolution rejection; the bug is
  accepting the flag and then behaving like it was absent.

## Latest Calibration: Validators Need Complete Target State

id630011 adds a validator-ordering rule:

```text
P_check:  FK modify validator compares type/flen/decimal before column options are applied
Q_claim:  the final modified column remains compatible with existing FK actions
fast path: unchanged type/flen/decimal returns nil, then NOT NULL is added later
missing D: nullability is a required compatibility dimension for SET NULL referential actions
oracle:   direct target-state rejection plus runtime SET NULL consequence
```

The source clue was the order:

```text
checkModifyColumnWithForeignKeyConstraint(...)
ProcessModifyColumnOptions(...)
```

That order is harmless only if later options cannot affect the validator's claim. Here they can:
`ColumnOptionNotNull` is applied after the FK validator, while CREATE/ADD FK already rejects
`NOT NULL` child columns for `ON DELETE SET NULL` or `ON UPDATE SET NULL`.

The useful matrix stayed small:

```text
direct NOT NULL child + ON DELETE SET NULL -> GREEN reject, ERROR 1830
direct NOT NULL child + ON UPDATE SET NULL -> GREEN reject, ERROR 1830
nullable child + ON DELETE SET NULL, then MODIFY NOT NULL -> RED accept
nullable child + ON UPDATE SET NULL, then MODIFY NOT NULL -> RED accept
parent DELETE/UPDATE after red ALTER -> RED, ERROR 1048
nullable child + ON DELETE RESTRICT, then MODIFY NOT NULL -> GREEN accept
```

Method improvement:

- For each transition validator, ask whether the object being validated is the final target object
  or an intermediate object.
- Build a "post-check mutation list": column options, defaults, generated expressions, charset /
  collation normalization, nullability flags, index flags, partition metadata, and placement/FK
  side metadata applied after the validator.
- Compare with sibling target-state validators such as CREATE TABLE, ADD FOREIGN KEY, ADD INDEX, or
  direct target schema creation. If the sibling rejects the final state, a transition path that
  reaches it needs a behavior oracle.
- Do not stop at "ALTER succeeded". Exercise the missing dimension. For id630011, parent
  DELETE/UPDATE is the behavior oracle because it forces the SET NULL action to touch the new
  NOT NULL column.
- Stop after one validated owner. More FK syntax is lower value than finding another validator that
  checks an incomplete target state.

id630012 refines the same rule from ordering to proof precision:

```text
P_check:  FK modify validator compares type/flen/decimal and returns nil
Q_claim:  the final FK column remains compatible with the related column
fast path: INT -> INT UNSIGNED keeps the coarse tuple equal
missing D: unsigned flag is a required FK compatibility dimension and affects cascade writes
oracle:   direct target-state rejection + signed/signed cascade control + round-trip ADD FK rejection
```

The useful matrix again stayed small:

```text
direct parent INT / child INT UNSIGNED FK -> GREEN reject, ERROR 3780
signed/signed FK, parent UPDATE 1 -> -1 -> GREEN cascade, both rows become -1
signed/signed FK, then child MODIFY INT UNSIGNED -> RED accept
parent UPDATE 1 -> -1 after red ALTER -> RED, ERROR 1264
drop/re-add FK after red ALTER -> GREEN reject, ERROR 3780
```

The same source scan produced two green calibrations:

```text
primary-key column MODIFY ... NULL -> GREEN, later PK/default checks preserve NOT NULL/reject writes
child FK collation change -> GREEN, later indexed-column collation validation blocks the ALTER
```

Method improvement:

- Upgrade S16 from "validator before options" to "coarse P_check trusted as rich Q_claim".
- For every omitted dimension, run a coverage pass before SQL:
  `is this dimension checked later on the complete target state?`
- Red cells need a missing-dimension behavior oracle. For id630012, the behavior is cascade writing
  a negative value into an unsigned child column.
- Green cells are useful only when the trigger fired. The collation probe was useful because the
  FK early-return suspicion existed, but execution proved a later safe validator blocked the final
  invalid state.

## Latest Calibration: Writers Need Safe-Path Invariants Too

id630013 adds a second axis: some proof obligations live in writers, not validators.

```text
P_check:  old rows satisfy CHECK and CastColumnValue succeeds during MODIFY COLUMN reorg
Q_claim:  converted rows still satisfy CHECK under the new column type
fast path: updateColumnWorker casts and writes encoded rows with txn.Set
missing D: successful conversion can change predicate truth value, e.g. 0.40 -> INT 0
oracle:   final predicate projection + ADD CHECK rejection + ordinary DML rejection
```

The source contrast was the clue:

```text
ADD CHECK:            verifyRemainRecordsForCheckConstraint scans existing rows
ordinary DML:         AddRecord/UpdateRecord calls CheckRowConstraint
MODIFY COLUMN reorg:  CastColumnValue -> EncodeRow -> txn.Set
```

The matrix stayed tiny:

```text
DECIMAL(10,2) 0.40 + CHECK(a > 0), then MODIFY a INT -> RED, final a=0 and a>0=0
DOUBLE 0.4 + CHECK(a > 0), then MODIFY a INT       -> RED, final a=0 and a>0=0
VARCHAR '0.4' + CHECK(a > 0), then MODIFY a INT    -> RED, final a=0 and a>0=0
ADD CHECK(a > 0) on INT data containing 0           -> GREEN reject, ERROR 3819
INSERT 0 into altered table                         -> GREEN reject, ERROR 3819
```

Method improvement:

- Treat "bypasses normal safe path" as a first-class selector, not just "validator missing".
- For every raw DDL writer, list the invariants normally enforced by DML or target-state DDL:
  CHECK, partition membership, generated/hidden columns, FK action writes, TTL/masking rules, and
  index/record consistency.
- Generate witnesses by truth-value transition, not by syntax enumeration:
  `old invariant true`, `conversion succeeds`, `new invariant false`.
- Do not use ADMIN CHECK TABLE as the main oracle for logical constraints; it can pass while CHECK
  predicates are false. Project the predicate itself and use ADD CHECK/DML as safe-path references.

## Latest Calibration: Cached Payloads Have Hidden Inputs

id30034 sharpens S7 cache payload purity. The old scan question was:

```text
is the session/config variable in the plan-cache key?
```

The better question is:

```text
is every cached payload a pure function of explicit SQL inputs, cache-key dimensions,
and implicit session/config inputs read during build, rewrite, folding, or boundary setup?
```

The proof card:

```text
P_check:  prepared plan cache says the plan is reusable
Q_claim:  folded constants remain valid under the current session semantics
fast path: cache hit reuses a folded Constant
missing D: WEEK(date) without mode reads @@default_week_format
oracle:   direct-vs-prepared cache hit plus ADMIN FLUSH SESSION PLAN_CACHE reference
```

The matrix stayed small:

```text
default_week_format=0, direct WEEK('2008-02-20') -> 7
default_week_format=1, direct WEEK('2008-02-20') -> 8
prepare under fmt=0, execute -> 7, cache=0
switch fmt=1, execute same prepared stmt -> 7, cache=1 RED
flush session plan cache, execute same prepared stmt -> 8, cache=0 GREEN reference
WHERE WEEK('2008-02-20')=8 after fmt switch -> cached count 0 vs direct count 2 RED
column WEEK(d) under cache hit -> 8 GREEN, boundary rebuilt at execution
explicit WEEK(date,1) under cache hit -> 8 GREEN, semantic input is explicit
```

Two green samples from the same pass matter:

```text
foreign_key_checks x prepared DML -> GREEN
partial-index a > ? eligibility x prepared plan cache -> GREEN
```

They prevent the selector from becoming "any session var read during planning is a bug." The real
red shape is narrower: the cache hit must skip the safe path and reuse an evaluated payload whose
hidden input is absent from the key/deferred-constant model.

Method improvement:

- For every cache candidate, split the suspected state into explicit SQL input, key dimension,
  rebuilt-at-execution boundary, and evaluated payload.
- A missing key dimension is only a bug when the cache hit reuses a payload that depends on it.
- Look for scalar functions that read `EvalContext` or session vars while all visible arguments are
  constants. These are high-value candidates for constant-fold payload bugs.
- Always include a flush/off-cache reference and a non-folded control. Without those, a stale-cache
  claim is too easy to confuse with ordinary function semantics.

id30035 immediately validated the improved selector on a different hidden input:

```text
P_check:  prepared plan cache says the plan is reusable
Q_claim:  folded decimal-division constants remain valid under current session precision
fast path: cache hit reuses folded Constant for 1/7
missing D: division reads @@div_precision_increment
oracle:   direct-vs-prepared cache hit plus ADMIN FLUSH SESSION PLAN_CACHE reference
```

The small matrix:

```text
div_precision_increment=4, direct 1/7 -> 0.1429
div_precision_increment=8, direct 1/7 -> 0.14285714
prepare under dpi=4, SELECT 1/7 -> 0.1429, cache=0
switch dpi=8, same EXECUTE -> 0.1429, cache=1 RED
flush session plan cache, same EXECUTE -> 0.14285714, cache=0 GREEN reference
WHERE CAST(1/7 AS CHAR)='0.142857142' after dpi switch -> cached count 0 vs direct count 2 RED
column a/b under cache hit -> follows dpi=8 GREEN
```

Method improvement after the second hit:

- Promote the search unit from "function" to "hidden context getter".
- For each getter, classify it as key-covered, deferred/rebuilt, execution-only safe, or payload
  folded risk.
- Two hits from two getters are enough to stop ad hoc enumeration and build a getter-level queue.

id30036 validated the next improvement: the payload class can change even when the hidden input is
the same.

```text
P_check:  prepared plan cache says the aggregate plan is reusable
Q_claim:  cached AVG return scale remains valid under current session precision
fast path: cache hit reuses aggregate descriptor / RetTp.Decimal
missing D: AVG type inference reads @@div_precision_increment
oracle:   direct-vs-prepared cache hit plus ADMIN FLUSH SESSION PLAN_CACHE reference
```

The small matrix:

```text
div_precision_increment=4, direct AVG(x) -> 1.5000
div_precision_increment=8, direct AVG(x) -> 1.50000000
prepare under dpi=4, SELECT AVG(x) -> 1.5000, cache=0
switch dpi=8, same EXECUTE -> 1.5000, cache=1 RED
flush session plan cache, same EXECUTE -> 1.50000000, cache=0 GREEN reference
derived COUNT over CAST(AVG(x) AS CHAR)='1.50000000' -> cached count 0 vs direct count 1 RED
```

Why this matters:

- id30035 and id30036 both use `div_precision_increment`, but they are not the same bug shape.
- id30035 is a folded scalar value.
- id30036 is cached type/descriptor metadata.
- The reusable selector is therefore not "try more functions that read this variable"; it is
  "for each hidden getter, find which durable payloads it writes."

Updated S7 workflow:

1. Start from a getter, not a function name.
2. For each consumer, name the payload class: folded scalar, semantic tree, type/descriptor,
   range/request boundary, or execution-only value.
3. Require a direct old/new semantic contract before building a cache matrix.
4. Use `@@last_plan_from_cache=1` plus direct and flush references as the red-cell oracle.
5. After a hit, pause and update the selector; continue only if the next candidate proves a new
   hidden input or a new payload class.

id30037 then validated the getter-level workflow on a new hidden input:

```text
P_check:  prepared plan cache says the expression plan is reusable
Q_claim:  cached _utf8mb4 literal collation remains current
fast path: cache hit reuses literal field-type metadata
missing D: expression rewrite reads @@default_collation_for_utf8mb4
oracle:   direct-vs-prepared cache hit plus ADMIN FLUSH SESSION PLAN_CACHE reference
```

The small matrix:

```text
default_collation_for_utf8mb4=utf8mb4_bin:
  COLLATION(_utf8mb4'A') -> utf8mb4_bin
  _utf8mb4'A' = _utf8mb4'a' -> 0

default_collation_for_utf8mb4=utf8mb4_general_ci:
  COLLATION(_utf8mb4'A') -> utf8mb4_general_ci
  _utf8mb4'A' = _utf8mb4'a' -> 1

prepare under bin, execute -> utf8mb4_bin / 0, cache=0
switch to general_ci, same EXECUTE -> utf8mb4_bin / 0, cache=1 RED
flush session plan cache, same EXECUTE -> utf8mb4_general_ci / 1, cache=0 GREEN reference
WHERE _utf8mb4'A'=_utf8mb4'a' after switch -> cached count 0 vs direct count 2 RED
explicit COLLATE utf8mb4_general_ci -> GREEN
```

Method improvement after id30037:

- Do not mark a semantic area "key-covered" because a nearby variable is in the key.
- Name the exact getter. `collation_connection` and `default_collation_for_utf8mb4` are different
  inputs with different consumers.
- Use a visible introspection scalar such as `COLLATION()` when possible; it gives a compact direct
  contract before the row-count oracle.

Negative calibration after id30037:

```text
@user_var type/collation:
  direct @u bin -> general_ci changed equality 0 -> 1
  prepared statement after switch had @@last_plan_from_cache=0
  result: GREEN/uncached, not a stale-payload bug

RAND():
  prepared statement hit plan cache on repeated EXECUTE
  values still changed across cached executions
  result: GREEN, cache hit did not freeze volatile payload
```

Method refinement:

- Direct old/new semantic drift is only the first gate.
- Cache hit is only the second gate.
- A RED claim requires the conjunction: direct semantics drift, cache hit occurs, cached output
  follows the old payload, and flush/off-cache reference follows current semantics.

## Calibration Rule: Name the Gate a Green Cell Failed

The P/Q/F card is good at creating candidates:

```text
code checked P
system believes Q
fast path F skips or shortens the safe path
```

But it can overclaim if every suspicious shortcut is treated as a bug. After the non-DDL
calibration pass on 2026-07-03, a red cell must pass four gates:

```text
G1 direct contract:
   Can old/new semantics differ without the shortcut?

G2 trigger evidence:
   Did the suspected shortcut actually fire?

G3 wrong reuse / skipped safety:
   Did the shortcut carry old derived state, trust an incomplete predicate, or truly bypass the
   safety owner?

G4 current reference:
   Does a strong reference path, usually flush/no-shortcut/CASE/direct-target, prove the current
   semantics?
```

The method improvement is to record green cells by the gate they failed, not just as "no bug".

```text
windowing_use_high_precision x prepared plan cache:
  G1 passed: direct ON/OFF window SUM over DOUBLE cancellation differed.
  G2 passed: prepared execution hit plan cache after switching the session variable.
  G3 failed: cached execution followed the current OFF aggregate result.
  G4 passed: flush-cache reference matched the current OFF result.
  lesson: cache hit does not imply stale payload.

prepared PointGet x privilege revoke:
  source suspicion: cached point-get has a skipPrivCheck branch because the executor is specially
  handled.
  G2 passed: the prepared point-get had already produced a cache hit before revoke.
  G3 failed: after REVOKE, the same prepared EXECUTE returned ERROR 1142 instead of rows.
  lesson: "skip checker" comments must be tested against the actual safety owner; here execution
  still blocked the user-visible operation.
```

This changes the selector workflow:

- Missing cache-key dimension -> candidate, not bug.
- Source comment saying a check is skipped -> candidate, not bug.
- Direct semantic drift -> necessary, not sufficient.
- Trigger evidence -> necessary, not sufficient.
- A useful green cell says exactly which proof obligation held in practice.

For optimizer predicate simplification, this same rule keeps the existing id30002 red cell strong:
the CASE-wrapped oracle is the G4 reference, and source revisit shows the G3 failure is deletion of
`!=` after shrinking `IN` without carrying the collation/coercibility proof used by sibling
contradiction checks.

## Held-Out Feedback Rule: Classify the Miss Before Changing the Method

The 2026-07-09 GitHub DDL held-out retrospective is the current calibration point:
`/Users/bba/pc/ai-native-ddl-github-heldout-methodology.md`. On 82 historical DDL validation
cases, the best DDL-docs run found 49, missed 29, and left 4 uncertain. That is good enough to
prove the proof-obligation method has signal, but not good enough to claim broad DDL coverage.

Before using a held-out miss to rewrite the selector ledger, classify it:

```text
SQL_ONLY          small SQL/testbed matrix could have caught it
SOURCE_ONLY       source shows the proof obligation, but no cheap runtime oracle exists
FAULT_INJECTION   needs an injected commit/error/cancel/retry/close failure
CLUSTER_TOPOLOGY  needs PD/TiKV/owner/upgrade/network or multi-cluster setup
STRESS_PERF       needs race/stress/profiling/counter evidence
LOW_VALUE         exact error text, test-only race, or low-severity performance drift
```

A miss only means "selector failed" when it was in a discoverability class the current loop claims
to cover. Otherwise it is an oracle-mining or harness-mining ticket. For DDL specifically, route
the miss into one of these pipeline obligations before adding cases:

```text
S-OBJ / S-ART / S-STATE / S-LIFE / S-ERR / S-RETRY / S-CFG / S-CACHE / S-ENV
```

This prevents two bad updates:

- Overfitting the SQL/schema matrix to bugs that actually require fault injection or topology.
- Dismissing high-value lifecycle/commit-boundary bugs just because `ADMIN CHECK` and rowset
  oracles are blind to them.

## Queue-Drain Feedback Rule

When a SOURCE_TARGETS lane reaches `next=null`, do not treat it as a dead end or as product
exhaustion. Classify the terminal states and feed them back into the generator:

```text
validated red cells     -> promote selector/oracle/state contract
retired ownership cells -> add negative cache or generator gate
blocked contract cells  -> keep as owner decision, not bug claim
empty refresh           -> switch to selector expansion or another oracle-debt lane
```

The 2026-07-10 state-ingress drain is the calibration example. The original
`STATE_INGRESS_INTERNAL_SQL` rule found binding-history and index-advisor RED/GREEN positives,
retired several sys/new-session/background paths, and pivoted into two better selectors:

```text
USER_SESSION_STATE_RESTORE
SYS_SESSION_POOLED_STATE_ISOLATION
```

The next method step is not to keep enumerating executor statements. It is to create narrow
source-target rules from the new selectors, then require the same gates again: product-feasible
wrapper, state-owner proof, strong oracle, and a stop rule after the first root cause. In the first
feedback pass, `pooled-session-state` and `user-session-state-restore` both produced zero duplicate
targets and correctly recognized their already validated examples as covered.

## Intra-Family D_dims Factorization

Once one family is live, the next move is often not "add more complicated siblings". It is to
factor `D_dims` inside the same family while holding the transaction shape and oracle fixed.

The 2026-07-11 DDL amend-path calibration is the current example. The family stayed the same:

```text
slow/paused prewrite crosses the delayForAsyncCommit boundary while DDL is adding new indexes
```

What changed was the hidden semantic dimension under test:

```text
fanout count:             one fresh index vs two fresh indexes
key shape:                single-column vs composite key
derived-key materialize:  stored generated vs virtual generated
txn protocol:             async commit vs 1PC
txn-side amend pressure:  one insert + one update vs one insert + two updates
txn-side op mix:          update-only pressure vs delete + insert + multi-update overlap
relative start order:     txn paused first vs DDL gets a small head start before txn starts
```

The rule is:

```text
fix the DML shape
fix the strong oracle
change exactly one hidden semantic obligation
measure the smallest green/red bracket again
```

This is much stronger than saying "richer DDL is more dangerous". In the live DDL lane:

```text
1PC + multi-add-index-rich            ~= 1.3s green / 1.4s red
1PC + stored-generated-index-rich     ~= 1.3s green / 1.4s red
1PC + composite-index-rich            ~= 1.4s green / 1.5s red
1PC + virtual-generated-index-rich    ~= 1.9s green / 2.0s red
1PC + virtual-generated + fanout3     ~= 1.90s green / 1.95s red
1PC + virtual-generated + mixed4      -> red even at 0.8s in the current live lane
async + virtual-generated             ~= 1.70s green / 1.75s red
1PC + virtual-generated + mixed4      -> hold=0ms green, hold=10ms red
1PC + virtual-generated + mixed4      -> ddl-lead=20ms green, ddl-lead=22ms red
```

That one matrix changed the selector. The useful claim is no longer "complex amend path". It is
closer to:

```text
stored-derived remap or multi-target amend under this boundary is materially tighter than
virtual-derived remap, and composite single-index encode is in between
```

The next refinement from the same family is that "fix the DML shape" is a staging trick, not a
permanent assumption. Once DDL kind, protocol, and key-shape axes are already factored, the next
hidden obligation may live on the transaction side:

```text
same DDL kind
same txn protocol
same boundary
same exact-row oracle
different number of rows / derived keys that must be amended before commit
```

That is not workload widening. It is another `D_dim`. In practice, the
`virtual-generated-index-rich + 1PC + ddl_start_gap=0` cell that stayed green at `1.97s` under
the basic `insert one + update one` transaction moved forward to `1.95s` red once the
transaction became `fanout3` (`insert one + update two`). The reusable selector refinement is:

```text
amend fanout itself can move the green/red boundary forward, even when DDL kind and protocol
stay fixed
```

This is why gray-cell search should raise amend pressure before it jumps to another module.

The next upgrade is to stop treating "more amend pressure" as a scalar. Operation mix can matter
more than raw fanout count:

```text
delete old key
insert new key
rewrite multiple old keys
```

is a different semantic obligation from `insert one + update two`, even if the total number of
amended keys is similar. In the current live lane, `mixed4` (`delete + insert + update + update`)
collapsed the `virtual-generated-index-rich + 1PC + ddl_start_gap=0` green window much more
aggressively than `fanout3`: instead of `1.90s green / 1.95s red`, the mixed operation shape was
already red at `1.8s`, `1.6s`, `1.2s`, and even `0.8s`, while the `MDL ON` control stayed green.

The selector upgrade is:

```text
txn-side operation mix can dominate DDL-side richness; delete + insert overlap may tighten the
boundary more than update-only amend fanout
```

That is still the same family. It just means the next gray-cell search should try transaction-side
semantic pressure before it assumes the remaining headroom lies in another module.

The last refinement from the same lane is temporal ordering inside the family. When the green/red
boundary is extremely sharp, stop treating "overlap" as one scalar. Ask which side got the head
start:

```text
txn reaches paused prewrite first, then DDL starts
vs
DDL starts first and gets 20ms/22ms/... lead before txn begins
```

This is still not a different bug class. It is another `D_dim` of the same proof obligation. In
the current `mixed4` lane, `hold=0ms` stayed green, but `hold=10ms` was already red; and with the
new `ddl-lead` axis, `20ms` stayed green while `22ms` was already red. That tells you the family
currently behaves more like a very steep availability cliff than a broad gray zone. This is also a
method signal:

```text
if a family keeps splitting into success vs schema-changed with almost no gray cells,
document it as an availability cliff and search for gray cells by changing the timing geometry,
not by widening the whole workload indiscriminately
```

One harness corollary matters too. If a control lets DDL finish before failpoint release, the
probe must not double-consume the DDL completion channel; otherwise a probe-level deadlock can
masquerade as a product liveness bug. Control-path correctness is part of the method.

Two practice rules fall out of this:

1. Do not merge all rich siblings into one bucket after the first red. First ask which hidden
   obligation actually tightened the window.
2. Do not promote a threshold from one bracket run. A single `1.8 green / 1.9 red` observation is
   only a candidate boundary; rerun or decompose it before turning it into a selector claim.

The next upgrade is to explicitly separate an amplified red cell from a natural red cell.

```text
amplified red:
  needs failpoint hold / injected slowdown / artificial widening to hit

natural red:
  still hits after removing the amplifier
  same-start
  hold=0
  no prewrite pause
```

The method rule is:

```text
once a family has a trustworthy amplified red,
immediately try to remove the amplifier before you widen the matrix again
```

The 2026-07-11 DDL lane is the model case. After `delayForAsyncCommit` had already produced
trustworthy amplified reds, the probe was upgraded with `pause-prewrite=false` and an intermediate
`mixed3(delete + insert + update)` shape. That produced a much higher-quality matrix:

```text
same-start + hold=0 + no-pause + MDL OFF
```

Under this natural lane, `multi-add-index-rich + fanout3` and `add-composite-index-rich + fanout3`
went RED on both async commit and 1PC, while the `MDL ON` siblings stayed GREEN; but the
`add-generated-index-rich` stored-generated sibling stayed GREEN on the same cells. That teaches a
stronger selector than "richer DDL is riskier":

```text
ordinary-column index amend obligations can enter natural availability red
earlier than stored-generated siblings under the same family
```

This matters for campaign quality. A natural red cell is closer to a user-facing product failure
than a heavily amplified red cell, so it should outrank nearby amplified siblings when deciding
what to minimize, store, or file.

Once a natural red appears on a mainstream path, minimize the transaction obligation itself before
you widen DDL siblings again.

The next useful split is:

```text
single-op green:
  insert-only
  update-only

multi-op red:
  insert + update
  or delete + insert + update
```

That teaches a stronger selector than "the DDL is fragile":

```text
the failure needs a combination of semantic obligations inside one transaction,
not merely any single DML that touches the table during DDL
```

The 2026-07-11 plain `ADD INDEX` lane is the example. Under:

```text
same-start + hold=0 + no-pause + MDL OFF
```

`ADD INDEX` itself went natural RED on both async commit and 1PC with the minimal two-step
`basic(insert + update)` transaction, while the minimized `insert-only` and `update-only` siblings
both stayed GREEN. That is more valuable than another richer sibling, because it turns a broad
availability family into a minimal combination obligation:

```text
new-key creation + old-key rewrite in one transaction
```

This is exactly the kind of selector improvement that should change campaign priority. A mainstream
natural red with a minimized two-step transaction is a better filing candidate than a richer,
harder-to-explain sibling that still needs extra amplification.

One more refinement from the same lane: when the minimized red still contains more than one DML,
split transaction-internal order as its own `D_dim`.

```text
insert -> update
vs
update -> insert
```

That is not a cosmetic variation if the final rowset is the same. It tests whether the obligation
depends on "which semantic rewrite happens later in the transaction."

The current plain `ADD INDEX` lane showed a useful protocol split:

```text
1PC:
  insert-only   green
  update-only   green
  insert2       green
  update2       red
  insert->update red
  update->insert green

async commit:
  insert-only   green
  update-only   green
  insert2       red
  update2       red
  insert->update red
  update->insert mostly red, with one green outlier before reruns converged red
```

The reusable lesson is:

```text
protocol-specific transaction-internal order can be a first-class selector dimension,
even inside the same DDL family and same final rowset
```

This matters because it prevents a bad simplification. If you stop at "mixed new+old is red," you
miss that 1PC already separates `insert2` from `update2`, and separates `insert->update` from
`update->insert`. The better selector is narrower and more explanatory:

```text
do not collapse a family before testing transaction-internal order and same-type multi-op siblings
```

The next refinement is sibling-specific compression. Do not assume the minimized selector from one
mainstream sibling transfers unchanged to another.

The 2026-07-11 `ADD UNIQUE INDEX` lane is the example. Plain `ADD INDEX` first looked like a
two-step obligation family; but when the same minimization grid was moved to the unique-index
sibling, the family compressed further:

```text
async commit + insert-only   red
1PC         + update-only   red
```

with `MDL ON` controls green.

That is a stronger result than "another sibling is also red." It means the sibling changed the
minimal obligation itself. The reusable rule is:

```text
after you minimize one mainstream sibling,
immediately test whether a nearby sibling compresses the selector further
```

In practice:

1. If sibling B turns a two-step red into a single-op red, promote sibling B in filing priority.
2. If sibling B turns a stable selector into red/green phase jitter, record it as a near-boundary
   lane rather than forcing a false clean rule.
3. Treat "single-op natural red on a mainstream sibling" as a stronger severity signal than
   "multi-op natural red on a richer sibling."

The next refinement is that sibling compression can differ by protocol, and another sibling may
look broader rather than merely stronger.

The 2026-07-11 `ADD PRIMARY KEY` lane is the example:

```text
async commit:
  basic       red
  insert-only red
  update-only red
  insert2     red
  update2     red after reruns

1PC:
  insert-only green
  update-only green
  insert2     red
  update2     red
  basic       red
  basicrev    red/green jitter
```

with `MDL ON` controls green on the trusted red cells.

This teaches a second reusable rule:

```text
do not force one sibling to inherit another sibling's minimized story;
some siblings widen into "single-op for protocol A, multi-op for protocol B"
```

That is still progress, not noise. It means the family is stronger than a single-path explanation,
but the issue package should present it as a sibling matrix rather than a single universal minimal
repro.

One more rule fell out of the same lane: micro-window observation can perturb the result enough to
destroy the selector if it runs inline with the critical path.

The attempted `DDL_JOBS` phase logger was useful diagnostically, but when it queried
`information_schema.DDL_JOBS` synchronously before txn start / before failpoint release, some
`fanout3 + ddl-lead` cells that had been RED moved to GREEN. The selector lesson is:

```text
for micro-window families,
inline observers are themselves D_dims
```

Practical rule:

1. Keep phase observers opt-in and default-off.
2. Do not promote thresholds learned with an inline observer until they are revalidated without it.
3. Prefer off-path/background observation, or use the observer only after the natural/amplified red
   is already pinned by a non-perturbing oracle.

The next refinement is transaction shell. Do not assume a red cell discovered with an explicit
multi-statement transaction automatically lifts to a statement-level or autocommit-level user
surface.

The 2026-07-11 `ADD UNIQUE INDEX` minimization is the model case. Local no-pause natural reds had
already compressed to:

```text
async commit + insert-only   red
1PC         + update-only   red
```

inside an explicit transaction shell. But when the shell itself was reduced, the story changed:

```text
explicit transaction:
  insert1       red
  update1       red on the 1PC sibling lane

autocommit single statement:
  stmtinsert1   green
  stmtupdate1   green
  stmtinsert2   green
  stmtupdate2   green
```

and the first live lift of `ADD UNIQUE INDEX + async commit + insert1` also stayed green.

The reusable rule is:

```text
explicit transaction shell vs autocommit statement shell is its own D_dim
```

Practical consequences:

1. Before calling a family "single-statement user-facing", remove the explicit transaction shell
   explicitly.
2. If autocommit turns the cell green, do not discard the family; narrow the selector to say the
   transaction shell is part of the obligation.
3. Treat "single-op red inside an explicit transaction" and "single-statement red" as different
   severity levels.

One more upgrade came from the live testbed lift. A single live red or single live green is often
too weak to rewrite the selector because near-boundary DDL families have micro-window jitter.

The 2026-07-11 live `plain ADD INDEX + basic(insert -> update)` lane is the example. Isolated
single reruns initially mixed red and green. The right move was not to argue from one outlier, but
to scan one tiny temporal dimension:

```text
ddl-start-gap = 0ms / 1ms / 2ms / 5ms / 10ms
```

while keeping every other dimension fixed.

On the real testbed cluster, the resulting matrix was:

```text
async commit + MDL OFF:
  10/10 red across 0-10ms

async commit + MDL ON:
  6/6 green

1PC + MDL OFF:
  3/6 red, 3/6 green

1PC + MDL ON:
  4/4 green
```

This teaches a stronger operational rule:

```text
after a local natural red hits a mainstream path,
lift it to live with a tiny one-dimension matrix, not a single replay
```

In practice:

1. Change only one temporal or ordering dimension for the live lift.
2. Use 3-5 nearby values and at least 2 reps per value.
3. Always pair the red lane with a protective control (`MDL ON`, safe-path toggle, or equivalent).
4. Record hit-rate, not just one successful screenshot.

The payoff is large. It turns "I once saw it red on a real cluster" into either:

```text
stable live red band     -> issue-quality severe evidence
phase-sensitive live lane -> keep as selector asset, but describe as boundary-sensitive
```

## Post-Red Aftermath Oracle

For severe execution-path families, a red cell alone is not enough to classify impact.

Add one more step after the first trustworthy red:

```text
1. capture the red execution symptom
2. release the pinned DDL / fault window cleanly
3. let the operation settle
4. run the strongest available final-state oracle
```

This separates two different bug shapes:

```text
transient execution failure:
  red symptom during the critical window
  final oracle is green after settle

persistent damage:
  red symptom during the critical window
  final oracle stays red after settle
```

The 2026-07-11 `issue62531` calibration is the model case. A live `MODIFY COLUMN` red cell hit
`table_scan_executor.rs:467` with `missing data for NOT NULL column`, but after releasing the pinned
apply-window and letting DDL finish, the final oracle (`ADMIN CHECK TABLE` + formula oracle +
table/index reader oracle) stayed green. That changed the claim from "persistent corruption" to
"strong transient execution failure in the DDL window".

Three practice rules:

1. Do not describe a red execution-path family as persistent corruption until a post-red aftermath
   oracle proves the final state is still bad.
2. If the aftermath is green, keep the family severe if the runtime symptom is severe, but move the
   selector wording toward consumer/window/bridge failure rather than stored damage.
3. Prefer adding aftermath oracle support to the probe itself, so the same red cell can be replayed
   without manual cleanup decisions.

## Historical Replay Shape Gate

A green replay is not evidence of a fix until the replay preserves the historical operation shape.
Record these dimensions explicitly before compressing an issue into a pinned probe:

```text
historical shape = repeated DDL transitions + prepared-parameter DML + natural timing
compressed shape = one held DDL + external DML + synthetic pause
```

The compressed shape can be a useful phase probe, but it is not an equivalent reproduction. If the
historical issue uses repeated `MODIFY COLUMN int <-> bigint` and prepared `DELETE`, a one-shot
`MODIFY COLUMN int -> varchar` GREEN must be classified as a boundary sample, not as a fix. A
current endpoint/version mismatch must be recorded alongside the result, and the original loop must
be replayed once before retiring the target. This prevents a harness improvement from silently
changing the bug's workload class.

## Cross-phase semantic-context proof

When phase A carries a token into phase B, do not stop after proving token equality. Ask what context
is required to interpret that token:

```text
token_A == token_B
does not imply
meaning(token_A, context_A) == meaning(token_B, context_B)
```

For every scan -> action, prepare -> commit, cache -> replay, or checkpoint -> resume handoff:

1. list the carried token fields;
2. list all semantic context owners used to decode them: time zone, locale, collation, SQL mode,
   schema version, policy version, feature flags, or session variables;
3. identify whether each owner is pinned, versioned, revalidated, or independently reloaded;
4. mutate one unpinned context after phase A has materially completed and before phase B's first
   irreversible action;
5. move current state into the disagreement window `safe(context_A) && actionable(context_B)`;
6. require an actual-action C3 oracle and a no-context-drift control.

This extends the existing `P -> Q -> fast path` loop. The AI should now distinguish **value proof**
from **meaning proof**. id1620002 demonstrates the difference: TTL carried the same expiration epoch
but reinterpreted it under two global time zones, so a delete recheck existed yet failed its only
safety purpose.

History remains post-hit calibration. In this case #41043 was found only after worker RED; replaying
its old schedule as a GREEN control proved that the new target was a mid-job context-stability gap,
not rediscovery used as a selector seed.

### Counterfactual consequence gate

Before admitting a context-drift target, remove the suspected stale phase but keep the user's
semantic mutation. If the same consequence remains valid by contract, the oracle cannot identify
the proposed root.

The first post-id1620002 source candidate illustrates the gate. `NEXTVAL(?)` may resolve a sequence
dynamically and retain an old cache across `ALTER SEQUENCE`, but a schedule using `RESTART WITH 2`
cannot use duplicate value 2 as its C3 oracle: RESTART itself intentionally moves the sequence into
that used range, and current source explicitly permits concurrent DML to use the old definition and
lose monotonicity. A live duplicate would therefore be non-discriminating. Retire such candidates in
source; require a consequence impossible in the no-stale-phase control before testbed admission.

## Replay compensation closure proof

A missing checkpoint field is only a candidate, not yet a missing effect. Before admitting it, model
the retry/resume mechanism as an ordered event log and compute its effect closure:

```text
checkpoint restore
+ ordered replayed actions
+ ordered replayed compensation
= final published state
```

For every rollback, cancel, undo, or release operation followed by retry/replay:

1. enumerate the exact statements or events retained by the replay owner;
2. mark both forward effects and compensating effects such as `ROLLBACK TO`, delete, release, or
   tombstone;
3. prove trigger evidence that replay actually happened;
4. compare the final user-visible state with the no-replay control;
5. only call RED when the compensation is omitted, reordered, or interpreted under a changed owner.

The savepoint screen demonstrates why this gate matters. `TxnCtxNeedToRestore` does not snapshot
`StmtHistory`, which initially looked like a rolled-back INSERT could resurrect after optimistic
retry. The real retry trace was `SAVEPOINT -> INSERT(1) -> ROLLBACK TO -> INSERT(2)`. Replaying the
compensating `ROLLBACK TO` removed row 1 again, and both retry and no-retry cells committed only row
2. The source suspicion was real, but its effect was dominated by event-log compensation.

Method change: field inventory remains the first pass, but event-history and compensation closure
are now mandatory before a rollback-state omission receives C3 admission. The strong oracle is the
final rowset plus the exact replay trace, never history length alone.

## Observer-signal interference proof

For every stale, timeout, takeover, cleanup, or abort decision driven by heartbeat/lease/progress
state, prove that observation is non-interfering. The key question is not only whether the code reads
the right signal, but whether resources held by the reader prevent the writer from producing it.

Build this graph before creating a schedule:

```text
observer-held lock/resource -> signal-writer required lock/write set
signal writer               -> heartbeat/lease/progress field
observed field               -> irreversible decision
```

If the first edge exists, require this altitude matrix:

1. prove the signal advances before the observer acquires the resource;
2. keep the same writer alive after acquisition and observe its real result;
3. observe the terminal action, not merely a lock conflict or timeout;
4. run a genuinely stale control with the same clock compression;
5. stop after one stable live-owner RED and stale GREEN.

This adds a measurement-independence proof to `P -> Q -> F`:

```text
P: the observer holds resource R to stabilize a safety decision
Q: unchanged signal S means the remote owner is stale
F: the producer of S also needs R, so the observer manufactures unchanged S
```

id1650002 is the calibration case. BR abort holds `FOR UPDATE` on the restore-registry row while
waiting for a heartbeat written to that row by another session. Real TiKV proved pre-lock progress,
post-lock write conflict, stale classification, and live-row deletion 3/3; a no-heartbeat stale
control deleted correctly 3/3. PR and issue history were excluded from discovery and searched only
after the RED.

## Terminal error identity and success-artifact coherence

An error check does not prove failure propagation. After finding `if E != nil`, trace the exact
identity of `E` through the branch to the owner that publishes return status, acknowledgement,
commit, or success. Then name the irreversible action and artifact that successful completion
requires.

```text
producer -> checked value E -> failure branch -> public terminal value
                                 |              -> required action/artifact
                                 +-- may publish stale sibling S
```

Admit a C3 target only when a compact matrix can jointly observe both sides:

1. injected boundary failure: public success plus absent action/artifact is RED;
2. no-fault control: public success plus present action/artifact is GREEN;
3. one-variable counterfactual: publish `E` instead of `S`; the same fault must become failure.

This pass is stronger than missing-error-check scanning because the check may be syntactically
present and dynamically taken. The proof obligation is that the checked identity dominates the
terminal owner. Count repeated syntax as blast radius after one command-level proof.

id1680003 is the calibration case. Five BR task paths check scheduler-removal error `e` but return
earlier setup error `err`. A local real-TiKV command exited 0 with a failed summary and no
`backupmeta`; the no-fault command wrote the artifact, and changing only the returned identity made
the same fault exit 1. PR/review/history were excluded from target generation and used only for
post-hit dedup.

## External-effect durable-boundary proof

For any local transaction that also calls an external service or non-transactional side store,
error identity is only the first layer. Build a durable-boundary ledger:

```text
local owner:     stage -> commit -> publish success | cancel/conflict/owner loss
external owner:  call  -> durable visible state     | compensate/reconcile
```

If the external call precedes local commit, enumerate every abort edge reachable after external
success. A missing compensation or reconciliation edge is a proof obligation even when every call
checks errors correctly.

The minimal matrix is:

1. pause after real external success;
2. trigger a supported local cancel or conflict;
3. observe local terminal result and history;
4. independently read metadata and runtime owners;
5. run normal publication GREEN;
6. prove the same cancel cannot drift state when the precommit external effect is removed or
   compensated.

id1710003 is the calibration case. ALTER RESOURCE GROUP updates PD before the DDL worker transaction
commits. ADMIN CANCEL wins the job-row conflict, the ALTER and history are cancelled, metadata stays
at 1000/LOW, but real PD remains at 1/HIGH. A normal ALTER aligned both owners at 2000/HIGH. This
turns the LOOP from error-path scanning into cross-system commit ownership analysis.

## Layered-dominance rejection gate

An unsound primary guard is a candidate, not yet a bug. After forcing it to pass, continue through
all independent owners that can reject the invalid operation:

```text
weak guard -> effective owner acquisition -> downstream data owner -> terminal status -> artifact
```

Admit RED only when the invalid state crosses every layer and reaches the user-visible terminal
owner. The BR GC calibration physically collected an old version, swallowed both primary
safepoint-read errors, and let the real PD service write return nil. TiKV still rejected the old
snapshot with error 9006, BR exited 1, and no backupmeta existed. This is a source weakness but not
a user-visible bug. Recording the GREEN prevents repeated overcounting and adds the downstream
owner to future proof graphs.

## Retry-attempt payload atomicity proof

Error propagation is not a complete retry proof when a callback mutates state captured outside the
attempt. Before accepting a retry closure, inventory every slice, map, cursor, summary, or object it
can publish after `RunWithRetry` returns:

```text
source domain -> attempt-local derivation -> retry terminal result -> published payload -> consumer
                     | failure after prefix |
                     +------ residue ------> next attempt
```

Use the smallest two-batch matrix and keep the fault after a nonempty prefix:

1. current source: `2 -> 1` with nil error proves partial-success publication;
2. propagate the real error only: `2 -> 3` proves failed-attempt residue survives retry;
3. propagate the error and reset/build attempt-local state: `2 -> 2` proves the full obligation;
4. lift the winning RED to a consumer oracle that checks exact coverage and uniqueness.

The postcondition must be `complete source coverage exactly once`, not `payload is nonempty`. A safe
shape builds the payload inside each attempt and copies it out only on complete success; resetting at
attempt entry is an acceptable bounded counterfactual, while an explicit exact-coverage validation
is an additional publication guard.

id2040003 is the calibration case. `generatePlanForPhysicalTable` appended ReadIndex metas to a
slice outside `RunWithRetry`, swallowed the second TSO error, and published one of two batches. On a
real DXF testbed the DDL finished synced, but FORCE INDEX missed a committed row and ADMIN CHECK
returned 8223. Changing only the returned error retried to three metas; resetting the attempt
payload as well returned exactly two. Candidate generation used current source only; PR/issue search
was reserved for post-RED dedup and found no exact root.

## Clone alias-graph proof

Deep-copy review must prove two properties separately:

```text
external ownership isolation: alternatives A and B do not share mutable objects
internal view coherence:      producer and consumer views inside A reference the same A-owned clone
```

A field-by-field copy audit proves neither alias graph automatically. For every clone routine,
identify canonical collections and derived/active/indexed views, then record which owner mutates
each object and which owner consumes it. The suspicious shape is:

```text
original canonical[i] ----+
                          +--> one mutable object
original active[j] -------+

cloned canonical[i] ------> clone X -- producer mutates X
cloned active[j] ----------> clone Y -- shortcut consumes stale Y
```

The smallest useful matrix varies **repair-path reachability**, not syntax breadth:

1. bypass the cloned strategy and establish the expected rowset;
2. select the cloned strategy while a later repair owner does not touch the leaf;
3. use an adjacent shape where that repair owner rebuilds both views;
4. preserve the original alias graph inside the clone and keep the same selected strategy.

A GREEN sibling in cell 3 is evidence about the mask, not evidence that the clone is safe. Promote
the candidate only when cell 2 has a direct behavior oracle and cell 4 changes only identity.

id2070003 is the calibration case. `cloneDataSource` independently cloned
`AllPossibleAccessPaths` and `PossibleAccessPaths`. Stats filled the canonical clone, physical
planning consumed an active clone with empty ranges, and aggregate IN became `TableDual`. Plain IN
was GREEN because correlation reached the leaf and rebuilt both views. Mapping active paths to the
canonical clones kept Apply selected and made all nine cells GREEN. Discovery used current source
only; post-RED dedup found no exact root, and testbed 8220955 reproduced the SQL-only wrong result.

## Retry closure must include non-transactional attempt state

For every automatic retry site, build three sets from current source:

```text
M = state mutated before a retryable error can occur
R = state restored or rebuilt before re-entry
C = state consumed by the retried operation
candidate debt = (M intersect C) minus R
```

Rank a debt edge only after tracing its highest consumer. Session-local drift is weak; a survivor
that controls a key, predicate, row image, external action, or terminal error is strong. The smallest
matrix changes error altitude around the mutation: no error, error before M, the same error after M,
an idempotent mutation, and an exact restore-or-decline-retry counterfactual.

id2100003 is the calibration case. SETVAR enters M and C; pessimistic `StmtRollback` restores KV
statement state but leaves UserVars outside R. A post-evaluation retry produced 2/2 instead of 1/1,
while pre-evaluation and idempotent controls passed. A concurrent unique-key owner then converted the
state leak into false success and committed row-image drift. Restoring only UserVars made the exact
matrix GREEN. Discovery and ranking used current source only; history was opened only after RED.

## Use normal entry as a reference reset

Retry code rarely documents every state dimension it owns, but the normal operation entry often
does. Treat that entry as a compact reference specification:

```text
N = fields, maps, caches, and external session values reset or restored at normal entry
R = the same dimensions reset or restored before retry re-entry
D = N minus R
candidate = D intersect state written or consumed by replay
```

Do not execute every member of D. First trace the highest consumer; `planHint` was omitted by retry
reset but reached only statement summary, slow log, and plan-replayer diagnostics. Then prove product
reachability; `SetVarHintRestore` was absent from whole-transaction replay and had a plausible
durable `sql_mode` consumer, but current TiDB forces `tidb_disable_txn_auto_retry=OFF` back to ON, so
a natural user transaction cannot enter that replay path. Both candidates were retired before
testbed use.

Only after both gates pass should the matrix be built. Prefer a successful zero-work replay and a
run beginning directly from the same final state. That isolates failed-attempt residue from ordinary
zero-work semantics and was the decisive control for id2190003.

## Retry closure includes external capabilities

Retry analysis must not stop at values read by re-entry or fields published at statement
completion. A failed attempt can create an independently live capability whose consumer is outside
the retried statement:

```text
attempt mutation -> external/session capability owner -> independent competing consumer
         |                    |
         +-> primary rollback +-> omitted from retry closure
```

Examples include locks, leases, registrations, handles, reservations, and long-lived internal
transactions. Compute `M`, `R`, and `C` over ownership effects, not only fields. For an external
capability, the strong oracle has three layers: query the owner identity, perform a competing
operation that must be denied or admitted, then remove the suspected residue and prove recovery.

id2310003 is the calibration. A failed pessimistic RC attempt evaluated a row-dependent `GET_LOCK`
before a natural unique-key conflict. The successful retry saw a newly committed gate, matched zero
rows, and returned success. `StmtRollback` restored statement KV but not `session.advisoryLocks` or
the dedicated internal lock transaction. With MDL enabled, local and real-TiKV runs showed retry
count one, zero affected rows, `IS_USED_LOCK=owner`, and competing `GET_LOCK=0`; the same-final-state
no-retry control showed `NULL/1`. This adds an **external capability consumer** to S45 alongside
re-entry and terminal publication consumers.

Do not repair only acquisition in a system that also supports release or bulk release. If inverse
operations can block or fail, partial journaling is not a closed rollback owner. Conservatively
declining transparent retry for the entire side-effect family may be safer than incomplete
compensation.

## Compile source packets before delegating reasoning

Do not ask a child agent to discover its own repository scope. The parent loop must first compile a
source packet from an owner graph:

```text
proof debt -> explicit owners -> exact source ranges -> bounded packet -> reasoning pass
```

The packet is an executable budget boundary. Enforce source-region count, lines per region, total
lines, encoded bytes, candidate count, and wall time outside the prompt. A prompt that says "use at
most N commands/tokens" is not a control: full-repository scouts exceeded 60k tokens without
returning a result. The calibrated default is at most 12 regions, 240 lines per region, 1,200 lines,
32 KiB, three candidates, and a process-group wall timeout. The model is capability-probed rather
than hard-coded, and this provider uses JSON-only prompting plus `--output-last-message`, never
`--output-schema`.

Packet size is part of selector precision. In the transaction campaign, a 47 KiB packet timed out
after 75 seconds. Removing neighboring modes and explanatory ranges produced a 25 KiB packet that
completed in about 45 seconds. The child should reason over a selected owner graph, not repeat
source discovery.

Treat the child result as an adversarial hypothesis, not a verdict. The fair-lock packet proposed a
three-attempt delayed-cleanup schedule, but assigned TiDB's newly allocated `forUpdateTS` to the
client-go committer too early. Direct owner verification showed that TiDB updated transaction
context and snapshot state first; client-go received the new timestamp only in the later
`LockKeys`. The cleanup therefore retained the older threshold, and TiKV's comparison protected the
newer lock. This is the desired division of labor: the child finds a sharp counterexample shape;
the parent verifies every owner transfer before admission.

### Packet completeness is a proof obligation

A packet result of zero candidates means only "zero candidates inside this compiled owner graph."
Before accepting that negative, the parent must close four packet boundaries:

```text
predecessor producers -> stored state -> transfer/filter owners -> highest consumers
```

Include the owner that created the value, every filter or representation change that can discard
information, lifetime/reset owners, and the highest correctness-bearing consumer. The first async
secondary packet was internally correct but underrepresented predecessor and lifetime owners; the
parent pass had to add them before the negative could be retired. Packet compilation is therefore
part of the proof, not prompt plumbing.

## Restore mutable values, not only containers

Reference-reset and retry-closure differentials must traverse reachable mutable state:

```text
N = state graph present at normal entry or savepoint creation
R = state graph restored at rollback or retry
C = state graph consumed afterward
debt = (mutable(N) intersect C) minus R
```

For every map, slice, interface, pointer, cache, or handle, classify container membership and value
lifecycle separately. A map may correctly survive rollback while a mutable field inside one of its
values remains attempt- or savepoint-scoped. Follow each such field to its highest consumer before
building a matrix.

`id2220003` calibrates this extension. `TemporaryTables` membership intentionally lives outside
`TxnCtxNeedToRestore`, but each value owns a mutable transaction dirty-size counter. Two 600000-byte
writes raised that counter above a 1 MiB limit; `ROLLBACK TO SAVEPOINT` made the table visibly empty
without restoring the counter, so a one-byte INSERT returned error 1114. Restoring only per-table
size made the exact local matrix GREEN, and the SQL-only RED reproduced on the pinned testbed.

This is strong selector evidence but only a moderate bug: the highest consumer rejects a valid
transaction-local write; no durable wrong data, cross-session corruption, or limit bypass was
shown. Keep method validation and consequence ranking as independent gates.

## Mark the irreversible proof horizon before auditing fallible epilogues

Fast protocols can become recoverably committed before their initiating function returns. Mark
`H`, the earliest point after which an independent component has enough durable evidence to finish
the outcome:

```text
request prefix -> H: independent recovery can finish -> fallible checks -> terminal response
```

Then enumerate every return-error edge after `H`. A cleanup call is not sufficient proof. Each edge
must either move before `H`, publish success/explicit undetermined, or synchronously prove
compensation before returning abort.

The smallest useful matrix is:

```text
guard altitude x compensation availability x independent recovery consumer
```

Inject only the guard predicate and compensation availability. Keep the downstream owner real, then
compare the terminal result with final durable truth. This catches errors that a caller-local unit
test cannot see.

`id2250003` calibrates the selector. client-go completed all async prewrites and selected a nonzero
`minCommitTS`, then returned ordinary `txn takes too much time`. With cleanup unavailable, both
local unistore and three real TiKV nodes recovered the two-key transaction as committed. Moving only
the age guard before prewrite made the old oracle fail with both keys absent and the safe oracle pass.

Store this selector as `POST_PROOF_FALLIBLE_EPILOGUE`. Stop after one root per proof horizon; late
error strings, TTLs, and key counts are blast radius unless they change the independent owner or
terminal-result class.

## Close the real downstream retry owner before promoting a local RED

A cross-layer local RED is provisional when the local store, mock RPC layer, or harness owns the
response that decides retry semantics. Extend the owner graph one step past the apparent terminal
error:

```text
local terminal mismatch
  -> exact downstream replay request
  -> idempotency or committed-record owner
  -> final terminal result and durable truth
```

The 1PC lost-response candidate calibrates this gate. The embedded store returned a write conflict
when a committed TryOnePc request was replayed after Region regrouping, producing an ordinary error
while both keys were visible. Real TiKV instead ran `check_committed_record_on_err`, recovered the
existing 1PC commit timestamp, and let client-go return success. The local observation was a useful
locator, but the real owner made the product matrix GREEN; the target is retired and is not a bug.

Fault ownership is part of the same proof. A process-wide retry failpoint can also intercept a
topology helper that shares the process and deadlock the experiment. Run Region split, owner change,
or independent recovery from a separate process when the target process is paused. Classify a
shared-failpoint timeout as `INVALID(harness)`, never as product liveness evidence.

## Require validation to cover the irreversible apply horizon

A fast path can validate the right fact at the wrong time. Mark the validation point `V` and the
irreversible apply point `H`, then inventory every semantic fact consumed at `H`:

```text
validation debt = facts consumed at H
                  that can change in (V,H]
                  minus lock/version/CAS/revalidation owners enforced at H
```

The strongest candidates have a safe-path sibling that validates later. Build a small matrix over
fast versus safe path and state change before, inside, and after the uncovered interval. The fault
should lengthen only `(V,H]`; it must not mock the validator, downstream apply result, or consequence
oracle.

Concurrent operations need a logical-order oracle. Invocation and response order do not establish
serialization when operations overlap. Capture commit timestamps, DDL finished timestamps, epochs,
versions, or another authoritative ordering token first. Judge the resulting state only after the
operation is proved to be ordered after the state change.

`id2280003` calibrates this selector. With MDL off, TiDB installed a delta schema checker and
client-go ran it from `calculateMaxCommitTS` before prewrite. A related `ADD INDEX` then finished;
TiKV selected a later 1PC commit timestamp and atomically committed the old mutation set. client-go's
1PC branch returned immediately, while its 2PC sibling checked the actual commitTS and retried.
On real TiKV, the 1PC table scan returned the row, the new index missed it, and `ADMIN CHECK TABLE`
failed. The paired 2PC run was fully coherent. `TRUNCATE TABLE` supplied the table-identity sibling.

The first local oracle almost overclaimed because it used response order around TRUNCATE. Comparing
DML `commit_ts` with DDL `FinishedTS` converted it into a valid serialization claim. This correction
is part of the method, not reporting polish.

Store the selector as `VALIDATION_HORIZON_COVERS_IRREVERSIBLE_APPLY`. Rank only debts reaching a
keyset, predicate, row image, table identity, commit outcome, or terminal truth. Stop after one root
per uncovered horizon; extra DDL types and delay values are blast radius.

## Compare semantic finalizers, not only fast-path checks

A specialized path can share the canonical child operation but bypass the finalizer that interprets
its side state. Build a differential owner graph:

```text
producer -> raw result + semantic side state -> canonical promotion -> public result
                                      `-----> specialized raw return -> public result
```

This is stronger than searching for ignored errors. The child can return an error correctly while
the missing operation is semantic promotion: ambiguity to undetermined, partial progress to retry,
or internal cause to a connection/cleanup policy. Require a sibling consumer that still trusts the
side state; otherwise it may be stale diagnostics.

The minimum matrix keeps durable truth fixed and changes only the promotion owner. For response-loss
faults, select a successful response rather than the first call. Then use an independent durable
consumer, such as a fresh real-TiKV transaction, and assert the public error class separately.

`id2340003` calibrates the method. Primary Commit response loss set `undeterminedErr`; ordinary 2PC
promoted it in `commitTxn`, but pipelined DML returned the raw `commitMutations` error. Real TiKV
showed the value committed while the caller received an ordinary transport error. Adding only the
missing promotion produced `ErrResultUndetermined` with the same durable value. Store this selector
as `SIDE_STATE_SEMANTIC_PROMOTION_BYPASS`.

## Build cross-attempt feedback edges, not only survivor lists

Rollback differentials originally asked whether a failed attempt left state behind. That misses a
more severe shape: the residue is read by the successful retry and changes durable output.

```text
failed-attempt write -> retry read -> key/predicate/row/action/terminal consumer
```

Enumerate both external and internal state owners. Require a same-final-state direct execution, then
compare the highest consumer. When the state owner is documented as nontransactional, add an equal-
owner-state anti-oracle so expected gaps are not mislabeled as corruption.

`id2370003` is the calibration. The hidden attempt executed `SETVAL(seq,100)` and the retry executed
it again, committing NULL instead of 100. Both the retry and direct-control sequence ended at 101,
so the only contradiction was the committed row. This selector is
`HIDDEN_ATTEMPT_FEEDBACK_INTO_RETRY_OUTPUT`.

## Treat evaluator cloning as a temporal ownership proof

Prepared plans and expressions can own mutable runtime objects. Search `Clone` implementations for
pointer, map, slice, interface, and receiver aliases, but do not promote an alias by itself. Prove
four edges:

```text
entry owner -> mutation before retry -> alias/reuse during rebuild -> correctness-bearing consumer
```

The snapshot altitude is part of the proof. A deep copy made while rebuilding the retry can preserve
only the state that exists then; it cannot recover state already consumed by the failed attempt.
Test the timing with a deliberately late-copy counterfactual before proposing clone isolation as a
fix.

`id2400003` is the calibration. Constant-seed RAND owns one mutable `MysqlRng`; the failed attempt
consumes its first value and the retry consumes its second. Mapping those values across a threshold
turns a subtle numeric difference into duplicate-key versus success. The narrow retry-decline gate
is GREEN, while deep-copy-at-retry remains RED. Store the selector as
`MUTABLE_EVALUATOR_STATE_SURVIVES_RETRY`.

For candidate generation, rank deterministic evaluators first. A seed, fixed clock, token stream,
or ordered iterator can map first and second outputs to opposing terminal outcomes, yielding a much
stronger oracle than comparing two arbitrary values. Stop after one root per evaluator owner; input
values and SQL forms are blast radius.

## Compare outer statement lifetime with inner replay lifetime

Field-differential scans miss state whose contract is expressed by a lifecycle rather than a reset
list. Search for the triad:

```text
initialize once -> preserve completed state -> reset only at outer statement boundary
```

Then enumerate every inner replay boundary inside that statement. A materialization that is correct
to share among sibling consumers can still be wrong to share across attempts. `sync.Once`, `Done`,
generation numbers, completed futures, memoized rowsets, and preserve-on-Close branches are compact
proof-debt markers.

Before testing, follow both owners:

```text
materialized path from failed attempt
fresh path from successful attempt
             -> same durable consumer
```

The strongest oracle changes two source values at the replay boundary and routes them through the
two paths into one row. A result that belongs to neither the old nor new generation proves mixed-
attempt execution without inspecting internal cache state.

`id2430003` is the calibration. `CTEStorageMap` owns a completed result and `sync.Once`; normal
statement setup clears it, but pessimistic retry reuses the same statement context. The retry row
`(u=2,v=10)` combines a fresh ordinary read with a stale materialized CTE, while one execution from
the same final state produces `(2,20)`. Resetting exactly the CTE storage before rebuild makes the
matrix GREEN. Store the selector as `REPLAY_PERSISTENT_MATERIALIZATION_STATE` and the oracle as
`MIXED_ATTEMPT_ROW_COHERENCE`.

Use AST automation only to compress candidate generation. The checked-in `clone-state-scan` parses
Go and reduced more than 600 expression clones to the one already-terminal RAND owner. Its empty
non-RAND result justified moving the owner boundary to statement materializations; it did not prove
the subsystem safe. This is the intended division: machines enumerate ownership debt, while P/Q/F,
consumer ranking, natural RED, and counterfactual decide the verdict.
