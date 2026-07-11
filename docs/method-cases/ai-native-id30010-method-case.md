# id30010 Method Case: InfoSchema LIKE Collation Drift

## Method Lesson

This bug came from switching modules without going back to random fuzzing.

The useful proof obligation was:

```text
If a virtual/system table pushes a SQL predicate into a custom extractor,
the extractor must be a semantics-preserving prefilter under the SQL-visible collation.
```

The matrix was tiny:

| Module | Extracted column | Predicate | Strong oracle | Result |
|---|---|---|---|---|
| information_schema.tables | table_name | `LIKE 'a_%'` | normal WHERE vs explicit predicate re-check / CASE-wrapped WHERE | red |
| information_schema.columns | table_name | `LIKE 'a_%'` | normal WHERE vs CASE-wrapped WHERE | red |
| information_schema.schemata | schema_name | RLIKE / LIKE combos | normal WHERE vs CASE-wrapped WHERE | green in sampled cases |

## Why This Worked

The selector was not "try all optimizer bugs". It was:

1. Pick a module with a custom shortcut path.
2. Identify the proof obligation the shortcut must preserve.
3. Build an equivalent query form that blocks or bypasses the shortcut.
4. Compare row sets.

For InfoSchema, `CASE WHEN (P) THEN 1 ELSE 0 END = 1` is a strong oracle because SQL WHERE keeps exactly the rows where `P` is true, but the custom predicate extractor is much less likely to consume `P` from inside CASE.

## Selector

High-value pattern:

```text
virtual/system table + predicate extractor + string matching + SQL-visible collation
```

The bug was found quickly because the implementation had two suspicious signs:

- InfoSchema extractor lowercases extracted strings globally.
- LIKE/regexp extraction removes the original predicate after building a prefilter.

That combination is only safe if the prefilter is at least as strict as the original predicate. With `utf8mb4_bin`, it is not.

## Quality

Quality is medium-high:

- User-visible wrong result: `information_schema.tables` and `information_schema.columns` return rows that do not satisfy `WHERE table_name LIKE 'a_%'`.
- No failpoint or timing dependency.
- Minimal repro is small and deterministic.
- Root cause is source-anchored.

Severity is likely medium: this affects metadata queries, tooling, migration/check scripts, and any client relying on case-sensitive metadata filters. It is not data corruption.

## Improvement To Methodology

For non-DDL modules, the best current pattern is:

```text
custom shortcut path -> semantic equivalence oracle -> small adversarial name/value set
```

Compared with the DDL matrix, the "object reference" is replaced by "shortcut must preserve SQL semantics". The same discipline still applies: find the proof obligation first, compress it into a few rows, then use a strong oracle.

