INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2970003,
 'Converting AUTO_ID_CACHE=1 AUTO_INCREMENT to AUTO_RANDOM can reuse primary keys and silently replace rows',
 'high',
 'data-loss',
 'ALTER TABLE ... AUTO_INCREMENT TO AUTO_RANDOM',
 'AUTO_ID_CACHE=1 allocator type migration',
 'After a supported AUTO_INCREMENT-to-AUTO_RANDOM conversion, generated AUTO_RANDOM values can restart from the beginning of the incremental range. Ordinary INSERT may fail with duplicate primary keys. REPLACE can report success with affected_rows=2 and permanently overwrite existing rows while ADMIN CHECK TABLE remains green.',
 'Create a clustered AUTO_INCREMENT primary-key table with AUTO_ID_CACHE=1 and insert IDs 1 through 64. Set tidb_allow_remove_auto_inc=1 and ALTER the column to BIGINT AUTO_RANDOM(1). Execute generated REPLACE statements without specifying the ID. Compare the original payload count and the generated IDs. Run the same matrix on a table with the default auto-ID cache as a control.',
 'The conversion transfers the high-water mark from the allocator that owned AUTO_INCREMENT. Every generated AUTO_RANDOM incremental component is above the old owner high-water mark, all REPLACE statements insert one row, and all original rows remain.',
 'On unmodified current master, with the current-master TiDB explicitly verified as DDL owner, 12 of 24 generated REPLACE statements reused IDs 1,5,9,10,12,14,15,16,19,20,21,22 and returned affected_rows=2. The final table had 76 rows: 52 original rows and 24 replacement rows. ADMIN CHECK TABLE succeeded. The default-cache control preserved all original rows.',
 'checkNewAutoRandomBits selects IncrementID when TableInfo.SepAutoInc is true, but applyNewAutoRandomBits unconditionally reads, rebases from, and deletes the RowID accessor. AUTO_ID_CACHE=1 stores AUTO_INCREMENT in the separated IncrementID allocator, so the conversion reads an unrelated zero RowID base and initializes AUTO_RANDOM near zero.',
 'When converting from AUTO_INCREMENT, select the old accessor using the same SepAutoInc rule as checkNewAutoRandomBits. Rebase AUTO_RANDOM from that owner and delete exactly that old accessor. Add RED/GREEN coverage that verifies which TiDB is the DDL owner.',
 'GENERATED_ID_DISJOINTNESS_PLUS_PREIMAGE_ROW_PRESERVATION',
 'ALLOCATOR_TYPE_MIGRATION_OWNER_TRANSFER',
 'autoid-to-autorandom-migration-reads-wrong-allocator-owner',
 'TiDB master 231dad5225 and nightly ed2376acc6; Classic real TiKV; MDL ON; AUTO_ID_CACHE=1',
 1,
 'confirmed',
 NULL,
 'Critical persistent-data-loss consequence; the catalog severity vocabulary records it as high. The RED needs no concurrency, failpoint, process pause, node failure, retry, disabled MDL, or unusual isolation. The table option AUTO_ID_CACHE=1 and the guarded supported conversion session variable are required. Current-master RED, default-cache GREEN, and an exact current-source allocator-owner counterfactual GREEN isolate the root. GitHub issue and remote found_bug searches found no exact existing root.');
