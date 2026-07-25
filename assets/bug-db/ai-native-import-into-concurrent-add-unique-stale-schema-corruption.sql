INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(3510003,
 'Classic IMPORT INTO can report success with an empty unique index after concurrent ADD INDEX',
 'high',
 'data corruption',
 'IMPORT INTO / ADD UNIQUE INDEX',
 'Classic TableMode and required checksum',
 'Both IMPORT INTO and ADD UNIQUE INDEX can report success while all imported records are absent from the new unique index. Indexed reads return no imported rows, ADMIN CHECK TABLE reports 8223, and a later INSERT can reuse an imported unique value.',
 'Create empty t(id BIGINT PRIMARY KEY, v BIGINT NOT NULL). Start IMPORT INTO from a large object prefix or directory so file discovery is in progress. After target resolution but before TableMode acquisition, run ALTER TABLE t ADD UNIQUE INDEX kv(v) in another session. Let import finish, compare USE INDEX() and FORCE INDEX(kv), insert a duplicate v, and run ADMIN CHECK TABLE. The stored real-TiKV test models discovery with 60,000 unrelated files and reproduced 3/3.',
 'TableMode acquisition must atomically validate that the current target identity and schema generation equal the schema captured by the import plan. A successful import must populate every current public index and preserve unique constraints.',
 'The DDL and import both succeed and the job is finished. The record scan returns three imported rows, FORCE INDEX(kv) returns zero, the default required checksum logs checksum pass, INSERT (4,101) succeeds despite existing imported (1,101), and ADMIN CHECK TABLE returns error 8223.',
 'IMPORT INTO skips statement MDL and copies tbl.Meta() into its plan before file discovery and prechecks. Classic TableModeImport is acquired only during task submission and blocks future changes, but does not compare the current schema with the captured schema. Workers therefore encode with stale TableInfo. A completely missing current index contributes zero to the remote checksum, matching the stale row-only local checksum.',
 'Atomically compare an expected schema token while transitioning Normal to Import mode, then submit workers only after the claim succeeds. Abort or rebuild schema-dependent state on mismatch. Also make terminal validation require closure for every public index in the current schema.',
 'import-finished-current-schema-index-unique-admin-closure',
 'atomic-fence-proof-state-cas',
 'classic-import-tablemode-stale-schema-claim',
 'TiDB master 05b396fb66; Classic kernel',
 1,
 'confirmed',
 NULL,
 'Critical consequence under ordinary successful operations and default required checksum; stored as high to match the current bug-library taxonomy. Production trigger is overlap between bulk-load preparation and schema deployment on the same empty table, amplified by large object prefixes or normal object-store latency. MDL ON, strict mode, one TiDB, one real TiKV. Natural timing RED 3/3; exact scheduler RED 1/1; before-planning GREEN 1/1. The exact barrier schedules ordinary DDL and injects no error. Post-RED open/closed GitHub searches found no exact root; #69798 is an empty umbrella.');
