INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(3540003,
 'SET GLOBAL tidb_gc_enable=OFF can return before an in-flight GC advances the safe point',
 'high',
 'data-loss',
 'SET GLOBAL tidb_gc_enable=OFF / GC prepare',
 'GC enable fence and transaction safe point',
 'The command to disable GC returns successfully and @@global.tidb_gc_enable is 0, but an in-flight GC prepare can still publish a newer safe point. Historical versions that were readable at command return become unavailable and are eligible for physical deletion.',
 'On one TiDB, one PD, and one real TiKV, create two committed historical versions between the stored and next GC safe points. Pause GC immediately after it reads tidb_gc_enable=ON. Execute SET GLOBAL tidb_gc_enable=OFF and verify it returns with value 0. Resume GC prepare, run the production GC job, then read the old version at its exact commit TS. Current master advances the safe point past that TS and the historical read fails. The stored real-TiKV test uses callbacks only as a deterministic scheduler and safe-point target override.',
 'Once SET GLOBAL tidb_gc_enable=OFF returns, every later GC safe-point publication must be ordered before that return. The user-visible disable and the GC prepare must serialize on one transaction and lock domain.',
 'The disable returns while prepare is paused. After release, prepare returns true, mysql.tidb records the newer safe point, the production GC job broadcasts it, the old exact snapshot becomes unreadable, and the latest version remains. The global variable still reads 0.',
 'prepare starts a transaction on session A, but checkPrepare reads and writes mysql.tidb through fresh sessions B..N. SELECT FOR UPDATE therefore locks and commits outside A. A 2020 refactor that removed GCWorker.session split the original transaction boundary. Even routing all operations through A is insufficient with plain BEGIN because the internal transaction is optimistic and does not wait on the row lock.',
 'Run prepare in BEGIN PESSIMISTIC and route the enable read, interval/lifetime reads, last-run and safe-point writes through that same session. Keep the mysql.tidb row lock until commit so SET GLOBAL serializes after the safe-point metadata update. Add a real-TiKV schedule test that proves OFF blocks while prepare owns the fence.',
 'gc-disable-linearization-history-closure',
 'transaction-identity-mode-closure',
 'gc-prepare-transaction-session-mode-split',
 'TiDB master 05b396fb66; behavior introduced by #14403 and still present on upstream master',
 1,
 'confirmed',
 NULL,
 'Critical recovery consequence with low timing probability, stored as high. The official DR migration procedure disables GC and treats @@global.tidb_gc_enable=0 as confirmation that history will no longer be deleted. Trigger needs the command to overlap a GC prepare after enable check; PD or internal SQL latency widens the window. MDL ON, default GC settings, one TiDB, one real TiKV. RED used the production 100-second GC synchronization wait and broadcast path. Matched GREEN showed OFF blocked until the pessimistic prepare committed. No exact open or closed upstream issue found.');
