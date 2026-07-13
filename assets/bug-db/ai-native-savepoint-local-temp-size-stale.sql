INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2220003,
 'ROLLBACK TO SAVEPOINT leaves local temporary table size accounting stale',
 'moderate',
 'wrong_error',
 'ROLLBACK TO SAVEPOINT',
 'local temporary table size limit',
 'After rolling back all local temporary table rows to a savepoint, a tiny INSERT can still fail with ERROR 1114 The table is full because size accounting from the rolled-back writes survives.',
 'Set tidb_tmp_table_max_size=1048576. Create a local temporary table, BEGIN, SAVEPOINT sp, insert two 600000-byte rows, ROLLBACK TO SAVEPOINT sp, verify COUNT(*)=0, then insert one 1-byte row.',
 'The rollback restores an empty table and the following 1-byte INSERT succeeds.',
 'COUNT(*) is 0 after rollback, but the 1-byte INSERT returns ERROR 1114 The table is full.',
 'The savepoint snapshot restores MemDB and TxnCtxNeedToRestore, but TransactionContext.TemporaryTables is classified as no-restore. Its mutable TemporaryTable value contains transaction-local dirty size. The two inserts raise size to about 1.2 MB; MemDB rollback removes the rows but does not restore that value. checkTempTableSize later consumes the stale size.',
 'Snapshot and restore each temporary table dirty size with the savepoint, or derive the size from checkpoint-owned MemDB state. A local counterfactual that restored only per-table size made the exact RED test pass.',
 'ROLLBACK_EMPTY_TEMP_TABLE_THEN_TINY_INSERT_LIMIT_DIFFERENTIAL',
 'SAVEPOINT_MUTABLE_VALUE_OWNER_CLOSURE',
 'savepoint-omits-local-temp-table-dirty-size-restore',
 'current TiDB 5c9198e9484d and authorized testbed 8220955',
 1,
 'confirmed',
 NULL,
 'Discovered from current source by comparing savepoint restoration ownership, not from PR review, issues, fixes, or history. Local RED, one-variable local GREEN, and SQL-only testbed RED are complete. Severity is moderate: the error blocks valid local-temporary-table writes inside the transaction, but no wrong durable data or cross-session corruption was shown.');
