INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(3120003,
 'TiKV pushdown rounds negative TIME half-seconds differently and DML can modify rows that fail its predicate',
 'high',
 'data-corruption',
 'UPDATE/DELETE',
 'coprocessor expression pushdown',
 'A WHERE predicate using CAST(TIME(6) AS SIGNED) can select a negative half-second row in TiKV even though TiDB evaluates the same cast to zero. A result row can report predicate_holds=0, and an ordinary UPDATE can persistently modify that row.',
 'Create t(id INT PRIMARY KEY,dur TIME(6),marker INT), insert -00:00:00.499999, -00:00:00.500000, and -00:00:00.500001, then run WHERE CAST(dur AS SIGNED)<0. Compare it with the same predicate wrapped in IF(SLEEP(0)=0,...,NULL) to keep evaluation in TiDB, then execute UPDATE t SET marker=1 WHERE CAST(dur AS SIGNED)<0.',
 'A deterministic expression must preserve exact row membership when pushed from TiDB into TiKV. UPDATE and DELETE must not mutate a row that fails the SQL predicate under TiDB semantics.',
 'The pushed predicate selected ids 2 and 3, while root TiDB selected only id 3. The returned id=2 row showed cast_value=0 and predicate_holds=0. UPDATE used a cop[tikv] Selection, affected ids 2 and 3, and persistently changed id 2; the root-only UPDATE affected only id 3.',
 'TiDB builtinCastDurationAsIntSig rounds Duration through types.Duration.RoundFrac, which anchors the operation on a Go time value and rounds a negative exact half-second toward zero. TiKV Duration::to_int calls Duration::round_frac directly and rounds the same negative tie away from zero. ScalarFuncSig_CastDurationAsInt is still admitted for pushdown without a semantic-equivalence guard.',
 'Make TiDB and TiKV use one shared negative-duration tie rule and add cross-engine boundary cases at .499999, .500000, and .500001 for every pushed cast. Until semantics are aligned, do not push CastDurationAsInt.',
 'pushdown-root-rowset-self-predicate-dml-preimage',
 'source-todo-directed-cross-engine-differential',
 'tikv-duration-cast-negative-half-tie-semantic-drift',
 'TiDB nightly ed2376acc6; TiKV nightly 730be34f95; current TiDB 05b396fb66 and TiKV 91ccfb2126',
 1,
 'confirmed',
 NULL,
 'Default strict sql_mode, one TiDB, one PD, one real TiKV, MDL=ON, no failpoint, source patch, process pause, retry, or node/network/disk fault. Current TiKV master fails a focused compatibility assertion with output -1 versus TiDB 0. Post-RED searches in pingcap/tidb, tikv/tikv, and found_bug found no exact root. Trigger requires a negative TIME value exactly at a half-second tie and an explicit numeric cast, so catalog severity remains high rather than critical despite persistent wrong DML.');
