INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2490003,
 'Pessimistic ON UPDATE CASCADE can commit a concurrent child orphan',
 'high',
 'data_integrity',
 'UPDATE',
 'pessimistic transaction foreign key ON UPDATE CASCADE',
 'Two pessimistic transactions can both commit successfully while a concurrent child row remains on a parent key removed by ON UPDATE CASCADE. The resulting foreign-key orphan is durable and ADMIN CHECK TABLE reports success.',
 'At READ COMMITTED with MDL ON, update a parent primary key through a join so ON UPDATE CASCADE runs. Pause after the main DML and cascade execute but before the outer final KeysNeedToLock. In a second pessimistic transaction, insert a child referencing the old parent key and commit. Resume and commit the parent update, then run a fresh-session parent-child anti-join.',
 'The child insert either serializes before the cascade and is moved to the new parent key, or waits for the parent update and fails with error 1452. Both commits must not leave an orphan.',
 'Both transactions committed. Parent 1 became 2 and the existing child moved to 2, but the concurrent child committed with pid=1. The anti-join returned (200,1), while ADMIN CHECK TABLE succeeded for both tables.',
 'handleStmtForeignKeyTrigger calls StmtCommit to expose the main DML to nested cascades. StmtCommit releases the current mem-buffer stage and cleanup creates a fresh stage. The later outer KeysNeedToLock inspects only the current stage, so the released old-parent mutation is absent from the final pessimistic lock set. A concurrent child can validate against the still-committed old parent and commit before the owner publishes the parent-key change.',
 'Preserve or acquire the pessimistic lock ownership of mutations released by FK intermediate publication, and make the final lock set the union of all statement stages. A proof counterfactual that locks current KeysNeedToLock before the first FK StmtCommit closes the exact schedule, but is not asserted as the production fix.',
 'BOTH_COMMIT_SUCCESS_PLUS_FK_ANTI_JOIN',
 'INTERMEDIATE_PUBLICATION_LOCK_CLOSURE',
 'fk-cascade-stmtcommit-drops-final-parent-lock',
 'current master b8d04e17a2ca, pessimistic READ COMMITTED, MDL ON',
 1,
 'issue-filed',
 'https://github.com/pingcap/tidb/issues/69828',
 'Discovered from current-source ownership analysis and a generated concurrency schedule, without PR review or issue seeds. Exact deterministic UT RED and pre-release lock counterfactual GREEN are recorded. Post-RED GitHub searches found no exact root. Upstream issue is labeled severity/critical and found-by-ai.');
