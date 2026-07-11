# Partial Index Wrong Result Draft (found_bug id30001)

## Summary
TiDB can treat a partial index as usable even when the query predicate does not imply the partial-index predicate. When that unsafe partial index is used, `SELECT` silently misses rows outside the partial subset.

This is a wrong-result bug, not index corruption. `ADMIN CHECK TABLE` passes because the index contents match the partial-index definition; the planner should have rejected the partial-index access path.

## Minimal Reproduction
```sql
DROP DATABASE IF EXISTS ai_native_pi_bug;
CREATE DATABASE ai_native_pi_bug;
USE ai_native_pi_bug;

CREATE TABLE t(
  id INT PRIMARY KEY,
  a INT NULL,
  b INT,
  INDEX pi(b) WHERE a < 3
);

INSERT INTO t VALUES
  (1,1,1),
  (2,2,2),
  (3,3,3),
  (4,10,4),
  (5,NULL,5);

SELECT id,a,b FROM t IGNORE INDEX(pi) WHERE a >= 0 ORDER BY b;
SELECT id,a,b FROM t USE INDEX(pi)    WHERE a >= 0 ORDER BY b;
ADMIN CHECK TABLE t;
```

Expected:
```text
1  1   1
2  2   2
3  3   3
4  10  4
```

Actual with `USE INDEX(pi)`:
```text
1  1  1
2  2  2
```

## Optional No-Hint Blast Radius
The no-hint path can also choose `pi(b)` and miss rows. This was reproduced under the default session with fresh pseudo stats and no `ANALYZE TABLE`.

```sql
DROP DATABASE IF EXISTS ai_native_pi_nohint_min;
CREATE DATABASE ai_native_pi_nohint_min;
USE ai_native_pi_nohint_min;

CREATE TABLE t(
  id INT PRIMARY KEY,
  a INT NULL,
  b INT,
  s VARCHAR(20),
  INDEX pi(b) WHERE a < 3
);

INSERT INTO t VALUES
  (1,NULL,1,'null1'),(2,-1,2,'neg1'),(3,0,3,'zero'),
  (4,1,4,'one'),(5,2,5,'two'),(6,3,6,'three'),
  (7,4,7,'four'),(8,10,8,'ten'),(9,100,9,'hundred'),
  (10,NULL,10,'null2');

EXPLAIN FORMAT='brief'
SELECT CONCAT_WS(',', id, IFNULL(a,'NULL'), b) FROM t
WHERE a >= 0 ORDER BY b LIMIT 5;

SELECT CONCAT_WS(',', id, IFNULL(a,'NULL'), b) FROM t IGNORE INDEX(pi)
WHERE a >= 0 ORDER BY b LIMIT 5;

SELECT CONCAT_WS(',', id, IFNULL(a,'NULL'), b) FROM t
WHERE a >= 0 ORDER BY b LIMIT 5;

ADMIN CHECK TABLE t;
```

Plan evidence:
```text
IndexLookUp
IndexFullScan(Build) ... table:t, index:pi(b) keep order:true, stats:pseudo
Selection(Probe) ge(t.a, 0)
```

Correct `IGNORE INDEX(pi)` result:
```text
3,0,3
4,1,4
5,2,5
6,3,6
7,4,7
```

Actual no-hint result:
```text
3,0,3
4,1,4
5,2,5
```

`ANALYZE TABLE` may make the optimizer choose table scan again. That should not affect the correctness claim: using an unsafe partial-index access path is wrong regardless of why the path was selected.

## Root Cause Anchor
Likely path:
- `/Users/bba/pc/tidb/pkg/planner/core/operator/logicalop/logical_datasource.go`: `DataSource.CheckPartialIndexes`
- `/Users/bba/pc/tidb/pkg/planner/core/partidx/check_constraint.go`: `partidx.CheckConstraints`

The intended invariant:
```text
query predicate => partial index predicate
```

The observed counterexample:
```text
query:   a >= 0
partial: a < 3
```

`a >= 0` does not imply `a < 3`, so `pi` must not be used. The current range-based implication proof appears to accept some non-subset range combinations.

## Known Boundary Evidence
Confirmed bad:
- `INDEX pi(b) WHERE a < 3` with query `a >= 0`
- `INDEX pi(b) WHERE a <= 3` with query `a >= 0`
- `INDEX pi(b) WHERE a != 10` with query `a BETWEEN 3 AND 10`

Observed negative so far:
- Symmetric `a > 3` / `a >= 3` with upper-bound query filters did not reproduce in the quick checks.
- `ADMIN CHECK TABLE` passes, so storage consistency or index maintenance is not the issue.

## Discovery Methodology Review
This hit is useful as a bug, but more useful as proof that the AI-native search loop is pointing at the right layer.

The productive move was to stop treating partial index as a feature surface and treat it as a proof obligation:

```text
planner may use partial index
only if
query predicate implies partial-index predicate
```

The search was efficient because AI contributed the high-leverage parts:

| Step | AI contribution | Why it raised hit rate |
|---|---|---|
| Target choice | Notice partial index was TiKV-only and less covered than ordinary add-index paths | Higher target bug density |
| Code reading | Identify `CheckPartialIndexes` / `CheckConstraints` as a semantic gate | Search aimed at a proof checker, not random SQL |
| Invariant extraction | Translate the gate into `query predicate => partial predicate` | Clear pass/fail condition |
| Counterexample design | Generate predicates that overlap but do not imply each other | Directly attacks the likely weak point |
| Oracle design | Compare `IGNORE INDEX(pi)` with `USE/FORCE INDEX(pi)` | High sensitivity to wrong planner applicability |
| Feedback | Record bad and negative predicate shapes | Converts one hit into the next search space |

Why this works better than shallow fuzz:
- Random SQL may create many partial indexes but rarely asks whether the optimizer's proof of applicability is logically valid.
- The differential oracle is cheap and deterministic: same stable user table, same query semantics, only the access path changes.
- The bug is silent wrong-result, so crash fuzz and `ADMIN CHECK TABLE` both miss it.

Improvement space:
- Replace string-level predicate enumeration with semantic counterexample generation over interval sets, NULL three-valued logic, excluded points, OR widening, and collation boundaries.
- Preserve the target fast path while making the oracle deterministic. In this run, `ORDER BY b,id` was too "safe": it made the result order deterministic but also prevented single-column `pi(b)` from satisfying the ordering. The better generator rule is to make `b` unique in the data, then use `ORDER BY b LIMIT`.
- Generalize beyond partial index: scan code for `Check*`, `CanUse*`, `Prune*`, `Derive*`, `Imply*`, and fast-path guards, then ask what semantic obligation each guard is claiming.
- Use a generic fast-path differential harness: force fast path vs block fast path, then compare row sets, errors, and plan evidence.
- Make every hit update a "proof-obligation table": proof target, claimed implication, counterexample family, oracle, confirmed bad shapes, confirmed negative shapes.

## Regression Test Sketch
Preferred test level: planner/executor integration with TiKV support, because partial index creation is TiKV-only in this environment.

Core assertion:
```sql
CREATE TABLE t(id INT PRIMARY KEY, a INT NULL, b INT, INDEX pi(b) WHERE a < 3);
INSERT INTO t VALUES (1,1,1),(2,2,2),(3,3,3),(4,10,4),(5,NULL,5);

SELECT id,a,b FROM t IGNORE INDEX(pi) WHERE a >= 0 ORDER BY b;
SELECT id,a,b FROM t USE INDEX(pi)    WHERE a >= 0 ORDER BY b;
```

Both result sets must be identical. A stronger regression should also assert that `EXPLAIN` for the unsafe `USE INDEX(pi)` path does not build an `IndexLookUp` on `pi`; either the hint should be ignored/rejected for semantic safety, or the planner should fall back to table scan.

Additional cases:
```sql
-- upper-bound partial condition + lower-bound query
CREATE INDEX pi ON t(b) WHERE a <= 3;
SELECT ... WHERE a >= 0;

-- not-equal partial condition + range containing excluded value
CREATE INDEX pi ON t(b) WHERE a != 10;
SELECT ... WHERE a BETWEEN 3 AND 10;
```

## Methodology Takeaways
This bug validates a new oracle family for AI-native DDL testing:

```text
partial-index planner applicability oracle
= result(IGNORE INDEX(partial)) must equal result(USE/FORCE INDEX(partial))
  whenever a query is otherwise deterministic over a stable user table
```

The useful generation space is not random SQL syntax. It is:
```text
partial condition shape
× query predicate shape
× hint/no-hint path
× stats state
× ordering/limit pressure
```

Important workflow rule after this hit:
After finding a new bug, pause before continuing. First extract the oracle, root-cause model, boundary evidence, and next generator dimensions. This prevents turning a high-signal hit into another blind fuzz loop.
