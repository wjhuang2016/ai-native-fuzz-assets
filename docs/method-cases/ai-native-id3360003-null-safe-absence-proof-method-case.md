# id3360003: make every absence proof NULL-safe

## Starting proof obligation

```text
P: a validator query returns zero violating rows.
Q: the object is safe to publish.
F: SQL UNKNOWN can suppress a real violation.
```

The useful source signal was an authorization boundary: a generated SQL query decides whether DDL
may publish a new integrity constraint. That makes predicate completeness a proof obligation.

## Small matrix

| Parent keys | Child keys | `NOT IN` violations | `NOT EXISTS` violations | DDL |
| --- | --- | ---: | ---: | --- |
| `1` | `1,2` | 1 | 1 | rejects |
| `1,NULL` | `1,2` | 0 | 1 | publishes incorrectly |
| `1,NULL` | `1` | 0 | 0 | accepts correctly |

Only one dimension matters: whether the anti-join input can contain `NULL`. Table size, scheduling,
concurrency, and FK state timing are unnecessary.

## Strong oracle

1. Run the implementation-shaped `NOT IN` query.
2. Run a correlated `NOT EXISTS` over the same snapshot.
3. Observe the DDL terminal result.
4. Verify public constraint metadata.
5. Count historical orphans with a fresh left anti-join.
6. Prove future enforcement rejects a new orphan.
7. Remove only the referenced `NULL` for the GREEN.

The combination distinguishes a validator false negative from disabled checks, publication delay,
or a general foreign-key enforcement failure.

## Selector improvement

Add `ABSENCE_PROOF_MUST_BE_NULL_SAFE`:

1. Find validators, cleaners, restore checks, and admission paths whose empty result authorizes a
   public or destructive action.
2. Normalize the predicate into two-valued and three-valued inputs.
3. Identify every nullable producer in anti-joins, comparisons, boolean aggregates, and
   generated predicates.
4. Build the smallest matrix: match, absence, and `NULL` contamination.
5. Compare the implementation predicate with a NULL-safe oracle.
6. Lift a RED into the highest persistent consumer: publication, deletion, restore success,
   cleanup, or uniqueness admission.

This generalizes beyond foreign keys. Useful code shapes include `NOT IN`, `<> ALL`, negated
nullable comparisons, `COUNT(expr)=0`, and boolean expressions where `UNKNOWN` is treated as
success.

## Dedup discipline

Before execution, query only internal root fingerprints and lifecycle status. That prevents
compressed context from repeating an existing asset without exposing issue-derived solutions.
Search upstream issues and history only after RED so target selection remains independent.

## Stop rule

Do not enumerate single-column versus composite foreign keys as separate bugs when they share the
same validator and NULL-poisoning root. Move the selector to another authorization or destructive
consumer. Reopen this root only if a different code owner has an independent nullable absence
proof.
