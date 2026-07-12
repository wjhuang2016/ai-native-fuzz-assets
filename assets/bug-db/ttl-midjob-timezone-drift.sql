INSERT INTO found_bug (
  title, severity, category, ddl_op, feature,
  symptom, repro, expected, actual, root_cause, fix_hint,
  oracle, method, root_cause_id, affects, confirmed, status, notes
) VALUES (
  'TTL can silently delete a refreshed DATETIME row when global time_zone changes during a job',
  'high',
  'correctness/data-loss',
  'TTL background delete',
  'DATETIME TTL scan/delete pipeline',
  'A row refreshed beyond the cutoff used by TTL scan is silently deleted after global time_zone changes before the delete phase; the TTL job reports successful completion.',
  'On a non-partition DATETIME TTL table, pause an actual TTL worker after scan selected an expired row and before delete ExecuteSQLWithCheck. Read current_job_ttl_expire under UTC, update the row to cutoff plus 4 hours, verify the original predicate is false, set GLOBAL time_zone=+08:00, then release delete. The row disappears. Repeat without changing time_zone and the row remains.',
  'The delete-time safety predicate must preserve a row that is no longer expired under the scan cutoff, or the job must abort/restart when the time-zone interpretation context changes.',
  'With expire epoch 1783880612, the refreshed row 2026-07-12 22:23:32 is not expired under UTC cutoff 18:23:32. After switching to +08, FROM_UNIXTIME renders 2026-07-13 02:23:32; the actual worker deletes the row and completes. No-drift control preserves it.',
  'SQLBuilder carries only expire.Unix() and both scan and delete render FROM_UNIXTIME(epoch). Each TTL statement independently resets to the current global time_zone, while validateTTLWork does not pin or compare time zone. The same epoch therefore has different DATETIME meanings across one job.',
  'Pin the time-zone interpretation context for the whole job, or persist a DATE/DATETIME wall-clock cutoff plus its time-zone identity and abort/restart on drift. Add a synchronized scan-to-delete regression test with a refreshed row.',
  'O_TTL_REFRESHED_ROW_SURVIVAL',
  'SCAN_DELETE_CONTEXT_STABILITY',
  'ttl-midjob-timezone-context-drift',
  'current master 13282a8bd06b; non-partition DATETIME TTL; global time_zone changes between scan and delete',
  1,
  'confirmed',
  'Authorized testbed 8220955. Discovered from current-source proof obligations. Post-hit dedup found #41043/#41044, whose pre-job time-zone-change scenario is GREEN on current source; this is the distinct unguarded mid-job scan/delete context transition.'
);

SELECT LAST_INSERT_ID() AS inserted_bug_id;
