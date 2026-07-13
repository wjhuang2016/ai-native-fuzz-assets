INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2130003,
 'IMPORT INTO can report success with inconsistent indexes after a transient conflict-deletion Commit error',
 'high',
 'correctness/data-integrity',
 'IMPORT INTO',
 'global sort conflict resolution with on_duplicate_key=capture',
 'IMPORT INTO reports finished and the expected imported/conflicted row counts, but a conflicted record and secondary-index entry survive without a matching unique-index entry. Queries return different row sets by access path and ADMIN CHECK TABLE reports 8223.',
 'Create a clustered-primary table with a unique index and a secondary index. Import rows (1,1,10),(1,1,10),(2,1,20),(3,3,30) using global sort, on_duplicate_key=capture, and checksum_table=off. Before the first conflict-deletion transaction Commit, return one retryable error and roll back that transaction. Compare IMPORT status, PRIMARY, unique-index and secondary-index row sets, then run ADMIN CHECK TABLE.',
 'The Commit error must reach RunWithRetry. A transient error should retry the whole delete batch. A successful capture-mode import must retain only (3,3,30), all access paths must agree, and ADMIN CHECK TABLE must pass.',
 'Job 180001 finished with Imported_Rows=1 and 3 conflicted rows, but PRIMARY/unique/secondary returned 2/1/2 rows: PRIMARY and iv retained id=2 while unique u did not. ADMIN CHECK TABLE returned ERROR 8223 for index u, handle 2. A same-process no-fault control and a named-return retry counterfactual both returned 1/1/1 and ADMIN green.',
 'deleteBufferedKeys has an unnamed error result and returns nil after staging deletes. Its defer assigns txn.Commit(ctx) to a local err, but return nil fixes the public result before deferred code runs. RunWithRetry therefore sees nil and publishes conflict-resolution success without retrying the failed Commit.',
 'Make the function result own the deferred Commit error, for example by using a named error result or by committing explicitly before return. Keep the existing retry classifier and add a one-shot retryable Commit-error test that checks task status plus PRIMARY/unique/secondary equality and ADMIN CHECK TABLE.',
 'IMPORT_STATUS_ACCESS_PATH_ADMIN_COHERENCE',
 'DEFERRED_TERMINAL_ERROR_RETURN_SLOT_OWNERSHIP',
 'importinto-conflict-delete-commit-false-success',
 'current master 13282a8bd06b; global-sort IMPORT INTO with on_duplicate_key=capture',
 1,
 'issue-filed',
 'https://github.com/pingcap/tidb/issues/69792',
 'Confirmed by current-source local RED, exact named-return GREEN, and real PD/TiKV one-shot retry matrix on authorized testbed 8220955. Discovery and ranking used current source only; PR reviews, issues, and history were excluded until after RED. Distinct from writer Close and sibling-Close roots.');
