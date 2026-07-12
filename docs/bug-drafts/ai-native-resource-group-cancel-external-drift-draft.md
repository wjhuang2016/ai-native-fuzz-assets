# Cancelled ALTER RESOURCE GROUP can leave its configuration active in PD

## Summary

`onAlterResourceGroup` stages the new resource-group definition in the DDL worker transaction, then
calls PD `ModifyResourceGroup` before the transaction publishes its schema version and final job
state. `ALTER RESOURCE GROUP` is still rollbackable at this point.

If another session cancels the DDL after the PD call, the cancellation updates the job row and the
worker transaction loses with a real write conflict. The generic rollback path marks the DDL job
cancelled but has no compensation for PD. SQL metadata remains old while the runtime resource
manager keeps the cancelled definition.

Severity: **High**. Resource limits, priority, and runaway actions can change even though the DDL
returns `Cancelled DDL job` and job history says `cancelled`.

Bug library: `id1710003` (`confirmed`, root cause
`resource-group-external-effect-before-ddl-commit`).

## Current-source proof

- `pkg/ddl/resource_group.go:111-139` stages `metaMut.UpdateResourceGroup`, commits
  `infosync.ModifyResourceGroup` to PD, and only then updates the schema version and finishes the job.
- `pkg/ddl/job_worker.go:591-684` keeps the metadata/job update in the DDL worker transaction; a
  concurrent job-row mutation invalidates its commit.
- `pkg/meta/model/job.go:810-856` leaves `ActionAlterResourceGroup` rollbackable.
- `pkg/ddl/rollingback.go:605-660` has no resource-group rollback case, so the default only marks the
  job cancelled.
- `pkg/executor/show.go:1773-1778` renders `SHOW CREATE RESOURCE GROUP` from InfoSchema metadata.
- `pkg/executor/infoschema_reader.go:3915` reads `INFORMATION_SCHEMA.RESOURCE_GROUPS` from the PD
  resource-manager client.

```text
P: DDL metadata and job publication are owned by one worker transaction.
Q: cancelling that transaction means the resource-group change did not happen.
F: PD is changed before commit, but cancellation has no external compensation or reconciliation.
```

## Deterministic reproduction

Use a test-only pause immediately after successful `infosync.ModifyResourceGroup`:

1. create `ai_rg_real` as `RU_PER_SEC=1000 PRIORITY=LOW`;
2. start `ALTER RESOURCE GROUP ai_rg_real RU_PER_SEC=1 PRIORITY=HIGH`;
3. pause after PD accepts the new definition;
4. from another session run `ADMIN CANCEL DDL JOBS <job_id>`;
5. release the worker and inspect the ALTER result, history, metadata view, and runtime view.

Real PD result:

```text
ADMIN CANCEL: successful
ALTER: ERROR 8214 Cancelled DDL job
DDL history: state=cancelled
SHOW CREATE RESOURCE GROUP: RU_PER_SEC=1000, PRIORITY=LOW
INFORMATION_SCHEMA.RESOURCE_GROUPS: RU_PER_SEC=1, PRIORITY=HIGH
```

The worker log records a write conflict on the same `mysql.tidb_ddl_job` row, then the generic
`the DDL job is cancelled normally` path.

## Control

After disabling the pause, a normal ALTER to `RU_PER_SEC=2000 PRIORITY=HIGH` completes and both
`SHOW CREATE` and the PD-backed runtime view report `2000/HIGH`.

## Counterfactual

The consequence becomes impossible if external publication occurs only after durable DDL metadata
publication, or if cancellation/commit failure compensates PD from persisted old state. Keeping the
same user cancellation while removing the precommit external effect leaves both owners at the old
definition.

## Fix direction

Treat TiDB metadata and PD resource-group state as a recoverable two-owner transaction. Viable
designs include post-commit external publication plus reconciliation, or a persisted intent/old
definition that cancellation and owner recovery use to compensate PD. A regression test must cancel
after the real PD mutation and compare both SQL owners; checking only DDL history is insufficient.

PR, issue, and history sources were excluded from discovery. Post-hit searches found no duplicate.
