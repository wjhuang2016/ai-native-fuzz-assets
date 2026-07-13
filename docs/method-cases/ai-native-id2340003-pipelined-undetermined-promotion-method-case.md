# id2340003: side-state semantic promotion bypass

Remote bug DB: `found_bug id2340003`, issue-filed high severity; upstream
[TiDB #69821](https://github.com/pingcap/tidb/issues/69821) carries `severity/critical` and
`found-by-ai`.

## Starting proof obligation

The transaction campaign started from `TXN_COMMIT_OUTCOME_TERMINAL_TRUTH`:

```text
P: a lower layer records that a primary Commit RPC may already have applied
Q: every public terminal path preserves that uncertainty
F: a specialized path bypasses the semantic promotion and publishes an ordinary failure
```

This was generated from current source. PRs, issues, and historical fixes stayed closed until the
RED existed.

## Why the candidate ranked first

The canonical ordinary-2PC path supplied a compact specification:

```text
Commit RPC ambiguity -> set undeterminedErr -> commitTxn promotes ErrResultUndetermined
```

The pipelined sibling shared the producer but not the finalizer:

```text
Commit RPC ambiguity -> set undeterminedErr -> commitFlushedMutations returns raw error
```

The deferred cleanup path also reads `undeterminedErr` and suppresses rollback. That sibling
consumer is strong internal evidence that the system itself no longer believes the raw error means
definite abort.

## Minimal matrix

1. Current source plus embedded store: precise post-success-response loss produces a raw
   `epoch_not_match`; RED on error class.
2. Current source plus real TiKV: fresh transaction sees the committed value, while Commit returns
   an ordinary transport error; strong RED.
3. Exact promotion counterfactual plus embedded store: typed undetermined; GREEN.
4. Exact promotion counterfactual plus the same real TiKV: durable value unchanged and typed
   undetermined; GREEN.
5. Testbed gate: global/session MDL are `ON`; default `tidb_dml_type` is `STANDARD`, so only the
   target session opts into the pipelined path.

## Injection lesson

"Drop the first response" was an invalid selector. The first Commit response may be
`CommitTsExpired` or another rejection, which proves no durable apply. The corrected fault waits for
a successful Commit response with no region/key error, drops exactly that response, and then keeps
the retry channel unavailable. Fault selection must be semantic, not ordinal.

## Reusable selector

`SIDE_STATE_SEMANTIC_PROMOTION_BYPASS` applies when:

1. a child operation records ambiguity or another semantic fact in side state;
2. the canonical finalizer converts raw errors using that state;
3. a specialized path invokes the child directly and returns its raw error;
4. another consumer still acts on the side state, proving it remains authoritative;
5. the public terminal class changes retry, connection, cleanup, or durable behavior.

Search by differential owner graph, not by error strings. The smallest counterfactual restores only
the missing promotion at the specialized terminal boundary.

## Method value

This is the first S48 current-source severe hit after several transaction negatives. It validates
the evolving LOOP: a broad terminal-truth obligation selected a canonical/specialized path
differential; a bounded source packet proposed one exact candidate; a semantic fault plus fresh
MVCC read made it RED; the exact promotion made it GREEN. The selector and fault calibration now
compound into the next module instead of being remembered only as this bug.
