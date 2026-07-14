INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2430003,
 'Pessimistic retry can reuse stale materialized CTE result and commit a mixed-attempt row',
 'high',
 'data-integrity',
 'UPDATE',
 'pessimistic READ COMMITTED retry / materialized CTE storage',
 'A successful retry commits one row whose unique key comes from the retry snapshot while another column comes from the failed attempt CTE result.',
 'Reference a CTE twice in a pessimistic RC UPDATE so it materializes. During SLEEP in the CTE, another session claims u=1 and changes source next_u,payload from 1,10 to 2,20. Compare the retried row with one execution from the same final state.',
 'Either expose the original conflict or rebuild every attempt-scoped materialization; retry and direct control should both persist row 1,2,20.',
 'The retry reports success with exec_retry_count=1 and persists 1,2,10; the direct same-state control persists 1,2,20.',
 'StatementContext.CTEStorageMap and sync.Once survive retry. A completed resTbl is preserved by CTEExec.Close, so buildCTE reuses the failed attempt producer/result while ordinary reads use the retry snapshot.',
 'Close and recreate attempt-scoped CTE storage before retry executor construction, or bind materialization state to an attempt generation and reject stale entries.',
 'MIXED_ATTEMPT_ROW_VS_SAME_STATE_CONTROL',
 'REPLAY_PERSISTENT_MATERIALIZATION_STATE',
 'pessimistic-retry-reuses-completed-cte-result',
 'TiDB b8d04e17 local; real TiKV TiDB 5c9198e, MDL ON',
 1,
 'issue-filed',
 'https://github.com/pingcap/tidb/issues/69826',
 'Discovered from current-source statement materialization lifecycle after expression-clone exhaustion, not PR review or issue findings. Local natural-conflict RED, exact CTE-storage reset GREEN, bounded source-packet confirmation, real-TiKV RED, and same-final-state control are complete. High silent-wrong-data consequence; materialized CTE plus retry conflict narrows frequency.');

