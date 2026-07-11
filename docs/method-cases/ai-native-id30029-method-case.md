# Method Case id30029: Prepared DDL AST mutation by non-strict sql_mode

Bug candidate: `prepared CREATE TABLE freezes non-strict VARCHAR auto-conversion across later strict sql_mode`

Remote bug DB: `found_bug` id30029, candidate, low, wrong-error-candidate.

## Why this target was selected

After id30028, S8 was useful but guarded: do not enumerate more noop syntax. A valid reopen needed another preprocessor/session switch with a direct current-session reference and a cache-flush/off-cache proof.

The source scan found a sharper variant in `preprocess.go`: `hasAutoConvertWarning` reads `SQLMode.HasStrictMode()` and mutates the DDL AST from overlong `VARCHAR` to `TEXT/BLOB` in non-strict mode. That is not just a validation result being frozen; it is prepare-time AST mutation being frozen.

## Audit card

```text
Target:
  Prepared CREATE TABLE with overlong VARCHAR under non-strict sql_mode, executed under strict.

Source anchors:
  pkg/planner/core/plan_cache_utils.go
  pkg/planner/core/preprocess.go
  pkg/planner/core/plan_cache.go

T_tests:
  Existing tests cover overlong VARCHAR behavior in strict and non-strict preprocessing, but not
  PREPARE under one sql_mode followed by EXECUTE under another.

P_check:
  PREPARE runs Preprocess. `checkColumn` sees overlong VARCHAR. In non-strict mode,
  `hasAutoConvertWarning` changes the AST type to TEXT/BLOB and appends warning 1246.

Q_claim:
  The mutated prepared DDL AST remains valid for later EXECUTE.

D_dims:
  `sql_mode` strictness and AST mutation side effects.

F_effect:
  EXECUTE uses the already-mutated AST and does not re-run strict validation under the current
  session sql_mode.

O_oracle:
  Direct strict CREATE TABLE is the current-session reference.
  Prepared non-strict -> strict EXECUTE is the reuse arm.

R_redflag:
  DDL syntax whose preprocessor validation both depends on sql_mode and mutates the AST.

S_selector:
  S8 sub-shape: prepared/preprocess semantic freeze with AST mutation, not only stale validation.
```

## Minimal matrix

```text
Candidate RED:
  SET sql_mode='STRICT_TRANS_TABLES';
  CREATE TABLE t_direct_strict(c VARCHAR(70000) CHARACTER SET utf8mb4);
  -> ERROR 1074

  SET sql_mode='';
  PREPARE s FROM 'CREATE TABLE ai_s8_varchar.t_prep(c VARCHAR(70000) CHARACTER SET utf8mb4)';
  -> Warning 1246, converting VARCHAR to TEXT
  SET sql_mode='STRICT_TRANS_TABLES';
  EXECUTE s;
  SHOW CREATE TABLE t_prep;
  -> c mediumtext

Control:
  direct non-strict CREATE TABLE
  -> Warning 1246, c mediumtext

Reverse:
  PREPARE under STRICT_TRANS_TABLES
  -> ERROR 1074, no prepared statement exists

Boundary:
  ALTER TABLE ADD COLUMN same shape did not reproduce; PREPARE under non-strict failed with 1074
  on the tested build.
```

## Why this is candidate quality

The behavioral split is real: strict direct SQL rejects, but prepared non-strict -> strict execute creates a table. However, PREPARE itself emitted the auto-convert warning and mutated the AST before execution. The product may reasonably decide that prepared DDL freezes that normalization at PREPARE time.

So this should be discussed as a contract candidate, not filed as a confirmed violation yet.

## Methodology improvement

S8 should split two sub-shapes:

```text
1. stale validation result:
   PREPARE accepts under A, EXECUTE under B skips a validation that direct B would run.

2. stale AST mutation:
   PREPARE under A rewrites/mutates AST, EXECUTE under B uses the rewritten AST even though
   direct B would reject or produce a different DDL object.
```

The second is stronger source evidence but more contract-sensitive. It needs an explicit owner/product ruling before being counted as confirmed.

Stop rule: after id30028 and this candidate, do not keep enumerating S8 session switches. Reopen S8 only if a new switch has a different consequence oracle or affects DML/query correctness rather than prepared DDL normalization.
