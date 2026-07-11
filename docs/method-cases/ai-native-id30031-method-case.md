# Method Case: id30031 information_schema LIKE custom ESCAPE

## One-line result

After id30030, the same operator-semantic-arity proof was checked against a different extractor
owner. `InfoSchemaBaseExtractor` also saved only the pattern string for `LIKE`, ignored custom
`ESCAPE`, and dropped scalar recheck.

## P/Q/D/F/O card

```text
P_check:
  InfoSchemaBaseExtractor sees TABLE_NAME LIKE const and extracts a regexp prefilter.

Q_claim:
  The regexp prefilter is equivalent to SQL scalar evaluation of
  TABLE_NAME LIKE pattern [ESCAPE x].

D_dim:
  The same pattern `%#_%` has opposite useful witnesses under different escape semantics:
  custom ESCAPE '#' matches `abc_def`, default escape matches `abc#x`.

F_effect:
  The extractor filters InfoSchema rows by the default-escape regexp and removes the scalar
  predicate, so the query can return `abc#x` even though the projected scalar predicate is false.

O_oracle:
  O4 self-predicate + CASE reference:
  fast row projects `table_name LIKE '%#_%' ESCAPE '#'` as 0;
  CASE-wrapped scalar reference returns `abc_def` with predicate 1.
```

## Matrix

```text
custom ESCAPE '#':
  fast information_schema.tables => abc#x:0
  CASE scalar reference          => abc_def:1
  classification                 => RED / confirmed

default escape:
  fast information_schema.tables => abc#x:1
  CASE scalar reference          => abc#x:1
  classification                 => GREEN control

SHOW TABLES LIKE ... ESCAPE:
  syntax rejected
  classification                 => INVALID source-screen, not a product bug
```

## Why this was fast

The source screen asked one question: "Which replacement paths reuse `CompileLike2Regexp` after
dropping the original predicate?" `InfoSchemaBaseExtractor` matched that shape:

1. `Extract()` calls `extractLikePatternCol`.
2. `extractLikePatternCol` keeps only the pattern string.
3. `CompileLike2Regexp` hardcodes the default escape.
4. `InfoSchemaBaseExtractor.filter` trusts the compiled regexp.

The red SQL is then forced by operator semantics: choose one literal that flips truth value when
the omitted `ESCAPE` input changes.

## Quality

Medium-quality wrong-result:

- user-visible on `information_schema.tables`;
- self-predicate evidence is very strong (`abc#x:0` returned by the fast arm);
- CASE reference is stable and uses the same SQL-visible rows;
- default-escape control is green;
- source root cause is narrow.

This is a blast-radius confirmation rather than a brand-new selector. Its value is proving that
"operator semantic arity" should be checked across extractor owners, then stopped once a second
owner proves the family.

## Stop rule

Do not enumerate `information_schema.columns`, `statistics`, `partitions`, or every
pattern-matchable column. Reopen only for:

- a different omitted semantic input;
- a different replacement mechanism that does not share this root;
- fix validation.
