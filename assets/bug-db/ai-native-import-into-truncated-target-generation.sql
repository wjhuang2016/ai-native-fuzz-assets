INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2850003,
 'NextGen IMPORT INTO can report success after writing the full input into a truncated table generation',
 'high',
 'data-loss',
 'IMPORT INTO / TRUNCATE TABLE',
 'NextGen cross-keyspace DXF import target generation',
 'A detached IMPORT INTO job and its SYSTEM DXF task report finished with the full row count, while the current target table receives none of the imported rows. The rows are physically written under the table ID retired by TRUNCATE and have no live SQL owner.',
 'Run NextGen with a user keyspace and a CSE TiKV worker. Stop the worker; create empty t; submit file IMPORT INTO t WITH DETACHED; after the job and DXF task are queued, TRUNCATE TABLE t and insert a marker into the new generation; restart the worker; wait for finished; compare the current table and a raw record-prefix scan of the pre-TRUNCATE table ID.',
 'TRUNCATE is blocked while the import owns the table, or the scheduler/worker rejects the task because the persisted target generation is no longer live.',
 'The job reports finished with row-count 2. Current t, whose ID changed from 44 to 46, contains only the post-TRUNCATE marker. Both imported records exist under retired table ID 44, and ADMIN CHECK TABLE t succeeds.',
 'NewImportPlan snapshots TableInfo and table ID. Classic IMPORT INTO sets TableModeImport, but NextGen user-keyspace submission skips it and commits a user job before creating a SYSTEM-keyspace DXF task. Scheduler preparation and the CSE task executor never bind the cached ID to a live table generation; the executor reconstructs a table from cached TableInfo and completion records statistics against the same retired ID.',
 'Persist the target generation identity and revalidate it before preparation and before the first irreversible write. Add a NextGen-compatible table-operation fence or generation lease so TRUNCATE/DROP cannot retire an active import target. Fail closed and cancel the job when the generation differs.',
 'IMPORT_JOB_TERMINAL_PLUS_LIVE_AND_RETIRED_TABLE_ID_ATTRIBUTION',
 'LIVE_RESOURCE_IDENTITY_ACROSS_OWNER_HANDOFF',
 'importinto-nextgen-stale-target-generation',
 'TiDB master 231dad5225f0; NextGen user and SYSTEM keyspaces; CSE TiKV server/worker ce46fc5067; PD 8.5.4; MDL enabled',
 1,
 'confirmed',
 NULL,
 'Discovered from current source and proof-obligation testing without PR-review findings. Strongest RED uses no product failpoint: worker absence creates a legal queue delay, ordinary TRUNCATE retires the generation, and worker restart completes the stale task. A no-DDL real-TiKV control is GREEN. Atomic rename/swap and scheduler-unit rows localize the same missing identity guard. Post-RED GitHub issue and remote bug searches found no exact root. Severity is high/C3 direct; critical requires product-scope calibration.');

