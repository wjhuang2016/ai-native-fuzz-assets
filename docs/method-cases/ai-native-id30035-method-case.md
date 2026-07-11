# Method Case: id30035 division constant after `div_precision_increment`

## What We Were Testing

id30035 is the second concrete proof of the improved S7 rule:

```text
cached payload must be a pure function of explicit SQL inputs,
cache-key dimensions, and hidden session/config inputs.
```

The proof card:

```text
P_check:  prepared plan-cache key says the plan can be reused
Q_claim:  folded constant payload remains valid after session variable changes
F_effect: cache hit reuses a folded Constant for decimal division
D_dim:    decimal division reads @@div_precision_increment
```

## Small Matrix

```text
direct dpi=4: 1/7 -> 0.1429
direct dpi=8: 1/7 -> 0.14285714

prepare under dpi=4, SELECT 1/7 -> 0.1429, cache=0
switch dpi=8, same EXECUTE -> 0.1429, cache=1 RED
direct under dpi=8 -> 0.14285714
flush session plan cache, same EXECUTE -> 0.14285714, cache=0 GREEN reference

WHERE form:
dpi=4, CAST(1/7 AS CHAR)='0.142857142' -> 0 rows
dpi=8 cache hit -> 0 rows RED
dpi=8 direct -> 2 rows
dpi=8 after flush -> 2 rows

non-folded control:
SELECT a/b FROM t follows dpi=8 even with cache=1 GREEN
```

## Why This Was Fast

The source proof was all in one narrow path:

- `builtinArithmeticDivideDecimalSig` uses `GetDivPrecisionIncrement()` at build and execution.
- `plan_cache_utils` does not hash `div_precision_increment`.
- constant folding turns all-constant division into a plain `Constant`.
- the existing mutable-constant guard only sees parameter/deferred constants.

That reduced the live matrix to direct contract, cache-hit red, flush reference, and non-folded
control.

## Selector Improvement

After id30034, a single hit could still have been a date-function quirk. id30035 shows the reusable
selector is broader:

```text
hidden EvalContext input
+ all explicit args constant
+ folded payload reused by cache
= likely stale semantic value
```

The next improvement should be systematic:

1. List `EvalContext` and `BuildContext` getters.
2. Mark which ones are already in the plan-cache key or deliberately deferred.
3. For the rest, find scalar functions where all visible SQL args can be constants.
4. Build a two-row direct contract plus cache-hit/flush matrix.

This keeps the search AI-native: source first, tiny matrix second, selector update third.
