INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2370003,
 'Pessimistic retry can persist NULL instead of SETVAL result',
 'high',
 'data-integrity',
 'UPDATE',
 'pessimistic READ COMMITTED statement retry / sequence SETVAL',
 'A pessimistic RC UPDATE returns success and commits NULL, while one execution from the state seen by the successful attempt commits 100. Both executions leave the sequence at the same next value.',
 'In session A, update a unique key from a source row and set another column to SETVAL(seq,100)+SLEEP(20). During SLEEP, session B claims the first unique value and changes the source to a non-conflicting value. A transparently retries and commits. Compare the row and NEXTVAL with a no-retry control using a fresh identical sequence.',
 'Either return the original conflict, or make successful retry observationally equivalent to one execution from its current state: row 1,2,100 and NEXTVAL 101.',
 'The slow log reports exec_retry_count=1 and success. The retry row is 1,2,NULL; the direct control row is 1,2,100. Both NEXTVAL results are 101.',
 'SETVAL immediately mutates the table-level sequence cache or allocator, and its return value depends on that owner. Pessimistic retry rolls back statement KV and resets statement context, but re-executes SETVAL without restoring or journaling the sequence result. The failed attempt therefore feeds back into the successful attempt row image.',
 'Decline transparent pessimistic retry after an attempt has executed an effective SETVAL, or implement an attempt journal that preserves the expression result without replaying the external mutation. Do not attempt partial sequence rollback across concurrent sessions.',
 'RETRY_ROW_VS_SINGLE_EXECUTION_EQUAL_SEQUENCE_STATE',
 'HIDDEN_ATTEMPT_FEEDBACK_INTO_RETRY_OUTPUT',
 'pessimistic-retry-reexecutes-setval-and-persists-null',
 'TiDB current source b8d04e17; SQL-only real-TiKV reproduction on TiDB 5c9198e, testbed 8220955, metadata locking enabled',
 1,
 'issue-filed',
 'https://github.com/pingcap/tidb/issues/69822',
 'Discovered from current-source retry ownership and mutable-effect analysis, not PR review or issue findings. Local natural-conflict RED, conservative retry-decline GREEN, real-TiKV RED, same-state control, and equal sequence-state anti-oracle are all present. Post-RED GitHub search found no exact root. Filed as TiDB issue 69822 with severity/major and found-by-ai. High silent-wrong-data consequence, low trigger frequency.');
