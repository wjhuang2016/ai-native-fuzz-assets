# id2610003: Revalidate proofs when a guarded value is replaced

Remote `found_bug id2610003`, issue-filed high severity / critical consequence:
[TiDB #69836](https://github.com/pingcap/tidb/issues/69836).

## Proof obligation

```text
P(x): commitTS x is below every cached-table WRITE lease.
Q:    every commit request uses a commitTS satisfying P.
F:    CommitTsExpired replaces x with x2 and retries while inheriting P(x).
```

The initial code looks safe because the main 2PC path visibly calls the checker. The useful question
was not "is commitTS checked?" but "which exact commitTS value was checked, and can any later edge
replace it before the irreversible consumer?"

## Small matrix

| CommitTS source | Candidate below lease | Candidate after lease | Exact recheck |
| --- | --- | --- | --- |
| initial TSO | accepted | rejected | present |
| `CommitTsExpired` replacement TSO | accepted | **accepted and committed** | missing |
| replacement TSO with counterfactual | accepted | rejected before RPC | present |

The smallest red cell is therefore not "cached table plus network fault". It is:

```text
value-dependent proof + value replacement + proof-dependent irreversible consumer - revalidation
```

## Why the high-level consumer mattered

An owner-level test proves that the checker is called once while two Commit RPCs are sent. That is a
source bug, but not yet the strongest product verdict. The TiDB test lifts it through real TiKV and
then consumes the stale cache with `INSERT SELECT`, producing durable source/sink divergence. This
turns a narrow retry omission into a C3 user-visible oracle.

## How to generate candidates

1. Locate a validation, lease, version, identity, range, or capability proof over a concrete value.
2. Follow that value to the highest irreversible consumer: Commit, publish, ack, delete, external
   apply, or durable response.
3. Enumerate every assignment after the proof: retry refresh, fallback, TSO regeneration, redirect,
   rebinding, decoding, defaulting, or recovery reconstruction.
4. For each replacement `x -> x2`, require one of three closures:
   - the proof is rerun on `x2`;
   - the transformation proves a monotonic implication `P(x) => P(x2)`;
   - the path fails closed before the consumer.
5. Build only the smallest matrix where the initial value passes and the replacement fails.
6. Use a natural producer for the replacement condition. Here a healthy reader and TiKV
   `CheckTxnStatus` naturally push `minCommitTS`; the test does not synthesize `CommitTsExpired` at
   the product level.
7. Patch only the missing revalidation edge. If that flips the same schedule, the owner is identified.

## Production-reachability discipline

Fault injection must name its production equivalent. The deterministic test directly stops lease
renewal and holds one Commit, but the production predicate is:

```text
writer progress/renewal pause > 5s
+ primary lock TTL > 5s
+ healthy peer reads after lease expiry
+ writer resumes before lock TTL
```

Current lock-TTL sizing makes a roughly 4 MiB write a practical example (~12s). A small transaction
is a negative control because its ~3s primary lock can be rolled back before the lease expires.

## Selector

Store this as `VALUE_REPLACEMENT_PROOF_REVALIDATION`:

```text
candidate = value-scoped proofs
            intersect post-proof replacement sites
            intersect proof-dependent irreversible consumers
            minus revalidation / monotonic derivation / fail-closed edges
```

Rank candidates by the consumer, not by the syntactic oddity of the replacement. A retry that only
changes logging is low value; a retry that reaches Commit, external publication, ownership transfer,
or an OK packet is high value.

## Method improvement

Record proof ownership with an argument, not as a boolean:

```text
bad:  commitTSChecked = true
good: checked(commitTS=4676..., lease=4676...)
```

Any assignment to either argument invalidates the cached proof token unless a derivation rule says
otherwise. This representation makes AI source analysis more precise: search value def-use chains,
post-check assignments, and consumer dominance instead of merely searching for a nearby `if err`.

Stop after one root per proof/replacement/consumer triple. Replacement sources, values, pause
durations, and SQL forms are blast radius.
