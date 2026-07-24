INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2880003,
 'BR snapshot restore can report success after concurrent DML creates persistent unique-index corruption',
 'high',
 'data-corruption',
 'BR restore table / concurrent INSERT',
 'BR snapshot restore target table mode',
 'During a table snapshot restore, an ordinary INSERT into the newly created target succeeds. BR later reports Table Restore success, but the table has two unique-index entries for one clustered row. A point lookup by the stale backup unique key returns a row whose value does not satisfy the predicate; COUNT(*) over the unique index overcounts; ADMIN CHECK TABLE reports data inconsistency.',
 'Back up t(id BIGINT PRIMARY KEY, u BIGINT UNIQUE, payload) containing (1,100001,...). Drop the target. Start ordinary BR restore table with enough duration (for example --ratelimit 1). As soon as BR creates empty target t, verify TIDB_TABLE_MODE=Normal and INSERT (1,900000000,''app-write-during-restore''). Wait for BR to report success. Compare COUNT/SUM using PRIMARY versus index u, query WHERE u=100001, and run ADMIN CHECK TABLE.',
 'BR keeps the physical restore target inaccessible until ingest and validation finish, or rejects concurrent target writes. A successful restore must preserve a one-to-one record/index mapping and every index lookup result must satisfy its predicate.',
 'Official nightly BR exits 0 and reports 256000 restored KV. PRIMARY has 128000 rows, while unique index u has 128001 entries. WHERE u=100001 returns (1,900000000,''app-write-during-restore'') with predicate u=100001 evaluating false. ADMIN CHECK TABLE reports index value 100001 differs from record value 900000000.',
 'Ordinary snapshot restore creates the target in TableModeNormal. TableModeRestore, which already blocks DML/DDL, is applied only to explicit-filter PiTR. SnapClient freezes TableInfo and rewrite rules after creation, and normal snapshot rewrite uses NewTimestamp=0, preserving backup MVCC timestamps. A newer application record therefore wins at the clustered key while the restored older unique-index key has no matching delete, leaving a persistent stale index entry. RestoreTables performs no target-mode/write-generation revalidation before SST ingest.',
 'Create or transition every physical snapshot-restore target into TableModeRestore before it becomes visible, retain that fence through ingest/checksum/stats, and atomically return it to Normal only after success. For existing targets, acquire an equivalent generation/write lease and fail closed if the table ID, mode, or write epoch changes before ingest.',
 'PRIMARY_INDEX_BIJECTION_PLUS_PREDICATE_SELF_CHECK',
 'BACKDATED_PHYSICAL_INGEST_WITHOUT_WRITE_FENCE',
 'br-snapshot-restore-missing-target-write-fence',
 'TiDB nightly ed2376acc6 and BR nightly a942e4684f on Classic real TiKV; MDL ON; target mode Normal; checksum default OFF',
 1,
 'confirmed',
 NULL,
 'Strongest RED uses the official unmodified BR binary, no failpoint, no process pause, no concurrent DDL, and a normal application INSERT during a long table restore. --ratelimit 1 only widens a production-realistic large/slow restore window. Same backup and parameters without the INSERT are GREEN. A separate generation-retirement probe also showed BR success after writing all rows under a truncated table ID. Post-RED GitHub issue/PR and remote found_bug searches found no exact root. Consequence is C3 silent persistent relational corruption and critical data-integrity impact; the found_bug severity vocabulary records it as high.');
