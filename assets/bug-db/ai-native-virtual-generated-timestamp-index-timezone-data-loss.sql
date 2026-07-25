INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(3240003,
 'Indexed virtual DATE(TIMESTAMP) can delete rows that fail the predicate after a time-zone change',
 'high',
 'data-loss',
 'CREATE TABLE/INSERT/DELETE',
 'indexed virtual generated column',
 'An index key for a virtual DATE(TIMESTAMP) generated column is computed in the writer session time zone, while the virtual value is recomputed in the reader session time zone. The index can return and DELETE a row whose current generated value and WHERE predicate are false.',
 'Create t(id primary key,ts TIMESTAMP,d DATE AS (DATE(ts)) VIRTUAL,index idx_d(d)). In time_zone=+08:00 insert (1,''2025-01-01 04:00:00''). Switch to -08:00. IGNORE INDEX returns no rows for d=''2025-01-01'', while USE INDEX returns id 1 and projects ts/d/DATE(ts) as 2024-12-31 with predicate_holds=0. Default DELETE WHERE d=''2025-01-01'' uses IndexRangeScan, reports one affected row, and removes it. The matched IGNORE INDEX DELETE affects zero and preserves the row.',
 'An indexed virtual generated column must represent the value exposed by its current expression semantics. A row whose generated value fails the WHERE predicate must not be returned or deleted.',
 'Root row set is empty; index row set is {1}. The index-returned row disproves its own predicate. Indexed DELETE succeeds and permanently removes the row; root DELETE affects zero. Default ADMIN CHECK TABLE does not report this stale-key direction. Same-time-zone and DATETIME controls are green.',
 'TiDB rejects direct DATE(TIMESTAMP) expression indexes as unsafe under default config, but checkIllegalFn4Generated enforces the non-GA function gate only for genType=typeIndex. A manually declared virtual generated column is admitted as typeColumn and a later ordinary index does not revalidate its expression. Insert evaluates the generated key with the writer session EvalContext; reads reevaluate the virtual value with the reader session EvalContext.',
 'Apply expression-index safety admission to every indexed generated-column composition. Reject context-sensitive indexed virtual expressions or freeze/canonicalize their semantic context. Recheck base-row generated predicates before irreversible DML, and make ADMIN CHECK use the same canonical contract.',
 'virtual-index-self-contradiction-delete-preimage',
 'composable-safety-gate-closure',
 'virtual-generated-timestamp-index-writer-timezone-dependent',
 'TiDB nightly ed2376acc6; current TiDB master 05b396fb66; TiKV nightly 730be34f95',
 1,
 'confirmed',
 NULL,
 'One TiDB, one PD, one real TiKV; default strict sql_mode; MDL ON; allow-expression-index remains at its default disabled state; no partial index, concurrency, retry, failpoint, source patch, process pause, or node/network/disk fault. Direct expression-index syntax returns ERROR 8200, proving the intended safety gate; virtual generated column plus ordinary index bypasses it. Relevant source files are unchanged between the tested nightly and current master. Post-RED GitHub and remote found_bug searches found no exact root. Distinct from id3210003, whose persisted owner is partial-index membership; this root is the generated index key and its admission gate. Critical silent-data-loss consequence with a common production shape; stored as high by catalog convention.');
