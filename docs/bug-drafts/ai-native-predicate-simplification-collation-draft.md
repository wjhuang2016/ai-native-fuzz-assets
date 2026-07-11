# Predicate Simplification Collation Wrong-Result Draft
> 2026-06-30. New proof-obligation hit after the plan-cache and partition-pruning negative passes.

## Summary

TiDB can simplify:

```sql
s IN ('a','A') AND s != _utf8mb4'A' COLLATE utf8mb4_bin
```

into a predicate equivalent to:

```sql
s IN ('a')
```

When `s` has a case-insensitive collation such as `utf8mb4_general_ci`, `s IN ('a')` still matches both `a` and `A`. The original predicate should keep only `a`, because the `!= ... COLLATE utf8mb4_bin` comparison filters out `A`.

This is a wrong-result bug in predicate simplification / expression simplification, not storage corruption and not an index-only issue.

## Minimal Repro

```sql
DROP DATABASE IF EXISTS ai_native_pred_min;
CREATE DATABASE ai_native_pred_min;
USE ai_native_pred_min;

CREATE TABLE t(
  id INT PRIMARY KEY,
  s VARCHAR(8) COLLATE utf8mb4_general_ci
);

INSERT INTO t VALUES (1,'a'),(2,'A'),(3,'b');

SELECT id, s,
       s IN ('a','A') AS in_pred,
       s != _utf8mb4'A' COLLATE utf8mb4_bin AS ne_pred,
       (s IN ('a','A')) AND (s != _utf8mb4'A' COLLATE utf8mb4_bin) AS both_pred
FROM t
ORDER BY id;

SELECT id, s
FROM t
WHERE s IN ('a','A')
  AND s != _utf8mb4'A' COLLATE utf8mb4_bin
ORDER BY id;

SELECT id, s
FROM t
WHERE CASE
        WHEN (s IN ('a','A') AND s != _utf8mb4'A' COLLATE utf8mb4_bin)
        THEN 1 ELSE 0
      END = 1
ORDER BY id;
```

Observed on the failpoint TiDB testbed:

```text
projection evaluation:
id=1 s=a in_pred=1 ne_pred=1 both_pred=1
id=2 s=A in_pred=1 ne_pred=0 both_pred=0
id=3 s=b in_pred=0 ne_pred=1 both_pred=0

plain WHERE:
1 a
2 A

CASE oracle:
1 a
```

## Plan Evidence

On a table without a secondary index, `EXPLAIN FORMAT='brief'` showed the pushed selection had already lost the `ne` predicate:

```text
Selection cop[tikv] in(t.s, "a")
```

The same wrong result reproduced with and without a secondary index, so the issue is not an index-range-only artifact.

## Control Evidence

These controls returned the expected single row:

```sql
SELECT id, s
FROM t
WHERE s IN ('a')
  AND s != _utf8mb4'A' COLLATE utf8mb4_bin
ORDER BY id;

SELECT id, s
FROM t
WHERE s COLLATE utf8mb4_bin IN ('a','A')
  AND s != _utf8mb4'A' COLLATE utf8mb4_bin
ORDER BY id;
```

This supports the root model: the unsafe step is removing the `!=` predicate after shrinking the `IN` list, because the remaining `IN ('a')` is still evaluated under the column's case-insensitive collation.

## Root Model

Likely source anchor:

- `/Users/bba/pc/tidb/pkg/planner/core/rule/rule_predicate_simplification.go:151`: `updateInPredicate`
- `/Users/bba/pc/tidb/pkg/planner/core/rule/rule_predicate_simplification.go:168`: uses `value.Equal(evalCtx, notEQValue)` to decide which `IN` item is redundant.
- `/Users/bba/pc/tidb/pkg/planner/core/rule/rule_predicate_simplification.go:250`: `mergeInAndNotEQLists` removes the `!=` predicate when the `IN` list was updated.

The sibling contradiction checker has an explicit string-collation compatibility guard around equality substitution, but the `IN`/`!=` merge path does not appear to have the same protection.

## Methodology Takeaway

This hit came from treating predicate simplification as a proof obligation:

```text
P: IN-list and != predicate look mergeable on the same column
Q: deleting the != predicate preserves SQL three-valued and collation semantics
F: planner/expression simplifier evaluates a smaller predicate
```

The effective oracle was not a random SQL crash check. It was a semantic metamorphic oracle:

```text
WHERE P
vs
WHERE CASE WHEN P THEN 1 ELSE 0 END = 1
```

Both forms should keep exactly rows where `P` is TRUE. The CASE wrapper is a compact way to make the reference path harder for the same simplifier to rewrite, while staying inside one TiDB instance and one stable user table.

## Search Feedback

Useful next generator weights:

- Upweight string/collation/coercibility cases for any proof that substitutes constants.
- Upweight "remove one predicate after shrinking another predicate" rules.
- Keep CASE-wrapped WHERE as a low-noise reference oracle for predicate simplification.
- Do not rely only on opt-rule blacklisting for this family; helper-level simplification can still be invoked from multiple planner paths.

Negative evidence from the same probe:

- 600+ small integer/NULL `scalar AND (OR branch)` and `IN`/`!=` cases matched the CASE oracle.
- LIST partition and plan-cache LIST/default cross-products produced cache hits but no row-set mismatch.

## Source Revisit After Non-DDL Calibration

The useful next step is not to enumerate more predicate strings. The source shape is now sharper:

```text
P_check:  an `IN` value and a `!=` value compare equal under one evaluation context
Q_claim:  deleting `!=` after shrinking the `IN` list preserves the original predicate
F_effect: predicate simplification publishes a smaller predicate to Selection/range planning
missing D: the remaining `IN` predicate may still be evaluated under a different collation relation
```

The important contrast is inside the same file. The OR-branch contradiction checker has explicit
string-collation compatibility checks before it substitutes equality across predicates. The
`updateInPredicate` / `mergeInAndNotEQLists` path removes the `!=` predicate after list shrinking
without the same compatibility proof.

That makes id30002 a high-quality bug for methodology purposes:

- The user-visible failure is wrong rows, not a diagnostic-only mismatch.
- The oracle is strong: plain `WHERE P` must equal `WHERE CASE WHEN P THEN 1 ELSE 0 END = 1`.
- The root model is local: predicate deletion lost a semantic dimension.
- The negative controls are meaningful: integer, NULL, and many same-collation cases stayed green.

Next search should therefore target sibling simplifier paths that delete a predicate after proving
redundancy under a narrowed comparison relation. Do not broaden to random expression fuzzing unless
the generator can name the deleted predicate, the preserved predicate, and the semantic dimension
that must survive deletion.
