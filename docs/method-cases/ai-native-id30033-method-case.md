# Method Case: id30033 cluster_log REGEXP_LIKE match_type

## One-line result

S3 found a new confirmed wrong-result by applying the improved "operator semantic arity" method
to `REGEXP_LIKE`: the extractor saved only the regexp pattern, ignored the third `match_type`
argument, and dropped scalar recheck.

## P/Q/D/F/O card

```text
P_check:
  ClusterLogTableExtractor sees REGEXP_LIKE(message, const, ...) and extracts a backend regexp
  pattern.

Q_claim:
  The backend regexp filter is equivalent to SQL scalar evaluation of
  REGEXP_LIKE(message, pattern[, match_type]).

D_dim:
  On utf8mb4_bin MESSAGE, match_type='i' makes the regexp case-insensitive while match_type='c'
  keeps it case-sensitive.

F_effect:
  The extractor sends only the pattern string to the memtable request and removes the original
  scalar predicate, so the backend regexp runs without the SQL match_type semantics.

O_oracle:
  O4 scalar-recheck differential:
  fast REGEXP_LIKE(message,'GC_SERVICE.GO','i')
  vs CASE WHEN REGEXP_LIKE(message,'GC_SERVICE.GO','i') THEN 1 ELSE 0 END = 1,
  with projected self-predicate counts and green controls for 'c'/lowercase.
```

## Matrix

```text
uppercase pattern + match_type='i':
  fast cluster_log predicate => 0 rows
  CASE scalar reference      => 35742 rows, self_true=35742
  classification            => RED / confirmed

uppercase pattern + match_type='c':
  fast cluster_log predicate => 0 rows
  CASE scalar reference      => 0 rows
  classification            => GREEN control

lowercase pattern + match_type='c':
  fast cluster_log predicate => 35744 rows
  CASE scalar reference      => 35744 rows
  classification            => GREEN control
```

## Why this was fast

The source proof obligation was already shaped by id30030/id30031:

1. Find a shortcut path that replaces a scalar pattern operator.
2. List all semantic inputs of that operator.
3. Compare that list with what the extractor records.
4. Choose the omitted input that flips truth value.

For `REGEXP_LIKE`, scalar semantics include at least `expr`, `pattern`, and optional
`match_type`. The extractor's helper only records column+pattern. That immediately suggests the
red cell: use a binary-collated column, uppercase pattern, and `match_type='i'`.

## Quality

Medium-quality wrong-result:

- user-visible on `information_schema.cluster_log`;
- strong scalar reference: CASE keeps SQL evaluation in the plan;
- strong self-predicate: reference rows project `ci=1` and `cs=0`;
- green controls prove this is not generic regexp failure;
- source root cause is narrow and local.

The quality is similar to id30030. It is not data corruption, but it is a clean user-visible log
query miss with an exact source proof.

## Methodology refinement

The most efficient current form is:

```text
Before probing, enumerate the scalar operator's semantic arity.

For each input:
  if code records it, do not spend a red cell there yet;
  if code drops it and removes scalar recheck, build a minimal truth-flip matrix.
```

This is the concrete improvement over "try more predicates." The AI is good at reading helper
code and spotting that the implementation models a 3-input SQL operator as a 2-input request.

## Stop rule

Do not enumerate `REGEXP_LIKE` flags or all pattern-matchable system tables. This case proves the
omitted-`match_type` dimension. Reopen only for:

- a different omitted semantic-input family;
- a different replacement mechanism;
- fix validation.
