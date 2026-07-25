# id3420003: pushdown semantics need operator-context closure

## Starting proof obligation

The existing pushdown oracle mainly compared predicate row sets:

```text
same expression + Selection at TiKV
must equal
same expression + Selection at TiDB root
```

An expression also participates in ordering, aggregation, and group partitioning. Correct row-set
membership does not prove those consumers preserve its type, collation, error, and equality
semantics.

The stronger obligation is:

```text
for every admitted remote expression E,
Selection(E), TopN(E), Aggregate(E), and GroupBy(E)
must match a type-preserving TiDB-root execution of the same operator
```

## Small matrix

The reusable probe expanded one expression manifest over four operator contexts:

| Context | Strong comparison |
| --- | --- |
| Selection | pushed predicate versus root predicate with a false volatile disjunct |
| TopN | pushed ordering versus derived-table root ordering |
| Aggregate | pushed partial aggregate versus derived-table root aggregate |
| GroupBy | pushed partial HashAgg versus derived-table root HashAgg |

The first pass rediscovered the known invalid-date cast root from TiDB #69292. That held-out hit
showed that the matrix can detect a serious bug it was not designed from. It was stored as
calibration, not counted as new.

The GroupBy arm then found a new root. `BINARY v` and `CAST(v AS BINARY(64))` each produced two groups
in TiKV and five at the TiDB root.

## Oracle correction

The first root wrapper used `IF` to prevent pushdown. That wrapper changed the inferred numeric type
and created a FLOAT precision mismatch. Repeated execution could reproduce the mismatch, but it was
an oracle artifact.

The corrected root barrier is a non-mergeable derived table:

```sql
SELECT ...
FROM (
  SELECT E AS _e
  FROM t
  LIMIT 18446744073709551615
) root
...
```

This keeps the original expression's `FieldType`, charset, collation, precision, and scale while
moving the consuming operator to TiDB. Every verdict also checks both plans: the candidate operator
must be in `cop[tikv]`, and the reference operator must be at `root`.

## Strong oracle

For GroupBy, compare the exact multiset of `(group key bytes, aggregate values)`. Also record:

1. pushed and root plans;
2. whether the target operator is really at the intended execution layer;
3. exact group keys, including spaces and binary zero padding;
4. a durable `INSERT ... SELECT` lift;
5. a matched source counterfactual that keeps pushdown enabled.

Counts alone are insufficient. In id3420003 both paths consumed five source rows, but one stored two
groups and the other stored five.

## Selector improvement

Add `PUSHDOWN_OPERATOR_CONTEXT_CLOSURE`:

```text
expression is admitted across the TiDB/TiKV boundary
  + expression carries a semantic dimension used by an operator
  + at least one operator can execute remotely and locally
  + a type-preserving root barrier exists
  -> enumerate operator contexts before enumerating more input values
```

Prioritize dimensions whose consumer changes state or identity:

```text
collation/equality -> GroupBy, DISTINCT, uniqueness, join keys
ordering/NULL order -> TopN, window order, merge operators
precision/error policy -> Aggregate, generated values, DML predicates
session context -> Selection, grouping, persisted derived state
```

## Why it worked

The search space was compressed twice. Source and prior assets selected semantic dimensions that can
drift across the protobuf boundary. Operator closure then selected four consumers where that drift
becomes observable. The winning input needed only five strings because binary versus
case-insensitive PAD SPACE equality directly determines the equivalence classes.

## Stop rule

After one root is confirmed, stop enumerating more string types, collations, aggregate functions,
or case variants that share the same protobuf collation overwrite. Move the operator-closure matrix
to a different semantic dimension or boundary owner.

Reject a red cell as oracle debt when the reference wrapper changes return type, collation,
precision, warnings, or row multiplicity. Repair and re-run the oracle before classifying the
product.
