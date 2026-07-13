# id2400003: replay-persistent evaluator state changes retry semantics

Remote bug DB: `found_bug id2400003`, confirmed high consequence / low frequency.
Upstream: [TiDB #69823](https://github.com/pingcap/tidb/issues/69823).

## Starting proof obligation

The #69822 selector looked for a failed attempt writing an external owner that the retry reads. This
case generalizes the write-read edge to state owned inside a prepared expression:

```text
statement entry creates mutable evaluator owner M
failed attempt mutates M
retry rebuild aliases or reuses M
successful attempt consumes the new M state
output reaches a key, row image, or public terminal result
```

Transparent replay is safe only if `M` is restored to statement-entry state, journaled, proven
idempotent, or replay is declined.

## How the candidate was generated

1. Start from the current-source pessimistic retry owner, without issue or PR seeds.
2. Enumerate expressions whose evaluation mutates retained state.
3. Inspect `Clone` ownership, especially pointer, map, slice, and receiver aliases.
4. Intersect the mutable owner with executors rebuilt after a failed attempt.
5. Rank only outputs that reach a C3 consumer.
6. Prefer deterministic state owners whose first and second outputs can be mapped to opposing
   terminal outcomes.

`builtinRandSig` survived every gate: constant-seed construction owns a mutable `MysqlRng`, `Gen()`
advances it, `Clone` aliases it, and the result can choose a unique key.

## P/Q/F

- **P**: `StmtRollback`, retry-context reset, and executor rebuild establish a fresh statement
  attempt.
- **Q**: rebuilding also restores stateful evaluator owners to their statement-entry position.
- **F**: the failed attempt advances the shared RNG. The retry consumes the next deterministic
  value, so hidden replay changes the row and terminal result.

## Compressed matrix

| Cell | Retry count | Terminal result | Final rows |
| --- | ---: | --- | --- |
| Natural conflict + hidden retry | 1 | success | `(1,2),(2,1)` |
| Same final DB state, direct execution | 0 | duplicate key | `(1,10),(2,1)` |
| Counterfactual: decline retry after RAND consumption | 0 | original conflict | `(1,10),(2,1)` |

A numeric sibling confirms the exact sequence positions: direct execution yields `665703432`, while
the hidden retry commits `912825259`. The unique-key cell is the stronger oracle because it turns
the same drift into failure versus success.

## Failed counterfactual as evidence

Deep-copying `mysqlRng` in `Clone` did not change the RED result. The clone copied an owner that the
failed attempt had already advanced. This rejects a shallow diagnosis of "pointer alias only" and
adds a temporal proof obligation:

```text
snapshot altitude must precede the first mutation it intends to undo
```

State isolation performed only at retry construction cannot restore statement-entry semantics.

## Selector improvement

New selector: `MUTABLE_EVALUATOR_STATE_SURVIVES_RETRY`.

```text
mutable state owned by prepared evaluators
intersect
state reused or aliased by retry rebuild
intersect
outputs reaching key, predicate, row image, action, or terminal truth
minus
entry snapshot, restore, journal, idempotency, or retry-decline guards
```

The practical source generator is a typed alias graph over `Clone` implementations plus a mutating
method check. A pointer copy is only a candidate; admission still requires mutation before the retry
edge and a correctness-bearing consumer after it.

## Quality and stop rule

The root silently converts a duplicate-key failure into committed success, so it is C3/high. Its
natural frequency is low. Treat one constant-seed RNG owner as one root. Seeds, thresholds, random
functions, SQL forms, sleep durations, and conflict schedules are blast radius. Reopen only for a
different mutable evaluator owner or a different retry boundary.

