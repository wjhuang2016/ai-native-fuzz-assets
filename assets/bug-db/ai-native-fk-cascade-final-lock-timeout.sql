INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2640003,
 'Failed pessimistic FK cascade update can be committed after lock wait timeout',
 'high',
 'atomicity/data-integrity',
 'UPDATE',
 'pessimistic transaction / ON UPDATE CASCADE / final LockKeys',
 'A pessimistic multi-table UPDATE returns definite error 1205, but a later COMMIT of the still-open explicit transaction makes the failed statement parent-key mutation and generated ON UPDATE CASCADE child mutation durable.',
 'Use a parent primary-key migration with an ON UPDATE CASCADE child and include migration_guard.version=migration_guard.version in the same multi-table UPDATE as a database mutex. Let an older batch worker hold the guard row longer than the default 50-second innodb_lock_wait_timeout because of a long batch, hot Region, server-busy backoff, or storage pressure. The racing UPDATE executes the parent and cascade stages before final LockKeys waits on the guard. After the UPDATE returns 1205, catch the retryable statement conflict and COMMIT the still-open explicit transaction, for example to retain earlier audit/progress work. MDL remains ON and one TiDB, one real TiKV, and two sessions suffice.',
 'A definite statement error must leave no mutation from that statement. After the later COMMIT, a fresh session must still read parent.id=1 and child.pid=1.',
 'With default MDL ON and default innodb_lock_wait_timeout=50, LockKeys waited 50.0017 seconds and the UPDATE returned 1205. COMMIT then succeeded, and a fresh session read parent.id=2 and child.pid=2.',
 'FK cascade execution uses intermediate StmtCommit publication and an internal transaction savepoint. handleStmtForeignKeyTrigger releases that savepoint as soon as the nested FK trigger succeeds, although the outer pessimistic statement still has a fallible final LockKeys phase. A terminal lock timeout reaches generic statement rollback after the checkpoint owner has gone, so already published parent and child mutations remain in the transaction and can later commit.',
 'Retain the FK savepoint through final pessimistic locking. Roll back to it on every terminal post-trigger error and release it only after the whole user statement succeeds. Audit every later post-trigger finalizer, not only LockKeys.',
 'FAILED_STATEMENT_PLUS_FRESH_PARENT_CHILD_STATE',
 'ROLLBACK_CHECKPOINT_FALLIBILITY_HORIZON',
 'fk-cascade-savepoint-released-before-final-lock-result',
 'TiDB 2964713e / real TiKV; MDL ON; default 50s timeout; default pessimistic constraint check and FK ON',
 1,
 'issue-filed',
 'https://github.com/pingcap/tidb/issues/69838',
 'Generated from current-source checkpoint ownership, not a PR review or issue seed. Mock and real-TiKV REDs, default-settings 50-second RED, and exact owner GREEN are recorded. Applications that always ROLLBACK after 1205 do not expose the surface. Post-RED issue search found no exact root. TiDB #69828 shares intermediate FK publication but is a distinct both-success lost-lock-owner orphan; this root is definite statement error plus durable mutation after rollback checkpoint release. Upstream #69838 contains the concrete production trigger and directly runnable real-TiKV test.');
