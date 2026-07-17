// Regression probe for TiKV master 67fccdb16.
//
// Add this module under the tikv crate (for example at the end of
// src/server/gc_worker/compaction_filter.rs) and run:
// cargo test -p tikv --lib \
//   test_snapshot_reapply_then_lower_compaction_keeps_reapplied_value \
//   --features test-engine-kv-rocksdb -- --nocapture

#[cfg(test)]
mod ai_native_snapshot_cleanup_compaction_filter_test {
    use engine_traits::{
        CF_DEFAULT, CF_WRITE, DeleteStrategy, MiscExt, Peekable, Range, WriteBatch,
        WriteBatchExt, WriteOptions,
    };
    use txn_types::{Key, Write, WriteType};

    use crate::{
        config::DbConfig,
        server::gc_worker::compaction_filter::test_utils::{TestGcRunner, rocksdb_level_files},
        storage::{
            kv::TestEngineBuilder,
            mvcc::tests::must_get,
            txn::tests::{must_commit, must_prewrite_delete, must_prewrite_put},
        },
    };

    #[test]
    fn test_snapshot_reapply_then_lower_compaction_keeps_reapplied_value() {
        let mut cfg = DbConfig::default();
        cfg.writecf.disable_auto_compactions = true;
        cfg.writecf.dynamic_level_bytes = false;
        let dir = tempfile::TempDir::new().unwrap();
        let mut engine = TestEngineBuilder::new()
            .path(dir.path())
            .build_with_cfg(&cfg)
            .unwrap();
        let raw_engine = engine.get_rocksdb();
        let key = b"z-snapshot-key";
        let value_20 = vec![b'b'; 512];
        let value_80 = vec![b'c'; 512];

        must_prewrite_put(&mut engine, key, &value_20, key, 20);
        must_commit(&mut engine, key, 20, 21);
        must_prewrite_put(&mut engine, key, &value_80, key, 80);
        must_commit(&mut engine, key, 80, 81);
        must_prewrite_delete(&mut engine, key, key, 100);
        must_commit(&mut engine, key, 100, 101);
        raw_engine.flush_cf(CF_WRITE, true).unwrap();
        raw_engine.flush_cf(CF_DEFAULT, true).unwrap();

        // Keep the old local history below L0.
        let mut gc_runner = TestGcRunner::new(1);
        gc_runner.target_level = Some(5);
        gc_runner.gc(&raw_engine);

        // Production snapshot cleanup uses this strategy by default because
        // raftstore.use-delete-range defaults to false.
        let cleanup_dir = tempfile::TempDir::new().unwrap();
        let cleanup_sst = cleanup_dir.path().join("cleanup.sst");
        let end = [0xff];
        let ranges = [Range::new(b"", &end)];
        fail::cfg("manually_set_max_delete_count_by_key", "return").unwrap();
        raw_engine
            .delete_ranges_cfs(
                &WriteOptions::default(),
                DeleteStrategy::DeleteByWriter {
                    sst_path: cleanup_sst.to_string_lossy().into_owned(),
                    allow_write_during_ingestion: true,
                },
                &ranges,
            )
            .unwrap();
        fail::remove("manually_set_max_delete_count_by_key");

        // Complete the snapshot reapply before any damaging compaction. This
        // deliberately avoids relying on a partial CF ingestion window.
        let write_key = Key::from_raw(key).append_ts(21.into()).into_encoded();
        let default_key = Key::from_raw(key).append_ts(20.into()).into_encoded();
        let write = Write::new(WriteType::Put, 20.into(), None);
        let mut wb = raw_engine.write_batch();
        wb.put_cf(CF_DEFAULT, &default_key, &value_20).unwrap();
        wb.put_cf(CF_WRITE, &write_key, &write.as_ref().to_bytes())
            .unwrap();
        wb.write().unwrap();
        raw_engine.flush_cf(CF_WRITE, true).unwrap();
        raw_engine.flush_cf(CF_DEFAULT, true).unwrap();

        eprintln!("oracle: after complete snapshot reapply");
        must_get(&mut engine, key, 200, &value_20);

        // RocksDB can select a scored L1+ level without including idle L0
        // files. Force that ordinary input shape deterministically.
        let level_files = rocksdb_level_files(&raw_engine, CF_WRITE);
        assert!(!level_files[5].is_empty());
        let files: Vec<String> = level_files[5]
            .iter()
            .map(|file| dir.path().join(file).to_string_lossy().into_owned())
            .collect();
        gc_runner.safe_point(120);
        gc_runner.ratio_threshold = Some(0.0);
        gc_runner.target_level = Some(6);
        gc_runner.gc_on_files(&raw_engine, &files, CF_WRITE);

        eprintln!(
            "physical oracle: write@21={}, default@20={}",
            raw_engine
                .get_value_cf(CF_WRITE, &write_key)
                .unwrap()
                .is_some(),
            raw_engine
                .get_value_cf(CF_DEFAULT, &default_key)
                .unwrap()
                .is_some()
        );
        eprintln!("oracle: after lower-level GC");
        must_get(&mut engine, key, 200, &value_20);
    }
}
