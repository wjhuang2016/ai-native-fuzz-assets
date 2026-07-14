# Method Case: fallible epilogue after the async commit proof frontier

Status: execution-verified and filed as [TiDB #69831](https://github.com/pingcap/tidb/issues/69831).

## Finding

The source finished all async prewrites and established a recovery commit timestamp, then ran a
generic transaction-age guard that could still return an ordinary error. A downstream lock resolver
provided the strong oracle: suppressing best-effort cleanup turned the supposedly failed transaction
into a committed one.

Production reachability is narrow but concrete: async commit must be explicitly enabled, the
transaction must be older than 24 hours, and background rollback must be delayed beyond lock expiry.
The next consumer can be the application's own retry on another TiDB node; touching the same keys
causes lock recovery to commit the first attempt before the retry applies the operation again.

## P/Q/D/F/O/R/S card

```text
Target:
  client-go async commit after all prewrite batches succeed.

P_check:
  The primary publishes all secondaries, every async prewrite succeeds, and minCommitTS is nonzero.

Q_claim:
  A later generic age check may still return an ordinary abort because deferred cleanup will undo
  the locks.

D_dims:
  - age guard false versus true;
  - cleanup completes versus cleanup unavailable;
  - no downstream consumer versus a later reader invoking lock recovery;
  - current implementation versus moving the guard before prewrite.

F_effect:
  After P, async recovery can derive a commit TS from the complete lock set. Cleanup is not a
  synchronous proof, so an ordinary error no longer means abort.

O_oracle:
  Compare the public terminal result with final MVCC truth after invoking the independent recovery
  owner. Ordinary error plus both values visible is RED.

R_redflag:
  `txn takes too much time` followed by `resolve async commit locks`, nonzero commitTS, and
  `ResolveLock action=commit`.

S_selector:
  `POST_PROOF_FALLIBLE_EPILOGUE`: identify the earliest prefix after which another component can
  make the operation durable, then enumerate every remaining return-error edge.
```

## Minimal matrix

| Age guard | Cleanup | Recovery consumer | Expected | Observed |
| --- | --- | --- | --- | --- |
| false | ordinary | none | commit success | GREEN baseline |
| true before prewrite | unavailable | point get | ordinary error, keys absent | GREEN counterfactual |
| true after async prewrite | completes | point get | ordinary error, keys absent | usually GREEN compensation |
| true after async prewrite | unavailable | point get | not an ordinary abort | RED: ordinary error, keys committed |

## Why this worked

The old loop often stopped after proving that an error path had a cleanup call. This case adds a
stronger question: **has the operation already crossed a proof frontier where an independent owner
can complete it?** If yes, deferred or best-effort cleanup is not enough to justify an ordinary
abort result.

The decisive test did not mock the lock resolver's answer. It injected only the age predicate and
cleanup availability, then asked the real downstream state machine for MVCC truth. Running the same
probe against three real TiKV nodes showed that the method survives a cross-layer lift.

## Method improvement

For every fast protocol, mark `H`, the earliest irreversible proof horizon:

```text
request prefix -> H: downstream can independently finish outcome -> fallible epilogue -> response
```

After `H`, each error edge must satisfy one of three rules:

1. move the check before `H`;
2. return success or explicit undetermined outcome;
3. prove compensation synchronously before publishing abort.

The high-value matrix is therefore not just fast path versus safe path. It is
`guard altitude x compensation availability x independent consumer`. This exposes bugs that a unit
test ending at the original caller cannot see.

## Pause gate

Stop after one root at this proof horizon. TTLs, key counts, and cleanup transport errors are blast
radius unless they change the irreversible owner or terminal-result class.
