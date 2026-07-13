INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2280003,
 '1PC can commit with an obsolete schema after concurrent DDL and corrupt a newly added index when MDL is off',
 'high',
 'consistency',
 'ADD INDEX / TRUNCATE TABLE',
 'TiDB 1PC / schema lease checker / metadata lock disabled',
 'An autocommit INSERT returns success in 1PC mode after a concurrent ADD INDEX has already finished. The table scan contains the row, the new index misses it, and ADMIN CHECK TABLE fails. With TRUNCATE TABLE, the INSERT also returns success with a commitTS later than the DDL FinishedTS, but the row is absent from the current table.',
 'On exact TiDB commit 5c9198e9484d with tidb_enable_metadata_lock=OFF, enable 1PC and pause tikvclient/beforePrewrite once. Start INSERT INTO t VALUES (1,10), wait until it is paused after calculateMaxCommitTS, execute ALTER TABLE t ADD INDEX idx_v(v), then release the pause. Capture @@tidb_last_txn_info, the DDL history FinishedTS, table and forced-index rowsets, and ADMIN CHECK TABLE. The reusable script is scaffolds/top-level/ai_native_onepc_schema_horizon_probe.sh. The same matrix was executed on testbed 8220955 against three real TiKV nodes.',
 'If the DML commitTS is later than the DDL FinishedTS, the write must be validated against and applied with the new schema, or return a retryable schema-change error. A successful commit must preserve table/index consistency and must not write an obsolete table identity.',
 'The ADD INDEX run returned txn_commit_mode=1pc and commit_ts greater than the DDL FinishedTS. The table scan returned 1:10, FORCE INDEX(idx_v) returned no row, and ADMIN CHECK TABLE failed. The paired 2PC run retried and returned 1:10 through both paths with ADMIN CHECK passing. The TRUNCATE sibling returned success with commitTS greater than FinishedTS while the current table was empty.',
 'When MDL is disabled, TiDB installs a delta SchemaChecker. client-go calls it from calculateMaxCommitTS before beforePrewrite, then 1PC asks TiKV to atomically prewrite and commit. Unlike 2PC, the successful 1PC branch returns immediately and never validates the schema at its actual commitTS. A DDL completed in that interval can therefore be older than the 1PC commitTS while the mutation still uses the obsolete table/index key set.',
 'Conservatively disable 1PC when needCheckSchemaByDelta is true, so the existing 2PC commitTS schema check and TiDB retry path own the transition. A local one-variable counterfactual made the full oracle GREEN. A deeper protocol change would need TiKV to enforce schema validity at the atomic 1PC apply point.',
 'SCHEMA_ORDER_AND_TABLE_INDEX_PHYSICAL_COHERENCE',
 'VALIDATION_HORIZON_COVERS_IRREVERSIBLE_APPLY',
 'onepc-schema-check-before-prewrite-mdl-off',
 'TiDB 5c9198e9484d and client-go 661db4f5f4e8; exact-commit testbed 8220955 with real TiKV confirmed',
 1,
 'confirmed',
 NULL,
 'Discovery used current source and a bounded proof-horizon pass, not PR review or issue seeds. Post-RED searches found no exact TiDB or client-go issue. Closed issue #24009 concerns an unstable skipped test and says it does not affect production; it does not record this commitTS-ordered silent corruption. Existing found_bug id1440001 is the async-commit sibling that returns ErrInfoSchemaChanged, not this 1PC false success. The failpoint compresses a delay that can naturally arise from RPC latency, retry/backoff, load, or Region/leader movement between schema validation and prewrite.');
