INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,notes)
VALUES
(2790003,
 'Snapshot apply can lose restored Default CF data after lower-level GC compaction',
 'high',
 'storage/data-integrity',
 'Raft snapshot apply / GC compaction',
 'snapshot cleanup / compaction-filter GC / RocksDB levels',
 'A peer can finish snapshot apply and initially read a restored row, then permanently lose its long Default CF value after background compaction; the Write record remains and reads return DefaultNotFound.',
 'On TiKV master 67fccdb16, write long Put@21, Put@81, Delete@101 and place them in L5. Run the production snapshot cleanup strategy DeleteByWriter with allow_write enabled, fully reapply snapshot Write@21 and Default@20, and verify a fresh MVCC read succeeds. Advance safe point to 120 and compact only the old L5 Write SST to L6. The attached Rust probe deterministically executes this schedule.',
 'After snapshot apply completes, later compaction must preserve Write/Default closure for every restored MVCC value, regardless of which RocksDB level is selected.',
 'After lower-level GC, physical state is Write@21=true and Default@20=false. A fresh MVCC read returns KV:Storage:DefaultNotFound. A full-input compaction control keeps both keys and the read succeeds.',
 'WriteCompactionFilter proves that Put@21 is stale only inside its selected lower-level SST input, but promotes that subset-local fact into a global point deletion of Default@20. Newer snapshot-cleanup tombstones and the fully restored Write@21 are outside the input. The ingest latch serializes individual writes but does not make newer layers visible to future lower-level compactions.',
 'Make cross-CF GC side effects depend on globally authoritative Write state, for example through a recovery generation/barrier, compaction closure over overlapping old files before snapshot completion, or revalidation against newer excluded Write state. Extending only the per-ingest latch is insufficient.',
 'SNAPSHOT_MVCC_READ_PLUS_PHYSICAL_WRITE_DEFAULT_CLOSURE',
 'SUBSET_READ_CROSS_CF_SIDE_EFFECT_CLOSURE',
 'snapshot-cleanup-tombstones-excluded-from-cross-cf-gc',
 'TiKV current master 67fccdb16; default use-delete-range=false and enable-compaction-filter=true; long values in Default CF',
 0,
 'candidate',
 'Discovered from current-source proof obligations without PR/review findings. RED uses real RocksDB and the production DeleteByWriter cleanup API; GREEN changes only compaction input closure. The physical mechanism is confirmed, but ordinary peer remove/re-add does not prove the required rollback-shaped same-Write history is production reachable. Retain as a high-impact candidate pending a legal cluster-level timeline.');
