INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(3390003,
 'TiDB Lightning can report success with a corrupted unique index when checksum and analyze are disabled',
 'high',
 'data-corruption',
 'TiDB Lightning local import',
 'conflict strategy replace / post-restore',
 'A local-backend import containing duplicate unique keys can exit successfully while record data and the unique index contain different row sets. Query results then depend on the chosen access path, and ADMIN CHECK TABLE reports physical inconsistency.',
 'Create a clustered-primary-key table with UNIQUE KEY uu(u). Import CSV rows (1,7) and (2,7) using backend=local, add-index-by-sql=false, conflict.strategy=replace, checkpoint.enable=false, post-restore.checksum=off, and post-restore.analyze=off. After Lightning exits successfully, compare IGNORE INDEX(uu), FORCE INDEX(uu), and ADMIN CHECK TABLE.',
 'The configured replace strategy must detect and resolve duplicate primary or unique keys independently of whether checksum and analyze are enabled. A successful import must leave record and index rowsets identical.',
 'Lightning exits 0 and reports the whole procedure complete. IGNORE INDEX(uu) returns 1:7,2:7, FORCE INDEX(uu) returns only 1:7, and ADMIN CHECK TABLE fails with error 8223 for handle 2. Changing only checksum from off to required detects two conflicts, resolves them, and leaves one consistent row.',
 'postProcess has an early return when checksum and analyze are both off. Local duplicate collection and ResolveDuplicateRows are embedded later inside the checksum stage, so the early return treats two optional reporting stages as if no required conflict-resolution work remains.',
 'Decouple duplicate detection and resolution from checksum and analyze. The early return may run only when the backend and conflict strategy prove no conflict work is required; alternatively reject this configuration until the stages are independent.',
 'lightning-success-base-vs-unique-index-admin-check',
 'optional-sibling-early-return-closure',
 'lightning-postprocess-off-returns-before-conflict-resolution',
 'Lightning a942e4684f; TiDB master 05b396fb66 retains the source root',
 1,
 'confirmed',
 NULL,
 'Production-shaped trigger: an operator selects conflict.strategy=replace because input may contain duplicate PK/UK values, and disables post-restore checksum and analyze to shorten import time or validate separately. No concurrency, failpoint, source modification, unusual TiDB variable, multi-TiDB topology, or infrastructure failure is needed. MDL was enabled and sql_mode was default strict. The self-contained real-TiKV scaffold reproduced RED and matched GREEN 3/3. A current-master counterfactual that bypasses the early return only when no conflict work remains resolved two conflicts under the original RED config and restored physical parity. Post-RED GitHub issue and PR searches found no exact root.');
