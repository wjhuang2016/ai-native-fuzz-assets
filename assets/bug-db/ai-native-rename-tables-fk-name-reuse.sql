INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2820003,
 'Multi-table RENAME can silently bind a foreign key to the wrong parent',
 'high',
 'data-integrity',
 'RENAME TABLE',
 'multi-table rename / foreign key',
 'One successful multi-table RENAME permanently redirects an existing foreign key to another physical table. Existing valid child rows become semantic orphans, the wrong parent authorizes new child rows, and the correct parent can be deleted while ADMIN CHECK TABLE remains green.',
 'Enable foreign keys; create parent p1, unrelated p2/p3, and child c1 referencing p1; insert p1(1), p3(3), c1(1,1); execute RENAME TABLE p1 TO tmp, p2 TO p1, tmp TO p2, p3 TO tmp. Query REFERENTIAL_CONSTRAINTS, insert c1(3,3), delete p2(1), and compare child rows against p2 and tmp.',
 'The foreign key follows the original p1 table object to its final name p2. Existing c1(1,1) remains valid, c1(3,3) is rejected, and deleting p2(1) is blocked.',
 'The foreign key references tmp, which is the original p3 object at statement completion. c1(3,3) succeeds, deleting p2(1) succeeds, c1(1,1) is orphaned, and ADMIN CHECK TABLE succeeds.',
 'onRenameTables iterates over RenameTableInfos while adjustForeignKeyChildTableInfoAfterRenameTable queries the pre-statement InfoSchema referred-FK map for each old name. The first rename mutates a loaded child FK to an intermediate name. A later rename of that intermediate name sees no entry in the frozen map and returns early, so the edge does not follow the table object. Reusing the name for another table turns the stale edge into a valid reference to the wrong object.',
 'Represent the full rename permutation by table ID or maintain an evolving referred-FK graph across the batch. Persist all affected loaded child tables after the batch and add a reused-intermediate-name regression matrix.',
 'FK_OBJECT_IDENTITY_AND_BIDIRECTIONAL_DML',
 'COLLECTION_SNAPSHOT_MUTATION_GAP',
 'rename-tables-fk-frozen-reference-graph',
 'TiDB master 231dad5225f0; one TiDB plus one real TiKV; foreign keys and metadata locking enabled',
 1,
 'confirmed',
 NULL,
 'Discovered from current source and proof-obligation testing without PR-review findings. Local current-master RED, real-TiKV RED, normal single-rename GREEN, and a narrow evolving-loaded-edge counterfactual GREEN are complete. No concurrency, failpoint, retry, or node failure is required. The trigger is a generated or hand-written multi-table name rotation that reuses an intermediate name. Post-RED GitHub issue and PR searches found no exact root.');

