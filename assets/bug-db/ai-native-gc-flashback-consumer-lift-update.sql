UPDATE found_bug
SET
  title = 'FLASHBACK DATABASE can report success after an in-flight GC deletes the recovered table ranges',
  category = 'data-loss',
  ddl_op = 'SET GLOBAL tidb_gc_enable=OFF / GC prepare / FLASHBACK DATABASE',
  feature = 'GC enable fence and DDL delete-range revocation',
  symptom = 'An in-flight GC prepare can advance after FLASHBACK DATABASE disables GC and accepts the old safe point. GC physically deletes the dropped tables'' record and index ranges, yet flashback reports success and publishes empty tables.',
  repro = 'On one TiDB, one PD, and one real TiKV with MDL enabled, create a database containing one 64-row table with a unique index and drop it. Start a due GC prepare and pause it after reading tidb_gc_enable=ON. Start FLASHBACK DATABASE and pause after its old safe-point check, before delete-range removal. Resume prepare and run the full production GC job, including the 100-second synchronization wait. GC loads and deletes five ranges. Resume flashback. It reports public/synced success, but a fresh session reads zero rows. Scheduler callbacks only select the overlap and target; they inject no error.',
  expected = 'GC prepare must serialize with the OFF write. Recovery must either remove its delete-range tasks before GC can load them, or observe the newer safe point and fail before publishing the database.',
  actual = 'GC deletes five eligible ranges and moves their tasks to done. FLASHBACK DATABASE still reports success and publishes the table, but all 64 rows and the unique-index entries are gone. ADMIN CHECK TABLE passes because both keyspaces are consistently empty.',
  fix_hint = 'Run prepare in BEGIN PESSIMISTIC and use the outer session for the enable SELECT FOR UPDATE. Keep that row lock until safe-point preparation commits. The matched GREEN makes FLASHBACK wait, then return error 8055 before publication.',
  oracle = 'gc-flashback-delete-range-publication-closure',
  method = 'transaction-identity-mode-closure + consumer-lift',
  notes = 'Critical direct recovery-data-loss consequence with low timing probability, stored as high. A large FLASHBACK DATABASE naturally widens the interval between safe-point validation and delete-range revocation; it must overlap a periodic GC prepare after the enable read and remain active through the 100-second GC wait. Default GC settings, MDL ON, one TiDB, and one real TiKV are sufficient. Matched GREEN made OFF wait on a same-session pessimistic fence; flashback then returned 8055 before publication and the delete-range task remained active. Same root as the earlier history-frontier RED; no new bug ID and no exact upstream issue.'
WHERE id = 3540003
  AND root_cause_id = 'gc-prepare-transaction-session-mode-split';
