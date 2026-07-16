INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2730003,
 'Pessimistic RC retry reuses a stale scalar-subquery constant and commits mixed-attempt data',
 'high',
 'atomicity/data-integrity',
 'UPDATE',
 'pessimistic READ COMMITTED retry / scalar subquery preprocessing',
 'A successful UPDATE and COMMIT persist rows whose unique routing value comes from the successful retry while an aggregate value comes from the failed attempt.',
 'Use a pessimistic READ COMMITTED transaction to UPDATE a target through a configuration-table join while assigning another column from a non-correlated scalar aggregate over the target. Let a concurrent allocator claim the first attempt unique value, advance the configuration value, and add a row included by the aggregate before the first attempt reaches pessimistic locking. The natural unique-key write conflict makes TiDB retry the statement. MDL stays ON; pessimistic mode and the default max-retry-count of 256 remain unchanged; READ COMMITTED is the only common non-default setting. SLEEP in the deterministic probe only replaces batch scan or storage latency.',
 'Under READ COMMITTED, one attempt uses one statement TS for both the scalar read and the DML source. The only coherent rows are old scalar/old source if the allocator commits after that TS, or new scalar/new source if it commits before the TS. The successful retry must persist the new/new state or expose the conflict.',
 'On a one-TiDB/three-TiKV testbed, UPDATE affected two rows, COMMIT succeeded, and ADMIN CHECK passed, but the target was (1,100,31),(2,300,32),(3,200,999) while a one-shot execution from the successful-attempt state was (1,100,1030),(2,300,1031),(3,200,999). A no-retry RC control with the publisher committing after statement start produced old/old, not the mixed result.',
 'The expression rewriter evaluates the non-correlated scalar subquery during planning and embeds its row as expression.Constant values carrying SubqueryRefID. Pessimistic RC retry refreshes the statement TS, but handlePessimisticLockError rebuilds only the executor from the existing ExecStmt.Plan. The constant therefore retains the failed-attempt generation while ordinary plan reads use the retry generation.',
 'Make preprocessed data-dependent constants attempt-scoped. Before an accepted retry with a refreshed statement TS, either rebuild the plan after rolling back the failed attempt or preserve an evaluable scalar-subquery node and invalidate/re-evaluate it for the new attempt. Add a generation assertion so plan-derived data cannot outlive its statement TS.',
 'RC_ALLOWED_ONE_ATTEMPT_SET_PLUS_REPLAN',
 'ATTEMPT_LOCAL_PREPROCESSED_CONSTANT_REUSE',
 'pessimistic-retry-reuses-preprocessed-scalar-constant',
 'TiDB 531e40c local and d573e28 real TiKV; TiKV 67fccdb; MDL ON; common READ COMMITTED',
 1,
 'confirmed',
 NULL,
 'Discovered from current source and an isolation-aware retry matrix, not a PR-review finding. Real-TiKV RED, real-TiKV allowed-outcome control, local retry-count RED, and exact replan counterfactual GREEN are complete. Post-RED GitHub issue and PR searches found no exact scalar-subquery retry root. Distinct from #69826, whose state owner is completed CTEStorageMap materialization rather than a scalar result embedded in ExecStmt.Plan. Silent logical data corruption is high severity; trigger frequency is bounded by common RC plus this SQL shape plus a natural retry conflict.');
