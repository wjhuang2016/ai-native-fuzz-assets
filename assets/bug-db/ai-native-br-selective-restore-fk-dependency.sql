INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(3060003,
 'BR selective table restore can publish a foreign-key child without its parent and silently accept orphan rows',
 'high',
 'data-corruption',
 'br restore table',
 'snapshot selective restore and foreign keys',
 'Restoring only a foreign-key child table reports success and passes BR checksum even though the referenced parent table is absent. Rows from the backup are immediately orphaned, and later inserts with foreign_key_checks=ON can also commit new orphan rows.',
 'Create parent p and child c with an enforced FK, insert p(1) and c(1,1), and back up the database. Drop the database and run br restore table --db <db> --table c against the backup. Verify that p is absent while c and its FK metadata are present, then insert c(2,999) with foreign_key_checks=ON.',
 'BR must either include the referenced parent table in the restore closure or reject the selective restore before publishing schema or data. A successful restore must not leave any restored FK row without its referenced parent.',
 'Two independent restores reported Table Restore success, validated checksum, restored c(1,1) without p, and accepted c(2,999). Restoring the full database from the same backup restored p and caused the same invalid insert to fail with error 1452.',
 'filterRestoreFiles applies the user table filter without closing foreign-key dependencies. BRIECreateTables then disables ForeignKeyChecks for the internal batch because it assumes the batch is dependency-complete. The selected child TableInfo and KVs are published without the excluded parent, while checksum validates only the selected physical table.',
 'Build an FK dependency graph before schema publication. For every enforced FK in the selected snapshot, either include the referenced table and report the expanded scope or fail with a clear dependency error. Keep the internal FK-check bypass only for a dependency-closed batch. Independently make runtime FK enforcement fail closed when referenced metadata is unavailable.',
 'restore-terminal-plus-fk-dependency-closure',
 'proof-obligation-small-matrix-strong-oracle',
 'br-filtered-batch-assumes-fk-dependency-closure',
 'master 05b396fb6636; verified BR a942e4684f43',
 1,
 'confirmed',
 NULL,
 'Default/common configuration: one TiDB, one PD, one real TiKV, MDL=ON, tidb_enable_foreign_key=ON, foreign_key_checks=ON, and no failpoint, source patch, process fault, network fault, or concurrent workload. The current master has no relevant diff from the verified BR revision in filterRestoreFiles, snapshot CreateTables, or BRIECreateTables. Related issue #65175 covers the downstream missing-parent DML fail-open, while #65256 only notes a PiTR log-restore adjustment; neither provides this snapshot table-restore RED or closes the restore selector root.');
