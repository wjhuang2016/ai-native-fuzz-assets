# Prepared Statements Bypass `tidb_enable_noop_functions` After The Switch Is Turned Off

Status: confirmed locally on testbed `8192975`; inserted into remote `found_bug` as id30028.

## Summary

`tidb_enable_noop_functions=OFF` rejects noop-only SQL syntax such as `SQL_CALC_FOUND_ROWS` and `GROUP BY expr DESC` when the statement is executed directly. If the same statement is prepared while the switch is `ON`, later `EXECUTE` still succeeds after the switch is turned `OFF`.

This is not a prepared plan cache key issue. It still reproduces after `ADMIN FLUSH SESSION PLAN_CACHE`, and it also reproduces with prepared plan cache disabled. The failure is that the statement's AST/preprocessor validation result is frozen at `PREPARE` time and ordinary `EXECUTE` does not re-run the current-session noop-function validation unless schema version changes.

## User-Visible Symptom

A user or operator can disable noop functions and see direct SQL rejected:

```sql
SET tidb_enable_noop_functions=OFF;
SELECT SQL_CALC_FOUND_ROWS a FROM ai_noop_pc.t ORDER BY a;
-- ERROR 1235 (42000): function SQL_CALC_FOUND_ROWS has only noop implementation in tidb now,
-- use tidb_enable_noop_functions to enable these functions
```

But a statement prepared before the switch was disabled continues to execute successfully:

```sql
SET tidb_enable_noop_functions=ON;
PREPARE s FROM 'SELECT SQL_CALC_FOUND_ROWS a FROM ai_noop_pc.t ORDER BY a';

SET tidb_enable_noop_functions=OFF;
EXECUTE s;
-- returns rows instead of ERROR 1235
```

So the session switch does not reliably enforce the advertised behavior for already prepared statements.

## Minimal Reproduction

```sql
DROP DATABASE IF EXISTS ai_noop_pc;
CREATE DATABASE ai_noop_pc;
USE ai_noop_pc;
CREATE TABLE t(a INT PRIMARY KEY, b INT);
INSERT INTO t VALUES (1,10),(2,20);

SET tidb_enable_noop_functions=OFF;
SELECT SQL_CALC_FOUND_ROWS a FROM t ORDER BY a;
-- ERROR 1235

SET tidb_enable_prepared_plan_cache=ON;
SET tidb_enable_noop_functions=ON;
PREPARE s FROM 'SELECT SQL_CALC_FOUND_ROWS a FROM ai_noop_pc.t ORDER BY a';
EXECUTE s;
EXECUTE s;
SELECT @@last_plan_from_cache;
-- 1

SET tidb_enable_noop_functions=OFF;
EXECUTE s;
-- BUG: still returns rows 1,2, warning_count=0

ADMIN FLUSH SESSION PLAN_CACHE;
EXECUTE s;
SELECT @@last_plan_from_cache;
-- BUG: still returns rows 1,2, last_plan_from_cache=0
```

Sibling reproducer:

```sql
SET tidb_enable_noop_functions=OFF;
SELECT a FROM ai_noop_pc.t GROUP BY a DESC;
-- ERROR 1235

SET tidb_enable_noop_functions=ON;
PREPARE sg FROM 'SELECT a FROM ai_noop_pc.t GROUP BY a DESC';
SET tidb_enable_noop_functions=OFF;
EXECUTE sg;
-- BUG: returns rows
ADMIN FLUSH SESSION PLAN_CACHE;
EXECUTE sg;
-- BUG: still returns rows
```

## Source Anchors

- `/Users/bba/pc/tidb/pkg/planner/core/plan_cache_utils.go`: `GeneratePlanCacheStmtWithAST` runs `Preprocess(..., InPrepare, ...)` during `PREPARE`.
- `/Users/bba/pc/tidb/pkg/planner/core/preprocess.go`: `checkSelectNoopFuncs` and `checkGroupBy` read `SessionVars.NoopFuncsMode` and emit error 1235 when it is `OFF`.
- `/Users/bba/pc/tidb/pkg/planner/core/plan_cache.go`: `planCachePreprocess` only re-runs `Preprocess` when schema version changes; changing `tidb_enable_noop_functions` does not trigger that path.
- `/Users/bba/pc/tidb/pkg/planner/core/plan_cache.go`: `generateNewPlan` optimizes the already-preprocessed AST via `OptimizeAstNodeNoCache`.
- `/Users/bba/pc/tidb/pkg/executor/prepared.go`: `PrepareExec.Next` stores the `PlanCacheStmt` created at prepare time.

## Internal Control

The existing session-state test for prepared statements expects some changed session semantics to apply at `EXECUTE` time. In `/Users/bba/pc/tidb/pkg/sessionctx/sessionstates/session_states_test.go`, the test prepares `SELECT id, name FROM test.t1 GROUP BY id` under empty `sql_mode`, then changes to `ONLY_FULL_GROUP_BY`, and expects `EXECUTE stmt` to fail with `ErrFieldNotInGroupBy`.

That control does not prove every switch must be execute-time checked, but it makes this less likely to be an intentional global "prepared statements freeze all prepare-time validation" rule.

## Impact

Severity: medium.

This does not corrupt user table data. The user-facing impact is policy/semantic bypass: after disabling noop-only SQL syntax, direct statements are blocked while existing prepared statements continue to use syntax that should now be rejected. The behavior is silent because it returns rows with `@@warning_count=0`.

## Fix Direction

The fix should make execute-time behavior match the direct current-session reference. Possible directions:

- include `tidb_enable_noop_functions` in the prepared statement semantic invalidation condition and re-run the relevant preprocessor checks on switch changes;
- or re-check noop-function validation during `EXECUTE` for AST forms that depend on `NoopFuncsMode`;
- or document and enforce a clear prepare-time contract, then make direct/prepared semantics intentionally different. This seems weaker because existing controls show at least some session semantic changes are expected to affect prepared execution.

Validation should include:

- `SQL_CALC_FOUND_ROWS` prepared under `ON`, executed under `OFF` must error 1235;
- `GROUP BY expr DESC` prepared under `ON`, executed under `OFF` must error 1235;
- plan cache flush and plan-cache-off variants must behave the same;
- `WARN` mode should produce warning behavior consistently between direct and prepared execution.
