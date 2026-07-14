INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2580003,
 'Expired optimistic transaction can resurrect a row deleted before GC',
 'high',
 'atomicity/data-integrity',
 'COMMIT',
 'optimistic transaction / GC safe point / long transaction',
 'A transaction that has stopped protecting the GC safe point can still return COMMIT success and permanently recreate a row that another transaction deleted.',
 'On a Classic cluster where SELECT @@global.tidb_txn_assertion_level returns OFF, set tidb_gc_max_wait_time=600 while keeping the default 10m GC lifetime and run interval. T1 begins optimistic, updates an order row, and remains open during an external call or stalled batch. T2 deletes the row and commits. After roughly 20-30 minutes, once T1 has aged out of min-start-TS reporting and a normal GC plus RocksDB compaction removes the delete tombstone and old row image, resume and commit T1 before client-go''s fixed 24h maximum.',
 'T1 must fail once its startTS is older than the GC safe point, and a fresh session must continue to see the row as deleted.',
 'T1 COMMIT returned success; a fresh session read the deleted row recreated with T1''s buffered value (1,11).',
 'TiDB can stop reporting active transactions older than configurable tidb_gc_max_wait_time, but client-go only calls CheckVisibility on read paths. Effectful KVTxn.Commit reaches prewrite without checking the transaction safe point. The fixed 24h MaxTxnTimeUse check runs after prewrite and does not align with the supported 600s GC exclusion horizon. After TiKV GC erases the newer delete record, prewrite has no write conflict left to detect.',
 'Close the GC-retirement contract for effectful commits. A minimal counterfactual checks visibility before prewrite, but the robust fix must also eliminate the check/prewrite race, for example by enforcing the safe point at TiKV prewrite or by preventing GC exclusion before the client admission horizon.',
 'COMMIT_RESULT_VS_FRESH_DELETED_ROW_DURABLE_STATE',
 'SAFE_POINT_RETIREMENT_CONSUMER_CLOSURE',
 'commit-does-not-check-gc-safe-point-before-prewrite',
 'TiDB b8d04e17 / client-go 01bd8f99 / TiKV 7ecce12e; MDL ON; 2PC; assertion OFF; gc_max_wait below 24h',
 1,
 'issue-filed',
 'https://github.com/pingcap/tidb/issues/69833',
 'Discovered from current-source GC-retirement proof obligations without issue or PR seeds. Raw client-go and SQL-level real-TiKV REDs both reproduced durable resurrection. FAST assertions mask the SQL UPDATE shape on newly bootstrapped Classic clusters; upgraded clusters can retain or fall back to the registered compatibility default OFF because FAST is applied only during initial bootstrap. Exact pre-prewrite CheckVisibility counterfactual is GREEN. Post-RED searches across TiDB, client-go, and TiKV found no exact issue; TiDB #18358 and #32725 concern read/service-safe-point protection and internal transactions, not effectful commit after GC.');
