# id3330003: compare conversion primitives before generating SQL

## Starting proof obligation

```text
P: TiDB and TiKV both implement CAST(DOUBLE AS UNSIGNED).
Q: both implementations use the same exact-half rounding rule.
F: TiKV supplies the row handles consumed by DELETE.
```

The fastest source comparison was one primitive:

```text
TiDB: math.RoundToEven
TiKV: f64::round
```

That difference determines the test matrix before any random values are generated.

## Small matrix

| Value | TiDB | TiKV | Result |
|---:|---:|---:|---|
| `0.4` | `0` | `0` | GREEN |
| `0.5` | `0` | `1` | RED |
| `1.4` | `1` | `1` | GREEN |
| `1.5` | `2` | `2` | GREEN |
| `2.5` | `2` | `3` | RED |

The meaningful dimension is the parity of the lower integer at an exact-half tie. Sign, magnitude,
table size, and concurrency are unnecessary.

## Strong oracle

1. `EXPLAIN` proves the cast predicate is a `cop[tikv]` Selection.
2. Pushed and root-controlled queries compare exact primary-key sets.
3. The pushed row projects its TiDB predicate as false.
4. Reset copies run the same predicate through `DELETE`.
5. Fresh reads compare affected and remaining IDs.
6. A parity-matched half tie supplies the GREEN.

This oracle turns a one-bit numeric disagreement into a durable consequence without relying on
warnings or hand-written expected row sets.

## Method improvement

Extend `PUSHDOWN_ROWSET_SEMANTIC_CLOSURE` with primitive-level comparison:

1. Map the local and remote function signatures to their final conversion or rounding primitive.
2. Classify the primitive difference: tie rule, saturation, sign conversion, precision, or error
   policy.
3. Derive the smallest boundary partition directly from that class.
4. Compare exact row sets before trying DML.
5. Lift only a proven row-set mismatch into the highest ordinary persistent consumer.
6. Deduplicate by primitive pair and target type, not by SQL predicate spelling.

This is more efficient than broad numeric fuzzing: source establishes the only values that can
separate the evaluators, and the SQL matrix only confirms reachability and consequence.

## Stop rule

All positive `.5` values whose lower integer is even share this root. Do not enumerate magnitudes,
operators, or `UPDATE`/`DELETE` variants. Reopen only for a different source/target type, another
primitive pair, or another admission owner that bypasses an independent proof.
