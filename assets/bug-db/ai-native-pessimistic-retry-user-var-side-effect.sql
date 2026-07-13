INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(2100003,
 'Pessimistic DML retry can change a SETVAR-derived unique key and turn duplicate-key failure into success',
 'high',
 'wrong_result',
 'UPDATE',
 'pessimistic statement retry / user-variable assignment',
 'A concurrent unique-key insert can make a pessimistic UPDATE retry after its expression has changed a user variable. The retried UPDATE then computes a different key, returns success, and commits a row image that no serial execution of the submitted statement should produce.',
 'Create t(id INT PRIMARY KEY,u INT UNIQUE) with (1,10). In session A set @x=0, BEGIN, then run UPDATE t SET u=SLEEP((@x:=@x+1)+7)*0+@x WHERE id=1. While A is evaluating, session B inserts and commits (2,1). Inspect A error, @x, and the final ordered rows.',
 'The retry must preserve statement semantics. Re-evaluating from @x=0 produces u=1, so the concurrent committed u=1 must surface as duplicate-key error and row 1 must remain u=10.',
 'The UPDATE returned success, @x became 2, and the committed rows were (1,2),(2,1). The expected duplicate-key error was suppressed.',
 'SETVAR mutates session user variables during expression evaluation. A later pessimistic LockKeys write conflict is accepted for automatic statement retry. StmtRollback clears the transaction statement buffer and the executor is rebuilt, but user variables are not restored, so the next attempt consumes the first attempt side effect.',
 'Make pessimistic retry close over all attempt-scoped side effects. A production fix can track and restore only user variables touched by the failed attempt, or decline automatic retry for expressions with non-transactional side effects. A full UserVars snapshot was sufficient as a local counterfactual but is not necessarily the final implementation.',
 'POST_EVALUATION_RETRY_ERROR_AND_ROW_IMAGE_DIFFERENTIAL',
 'ATTEMPT_SCOPED_SIDE_EFFECT_ROLLBACK_CLOSURE',
 'pessimistic-retry-omits-user-var-side-effect-rollback',
 'current master 13282a8bd06b and testbed 8220955 version 5c9198e948',
 1,
 'issue-filed',
 'https://github.com/pingcap/tidb/issues/69791',
 'Discovery, ranking, and local RED/GREEN used current source only. PR reviews, issues, fixes, and history were excluded until after RED. Post-RED issue searches found no exact root. This differs from the documented deprecated optimistic whole-transaction retry boundary: it affects the default/recommended pessimistic statement retry and SETVAR is inside the retried write itself.');
