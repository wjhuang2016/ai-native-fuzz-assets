# Method Case id30001: Partial-index implication proof

Status: confirmed on current master; upstream issue [TiDB #69779](https://github.com/pingcap/tidb/issues/69779).

## What the loop found

The target was not "test partial indexes broadly". The source exposed a proof gate:

```text
planner may retain partial index pi
only if query predicate => pi's stored predicate
```

The counterexample was deliberately non-overlapping in meaning but overlapping in range:

```text
query:   a >= 0
partial: a < 3
```

The predicates share rows, so a shallow test can look plausible, but the query also admits
rows that are absent from the partial index.

## Audit card

```text
P_check:
  DataSource.CheckPartialIndexes parses Index.ConditionExprString and calls
  partidx.CheckConstraints before index paths are filled.

Q_claim:
  Keeping pi as an access path is safe only when every row satisfying the query is present
  in pi, i.e. query predicate implies the partial predicate.

F_effect:
  IndexFullScan(pi) can satisfy ORDER BY b, while the residual query filter is applied only
  after the index lookup. Rows outside the partial subset are never visited.

D_dimensions:
  partial predicate shape x query interval shape x natural/forced path x statistics state
  x LIMIT/order pressure.

O_oracle:
  stable table scan or IGNORE INDEX(pi) is the complete reference; compare it with the
  default, USE, or FORCE partial-index path and then run ADMIN CHECK TABLE as a storage
  consistency control.
```

## Small matrix and result

The decisive current-master cell used a five-row, `NOT NULL` table on testbed `8220955`:

```text
INDEX pi(b) WHERE a < 3
WHERE a >= 0 ORDER BY b LIMIT 5
```

Results:

```text
IGNORE INDEX(pi):  ids 1,2,3,4,5
default plan:     ids 1,2,3
FORCE INDEX(pi):  ids 1,2,3
ADMIN CHECK:      green/silent
```

The plan showed `IndexLookUp(Build: pi(b), Probe: Selection(a >= 0))`. This is a strong
wrong-result oracle because the two arms differ only in access-path eligibility. It also
separates planner unsafety from index corruption: the partial index is internally correct,
but the planner visits an incomplete physical subset.

The first live cell used `FORCE INDEX` to make the bad path deterministic. A larger follow-up
cell showed that the optimizer can choose the same path naturally under pseudo statistics when
the partial index satisfies `ORDER BY b`; `ANALYZE TABLE` changing the plan is a state control,
not a semantic defense.

## Source-to-observation chain

The relevant chain is:

```text
stats.go:122-131
  query predicates receive normal cast/not/range preparation
        |
logical_datasource.go:814-817
  raw stored partial condition is parsed and sent to CheckConstraints
        |
partidx/check_constraint.go:92-128
  partial and query ranges are unioned; equality is treated as implication proof
        |
unsafe IndexLookUp(pi)
  residual filter cannot recover rows that were never read from pi
```

A temporary planner probe found a key boundary: the first range build for raw metadata
`a < 3` returned `[-inf,+inf]`, while the same expression after the normal predicate handling
path returned `[-inf,3)`. The exact lower-level normalization mechanics need owner review, but
the actionable obligation is already proven: the implication checker must fail closed when its
metadata input is not semantically normalized.

## Why this method worked

1. The source supplied a proof obligation before any workload generation.
2. The counterexample generator attacked implication, not syntax coverage.
3. `IGNORE INDEX` supplied a cheap reference and `FORCE INDEX` isolated the unsafe fast path.
4. `ADMIN CHECK TABLE` prevented a false diagnosis of storage/index corruption.
5. The `NOT NULL` control removed nullable semantics as an alternative explanation.

This is materially stronger than random partial-index fuzzing: one five-row table produced a
silent, user-visible wrong result and a source-level explanation.

## Improvements promoted to the loop

```text
1. Treat proof-input normalization as part of the proof, not as parser plumbing.
2. Generate semantic counterexamples from interval sets: overlap without implication,
   excluded points, OR widening, NULL rejection, collation, and casts.
3. Prefer fast-path differential oracles: force/allow the path versus block the path.
4. Keep statistics changes as controls; they may change plan selection but cannot repair
   a semantically invalid path.
5. Record negative boundaries immediately. Do not generalize this hit to every partial-index
   predicate or every planner range path without an independent red oracle.
```

The durable selector is:

```text
semantic proof gate
× metadata/query normalization asymmetry
× fast-path rowset differential
=> high-value planner wrong-result target
```

No formal product test was added. The reusable asset is the proof card, counterexample family,
oracle, source boundary, and the stop rule against blindly enumerating predicate syntax.
