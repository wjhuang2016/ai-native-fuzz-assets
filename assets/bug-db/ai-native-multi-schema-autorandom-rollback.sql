INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(3570003,
 'Failed multi-schema AUTO_RANDOM conversion can make a cold TiDB overwrite existing rows',
 'high',
 'data-loss',
 'ALTER TABLE multi-schema change',
 'AUTO_INCREMENT to AUTO_RANDOM rollback',
 'A composite ALTER returns duplicate-key error and DDL history says rollback done, but leaves a hybrid AUTO_INCREMENT plus AUTO_RANDOM schema. A new or restarted TiDB allocates existing primary keys. Plain INSERT reports duplicate key; generated REPLACE succeeds with affected_rows=2 and permanently overwrites an existing row while ADMIN CHECK remains green.',
 'Create a default-cache clustered AUTO_INCREMENT table with 64 rows and duplicate business values. Enable tidb_allow_remove_auto_inc in the DDL session. Run one ALTER that converts the key to AUTO_RANDOM(1) and adds a unique index on the duplicate column. After the expected 1062, start a second same-version TiDB against the same PD/TiKV. Run generated INSERT, then generated REPLACE, and fresh-read the old id=2 payload.',
 'A failed composite DDL restores a self-consistent pre-DDL schema and every allocator owner. A cold TiDB generates an ID above the durable high-water mark, and no preexisting payload is removed.',
 'On current master 05b396fb66 and nightly ed2376acc6, the ALTER returns 1062 and parent history is rollback done, but SHOW CREATE contains AUTO_INCREMENT and AUTO_RANDOM together. A cold TiDB INSERT collides at id=1. Its next REPLACE returns LAST_INSERT_ID=2 and ROW_COUNT=2, replacing the old id=2 payload. A fresh session sees the old payload count 0 and ADMIN CHECK succeeds.',
 'onModifyColumn calls checkAndApplyAutoRandomBits while the proxy subjob is still revertible. The apply step sets AutoRandomBits, rebases AutoRandom, and deletes the RowID accessor. The parent later saves/restores TableInfo and subjob state, but cannot compensate the allocator migration. Its saved snapshot already combines table AutoRandomBits with the old AUTO_INCREMENT column flag, so cold allocator reconstruction selects the deleted/reset RowID owner.',
 'Reject this conversion inside multi-schema change as a safe short-term fix. A complete fix must stage allocator migration until all siblings can cross the parent commit boundary, or make the migration and TableInfo publication one atomic operation with exact rollback compensation. Add warm/cold RED and failed-index/successful-conversion GREEN controls.',
 'FAILED_DDL_SCHEMA_ALLOCATOR_IDENTITY_CLOSURE',
 'IRREVERSIBLE_SUBJOB_ROLLBACK_CLOSURE',
 'multi-schema-autorandom-migration-before-parent-commit',
 'TiDB master 05b396fb66 and nightly ed2376acc6; Classic real TiKV; default auto-ID cache; MDL ON',
 1,
 'confirmed',
 NULL,
 'Critical-class persistent data-loss consequence under default allocator cache and MDL. The supported conversion guard must be enabled, a later subjob must fail, and traffic must reach a cold TiDB. No failpoint, DDL race, network fault, process crash during DDL, AUTO_ID_CACHE=1, or unusual isolation is required. Current-master and nightly RED, two sibling GREEN controls, and a pre-apply guard GREEN isolate the root. Post-RED upstream issue and internal asset searches found no exact duplicate.');

