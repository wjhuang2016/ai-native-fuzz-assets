INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2340003,
 'Pipelined DML can return a definite failure after its primary commit is durable',
 'high',
 'atomicity/data-integrity',
 'BULK autocommit DML',
 'client-go pipelined commit / undetermined promotion',
 'TiKV durably commits the pipelined transaction primary, but TiDB can return an ordinary transport or region error and keep the SQL connection usable. Retrying a non-idempotent logical operation can apply it twice.',
 'Run TestAINativePipelinedCommitResponseLossRED from the stored client-go integration probe. The wrapper forwards Commit RPCs until real TiKV returns the first successful CommitResponse, drops that response, and makes subsequent Commit RPCs fail. After Commit returns, restore the real client and read the key in a fresh transaction.',
 'Once a successful primary Commit response is lost, Commit returns ErrResultUndetermined. TiDB maps that error and closes the connection so the outcome is not represented as a definite retryable failure.',
 'On current upstream client-go 01bd8f99 and nightly TiKV 7ecce12e, the fresh transaction reads the committed value, but Commit returns ordinary injected commit transport outage and IsErrorUndetermined is false.',
 'actionCommit records primary RPC ambiguity in twoPhaseCommitter.undeterminedErr. Ordinary 2PC commitTxn promotes any raw error with that side state to ErrResultUndetermined. The pipelined execute branch calls commitFlushedMutations directly, and that specialized finalizer returned commitMutations raw errors without the promotion.',
 'At the pipelined commitFlushedMutations terminal boundary, if commitMutations fails and getUndeterminedErr is non-nil, return ErrResultUndetermined as ordinary commitTxn does. Keep durable-state and cleanup behavior unchanged.',
 'POST_APPLY_MVCC_TRUTH_VS_TERMINAL_ERROR_CLASS',
 'SIDE_STATE_SEMANTIC_PROMOTION_BYPASS',
 'pipelined-commit-bypasses-undetermined-promotion',
 'client-go master 01bd8f99; nightly TiKV 7ecce12e; TiDB 5c9198e with MDL ON',
 1,
 'confirmed',
 NULL,
 'Discovered from current source and a bounded owner-graph source packet, without PR review or issue seeds. Post-RED searches found no exact client-go, TiDB, or found_bug root. Internal severity remains high for catalog consistency; consequence is critical data integrity. Frequency is lower because tidb_dml_type=BULK is opt-in and the fault must land after primary apply but before response delivery. Local RED/GREEN and current-nightly real-TiKV RED/GREEN are stored in the private asset repository.');
