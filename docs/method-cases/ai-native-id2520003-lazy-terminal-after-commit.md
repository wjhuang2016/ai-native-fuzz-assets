# id2520003: irreversible commit before a lazy terminal check

Remote bug DB: `found_bug id2520003`. Upstream: [TiDB #69829](https://github.com/pingcap/tidb/issues/69829).

## Proof obligation

`EXPLAIN ANALYZE` DML has a split lifetime: the inner DML is executed eagerly, while the explain
rows remain a lazy `RecordSet`. TiDB commits the autocommit transaction between those two phases.
The proof obligation is therefore not merely "commit succeeded". Every later operation that can
still determine the public statement result must be infallible, classified as undetermined, or
ordered before commit.

The violated implication was:

```text
P: CommitTxn succeeded before the explain RecordSet was returned.
Q: The remaining lazy result path cannot return a definite statement failure.
F: recordSet.Next consumes SQLKiller before generating the first explain chunk.
```

## Minimal matrix

| Commit location | Same QueryInterrupted signal | Client result | Fresh value | Verdict |
| --- | --- | --- | --- | --- |
| autocommit, before first `Next` | after `ExecuteStmt`, before `Next` | error 1317 | `1` | RED |
| explicit transaction, after result boundary | before first `Next`, then rollback | error 1317 | `0` | GREEN |

The key is to inject the production signal through `SQLKiller`, not invent a synthetic rendering
error. The fresh-session row is the durable oracle; the client error is the terminal oracle.

## Why this was found quickly

The scan started from specialized finalizers rather than SQL syntax. It looked for a non-nil lazy
result returned by a statement that had already crossed `CommitTxn`. That reduced the source search
to four ownership points: eager DML execution, irreversible commit, first lazy `Next`, and server
terminal publication. Intersecting the last two exposed the SQLKiller check immediately.

This is a distinct root from id2340003. The pipelined-DML bug lost an undetermined-error promotion
inside a specialized client-go commit finalizer. Here TiDB itself orders an ordinary, fallible lazy
consumer after a successful commit. The shared invariant is terminal truth versus durable truth;
the owners and fixes differ.

## Method improvement

Add `IRREVERSIBLE_EFFECT_BEFORE_LAZY_TERMINAL_CHECK` after intermediate-publication scans:

1. Enumerate statements that execute an effect eagerly but return a lazy result, iterator, stream,
   encoder, or close finalizer.
2. Locate `Commit`, `Publish`, external apply, or durable checkpoint before the first lazy consumer.
3. Enumerate every remaining cancellation, render, encode, retry-classification, and cleanup error.
4. Inject the real signal at the exact boundary and compare the client terminal class with fresh
   durable state.
5. Admit only `definite error + durable effect` or `success + absent effect`; classify explicit
   undetermined outcomes separately.

Ranking must include frequency. This root has critical terminal-integrity consequences but a lower
frequency because it needs `EXPLAIN ANALYZE` DML and a narrow late-cancellation window. The selector
should next target ordinary DML/RETURNING or import/stream paths with wider post-effect lazy work.

## Negative calibration: IMPORT INTO terminal job lookup

The same source shape does not by itself prove a bug. `IMPORT INTO` waits for a distributed task to
finish and then calls `fillJobInfo` with the original request context. A source-only pass suggested
that cancellation after task success might make the SQL fail after imported rows were durable.

A deterministic real-TiKV probe cancelled that context exactly after `waitTask` observed task
success and before `fillJobInfo`. The imported rows were durable, but the SQL still returned success:
the internal job lookup did not promote the cancelled context into a public error on this path.
This candidate is retired.

Add a fallibility reachability gate before building a full RED:

```text
post-effect call accepts an error-capable input
  is not enough

require:
  supported producer -> exact consumer -> public terminal error
```

For context and killer candidates, probe the concrete consumer first. Do not infer public
cancellation behavior merely because a function accepts `context.Context`.

## Terminal rule

This owner is terminal. Do not enumerate INSERT/DELETE forms, kill sources, explain formats, delays,
or signal timings. Reopen only for a different eager-effect owner or lazy terminal consumer.
