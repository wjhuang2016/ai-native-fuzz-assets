INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(1950003,
 'TiDB Lightning can silently skip all data after the target table is recreated',
 'high',
 'data_loss',
 'tidb-lightning import',
 'classic Lightning checkpoint resume',
 'With a completed checkpoint retained, dropping and recreating the target table under the same name lets a later Lightning run exit successfully while importing none of the current input into the new table generation.',
 'Configure classic tidb-lightning with a file checkpoint and keep-after-success="origin". Import a nonempty CSV into db.t and record the table ID. Drop and recreate db.t with the same schema, confirming that its table ID changed and it is empty. Run the same Lightning command again with the retained checkpoint, then inspect the process exit status and the current table rowset.',
 'Lightning must reject a checkpoint whose target-table generation does not match the current table, or reset that table checkpoint and import the current input from Loaded state.',
 'On current master the first import produced table ID 5412 with rows 1 and 2. After recreation, table ID 5415 was empty. The second Lightning run exited 0 in under one second, but table ID 5415 remained empty; ADMIN CHECK TABLE was green because the empty table was internally consistent.',
 'Classic Lightning checkpoint tables are keyed by table name but are not bound to the target table generation. FileCheckpointDB.Initialize explicitly leaves hash validation as TODO and preserves old TableID/status/engines/chunks. The MySQL checkpoint path writes the constant CheckpointStatusLoaded value into its hash column. In the TiDB backend the persisted TableID is also 0, so a completed status and imported engines survive a real table recreation and dominate all current work and postprocess checks.',
 'Persist and validate a target-generation fingerprint before consuming table status, engines, or chunks. Refresh target metadata after schema creation and bind the checkpoint to an actual remote table identity plus relevant schema/input lineage. Reject or reset on mismatch; do not use table name, constant hash, or zero TableID as proof.',
 'lightning-success-current-generation-rowset',
 'source-lineage-local-matrix-live-consumer',
 'lightning-checkpoint-unbound-target-table-generation',
 'master 13282a8bd06b',
 1,
 'confirmed',
 NULL,
 'No PR/review finding, issue, or fix history generated this candidate. Local RED: same-generation resume and fresh-current controls passed, while cross-generation initialization returned old TableID=101, Status=Analyzed, Engines=2 instead of current TableID=202, Loaded, 0 engines. Testbed 8220955 live RED used current commit 13282a8bd06b and the supported keep-after-success=origin option: first import table 5412 had two rows; recreated table 5415 stayed empty after a second exit-0 Lightning run. Post-RED GitHub issue searches returned no exact root.');
