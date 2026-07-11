# Method Case id30028: Prepared/preprocessor semantic freeze

Bug: `prepared statements bypass tidb_enable_noop_functions after the switch is turned off`

Remote bug DB: `found_bug` id30028, confirmed, medium, wrong-error.

## Why this target was selected

After id30027, broad S3 cache/extractor enumeration was closed. The next high-value source shape was not another diagnostic-table cache. It was a prepared-statement path where `PREPARE` runs semantic validation once and `EXECUTE` reuses the prepared AST.

The smell was sharper than "cache may be stale": `tidb_enable_noop_functions` is a user-visible semantic switch read by the preprocessor, while prepared execution only re-runs the preprocessor on schema-version changes. That creates a clean proof obligation:

```text
If direct current-session SQL rejects a noop-only construct, an existing prepared statement
using the same construct should not silently bypass that current-session switch.
```

## Audit card

```text
Target:
  Prepared statements whose prepare-time preprocessor checks consume session variables.

Source anchors:
  pkg/planner/core/plan_cache_utils.go
  pkg/planner/core/preprocess.go
  pkg/planner/core/plan_cache.go
  pkg/executor/prepared.go
  pkg/sessionctx/sessionstates/session_states_test.go

T_tests:
  Existing prepared/session-state tests include a control where changing `sql_mode` after PREPARE
  makes EXECUTE fail under ONLY_FULL_GROUP_BY. They do not cover `tidb_enable_noop_functions`.

P_check:
  PREPARE runs `Preprocess(..., InPrepare, ...)`, and `checkSelectNoopFuncs` / `checkGroupBy`
  consult `SessionVars.NoopFuncsMode`.

Q_claim:
  The prepared AST and resolve context remain semantically valid for later EXECUTE calls.

D_dims:
  Current session semantic switches, specifically `tidb_enable_noop_functions`.

F_effect:
  EXECUTE optimizes the stored AST and only re-runs Preprocess when schema version changes.
  Plan cache flush rebuilds the physical plan but does not redo the prepare-time noop validation.

O_oracle:
  Direct current-session SQL under OFF is the reference.
  Prepared statement prepared under ON then executed under OFF is the fast/reuse arm.
  `ADMIN FLUSH SESSION PLAN_CACHE` and plan-cache-off variants separate AST/preprocess freeze
  from physical prepared plan cache reuse.

R_redflag:
  SQL constructs whose rejection is decided in preprocessor, not in normal expression execution:
  `SQL_CALC_FOUND_ROWS` and `GROUP BY expr ASC|DESC`.

S_selector:
  prepared/preprocess semantic freeze: PREPARE-time validation consumes a session variable,
  EXECUTE reuses the AST, and current-session direct SQL provides a stronger reference.
```

## Minimal matrix

```text
RED 1:
  Direct OFF:
    SELECT SQL_CALC_FOUND_ROWS a FROM t ORDER BY a
    -> ERROR 1235
  Prepared ON -> OFF:
    EXECUTE s
    -> rows 1,2; warning_count=0
  After ADMIN FLUSH SESSION PLAN_CACHE:
    EXECUTE s
    -> rows 1,2; last_plan_from_cache=0

RED 2:
  Direct OFF:
    SELECT a FROM t GROUP BY a DESC
    -> ERROR 1235
  Prepared ON -> OFF:
    EXECUTE sg
    -> rows
  After ADMIN FLUSH SESSION PLAN_CACHE:
    EXECUTE sg
    -> rows

Control:
  Prepared under sql_mode='', then switch to ONLY_FULL_GROUP_BY, then EXECUTE
  -> error 1055. This shows prepared execution is not universally intended to freeze
     every prepare-time semantic condition.
```

## Why this is a good bug

The symptom is simple and user-visible: direct SQL follows the disabled noop-function policy, but an existing prepared statement silently bypasses it. The oracle is strong because it compares direct current-session behavior with the same SQL through a prepared statement.

The case also teaches a new selector. Plan cache bugs were already productive under S7, but this one survives cache flush and cache disablement. The real reusable shape is earlier: prepare-time preprocessing can freeze validation outcomes that should depend on current session semantics.

## Methodology improvement

This hit adds S8:

```text
prepared/preprocess semantic freeze
```

Search rule:

```text
Find session variables read by preprocessor/validator during PREPARE.
Ask whether EXECUTE revalidates the affected AST under current session state.
Use direct current-session SQL as the reference, then add plan-cache flush/off-cache controls.
```

The key improvement is the control split:

- `@@last_plan_from_cache=1` can show a cache hit, but it is not enough.
- `ADMIN FLUSH SESSION PLAN_CACHE` proves whether the bug is physical plan reuse or prepared AST/preprocessor reuse.
- A sibling session semantic switch such as `sql_mode` helps avoid overclaiming that all prepared semantics are intentionally frozen.

Stop rule: do not enumerate every syntax guarded by `tidb_enable_noop_functions`. Reopen only for another preprocessor/session switch with an independent current-session contract and a direct-vs-prepared oracle.
