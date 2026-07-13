INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2310003,
 'Pessimistic retry can retain an advisory lock acquired by a rolled-back attempt',
 'high',
 'liveness',
 'UPDATE',
 'pessimistic statement retry / advisory locks',
 'A pessimistic READ COMMITTED UPDATE returns success with zero matched rows, but the session still owns a GET_LOCK acquired only by its failed internal attempt. Other sessions are denied until the hidden owner releases the lock or disconnects.',
 'In pessimistic RC, update row 1 to a concurrently claimed unique key and set another column to GET_LOCK(CONCAT(row-dependent-name),0)+SLEEP(20), guarded by NOT EXISTS on a gate row. During SLEEP, another transaction claims the unique key and inserts the gate. The statement retries and succeeds with zero rows. Query IS_USED_LOCK from the owner and GET_LOCK from another session.',
 'The successful zero-row attempt does not evaluate GET_LOCK. IS_USED_LOCK returns NULL and a competing session acquires the name, matching a run that starts directly from the same final database state.',
 'The slow log reports exec_retry_count=1 and success. ROW_COUNT is zero, but IS_USED_LOCK returns the statement connection ID and a competitor returns 0. After the hidden owner calls RELEASE_LOCK, the competitor returns 1. The same-final-state no-retry control returns NULL and 1.',
 'GET_LOCK creates or increments session.advisoryLocks and keeps a dedicated internal pessimistic transaction open. handlePessimisticLockError rebuilds the executor and rolls back statement KV state, but neither StmtRollback nor ResetForRetry restores advisory-lock ownership. A zero-work retry therefore publishes success while the failed attempt capability remains live.',
 'Conservatively classify DML containing advisory-lock operations as unsafe for transparent pessimistic retry before execution and surface the original conflict. A complete attempt journal would have to restore GET_LOCK, repeated references, RELEASE_LOCK, and RELEASE_ALL_LOCKS; partial acquisition-only cleanup is insufficient.',
 'ZERO_WORK_RETRY_EXTERNAL_LOCK_OWNER_AND_COMPETITOR_DIFFERENTIAL',
 'ATTEMPT_SCOPED_SIDE_EFFECT_ROLLBACK_CLOSURE',
 'pessimistic-retry-retains-failed-attempt-advisory-lock',
 'TiDB 5c9198e9484d; testbed 8220955 with three real TiKV nodes and metadata locking enabled',
 1,
 'issue-filed',
 'https://github.com/pingcap/tidb/issues/69820',
 'Discovered from current-source retry ownership, not PR review or issue findings. Local natural-conflict RED, SQL-only real-TiKV RED, and same-final-state control all use a row-dependent lock name. The testbed slow log records exec_retry_count=1, succ=1, and @@global.tidb_enable_metadata_lock=1. Post-RED GitHub and found_bug searches found no exact root. Trigger frequency is low, but long-lived pooled sessions can turn the residual advisory lock into an unbounded singleton-job or service stall. Upstream issue #69820.');
