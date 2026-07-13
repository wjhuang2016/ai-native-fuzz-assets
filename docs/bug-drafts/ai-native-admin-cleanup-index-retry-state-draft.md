# ADMIN CLEANUP INDEX retains failed-attempt batch state across retry

## Summary

`ADMIN CLEANUP INDEX` uses `kv.RunInNewTxn(..., true, callback)` for each batch. The callback mutates
`CleanupIndexExec` fields before Commit. If Commit returns a retryable error, KV changes roll back
but the receiver fields do not.

On current master `13282a8bd06b`:

- 3 dangling index entries plus one Commit retry are repaired, but the command reports 9 removals;
- 20001 entries plus the same retry fail with `index out of range [20000] with length 20000`.

## Source chain

- `pkg/executor/admin.go:778`: retryable `RunInNewTxn` callback.
- `pkg/executor/admin.go:690`: `fetchIndex` advances `lastIdxKey` and `scanRowCnt`, and populates
  `idxValues`/`idxValsBufs`.
- `pkg/executor/admin.go:634`: `batchGetRecord` appends to `batchKeys`.
- `pkg/executor/admin.go:646`: `deleteDanglingIdx` increments `removeCnt` before Commit.
- `pkg/kv/txn.go:107`: retry opens a new transaction but does not restore callback-owned memory.

## Realistic trigger family

The deterministic oracle uses TiDB's existing `mockCommitErrorInNewTxn=retry_once` failpoint. A
natural equivalent is a write conflict on a dangling index key after the cleanup callback has staged
its delete, such as overlapping cleanup/repair activity on the same inconsistent index. This round
did not reproduce that race on a live cluster, so the deterministic source-level proof is the
current evidence boundary.

## Expected behavior

Every attempt starts from the same committed batch frontier and empty attempt-local buffers. A
retry must not change the reported removal count or cause a panic.

## Fix direction

Keep batch state local to the callback and publish it only after Commit, or restore `lastIdxKey`,
`scanRowCnt`, `batchKeys`, `idxValues`, and `removeCnt` to the committed batch state at every callback
entry.
