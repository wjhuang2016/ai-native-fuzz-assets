# id3120003: pushdown must preserve row-set semantics

Status: confirmed high severity with persistent wrong DML.

## Proof obligation

```text
P: TiDB and TiKV both implement one pushed scalar-function signature.
Q: moving the expression into TiKV preserves exact row membership for every legal input.
F: the two implementations differ at one boundary; DML consumes TiKV's false row set.
```

The bug is not established by a different displayed value alone. It becomes high severity when the
wrong evaluator owns `UPDATE` or `DELETE` admission.

## Source-directed matrix

The candidate did not come from random SQL generation. TiKV already contained a narrow clue that
duration-to-int error handling was not identical to TiDB, and its cast tests covered positive and
negative values without the zero-crossing half tie.

The matrix compressed that clue to:

| Input | TiKV pushed predicate | TiDB root predicate | Verdict |
| --- | --- | --- | --- |
| `-00:00:00.499999` | false | false | GREEN |
| `-00:00:00.500000` | true | false | RED |
| `-00:00:00.500001` | true | true | GREEN |
| `00:00:00.500000` | false | false | GREEN |

The exact `.5` boundary distinguishes a tie-rule mismatch from general parsing or sign handling.

## Strong oracle

Join four views of the same expression:

1. `EXPLAIN` proves the predicate is evaluated in `cop[tikv]`;
2. pushed and root-forced queries compare exact primary-key sets;
3. the returned row projects `predicate_holds=0`, creating a self-checking contradiction;
4. pushed and root-forced `UPDATE` compare durable preimages and affected IDs.

The strongest witness is not a warning or output-format difference:

```text
pushed UPDATE changed: 2,3
root UPDATE changed:   3
wrong durable row:     id=2, predicate_holds=0
```

## Selector

Use `PUSHDOWN_ROWSET_SEMANTIC_CLOSURE`:

```text
candidate = independently implemented pushed expression
            intersect semantic TODO, boundary conversion, collation, or context input
            intersect a plan that moves row admission to the remote evaluator
            intersect exact root/push row-set differential
            intersect UPDATE, DELETE, uniqueness, CHECK, or index consumer
            minus warning-only, formatting-only, or unpushed differences
```

## Why this worked

Source analysis supplied a proof obligation and a tiny set of high-information boundaries. The
test then treated TiDB and TiKV as mutual differential oracles. This avoided broad random fuzzing
and immediately distinguished:

- no pushdown;
- same rows with different warnings;
- query-only failure;
- persistent wrong mutation.

The row's own predicate projection is especially useful: it turns a cross-process mismatch into one
SQL-visible contradiction before any source-level diagnosis.

## Loop improvement

For every pushed scalar function:

1. find the two evaluator implementations and their context inputs;
2. derive boundaries from comments, tests, numeric domains, collations, and error paths;
3. verify that the candidate is actually pushed;
4. compare exact row IDs against a root-evaluation barrier;
5. project the predicate back onto every returned row;
6. lift only row-set mismatches through a persistent consumer;
7. bound the red input with adjacent green values;
8. confirm the current owner revision with a focused compatibility assertion;
9. deduplicate only after RED.

This selector transfers beyond casts to arithmetic, temporal functions, JSON, collations,
generated expressions, and storage-side index or constraint evaluation.

## Stop rule

One evaluator pair, one semantic boundary, and one highest persistent consumer form one root.
Different predicates that reuse the same duration tie mismatch are blast radius. Reopen for a
different scalar signature, context input, or durable consumer that bypasses a separate proof.
