INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2940003,
 'Concurrent NextGen IMPORT INTO jobs can leave persistent row/index corruption after both report failed',
 'high',
 'data-corruption',
 'IMPORT INTO / concurrent job admission',
 'NextGen cross-keyspace DXF import admission',
 'Two concurrent imports into one empty table are both admitted. Both jobs later report checksum failure, but the target remains physically corrupted: the table has two record rows while its unique index has four entries, forced access paths disagree, and ADMIN CHECK TABLE returns 8223.',
 'On NextGen, create an empty table t(v VARCHAR(100), UNIQUE KEY uk(v)) without a primary key. Put disjoint files a.csv={a1,a2} and b.csv={b1,b2} in object storage. From two sessions concurrently execute IMPORT INTO t FROM each file WITH DETACHED and distinct cloud_storage_uri paths. Wait for both jobs to become terminal; compare a table scan with FORCE INDEX(uk), then run ADMIN CHECK TABLE.',
 'The active-owner claim is atomic. At most one import is admitted for a target table, and the other request fails before irreversible ingest. A failed job never leaves record/index disagreement.',
 'Natural current-master runs admitted both jobs three times in a row. Both jobs failed with ErrChecksumMismatch. The table retained one input under hidden handles 1 and 2, while FORCE INDEX returned entries from both inputs under the same handles. ADMIN CHECK reported that handle 1 had one value in the index and another in the record.',
 'GetActiveJobCnt reads pending/running jobs before CreateJob inserts the new owner. The check and claim are not atomic and no target-unique active lease exists. NextGen skips Classic TableModeImport. Two accepted plans independently allocate hidden handles 1 and 2; record and index KV groups ingest separately, so record collisions choose one input while distinct index keys from both survive. Checksum detects the merged state only after durable SST ingest and cannot roll it back.',
 'Atomically acquire a durable per-target active-owner lease with job creation, keyed by keyspace and stable table identity. Keep it through ingest and post-processing, validate its fencing token on recovery, and release it only at a terminal state. Reject a second request before planning or physical writes.',
 'JOB_ADMISSION_PLUS_PRIMARY_INDEX_BIJECTION_AND_ADMIN_CHECK',
 'ATOMIC_ADMISSION_BEFORE_IRREVERSIBLE_PARALLEL_OWNERS',
 'nextgen-import-active-owner-check-then-create-race',
 'TiDB master 231dad5225f0; NextGen user/SYSTEM keyspaces; TiKV/CSE ce46fc5067; real PD/TiKV; MDL enabled',
 1,
 'confirmed',
 NULL,
 'Critical persistent-data-corruption consequence; catalog severity uses high as the top existing grade. Final RED used unmodified product packages, ordinary concurrent SQL, and no failpoint, source hook, process pause, node failure, or network/disk fault. Natural concurrency reproduced 3/3; an exact single-import control was GREEN. A deterministic check-to-create barrier separately proved the TOCTOU mechanism. Post-RED TiDB issue and remote found_bug searches found no exact root. Distinct from id1590002 partial single-import commit, id2850003 stale target generation, and id2880003 BR write-fence corruption.');
