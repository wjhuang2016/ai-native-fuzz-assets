INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2460003,
 'Pessimistic retry returns explicit auto-increment ID from a rolled-back attempt',
 'high',
 'wrong_result',
 'INSERT SELECT',
 'pessimistic retry MySQL OK-packet insert id',
 'An INSERT SELECT can succeed after one transparent pessimistic retry and affect zero rows, while the MySQL OK packet returns an explicit auto-increment ID evaluated only by the rolled-back attempt. An application can persist or associate later data with an ID that the successful attempt never inserted.',
 'In pessimistic READ COMMITTED, INSERT SELECT an explicit id 42 and a sleeping unique value while guarding the source row with NOT EXISTS on a gate. During the sleep, another transaction inserts the unique value and the gate. The first attempt conflicts; the successful retry sees the gate and inserts zero rows. Read sql.Result.RowsAffected and LastInsertId, then persist that ID. Compare with direct execution from the same final database state.',
 'Both the retried statement and the same-state direct control return affected_rows=0 and last_insert_id=0. The sink stores zero for both arms.',
 'The retried statement returned affected_rows=0 and last_insert_id=42 while the control returned 0 and 0. Only the competitor row existed in the destination, but the sink durably stored retry=42 and control=0.',
 'The explicit nonzero auto-increment path writes StatementContext.InsertID before the later unique-key conflict. Pessimistic retry rolls back statement KV state and calls StatementContext.ResetForRetry, but that reset omits InsertID. The zero-row successful attempt does not overwrite it; session.LastInsertID falls back to stale InsertID and the server serializes 42 in the OK packet.',
 'Clear attempt-owned InsertID at the pessimistic retry boundary, or restore a statement-entry snapshot if a broader compatibility contract requires one. Clearing only InsertID in ResetForRetry made the exact natural-conflict test pass.',
 'ZERO_ROW_RETRY_OK_PACKET_VS_SAME_STATE_CONTROL',
 'PROTOCOL_OUTPUT_RESET_DIFFERENTIAL',
 'pessimistic-retry-publishes-failed-attempt-insert-id',
 'current master b8d04e17a2ca and authorized real-TiKV testbed 8220955',
 1,
 'issue-filed',
 'https://github.com/pingcap/tidb/issues/69827',
 'Discovered from current-source terminal-output ownership, without PR review or issue seeds. Local natural-conflict RED, exact InsertID-reset GREEN, same-final-state control, driver-level real-TiKV RED, slow-log retry proof, MDL ON, and cleanup were recorded. Distinct from #69796: that root owns LastInsertID and LastInsertIDSet set by LAST_INSERT_ID(expr); this root owns singleton InsertID set by explicit AUTO_INCREMENT input.');
