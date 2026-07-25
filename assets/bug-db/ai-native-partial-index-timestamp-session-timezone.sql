INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(3210003,
 'TIMESTAMP partial index membership depends on writer time zone and causes wrong DELETE',
 'high',
 'data-corruption',
 'CREATE INDEX/INSERT/DELETE',
 'partial index over TIMESTAMP',
 'Rows representing the same UTC instant can receive different partial-index membership when written by sessions with different time_zone values. A unique partial index can contain only one of two logical members, ADMIN CHECK fails, and indexed DML can silently miss a matching row.',
 'Create t(id primary key,k,ts TIMESTAMP,unique index uk(k) where ts >= ''2025-01-01 00:00:00''). In time_zone=-08:00 insert (1,7,''2024-12-31 12:00:00''). In time_zone=+08:00 insert (2,7,''2025-01-01 04:00:00''). These are one UTC instant. Under +08:00, IGNORE INDEX returns ids 1,2 but USE INDEX(uk) returns id 2. ADMIN CHECK returns 8223. DELETE with the same predicate and k=7 uses uk Point_Get, reports one affected row, and leaves id 1 although its predicate evaluates true.',
 'One schema predicate over one canonical stored TIMESTAMP must have one membership result independent of the writer session. The second logical unique member must be rejected, table and index row sets must agree, and matched DELETE must remove every preimage row.',
 'Both inserts succeed. In the +08:00 observer session both rows render as 2025-01-01 04:00:00, satisfy the predicate, and have k=7, but only id 2 has an index key. ADMIN CHECK reports missing handle 1. DELETE reports success with ROW_COUNT()=1 and leaves id 1 with predicate=true.',
 'Partial-index conditions are parsed and evaluated through process-global indexConditionECtx, while the TIMESTAMP datum supplied by the mutation path has already been shaped into the writer session wall-clock representation. Persisted index-key presence therefore varies by writer context. Planner implication later treats the syntactically matching predicate as proof that the partial index is complete.',
 'Evaluate schema-owned predicates over canonical TIMESTAMP values with frozen schema semantics. Use the same semantic representation in mutation, backfill/check, and optimizer implication paths. Until then, reject TIMESTAMP partial-index predicates or avoid using them as complete access paths across time zones.',
 'same-instant-partial-unique-delete-closure',
 'persisted-evaluator-context-closure',
 'partial-index-timestamp-membership-writer-timezone-dependent',
 'TiDB nightly ed2376acc6; current TiDB master 05b396fb66; TiKV nightly 730be34f95',
 1,
 'confirmed',
 NULL,
 'One TiDB, one PD, one real TiKV; default strict sql_mode; MDL ON; fast table check ON; no concurrency, retry, failpoint, source patch, process pause, or node/network/disk fault. Same-time-zone control rejects the duplicate with 1062 and passes ADMIN CHECK. Relevant partial-index source files are unchanged between the tested nightly and current master. Post-RED searches found no exact TiDB issue or found_bug root. The affected-table consequence is critical, but catalog severity is high because a TIMESTAMP partial index and mixed session time zones are required.');
