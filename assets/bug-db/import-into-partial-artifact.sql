INSERT INTO found_bug (
  title, severity, category, ddl_op, feature,
  symptom, repro, expected, actual, root_cause, fix_hint,
  oracle, method, root_cause_id, affects, confirmed, status, notes
) VALUES (
  'IMPORT INTO can leave durable rows without secondary indexes after index-engine terminal failure',
  'high',
  'correctness/data-integrity',
  'IMPORT INTO ... FROM SELECT',
  'standalone local-sort multi-engine import',
  'IMPORT INTO returns an error, but table scans expose imported rows while forced secondary-index scans miss them; ADMIN CHECK TABLE reports 8223.',
  'Set GLOBAL tidb_enable_dist_task=OFF. Create src(a PK,b) with rows (1,10),(2,20),(3,30) and dst(a PK,b,INDEX ib(b)). Inject an index-engine terminal error immediately after closedDataEngine.Import succeeds and before indexEngine.Close. Run IMPORT INTO dst FROM SELECT a,b FROM src. Then compare dst IGNORE INDEX(ib), dst USE INDEX(ib) WHERE b>=0, and ADMIN CHECK TABLE dst.',
  'If IMPORT INTO returns an error, it must not leave an unrecoverable physical inconsistency. Either no durable target KVs are visible, or every visible row has its required secondary-index entries and recovery state remains available.',
  'The statement returns ERROR 1105. A table scan returns 3 rows, the forced ib scan returns 0 rows, and ADMIN CHECK TABLE returns ERROR 8223. No-fault control is 3/3/green; a fault before data-engine Import is 0/0/green.',
  'ImportSelectedRows closes and irreversibly imports the data engine before closing/importing the index engine. A later index Close/Import error returns to the user; deferred cleanup cannot roll back imported record KVs and removes local index-engine recovery state.',
  'Prepare/close all sibling artifacts before the first irreversible import, and retain a resumable repair/checkpoint owner until data, indexes, and PostProcess all complete. Fix validation must cover both index Close and index Import failures after data import.',
  'O_IMPORT_ROW_INDEX_TERMINAL_CONSISTENCY',
  'SIBLING_ARTIFACT_PRECOMMIT_ATOMICITY',
  'importinto-data-before-index-finalization',
  'current master 13282a8bd06b; standalone IMPORT INTO FROM SELECT with local sort',
  1,
  'confirmed',
  'Authorized testbed 8220955. RED: import error, table/index=3/0, ADMIN CHECK 8223. GREEN controls: no fault=3/3; fault before data import=0/0. Distinct from id1260008 writer-Close reachability.'
);

SELECT LAST_INSERT_ID() AS inserted_bug_id;
