INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2520003,
 'Killed EXPLAIN ANALYZE DML can return an error after committing its mutation',
 'high',
 'atomicity/data-integrity',
 'EXPLAIN ANALYZE UPDATE',
 'autocommit EXPLAIN ANALYZE DML / lazy result / SQLKiller',
 'TiDB can return error 1317 Query execution was interrupted for an EXPLAIN ANALYZE DML whose mutation is already committed. Retrying a non-idempotent operation can apply the effect twice.',
 'Execute autocommit EXPLAIN ANALYZE UPDATE through Session.ExecuteStmt without fetching its result set. Deliver the same SQLKiller QueryInterrupted signal used by KILL QUERY, call the first RecordSet.Next, and inspect the row from a fresh session.',
 'Terminal result and durable state agree: either interruption wins and the row remains unchanged, or commit wins and TiDB returns a successful explain result.',
 'The first Next returned Query execution was interrupted, while a fresh session observed v changed from 0 to 1. An explicit-transaction control with the same signal and rollback left v=0.',
 'ExecStmt executes the inner DML eagerly but leaves explain rendering lazy. session.ExecuteStmt commits the autocommit transaction before returning the non-nil RecordSet. The first recordSet.Next then calls SQLKiller.HandleSignal before ExplainExec generates its first chunk, so a pending KILL is converted into a definite statement error after commit is irreversible.',
 'Do not expose a fallible lazy terminal boundary after irreversible commit. Generate and buffer the first explain result before commit, or after commit ensure a late SQLKiller signal cannot be reported as a definite failure of the already committed statement.',
 'TERMINAL_ERROR_VS_FRESH_DURABLE_STATE',
 'IRREVERSIBLE_EFFECT_BEFORE_LAZY_TERMINAL_CHECK',
 'explain-analyze-dml-commit-before-kill-check',
 'current master b8d04e17a2ca, default MDL ON',
 1,
 'issue-filed',
 'https://github.com/pingcap/tidb/issues/69829',
 'Discovered from current-source specialized-finalizer analysis without PR review or issue seeds. Deterministic local RED, explicit-transaction ordering control, and post-RED dedup are recorded. Upstream issue is labeled severity/critical and found-by-ai. Catalog severity remains high because EXPLAIN ANALYZE DML and the post-commit kill window are lower frequency, while the terminal-integrity consequence is critical.');
