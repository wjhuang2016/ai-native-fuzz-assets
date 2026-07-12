# BR can report backup success when scheduler removal fails

## Summary

Several BR top-level operations capture PD scheduler-removal failure in `e`, check `e != nil`, but
return `errors.Trace(err)`. The outer `err` belongs to an earlier successful setup step and is
normally nil. BR therefore returns success before backup or restore work starts.

Severity: **High**. A command-level real-TiKV run exited 0 and produced no `backupmeta`. Backup
automation that trusts the process status can publish a nonexistent backup as successful.

Bug library: `id1680003` (`confirmed`, root cause
`br-scheduler-removal-stale-error-false-success`).

## Current-source proof

- `br/pkg/task/backup.go:535-549`
- `br/pkg/task/backup_raw.go:165-178`
- `br/pkg/task/backup_txn.go:152-165`
- `br/pkg/task/backup_ebs.go:167-172`
- `br/pkg/task/restore_data.go:105-110`

All five surfaces have the same proof shape:

```text
P: scheduler-removal failure is captured and checked as e != nil
Q: the checked branch must terminate the BR operation with a nonzero error
F: the branch returns stale outer err, commonly nil, before the irreversible action
```

The representative `RunBackupTxn` branch precedes `BackupRanges`, `FinishWriteMetas`, and
`FlushBackupMeta`.

## Reproduction

On a local one-PD/one-TiKV playground at source commit
`13282a8bd06bd33324a4dbfd3c1c03685f3cd9aa`, inject a sentinel error from
`PdController.RemoveSchedulers`, then run:

```bash
GO_FAILPOINTS='github.com/pingcap/tidb/br/pkg/pdutil/aiNativeSchedulerRemovalError=return()' \
  bin/br backup txn \
  --pd 127.0.0.1:2379 \
  --storage local:///private/tmp/ai-native-br-scheduler-red \
  --remove-schedulers \
  --check-requirements=false
```

Current result:

```text
exit_code=0
Txn Backup failed summary
backup directory contains no files
backupmeta absent
```

## Controls

The same command without the injected failure exits 0, reports `Txn Backup success summary`, and
writes a 285-byte `backupmeta`.

Changing only `return errors.Trace(err)` to `return errors.Trace(e)` and retaining the injected
failure exits 1 with `ai-native injected scheduler removal error`; no backup artifact is written.
This counterfactual isolates the stale error identity as the cause of false success.

## User-visible trigger

A real trigger is a transient or persistent PD API failure while BR is removing schedulers, for
example an unavailable PD endpoint, authorization/configuration rejection, or a failed scheduler
update after earlier BR setup succeeded. EBS backup uses scheduler removal by default; txn/raw/full
backup expose the same root when scheduler removal is enabled. An orchestrator may mark the backup
successful and advance retention or disaster-recovery state although no usable backup exists.

## Fix direction

Return the checked error `e` in all five branches. Add a command-level regression oracle that checks
both process status and the expected artifact/action. A failed summary or log line is not sufficient
when the external terminal status remains success.

History and upstream issues were excluded from discovery. Three post-hit GitHub searches found no
matching issue or pull request.
