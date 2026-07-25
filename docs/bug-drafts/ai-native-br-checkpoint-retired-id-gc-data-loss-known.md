# BR checkpoint resume can reconnect a restored table to an old GC delete range

Status: independently reproduced on current master code, then deduplicated to
[TiDB #68709](https://github.com/pingcap/tidb/issues/68709). The upstream issue is labeled
`severity/critical`. This entry is a current-master validation and method asset, not a new root.

## User-visible failure

A checkpoint-enabled BR restore is interrupted after some physical ranges finish. An operator
deletes the partial target and reruns the same restore command. BR reuses the checkpoint's
preallocated TableID, skips completed ranges, and reports `Table Restore success`.

The DROP still owns a `mysql.gc_delete_range` entry for that TableID. When the normal GC safe point
passes the DROP timestamp, TiKV runs `UnsafeDestroyRange` against the live restored table. In the
local RED, a table that had 128,000 rows immediately after BR success became empty. Primary and
unique-index scans both returned zero, and `ADMIN CHECK TABLE` still passed because records and
indexes disappeared together.

## Production trigger

1. Restore a large table with BR checkpointing enabled.
2. The process or host is interrupted after at least one range is durable in the checkpoint.
3. Cleanup automation or an operator drops the partial target database or table.
4. The same BR command resumes from the retained checkpoint.
5. The default GC lifecycle reaches the old DROP task.

No failpoint, source patch, multi-TiDB race, MDL change, or application DML is required. The local
test only shortened the wait after proving that the pending delete range exactly matched the current
TableID. Production reaches the same consumer after the normal GC lifetime.

## Current-master owner chain

- `CheckpointMetadataForSnapshotRestore.PreallocIDs` stores an ID interval and source-ID hash.
- `checkPreallocIDReusable` validates the command hash, not the downstream target generation.
- `ReuseCheckpoint` rebuilds the fixed source-to-target ID mapping.
- BR table creation uses `WithIDAllocated` and `OnExistIgnore`.
- Checkpoint ranges are keyed by the downstream physical TableID.
- DROP registers that physical prefix in `mysql.gc_delete_range`.
- The GC worker later sends `UnsafeDestroyRange` to TiKV.

The relevant files are unchanged between BR runtime `a942e4684f` and current master
`05b396fb66`.

## Minimal RED

The execution used a 74.62 MB backup split into four restore groups and one TiKV restore worker.
After one durable checkpoint segment:

```text
partial target TableID: 1648
DROP cleanup range:      t1648 .. t1649
resumed target TableID: 1648
BR result:               success
checkpoint skipped:      59,788 KV / 32.7 MB
rows before GC:          128,000
rows after GC:           0
```

Use a changed aggregate expression or point gets for the post-GC oracle. Repeating the exact
pre-GC aggregate can briefly hit a stale coprocessor cache entry after physical range destruction.

## Control

After discarding the checkpoint, a fresh restore of the same backup allocated TableID 1669. The
old cleanup remained scoped to 1648, no pending cleanup covered the current table, all 128,000 rows
were present, and `ADMIN CHECK TABLE` passed.

## Fix direction

Checkpoint admission must bind every preallocated downstream ID to its current target generation
and delayed cleanup owners. If a target is absent, retired, generation-mismatched, or covered by a
pending cleanup range, BR should invalidate completed-range checkpoints and allocate a fresh ID.

Evidence:

- `assets/store/logs/br-checkpoint-retired-id-interrupt-20260725.log`
- `assets/store/logs/br-checkpoint-retired-id-resume-success-20260725.log`
- `assets/store/logs/br-checkpoint-retired-id-gc-oracle-20260725.txt`
- `assets/store/logs/br-checkpoint-retired-id-fresh-control-20260725.log`
