INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2190003,
 'Pessimistic RC retry publishes LAST_INSERT_ID from a rolled-back attempt',
 'high',
 'wrong_result',
 'UPDATE',
 'pessimistic retry last insert id',
 'A pessimistic READ COMMITTED UPDATE can internally retry after evaluating LAST_INSERT_ID(expr). If the retry matches zero rows, the statement returns success and publishes the value from the rolled-back attempt. A following statement can persist that nonexistent allocation into another table.',
 'Set LAST_INSERT_ID to 7. In pessimistic RC, update row 1 to a concurrently claimed unique key and set another column to LAST_INSERT_ID(99+SLEEP(20)), guarded by NOT EXISTS on a gate row. During SLEEP, another transaction claims the unique key and inserts the gate. The UPDATE retries, matches zero rows, and succeeds. Query LAST_INSERT_ID and insert it into a sink table.',
 'The successful zero-match attempt is the only user-visible attempt. LAST_INSERT_ID remains 7 and the sink receives 7, matching an execution that starts from the final database state.',
 'The UPDATE reported 0 matched/changed rows but LAST_INSERT_ID became 99. The following INSERT persisted 99. The business row was unchanged and the concurrent unique-key/gate rows were present. Without the failed attempt, the same zero-match UPDATE kept and persisted 7.',
 'LAST_INSERT_ID(expr) sets StatementContext.LastInsertID and LastInsertIDSet during expression evaluation. After the later unique-key lock conflict, pessimistic statement retry calls StmtRollback and StatementContext.ResetForRetry. ResetForRetry clears row counters and warnings but not those two fields. When the rebuilt RC executor sees the new gate and matches zero rows, no successful-attempt setter overwrites the failed-attempt value, which is then published.',
 'Treat LastInsertID and LastInsertIDSet as attempt-scoped StatementContext state. Restore their statement-entry values, or at minimum clear the current-statement value and set flag in ResetForRetry before rebuilding the executor. The latter made the exact RED matrix return and persist 7.',
 'ZERO_MATCH_RETRY_LAST_INSERT_ID_AND_DURABLE_SINK_DIFFERENTIAL',
 'ATTEMPT_SCOPED_SIDE_EFFECT_ROLLBACK_CLOSURE',
 'pessimistic-retry-omits-last-insert-id-reset',
 'current master 13282a8bd06b and authorized testbed 8220955',
 1,
 'issue-filed',
 'https://github.com/pingcap/tidb/issues/69796',
 'Discovered from current source and the S45 rollback-closure method, not from PR review findings. Local natural-conflict RED and exact ResetForRetry GREEN were followed by SQL-only real-TiKV RED and a same-final-state zero-match control. Post-RED local and GitHub searches found no exact root. Distinct from id2100003: this survivor is StatementContext LAST_INSERT_ID publication state, not UserVars consumed while rebuilding the same DML.');
