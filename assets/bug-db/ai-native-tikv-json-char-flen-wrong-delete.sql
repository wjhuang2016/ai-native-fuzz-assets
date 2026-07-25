INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(3150003,
 'TiKV JSON-to-CHAR pushdown ignores target length and DELETE can remove rows after TiDB would abort',
 'high',
 'data-loss',
 'UPDATE/DELETE',
 'JSON cast coprocessor pushdown',
 'CAST(json_col AS CHAR(n)) ignores n when evaluated in TiKV. A pushed filter can select a row whose TiDB-projected predicate is false, and strict-mode UPDATE or DELETE can mutate it even though root TiDB returns Data Too Long and leaves the table unchanged.',
 'Insert JSON scalars 1234.5, 1234, and 12. Compare WHERE CAST(j AS CHAR(4))<>''1234'' with the same predicate inside IF(SLEEP(0)=0,...,NULL). Then run DELETE using the pushed predicate on one copy and the root-forced predicate on another.',
 'Return field length, truncation warning/error behavior, and row membership must be identical in TiDB and TiKV. Under default strict DML semantics the overlong JSON cast must abort before any row is changed.',
 'The pushed query selected ids 1 and 3; root TiDB selected only id 3. Id 1 was returned with cast_value=1234 and predicate_holds=0. Pushed DELETE removed ids 1 and 3 and left only id 2. Root-forced DELETE returned error 1406 and preserved all three rows.',
 'TiDB builtinCastJSONAsStringSig calls ProduceStrWithSpecifiedTp with the return FieldType and enforces flen. TiKV cast_json_as_bytes captures only EvalContext; ConvertTo<String> for JsonRef returns the full JSON string and has an explicit FIXME that TiDB performs an additional ProduceStrWithSpecifiedTp step. The return-type context never reaches the remote evaluator.',
 'Pass RpnFnCallExtra/ret_field_type into cast_json_as_bytes and apply the same ProduceStrWithSpecifiedTp logic and error context as TiDB. Disable CastJsonAsString pushdown until flen, charset, collation, padding, and truncation semantics are equivalent.',
 'pushdown-root-rowset-self-predicate-strict-error-delete-preimage',
 'remote-evaluator-context-parameter-differential',
 'tikv-json-cast-string-return-type-context-omission',
 'TiDB nightly ed2376acc6; TiKV nightly 730be34f95; current TiDB 05b396fb66 and TiKV 91ccfb2126',
 1,
 'confirmed',
 NULL,
 'Default strict sql_mode, one TiDB, one PD, one real TiKV, MDL=ON, no concurrency, retry, failpoint, source patch, process pause, or node/network/disk fault. Any serialized JSON value longer than CHAR(n) reaches the missing context path. Current TiKV master fails a focused compatibility assertion, returning 1234.5 instead of CHAR(4) value 1234. Post-RED TiDB, TiKV, and found_bug searches found no exact root. The SQL pattern is specific enough to keep catalog severity high rather than critical, despite direct silent row deletion.');
