# Method Case: id30034 `WEEK()` cached constant after `default_week_format`

## What We Were Testing

The proof obligation was S7 cache payload purity:

```text
P_check:  plan-cache key and clone rules say the prepared plan is reusable
Q_claim:  every cached expression payload is still semantically valid in the current session
F_effect: plan cache reuses a folded Constant instead of re-evaluating WEEK(date)
D_dim:    WEEK(date) has an implicit session input, @@default_week_format
```

The key insight was that not all session dependence appears as an explicit SQL argument. `WEEK(d, 1)`
is easy: mode is part of the expression. `WEEK(d)` is different: mode is pulled from session state.

## Small Matrix

```text
direct default_week_format=0, WEEK('2008-02-20') -> 7
direct default_week_format=1, WEEK('2008-02-20') -> 8

prepare under fmt=0, execute -> 7, cache=0
switch fmt=1, execute same prepared stmt -> 7, cache=1 RED
direct under fmt=1 -> 8
flush session plan cache, execute same prepared stmt -> 8, cache=0 GREEN reference

WHERE form:
fmt=0: SELECT COUNT(*) FROM t WHERE WEEK('2008-02-20')=8 -> 0
fmt=1 cache hit: same prepared query -> 0 RED
fmt=1 direct -> 2
fmt=1 after flush -> 2

column-value control:
SELECT WEEK(d) FROM t follows fmt=1 even with cache=1 GREEN

explicit-mode control:
WEEK('2008-02-20', 1) stays 8 across cache hit GREEN
```

## Why This Was Fast

The source made the hidden dimension obvious:

- The evaluator for single-argument `WEEK()` calls `GetDefaultWeekFormatMode()`.
- The prepared plan-cache key does not include `default_week_format`.
- Constant folding converts all-constant `WEEK('date')` into a plain `Constant`.
- The mutable-constant guard only sees `ParamMarker` and `DeferredExpr`.

That turns the test into a four-cell matrix instead of a broad date-function fuzz:

1. prove the session variable changes direct semantics;
2. prove a prepared cache hit reuses the old result;
3. prove flush rebuilds the correct result;
4. prove the non-folded/explicit-input controls are green.

## Selector Improvement

Old S7 wording was a little too focused on explicit cache keys:

```text
cache key omits variable X -> maybe stale plan
```

The better version is:

```text
cached payload must be a pure function of:
  explicit SQL inputs
  cache-key dimensions
  all implicit session/config inputs read during build, folding, rewrite, and execution-boundary setup
```

This helps avoid two mistakes:

- False positive: `foreign_key_checks` looked suspicious because DML plan construction reads it,
  but plan-cache keying and `FKChecks must-nil` cloning already preserve the safe path.
- False positive: partial-index eligibility with parameter values looked suspicious, but sampled
  parameter-sensitive cases were marked uncacheable and returned correct rowsets.

The improved selector asks one extra question before SQL:

```text
After the cache hit, is the semantic boundary rebuilt under the current context,
or is an evaluated payload reused as-is?
```

id30034 hits because the evaluated payload is reused as-is.
