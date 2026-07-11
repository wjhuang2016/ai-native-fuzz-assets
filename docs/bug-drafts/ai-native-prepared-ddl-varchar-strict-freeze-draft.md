# Prepared CREATE TABLE Freezes Non-Strict VARCHAR Auto-Conversion Across Later Strict `sql_mode`

Status: candidate on testbed `8192975`; inserted into remote `found_bug` as id30029.

## Summary

Direct `CREATE TABLE` under `STRICT_TRANS_TABLES` rejects an overlong `VARCHAR(70000) CHARACTER SET utf8mb4` with error 1074. If the same DDL is prepared under non-strict `sql_mode`, TiDB emits the non-strict auto-convert warning at `PREPARE` time, mutates the AST to `mediumtext`, and later `EXECUTE` succeeds even after switching the session to `STRICT_TRANS_TABLES`.

This is a candidate rather than confirmed because there is a product-contract question: prepared DDL may intentionally freeze some prepare-time validation. The user-visible split is still real and low-noise, and it extends S8 from "prepare-time validation result is frozen" to "prepare-time validation can mutate the stored DDL AST."

## User-Visible Symptom

Direct strict execution rejects the statement:

```sql
SET sql_mode='STRICT_TRANS_TABLES';
CREATE TABLE t_direct_strict(c VARCHAR(70000) CHARACTER SET utf8mb4);
-- ERROR 1074 (42000): Column length too big for column 'c' (max = 16383); use BLOB or TEXT instead
```

Prepared under non-strict mode, then executed under strict mode, it succeeds:

```sql
SET sql_mode='';
PREPARE s FROM 'CREATE TABLE ai_s8_varchar.t_prep(c VARCHAR(70000) CHARACTER SET utf8mb4)';
-- Warning 1246: Converting column 'c' from VARCHAR to TEXT

SET sql_mode='STRICT_TRANS_TABLES';
EXECUTE s;
SHOW CREATE TABLE t_prep;
-- `c` mediumtext DEFAULT NULL
```

At `EXECUTE` time the only warning observed was `skip prepared plan-cache: not a SELECT/UPDATE/INSERT/DELETE/SET statement`; the auto-convert warning happened at `PREPARE` time.

## Evidence Matrix

```text
RED/candidate:
  direct strict CREATE TABLE
    -> ERROR 1074
  PREPARE under sql_mode='', EXECUTE under STRICT_TRANS_TABLES
    -> succeeds, creates c as mediumtext

Control:
  direct non-strict CREATE TABLE
    -> succeeds with Warning 1246, creates c as mediumtext

Reverse control:
  PREPARE under STRICT_TRANS_TABLES
    -> fails immediately with ERROR 1074; no prepared statement exists

Boundary:
  ALTER TABLE ... ADD COLUMN same shape
    -> direct strict fails as expected; PREPARE under non-strict also failed during PREPARE on
       the tested build, so the current candidate is CREATE TABLE-specific.
```

## Source Anchors

- `/Users/bba/pc/tidb/pkg/planner/core/plan_cache_utils.go`: `GeneratePlanCacheStmtWithAST` runs `Preprocess(..., InPrepare, ...)` during `PREPARE`.
- `/Users/bba/pc/tidb/pkg/planner/core/preprocess.go`: `checkColumn` detects overlong `VARCHAR`.
- `/Users/bba/pc/tidb/pkg/planner/core/preprocess.go`: `hasAutoConvertWarning` reads `SQLMode.HasStrictMode()`, mutates `VARCHAR` to `BLOB/TEXT` in non-strict mode, and appends warning 1246.
- `/Users/bba/pc/tidb/pkg/planner/core/plan_cache.go`: execute-time preprocessing only re-runs on schema-version change.

## Impact

Severity: low unless owner rules that current execute-time `sql_mode` must govern prepared DDL. The behavior can surprise users who switch to strict mode before execution expecting direct strict semantics. It does not corrupt existing data; it creates a different column type from the direct strict reference.

## Fix Direction / Contract Question

The owner decision is the first step:

- If current `sql_mode` at `EXECUTE` is authoritative, revalidate DDL AST semantics or rebuild the prepared DDL AST when strictness changes.
- If `PREPARE`-time `sql_mode` is authoritative for DDL normalization, document that prepared DDL freezes this conversion and make the warning/observability clear enough that `EXECUTE` under strict mode is not misleading.

Validation should include direct strict, direct non-strict, prepare non-strict -> execute strict, and prepare strict -> execute non-strict controls.
