INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2040003,
 'Distributed ADD INDEX can publish a partial index after a transient TSO error during plan generation',
 'high',
 'wrong_result',
 'ALTER TABLE ADD INDEX',
 'distributed fast reorg / DXF read-index planning',
 'ALTER TABLE reports success and publishes the index, but rows in the unplanned tail range are absent from index scans. Ordinary queries forced onto the index silently miss committed rows.',
 'Use two TiDB task executors and enable tidb_enable_dist_task plus tidb_ddl_enable_fast_reorg. Create a nonpartition table whose populated row range spans 101 TiKV regions, set tidb_max_dist_task_nodes=2 so the planner uses batches of 100 regions, and make the second allocNewTS call return a transient error. Run ADD INDEX, compare IGNORE INDEX with FORCE INDEX, then run ADMIN CHECK TABLE.',
 'A transient TSO error must either fail/retry the whole planning attempt or rebuild a complete two-batch plan. A successful DDL must index every committed row.',
 'On testbed 8220955, job 5456 finished synced with max_node_count=2, but the table scan returned ids 1 and 100999 while FORCE INDEX returned only id 1. The tail-row count was table=1/index=0 and ADMIN CHECK TABLE returned 8223 for handle 100999.',
 'generatePlanForPhysicalTable appends the first batch meta, then returns (retry=true, err=nil) when allocNewTS fails for a later batch. handle.RunWithRetry treats nil error as success, and the only postcondition checks len(subTaskMetas)>0, so the partial plan is published. Propagating the error alone is insufficient because subTaskMetas is outside the retry closure and accumulates failed-attempt metas.',
 'Return the real TSO error and rebuild/reset all derived subtask metas at the start of every retry attempt, or construct an attempt-local plan and publish it atomically only after complete source-range coverage is proven.',
 'COMPLETE_PLAN_OR_FAIL_INDEX_DIFFERENTIAL',
 'RETRY_ATTEMPT_DERIVED_PAYLOAD_ATOMICITY',
 'distributed-backfill-partial-plan-on-tso-error',
 'current master 13282a8bd06b and authorized testbed 8220955',
 1,
 'issue-filed',
 'https://github.com/pingcap/tidb/issues/69789',
 'Discovery used only current-source retry-closure proof obligations and an independently designed two-batch matrix. PR reviews, issues, and fixes were excluded until after the local and live RED; post-RED local asset and upstream issue searches found no exact root.');
