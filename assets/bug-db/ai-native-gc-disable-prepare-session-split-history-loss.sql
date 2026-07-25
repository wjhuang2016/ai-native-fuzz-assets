INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(3540003,
 'FLASHBACK DATABASE can report success after an in-flight GC deletes the recovered table ranges',
 'high',
 'data-loss',
 'SET GLOBAL tidb_gc_enable=OFF / GC prepare / FLASHBACK DATABASE',
 'GC enable fence and DDL delete-range revocation',
 'An in-flight GC prepare can advance after FLASHBACK DATABASE disables GC and accepts the old safe point. GC physically deletes the dropped tables'' record and index ranges, yet flashback reports success and publishes empty tables.',
 'On one TiDB, one PD, and one real TiKV with MDL enabled, create a database containing one 64-row table with a unique index and drop it. Start a due GC prepare and pause it after reading tidb_gc_enable=ON. Start FLASHBACK DATABASE and pause after its old safe-point check, before delete-range removal. Resume prepare and run the full production GC job, including the 100-second synchronization wait. GC loads and deletes five ranges. Resume flashback. It reports public/synced success, but a fresh session reads zero rows. Scheduler callbacks only select the overlap and target; they inject no error.',
 'GC prepare must serialize with the OFF write. Recovery must either remove its delete-range tasks before GC can load them, or observe the newer safe point and fail before publishing the database.',
 'GC deletes five eligible ranges and moves their tasks to done. FLASHBACK DATABASE still reports success and publishes the table, but all 64 rows and the unique-index entries are gone. ADMIN CHECK TABLE passes because both keyspaces are consistently empty.',
 'prepare starts a transaction on session A, but checkPrepare reads and writes mysql.tidb through fresh sessions B..N. SELECT FOR UPDATE therefore locks and commits outside A. A 2020 refactor that removed GCWorker.session split the original transaction boundary. Even routing all operations through A is insufficient with plain BEGIN because the internal transaction is optimistic and does not wait on the row lock.',
 'Run prepare in BEGIN PESSIMISTIC and route the enable read, interval/lifetime reads, last-run and safe-point writes through that same session. Keep the mysql.tidb row lock until commit so SET GLOBAL serializes after the safe-point metadata update. Add a real-TiKV schedule test that proves OFF blocks while prepare owns the fence.',
 'gc-flashback-delete-range-publication-closure',
 'transaction-identity-mode-closure + consumer-lift',
 'gc-prepare-transaction-session-mode-split',
 'TiDB master 05b396fb66; behavior introduced by #14403 and still present on upstream master',
 1,
 'confirmed',
 NULL,
 'Critical direct recovery-data-loss consequence with low timing probability, stored as high. A large FLASHBACK DATABASE naturally widens the interval between safe-point validation and delete-range revocation; it must overlap a periodic GC prepare after the enable read and remain active through the 100-second GC wait. Default GC settings, MDL ON, one TiDB, and one real TiKV are sufficient. Matched GREEN made OFF wait on a same-session pessimistic fence; flashback then returned 8055 before publication and the delete-range task remained active. Same root as the earlier history-frontier RED; no new bug ID and no exact upstream issue.');
