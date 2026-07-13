# Pipelined DML can return a definite failure after its primary commit is durable

Status: confirmed on current upstream client-go `01bd8f99f4da` with current nightly TiKV
`7ecce12e7573`; remote `found_bug id2340003`. The authorized testbed `8220955` was used only to
confirm the product gate: global and session metadata locking are both `ON` by default.

## User-visible consequence

For an autocommit `INSERT`, `REPLACE`, `UPDATE`, or `DELETE` executed with
`tidb_dml_type='BULK'`, TiKV can durably commit the primary key and lose the successful Commit
response. Pipelined DML then returns an ordinary transport or region error instead of
`ErrResultUndetermined`. TiDB therefore does not run its undetermined-result connection-close path.
An application can treat the operation as a definite failure and retry a non-idempotent business
operation, causing duplicate ledger entries, repeated balance changes, or repeated inventory
updates even though the first operation already committed.

The impact is critical data integrity. Trigger frequency is lower: bulk DML is opt-in and the
network failure must land after primary apply but before the response reaches client-go.

## Exact trigger

1. Metadata locking remains enabled. TiDB requires `TxnCtx.EnableMDL` before it selects pipelined
   DML.
2. The session explicitly enables bulk DML, and an eligible autocommit DML reaches client-go's
   pipelined transaction path.
3. TiKV successfully applies the primary Commit request.
4. The successful response is lost, and subsequent Commit retries cannot obtain a decisive result
   before the caller context/backoff ends.

The probe does not drop the first Commit response blindly. It forwards error responses such as
`CommitTsExpired` and drops only the first response with no region or key error. All later Commit
RPCs fail, which models a response loss followed by a short transport outage.

## Strong RED

The reusable probe is
`scaffolds/client-go-tests/pipelined_undetermined_probe_test.go`.

On current upstream client-go plus current nightly real TiKV:

```text
successful primary Commit response observed and replaced with an RPC error
fresh transaction read: durably-committed
Commit result: injected commit transport outage
IsErrorUndetermined: false
```

The fresh transaction read happens after the injecting client is removed, so the durable oracle is
owned by real TiKV, not by the mock. Evidence:

- `assets/store/logs/txn-pipelined-undetermined-current-head-realtikv-red.log`
- `assets/store/logs/txn-pipelined-undetermined-current-head-local-red.log`

## Root cause

`actionCommit.handleSingleBatch` records a lost primary response in
`twoPhaseCommitter.undeterminedErr`. Ordinary 2PC returns through `commitTxn`, which checks that side
state and promotes the raw error to `ErrResultUndetermined`. Pipelined DML has a dedicated terminal
path: `execute` calls `commitFlushedMutations`, which called `commitMutations` and directly returned
its raw error. It bypassed the semantic promotion owner while the surrounding deferred cleanup code
still observed `undeterminedErr` and correctly avoided unsafe rollback.

TiDB maps only typed `ErrResultUndetermined` to its SQL-level undetermined error and closes the
connection only for that error. The raw transport/region error therefore escapes as a definite
ordinary failure.

## Counterfactual

The minimal counterfactual checks `getUndeterminedErr()` at the pipelined terminal boundary and
returns `ErrResultUndetermined`, matching ordinary 2PC. It changes no request, retry, cleanup, or
durable-state behavior. The same local and real-TiKV probes pass, while the fresh transaction still
observes the committed value.

Patch: `scaffolds/client-go-tests/pipelined_undetermined_promotion_counterfactual.patch`.

## Severity and stop rule

The internal bug database uses `severity=high`, consistent with the existing catalog, and records
`critical data-integrity consequence` separately. Do not enumerate error strings, context lengths,
key counts, or DML forms. Reopen only for another specialized terminal path that bypasses a semantic
promotion owner or for a different terminal classification.
