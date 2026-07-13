INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(1980003,
 'FLASHBACK CLUSTER can restore a cached table without its lock row and block all writes',
 'high',
 'ddl-availability',
 'FLASHBACK CLUSTER after ALTER TABLE NOCACHE',
 'cached table runtime metadata',
 'FLASHBACK CLUSTER reports synced/public and the table remains readable, but INSERT, UPDATE, and DELETE fail because the restored cached TableInfo has no mysql.table_cache_meta row.',
 'Create and CACHE a non-partition table, record a transaction TSO, run NOCACHE and verify its table_cache_meta row is deleted, then FLASHBACK CLUSTER to that TSO. SHOW CREATE restores CACHED ON while the row remains absent. SELECT succeeds by fallback; a fresh-session INSERT fails with table_cache_meta tid not exist.',
 'A successful Flashback must either restore/rebuild every runtime owner required by restored metadata, or reject a window containing unsupported cache-state DDL. Normal DML must remain writable after completion.',
 'On testbed 8220955, job 5432 synced/public and restored table ID 5428 as CACHED ON. table_cache_meta count stayed 0; SELECT returned (1,10), INSERT (2,20) returned ERROR 1105, and the rowset stayed unchanged. Replacing only the missing row made the same INSERT succeed.',
 'getFlashbackKeyRanges restores user schema metadata and data but excludes mysql schema state. NOCACHE deletes mysql.table_cache_meta, while CACHE/NOCACHE are allowed by the Flashback DDL compatibility guard. The restored TableCacheStatusEnable therefore points to an absent mandatory lock row; cached-table DML propagates loadRow failure before commit.',
 'Reject CACHE/NOCACHE changes in a Flashback window, or reconcile table_cache_meta from restored TableInfo before schema publication. Rebuild rows with lease-safe defaults and remove rows for metadata restored as NOCACHE.',
 'flashback-restored-cache-metadata-side-row-dml-coherence',
 'RESTORE_DOMAIN_COVERS_RUNTIME_DEPENDENCIES',
 'flashback-cluster-cache-side-state-exclusion',
 'current master 13282a8bd06b local consumer RED; testbed 8220955 commit 5c9198e948 SQL-only RED',
 1,
 'confirmed',
 NULL,
 'Candidate came from current-source restore-domain and consumer analysis. PR reviews, issues, fix history, and partition paths were excluded during generation. Local state simulation proved read fallback, write failure, and one-row compensation. Actual FLASHBACK CLUSTER reproduced the same owner split without failpoints. Six post-RED GitHub issue searches found no exact root.');
