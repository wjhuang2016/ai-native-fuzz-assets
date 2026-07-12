# CRR resume state is not bound to a replication lineage

Status: confirmed on current master `13282a8bd06b`; remote bug DB `id1860003`, high severity.

## User impact

If an operator reuses a downstream log-backup bucket for a new upstream cluster or a recreated
log-backup task, the old `crr-checkpoint/resume-state.json` can survive. CRR may report the old
checkpoint as safe even when the current upstream task is behind it. PITR prefers that value as
`logMaxTS`; with no explicit restore TS it also becomes the default target.

The missing interval is indistinguishable from an interval with no writes. The restore precheck can
therefore accept a target that the current replication lineage never proved recoverable.

## Source proof

- `PersistentState` contains only `LastCheckpoint`, `SyncedTS`, and `SyncedByStore`.
- `storageResumeStateStore` always uses `crr-checkpoint/resume-state.json` in downstream storage.
- `RestorePersistentState` accepts the values without a cluster, task-generation, or storage check.
- If current upstream checkpoint is not greater than restored `LastCheckpoint`,
  `ComputeNextCheckpoint` returns the restored value without metadata listing or object checks.
- `getMaxRecoverableCheckpointFromStorage` prefers the resume file over storage checkpoints.

## Local matrix

| Cell | Result |
| --- | --- |
| old lineage state 100 + current upstream 10 | RED: returns 100; object checks = 0 |
| same lineage state 100 + current upstream 100 | GREEN: returns 100 |
| no resume state + current upstream 10 | GREEN: returns 10 |
| restore consumer with resume 100 + storage checkpoint 10 | RED: claims 100 |

## Fix direction

Persist a lineage fingerprint containing at least upstream cluster ID, task identity/generation, and
normalized upstream/downstream storage identity. Validate it before restoring progress. A mismatch or
checkpoint regression must fail closed or discard and rebuild state; silently trusting the old token
is unsafe.

Discovery did not use PR review findings or issue reproductions. GitHub issue searches ran only after
the independent RED and found no exact match.
