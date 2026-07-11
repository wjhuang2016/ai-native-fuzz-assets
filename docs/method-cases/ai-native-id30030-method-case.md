# Method Case: id30030 cluster_log LIKE custom ESCAPE

## One-line result

S3 found another confirmed wrong-result because the code proved only "I extracted the LIKE pattern"
but the system believed "I preserved LIKE semantics"; the missing dimension was the custom
`ESCAPE` character.

## P/Q/D/F/O card

```text
P_check:
  ClusterLogTableExtractor sees `message LIKE const` and extracts only the pattern string.

Q_claim:
  The backend regexp filter is equivalent to SQL-visible `message LIKE pattern [ESCAPE x]`.

D_dim:
  LIKE pattern syntax has a semantic parameter: ESCAPE. Pattern `%#_%` with ESCAPE '#'
  matches messages containing a literal underscore, while the same pattern under default
  backslash escape means "contains # followed by any char".

F_effect:
  The extractor compiles the pattern with default backslash escape and removes the original
  scalar predicate, so rows matching the SQL predicate can be dropped before scalar evaluation.

O_oracle:
  O4 scalar-recheck differential:
  fast `message LIKE '%#_%' ESCAPE '#'`
  vs reference `message LIKE '%' AND CASE WHEN message LIKE '%#_%' ESCAPE '#' THEN 1 ELSE 0 END`.
```

## How it was found quickly

The selector was not "try more cluster_log predicates". It was:

1. Find a shortcut extractor that drops the original predicate.
2. Ask what semantic inputs are needed to make the replacement exact.
3. Compare that list with what the extractor actually records.
4. Build a tiny red/green matrix around the missing input.

For `cluster_log` message `LIKE`, the replacement uses `CompileLike2Regexp(pattern)`. The scalar
SQL operator has at least two inputs that matter: pattern and escape char. The extractor kept only
the pattern, so the red cell was forced: a pattern whose meaning flips under a custom escape.

## Matrix

```text
custom ESCAPE '#':
  fast cluster_log predicate => 0 rows
  CASE scalar reference      => 130683 rows
  classification            => RED / confirmed

default ESCAPE backslash:
  fast cluster_log predicate => 130759 rows
  CASE scalar reference      => 130759 rows
  classification            => GREEN control

ordinary scalar table:
  `%#_%` ESCAPE '#' matches `gc_service.go`
  `%#_%` without ESCAPE matches `abc#x`
  classification            => SQL contract settled
```

## Quality

Medium-quality wrong-result:

- user-visible: a normal `SELECT` over `information_schema.cluster_log` drops matching log rows;
- deterministic oracle: CASE-wrapped scalar recheck on the same SQL-visible rows;
- narrow root cause: custom `ESCAPE` is ignored only after the predicate is consumed;
- good controls: default escape remains green and ordinary table semantics are correct.

It is not as severe as storage corruption, but it is a clean proof that the S3 shortcut methodology
is still productive outside DDL.

## Methodology refinement

S3 now has a sharper question:

```text
When a fast path replaces a scalar operator, list every semantic input of that operator:
collation, timezone, precision, type domain, null behavior, pattern syntax, escape char,
session switch, backend error domain, and cache key dimensions.

If the fast path records fewer inputs than the scalar operator uses, the red cell is usually
the value where the omitted input changes the scalar truth value.
```

For future work this means we should ask for "operator semantic arity" before writing probes.
The AI should not only find a checker and a fast path; it should enumerate the hidden operands
that the checker may have forgotten.

## Stop rule

Do not enumerate every `cluster_log.message LIKE` pattern. This bug proves the custom-escape
dimension. Reopen only for:

- a different omitted LIKE semantic dimension;
- a different extractor owner with a different replacement mechanism;
- fix validation.
