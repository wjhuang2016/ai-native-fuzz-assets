# id1800003: cancelled table placement DDL leaves the uncommitted PD rule active

## Status

- Severity: high
- Status: issue-filed: https://github.com/pingcap/tidb/issues/69784
- Root cause ID: `table-placement-pd-bundle-before-ddl-commit`
- Affected path: nonpartition `ALTER TABLE ... PLACEMENT POLICY`

## User-visible symptom

An operator cancels an `ALTER TABLE ... PLACEMENT POLICY` job and receives `ERROR 8214 Cancelled
DDL job`. DDL history is `cancelled`, and `SHOW CREATE TABLE` still declares the old policy. Despite
that terminal result, PD keeps the new policy's placement bundle and schedules the table with the
uncommitted replica count.

The real-PD reproduction used an old policy with `FOLLOWERS=2` and a new policy with
`FOLLOWERS=1`. After cancellation, TiDB still declared the three-voter policy while PD actively
held a two-voter rule. This silently weakens the table's declared replica redundancy.

## Proof obligation

`onAlterTablePlacement` stages the new `PlacementPolicyRef` and schema version in the DDL worker
transaction. Before that transaction and job state are durable, it calls
`PutRuleBundlesWithDefaultRetry(context.TODO(), ...)`. A later supported cancellation can abort the
local transaction, but generic cancellation has no edge that restores the old PD bundle.

The false implication is:

```text
PD accepted p2 before local commit
AND local DDL later returned cancelled
THEREFORE metadata and PD both remain on p1
```

The last step is false.

## Deterministic reproduction

1. Create policies p1 (`FOLLOWERS=2`) and p2 (`FOLLOWERS=1`).
2. Create a nonpartition table using p1 and record its table ID and PD bundle.
3. Pause immediately after `PutRuleBundlesWithDefaultRetry` returns success.
4. Start `ALTER TABLE t PLACEMENT POLICY p2`.
5. Verify PD already changed from voter count 3 to 2.
6. Run `ADMIN CANCEL DDL JOBS <job_id>` from another session.
7. Release the worker and observe the ALTER result, DDL history, `SHOW CREATE TABLE`, and PD group
   `TiDB_DDL_<table_id>`.

Observed on testbed 8220955:

```text
job 5369: cancelled
ALTER: ERROR 8214 Cancelled DDL job
SHOW CREATE: p1
PD TiDB_DDL_5367: voter count 2 (p2)
```

## Controls

- Normal ALTER on table 5370 finished as job 5372; metadata and PD both became p2/count 2.
- The local mock-PD schedule reproduced the same split with region r1/r2.
- Republish the committed InfoSchema bundle after the same cancellation and both owners return to
  p1. This changes only the missing compensation edge.

## Root cause and fix direction

The PD placement-rule store and the DDL metadata transaction are separate durable owners. The code
publishes to PD while the local owner is still abortable and does not retain a compensation or
reconciliation obligation.

A robust fix should use a durable intent/reconciler or guarantee that every post-publication abort
restores the bundle derived from committed metadata. Merely moving the RPC can invert the failure
window, so the invariant should be owner convergence, not a one-off call reorder.

## Discovery provenance

The candidate came from the current-source `EXTERNAL_EFFECT_PRECOMMIT_ROLLBACK_COHERENCE`
selector. Local mock-PD and real-PD RED were completed before any upstream search. Post-RED private
asset and issue searches found no exact root. No PR review finding was used.
