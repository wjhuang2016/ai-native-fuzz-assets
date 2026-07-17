# Snapshot apply can lose restored Default CF data after lower-level GC compaction

Status: storage mechanism confirmed on current TiKV master `67fccdb16f5517e96a53c968879f8e5d99bcf1b3`, but production reachability is not confirmed. Bug DB ID `id2790003` is a `high/candidate` with `confirmed=0`.

## User-visible failure

For the injected physical history, a TiKV peer can finish applying a snapshot and initially read the restored row correctly. A later background compaction permanently deletes the row's long value from Default CF while its Write CF record remains. Reads then return `KV:Storage:DefaultNotFound`.

The damage is persistent RocksDB state, not a transient read race: the RED physical oracle is `Write@21=true, Default@20=false`.

## Required trigger shape

The RED requires all of the following:

1. The peer's local RocksDB contains a key with a long `Put@21`, a later `Put@81`, and `Delete@101` in a lower level such as L5.
2. The peer must apply a snapshot that makes the exact old `Write@21(start_ts=20)` live again while omitting the later local versions.
3. With the default `raftstore.use-delete-range=false`, `clean_overlap_ranges` clears the local range through `DeleteStrategy::DeleteByWriter`. The deletion entries are newer than the old L5 data and hide it, but do not necessarily compact it away.
4. Snapshot apply restores Default CF and Write CF. A fresh read succeeds at this point.
5. The GC safe point later advances beyond the removed local history.
6. RocksDB selects an L5-to-L6 compaction without the idle overlapping L0 cleanup/reapply files. The level picker permits L1+ scored compactions independently of idle L0 files.
7. `WriteCompactionFilter` sees `Delete@101` and the older long `Put@21`, concludes that the Put is stale, and writes a point deletion for `Default@20`.
8. The restored `Write@21`, outside the compaction input, remains live. Subsequent reads return `DefaultNotFound`.

The storage behavior uses default settings:

- `raftstore.use-delete-range=false`
- `gc.enable-compaction-filter=true`
- MDL is unrelated and can remain enabled

The value must be large enough to live in Default CF instead of being embedded as a short value in Write CF.

## Production reachability gap

Ordinary peer removal and re-addition is not yet a valid witness for step 2. Raft snapshot application is state-forward: a snapshot for the current region normally contains the same or newer logical state, rather than resurrecting an exact MVCC Write that local committed history had already superseded. Recreating the same user value with a later transaction does not reuse the same Default key because its start TS changes.

Therefore the current probe proves a storage-engine failure mechanism, not that routine balancing reaches it. A legitimate production trigger would need a state rollback or recovery timeline, or another legal lifecycle that places a resurrected identical Write over higher stale local history. That timeline has not yet been demonstrated end to end.

The test also controls the LSM layout and sets the ratio threshold to zero for determinism. The threshold is not the main reachability gap because bottommost compaction runs GC under the default ratio, and the selected lower-level layout is RocksDB-valid. The unresolved condition is the logical origin of the same-Write history.

## Expected behavior if the state is reachable

Once snapshot apply completes, later GC and compaction must preserve every MVCC value present in that snapshot. A compaction input that excludes newer cleanup/reapply state must not perform a global cross-CF delete based only on the older selected files.

## Actual storage behavior

The probe first proves that the completed snapshot state is readable. After selecting only the old L5 Write SST for GC:

```text
oracle: after complete snapshot reapply
physical oracle: write@21=true, default@20=false
oracle: after lower-level GC
Error(DefaultNotFound { key: ... })
```

## Reproduction

Apply [the regression probe](../../scaffolds/tikv-tests/ai_native_snapshot_cleanup_compaction_filter_test.rs) to the TiKV crate and run:

```bash
cargo test -p tikv --lib \
  test_snapshot_reapply_then_lower_compaction_keeps_reapplied_value \
  --features test-engine-kv-rocksdb -- --nocapture
```

The probe uses the production `DeleteStrategy::DeleteByWriter` cleanup API with `allow_write_during_ingestion=true`, completes both Default and Write reapply before compaction, and then invokes the real TiKV compaction filter over real RocksDB files.

## RED/GREEN matrix

| Cell | Compaction input | Physical result | MVCC result |
| --- | --- | --- | --- |
| Current RED | old L5 Write SST only | `Write@21=true`, `Default@20=false` | `DefaultNotFound` |
| Input-closure GREEN | full range including L0 cleanup/reapply | `Write@21=true`, `Default@20=true` | restored value readable |

The data, safe point, target level, cleanup strategy, and filter code are identical. Only input closure changes.

## Root cause

The filter proves a subset-local fact and publishes a global side effect:

- P: inside the selected lower-level files, `Put@21` is older than `Delete@101`;
- Q: `Default@20` is globally unreferenced and can be deleted;
- missing proof: newer files outside the compaction input contain cleanup tombstones and a restored `Write@21` that still references `Default@20`.

The ingest range latch only serializes the direct delete with an individual ingestion call. It does not make cleanup tombstones part of every future lower-level compaction input and cannot close this post-apply schedule.

## Fix direction

If a legal production timeline is found, the fix must make the global side effect depend on globally authoritative state. Possible designs include establishing a recovery generation/barrier that compaction-filter GC observes, compacting cleanup state through all overlapping old files before the snapshot is declared complete, or revalidating the Default deletion against newer Write state outside the compaction input. Until then, this remains a candidate and should not be filed as a confirmed product bug.

## Deduplication

- TiKV #13448 described the same physical visibility family for flashback and explicitly mentioned `reset-to-version`; PR #13450 changed online flashback to MVCC overwrites. It did not cover snapshot cleanup.
- TiKV #18081 / PR #18096 added an ingest range latch for concurrent compaction-filter writes. Its tests assert mutual blocking, not durable Write/Default closure after snapshot completion.
- Post-RED searches found no exact open or closed TiKV/TiDB issue for snapshot cleanup followed by a lower-level-only compaction.
