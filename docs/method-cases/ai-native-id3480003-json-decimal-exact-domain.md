# id3480003: exact-domain closure for remote intermediate representations

## Result

S76 found a second independent persistent-data bug without broad random fuzzing. TiKV collapses
JSON `I64`, `U64`, `Double`, literals, arrays, and objects into one `f64` conversion branch before
building a decimal. TiDB keeps exact integer branches. The first lost integer boundary generated a
three-value matrix and a default-config wrong `DELETE`.

## Starting proof obligation

For a pushed expression, every intermediate representation must preserve the distinctions that
the SQL source and target types promise:

```text
JSON I64/U64 exact domain
  -> intermediate representation
  -> DECIMAL exact domain
```

If the intermediate domain is narrower, remote and local row membership can diverge even when both
sides expose the same SQL function signature.

## Why the selector worked

The source diff exposed the relevant partition directly:

```text
TiDB: literal | I64 | U64 | Double | String | non-scalar
TiKV: String | everything else through f64
```

This yielded one deterministic boundary: the largest consecutive integer representable by
`f64`, `2^53`. The useful matrix was:

```text
2^53-1  exact control
2^53    exact control
2^53+1  RED
2^53+2  exact parity control
2^53+3  RED
```

No random values, concurrency, failpoint, or large workload were needed.

## Strong oracle

1. `EXPLAIN` proves the candidate `Selection` runs in `cop[tikv]`.
2. A zero-delay volatile wrapper keeps the same predicate in TiDB.
3. Ordered primary-key sets are compared.
4. Root projection confirms JSON and DECIMAL values are equal.
5. Matched table copies lift the row-set mismatch through ordinary `DELETE`.
6. Fresh reads compare every surviving primary key.
7. A current-master unit probe and exact-owner counterfactual establish the implementation owner.

The highest oracle is not `ADMIN CHECK TABLE`: wrong rows can be deleted while all remaining KV
structures stay valid.

An implicit `JSON <> DECIMAL` comparison is GREEN because the planner converts DECIMAL to JSON.
This control narrows production reachability to explicit JSON-to-DECIMAL casts and prevents impact
from being overstated.

## S76 refinement

Before generating values for two evaluator implementations:

1. Compare their semantic branch partitions.
2. Identify any many-to-one intermediate conversion.
3. Prove the intermediate is injective over the admitted source domain.
4. If it is not, derive values at the exact-domain cliff.
5. Run the smallest pushed/root matrix and lift only a real mismatch into DML.

For numeric conversions, useful cliffs include:

```text
2^24 for float32 integer exactness
2^53 for float64 integer exactness
signed and unsigned integer endpoints
decimal precision and scale limits
rounding half ties and saturation boundaries
```

## Pre-matrix closure gate

This run first rediscovered id3120003 because the known-root lookup happened after execution.
Future S73/S74/S76 rounds should load:

```text
validated root_cause_id and title tokens
known duplicate packs
negative persistent-lift assets
```

before matrix admission. A matching cell remains useful as an oracle calibration, but it cannot
enter reproduction or bug-counting work.

## Stop rule

This root closes all JSON integer-to-DECIMAL values that pass through the same TiKV conversion
branch. Do not enumerate more large integers, JSON paths, or DML verbs. Reopen only for another
target type, another intermediate representation, or a distinct persistent consumer with a
different root cause.
