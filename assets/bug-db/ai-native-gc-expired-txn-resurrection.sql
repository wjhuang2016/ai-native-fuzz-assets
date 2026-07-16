INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2580003,
 'Expired optimistic transaction can resurrect a row deleted before GC',
 'high',
 'atomicity/data-integrity',
 'COMMIT',
 'optimistic transaction / GC safe point / long transaction',
 'A transaction that has stopped protecting the GC safe point can still return COMMIT success, recreate a row, or persist a child whose validated parent was deleted.',
 'Set tidb_gc_max_wait_time=600 while keeping normal GC/compaction. T1 begins optimistic and stalls after either (a) buffering an INSERT into an absent key or (b) validating a parent and buffering a child INSERT. T2 performs INSERT->DELETE on the same key, or deletes the parent. After T1 ages out of min-start-TS reporting and GC removes the post-startTS write history, resume T1 before client-go''s fixed 24h maximum. The original existing-row UPDATE variant additionally requires assertion OFF; the INSERT and FK variants reproduce under FAST and STRICT.',
 'T1 must fail once its startTS is older than the GC safe point. A fresh session must see neither the resurrected row nor a child without its referenced parent.',
 'T1 COMMIT returned success under FAST and STRICT; a fresh session read the ABA key as (1,11). Under STRICT with FK checks ON, COMMIT also returned success and a fresh anti-join found orphan child (1,1). Both no-GC controls returned error 9007 and left no row/orphan.',
 'TiDB can stop reporting active transactions older than configurable tidb_gc_max_wait_time, but client-go only calls CheckVisibility on read paths. Effectful KVTxn.Commit reaches prewrite without checking the transaction safe point. The fixed 24h MaxTxnTimeUse check runs after prewrite and does not align with the supported 600s GC exclusion horizon. After TiKV GC erases the newer delete record, prewrite has no write conflict left to detect.',
 'Close the GC-retirement contract for effectful commits. A minimal counterfactual checks visibility before prewrite, but the robust fix must also eliminate the check/prewrite race, for example by enforcing the safe point at TiKV prewrite or by preventing GC exclusion before the client admission horizon.',
 'COMMIT_RESULT_VS_FRESH_ROW_AND_FK_DURABLE_STATE',
 'SAFE_POINT_RETIREMENT_CONSUMER_CLOSURE',
 'commit-does-not-check-gc-safe-point-before-prewrite',
 'TiDB 94b834d9 / client-go 01bd8f99 / TiKV c27c6620; MDL ON; FK ON; ordinary 2PC; FAST and STRICT; gc_max_wait below admitted txn age',
 1,
 'issue-filed',
 'https://github.com/pingcap/tidb/issues/69833',
 'Discovered from current-source GC-retirement proof obligations without issue or PR seeds. Raw client-go and SQL-level real-TiKV REDs reproduced durable resurrection. Current-master expansion proved Assertion=Exist only masks existing-row UPDATE: lazy INSERT uses AssertUnknown, and FK uses a parent lock-only proof, so both lose their only conflict evidence after GC under FAST/STRICT. This is critical blast radius of the same root, not a new bug count. Exact pre-prewrite CheckVisibility counterfactual is GREEN; a robust fix must also close the safe-point/prewrite race.');
