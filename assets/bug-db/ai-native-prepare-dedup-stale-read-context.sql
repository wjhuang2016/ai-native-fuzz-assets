INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2010003,
 'COM_STMT_PREPARE dedup can keep stale-read semantics after tidb_read_staleness is cleared',
 'high',
 'wrong_result',
 'COM_STMT_PREPARE / SELECT',
 'session prepare dedup cache and stale read',
 'After the session clears tidb_read_staleness and updates a row, a newly prepared statement silently returns the historical value cached by an earlier prepare.',
 'On one physical connection enable tidb_enable_cache_prepare_stmt, create t(id,v), insert (1,1), wait until that version is older than one second, set tidb_read_staleness=-1, prepare and execute SELECT v FROM t WHERE id=1 through COM_STMT_PREPARE, clear tidb_read_staleness, update v=2, then prepare and execute the identical SELECT again. Compare with the same SQL after disabling only tidb_enable_cache_prepare_stmt.',
 'Clearing tidb_read_staleness must make subsequent newly prepared reads use current data; both executions after the update should return 2.',
 'On testbed 8220955 the dedup-on prepared statement returned 1 while the identical SQL with dedup disabled returned 2. Local current-source execution produced the same RED. Replacing only the cached evaluator with the fresh Preprocess evaluator made the matrix GREEN.',
 'PrepareDedupCacheKey does not bind ReadStaleness or its derived snapshot evaluator. rebuildFromPrepareCache runs fresh Preprocess but discards ret.SnapshotTSEvaluator and copies cached.Stmt.SnapshotTSEvaluator, which captured the previous -1 second stale-read duration.',
 'Use the evaluator produced by fresh Preprocess on each dedup hit. Audit every copied derived context field and either bind all of its producers in the dedup key or rebuild it from current session state.',
 'prepare-after-staleness-clear-latest-rowset',
 'DERIVED_CONTEXT_KEY_OR_REBUILD',
 'prepare-dedup-stale-read-context-leak',
 'current master 13282a8bd06b and testbed 8220955 version 5c9198e948',
 1,
 'confirmed',
 NULL,
 'The feature is opt-in and default-off, which constrains reachability but does not weaken the silent wrong-result consequence. Discovery used only current-source proof obligations and an independently designed binary-protocol matrix. Local asset and upstream issue searches occurred only after RED and found no exact root.');
