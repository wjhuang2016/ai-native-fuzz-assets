# Method Case id30018: Scalar pushdown plus normalization selector
> 2026-07-03. InfoSchema `LOWER/UPPER(TABLE_NAME)` extraction. This note records the methodology result, not the bug details.

## What Was Being Tested

The S3 selector had already found several shortcut/extractor bugs. The next question was narrower:

```text
can a shortcut extractor safely combine scalar-function pushdown
with column-level value normalization
after it removes the original predicate?
```

This is different from expanding the earlier LIKE case. The target is not another pattern variant; it is a second proof obligation inside the same extractor family.

## Why This Target Was Picked

The source had three risky facts next to each other:

- InfoSchema object-name extraction uses `valueToLower=true`;
- `extractCol` can push down `LOWER(col)=const` or `UPPER(col)=const`;
- the extractor can remove the original predicate from `remained`.

That creates a precise P/Q/F shape:

```text
P_check:
  LOWER/UPPER(object_name) = const is extractable as an object-name prefilter

Q_claim:
  the prefilter is equivalent to the SQL-visible scalar predicate

F_effect:
  the original scalar predicate is gone, so returned rows are not rechecked
```

The adversarial set is tiny: one mixed-case table name and one deliberately wrong-case constant.

## Tiny Matrix

Red cells:

1. `LOWER(table_name) = 'ACASE'` returns `Acase`, but projected `LOWER(table_name) = 'ACASE'` is `0`.
2. `UPPER(table_name) = 'acase'` returns `Acase`, but projected `UPPER(table_name) = 'acase'` is `0`.

Reference:

1. CASE-wrapped predicates return no rows for both red cells.

Green controls:

1. `LOWER(table_name) = 'acase'` returns `Acase` with self predicate `1`.
2. `UPPER(table_name) = 'ACASE'` returns `Acase` with self predicate `1`.

## Why It Worked

The key was to make the row itself judge the shortcut:

```sql
SELECT table_name, LOWER(table_name) AS lowered,
       LOWER(table_name) = 'ACASE' AS self_ok
FROM information_schema.tables
WHERE table_schema='ai_s3_funcpush'
  AND LOWER(table_name) = 'ACASE';
```

The fast path returned a row whose `self_ok` value was `0`. That is a strong oracle because it does not depend on plan text, implementation guesses, or a reference database. It asks whether the row satisfies the predicate TiDB just used to return it.

## Quality

Medium. It is a deterministic wrong-result bug on a SQL-visible system table:

- the returned row fails the query predicate;
- the CASE reference returns the expected empty result;
- the symmetric `LOWER` and `UPPER` red cells point to the same source mechanism;
- lower-case and upper-case matching constants stay green, so the oracle is not just rejecting scalar pushdown wholesale.

The blast radius should still be guarded. This is the InfoSchema extractor family, but it is a new mechanism: scalar pushdown plus value normalization, not LIKE collation.

## Methodology Improvement

S3 should now split shortcut risk into three sub-claims:

```text
prefilter semantics
value/key normalization semantics
scalar-function pushdown semantics
```

Any target that composes two of them and drops the original predicate gets a high score, but every red cell must include:

- trigger evidence that extraction happened;
- a projected self-predicate on returned rows;
- a CASE-wrapped or no-shortcut reference;
- a green control where the transformed predicate is genuinely true.

This turns "the plan looks suspicious" into "the returned row disproves the proof obligation."
