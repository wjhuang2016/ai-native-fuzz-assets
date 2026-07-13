INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,notes)
VALUES
(2160003,
 'ADMIN CLEANUP INDEX retains executor batch state across transaction retry',
 'moderate',
 'correctness/reliability',
 'ADMIN CLEANUP INDEX',
 'retryable txn batch state',
 'After a retryable error at the cleanup transaction Commit, a small cleanup can succeed with an inflated removed-row count, while a cleanup with more than 20000 scanned index entries can fail with an index-out-of-range panic.',
 'Create a table with a secondary index and inject dangling index entries directly. Force the first RunInNewTxn Commit used by ADMIN CLEANUP INDEX to return kv.ErrTxnRetryable. With 3 dangling entries, assert that the command reports 3. With 20001 entries, assert that the command completes, reports 20001, and ADMIN CHECK INDEX succeeds.',
 'A retried attempt starts from the same committed batch frontier with empty attempt-local buffers and counters. The command reports the number of index entries actually removed and completes for any batch count.',
 'The 3-entry oracle reported 9. The 20001-entry oracle reached RunInNewTxn retry and then failed with runtime error: index out of range [20000] with length 20000. Resetting lastIdxKey, scanRowCnt, batchKeys, idxValues, and removeCnt at every attempt made both matrices pass.',
 'CleanupIndexExec.fetchIndex, batchGetRecord, and deleteDanglingIdx mutate executor fields inside the RunInNewTxn callback. A retry rolls back only KV writes. The callback re-enters with the advanced lastIdxKey, nonzero scanRowCnt/removeCnt, populated idxValues, and appended batchKeys from the failed attempt.',
 'Keep the committed batch frontier outside RunInNewTxn and restore all attempt-local executor fields at callback entry, or move the batch state into callback-local values and publish it only after Commit succeeds.',
 'ADMIN_CLEANUP_RETRY_COUNT_COMPLETION_AND_INDEX_CHECK',
 'ATTEMPT_SCOPED_RECEIVER_STATE_ROLLBACK_CLOSURE',
 'admin-cleanup-index-retry-state-survives-rollback',
 'current master 13282a8bd06b; ADMIN CLEANUP INDEX on nonpartition tables',
 1,
 'confirmed',
 'Independent current-source S45 scan and local failpoint RED/GREEN. PR reviews, issues, fixes, and history were excluded until after RED; post-RED GitHub searches found no exact issue. A natural equivalent is a write conflict on a dangling index key, for example overlapping cleanup/repair activity, but this round did not reproduce that race on a live cluster. Severity is moderate because the observed durable index state is repaired or the command fails; no wrong durable data was proved.');
