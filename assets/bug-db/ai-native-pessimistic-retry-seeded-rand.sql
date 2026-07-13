INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2400003,
 'Pessimistic retry can advance seeded RAND and change failure into success',
 'high',
 'data-integrity',
 'UPDATE',
 'pessimistic READ COMMITTED retry / constant-seed RAND evaluator',
 'A hidden retry consumes the next deterministic RAND value, turns a duplicate-key failure into success, and commits a different unique key.',
 'In session A, run a pessimistic RC UPDATE assigning IF(RAND(12345)<0.8,1,2)+SLEEP(20)*0. During the sleep, session B commits u=1. Compare the retried table with an identical table executed once from the same final state.',
 'Return the duplicate-key conflict and preserve rows (1,10),(2,1), or restore all evaluator state to statement entry before transparent replay.',
 'The retry reports success with exec_retry_count=1 and rows (1,2),(2,1); the same-state direct control returns duplicate key and retains (1,10),(2,1).',
 'Constant-seed RAND owns a mutable MysqlRng. Eval advances it, Clone aliases it, and pessimistic retry rebuilds from the already-advanced expression state, so the successful attempt consumes the second seeded value.',
 'Snapshot mutable evaluator state at statement entry and restore it before replay, or decline transparent retry after a non-restorable evaluator has been consumed.',
 'SEEDED_RAND_DUPLICATE_VS_SUCCESS_AND_ROWSET',
 'MUTABLE_EVALUATOR_STATE_SURVIVES_RETRY',
 'pessimistic-retry-advances-seeded-rand-output',
 'TiDB b8d04e17 local; real TiKV TiDB 5c9198e, MDL ON',
 1,
 'issue-filed',
 'https://github.com/pingcap/tidb/issues/69823',
 'Discovered from current-source mutable evaluator ownership, not PR review or issue findings. Local natural-conflict RED, retry-decline GREEN, real-TiKV RED, same-state control, and deterministic numeric control are present. Deep-copy-at-retry remained RED, proving snapshot timing is part of the root. Severity is high consequence with low trigger frequency.');

