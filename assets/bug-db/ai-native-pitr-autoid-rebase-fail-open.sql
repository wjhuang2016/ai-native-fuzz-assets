-- Remote found_bug mirror for id3030003.
INSERT INTO found_bug (
  id, title, severity, category, ddl_op, feature, symptom, repro, expected, actual,
  root_cause, fix_hint, oracle, method, root_cause_id, affects, confirmed, status, notes
) VALUES (
  3030003,
  'PiTR can report success after an AUTO_ID rebase failure and later overwrite restored rows',
  'high',
  'data-loss',
  'BR PiTR restore',
  'AUTO_ID_CACHE=1 / post-replay allocator repair',
  'A single-table allocator repair error is downgraded to a warning, so PiTR reports success with a stale auto-ID service. A later generated REPLACE can reuse a restored primary key and silently remove its payload.',
  'Create an AUTO_ID_CACHE=1 table and initialize its auto-ID service. Model PiTR raw-KV replay by adding restored row id=2 and advancing persisted IncrementID without notifying the service. Enable the existing pkg/kv mockCommitErrorInNewTxn failpoint for the final rebase transaction. RebaseAutoIncrementIDForSepAutoIncTables returns nil; REPLACE INTO t(data) allocates id=2, returns affected_rows=2, and overwrites the restored row. Disable only the error and repeat for the matched GREEN.',
  'Any failure of the mandatory post-replay allocator repair must fail the restore or be retried until every affected table is synchronized before the cluster is returned to writes.',
  'The helper logs a warning and returns nil. The next generated REPLACE reports last_insert_id=2 and row_count=2; fresh reads show id=2 changed from restored-two to replacement. With the same state and no error, rebase reaches 1004000, the next ID is 1004001, and id=2 is preserved.',
  'RebaseAutoIncrementIDForSepAutoIncTables treats each rebaseAutoIncrementIDForTable error as best effort even though this repair is the only step that closes the stale in-memory allocator state created by raw log replay. No later restore stage validates generated-ID disjointness.',
  'Make per-table rebase failure terminal, or add bounded retry plus a final all-table closure check. Do not publish restore success while any required allocator repair remains unconfirmed.',
  'O69_PITR_AUTOID_PREIMAGE_PRESERVATION',
  'S70_REQUIRED_REPAIR_FAIL_OPEN',
  'pitr-autoid-required-repair-fail-open',
  'TiDB master 231dad5225; AUTO_ID_CACHE=1; BR PiTR finalization; current source test path',
  1,
  'confirmed',
  'Current-master source-level RED/GREEN uses an existing TiDB failpoint and real SQL DML; no product-code probe is required. Natural producers include a transient TiKV metadata transaction error and autoid owner transition returning not leader. Post-RED GitHub and remote bug-library searches found no exact root. Classified high rather than critical because PiTR, AUTO_ID_CACHE=1, a repair-time fault, and a destructive upsert consumer must coincide.'
);
