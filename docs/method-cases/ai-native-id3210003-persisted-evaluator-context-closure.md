# id3210003: persisted predicates need canonical inputs and stable context

Status: confirmed high severity with persistent index corruption and silent wrong DML.

## Proof obligation

```text
P: partial-index membership is evaluated by one process-global expression context.
Q: the schema predicate therefore defines one stable set of rows.
F: TIMESTAMP reaches that evaluator in a writer-session wall-clock representation,
   so one UTC instant receives different persisted membership across sessions.
```

The original context audit asked which session values the evaluator reads. This case adds a second
question: which context shaped the typed value before it reached the evaluator?

## Small matrix

Use one canonical instant, one unique key, and two ordinary session time zones:

| Writer | SQL TIMESTAMP text | UTC instant | Stored partial-index member |
| --- | --- | --- | --- |
| `-08:00` | `2024-12-31 12:00:00` | `2024-12-31 20:00:00` | no |
| `+08:00` | `2025-01-01 04:00:00` | `2024-12-31 20:00:00` | yes |

Observed from `+08:00`, both rows have the same TIMESTAMP, satisfy the same schema predicate, and
carry the same partial-unique key. A full scan returns both rows, while the partial index returns
one. `ADMIN CHECK TABLE` reports error 8223.

The persistent lift is an ordinary statement:

```sql
DELETE FROM t
WHERE ts >= '2025-01-01 00:00:00' AND k = 7;
```

The optimizer uses a point get on the partial unique index. The statement reports one deleted row
and succeeds, but a fresh full scan finds a surviving row whose predicate is still true.

The same-time-zone control rejects the second insert with error 1062 and passes `ADMIN CHECK`.

## Selector refinement

`PERSISTED_EVALUATOR_CONTEXT_CLOSURE` checks both sides of a persisted expression:

1. find an expression result that controls an index key, generated value, routing decision, or
   other persisted derived state;
2. inventory the evaluator's explicit and hidden context;
3. inventory the representation context of every typed operand;
4. hold the schema expression and canonical value fixed while varying one session representation;
5. compare the source-of-truth row set with the persisted derived structure;
6. lift the mismatch through uniqueness, point lookup, UPDATE, or DELETE;
7. include a same-context control before classifying the root.

## Why this worked

The source already showed a suspicious fixed evaluator context. A direct timezone matrix initially
looked green because all rows were written by one session. Reframing the invariant around one
canonical instant exposed the missing dimension: writer context can alter the operand before the
fixed evaluator sees it.

This turns a broad "test time zones" idea into a four-cell proof:

```text
same canonical value
  x different writer representation
  x persisted membership
  x source-of-truth consumer
```

## Stop rule

Additional offsets, timestamps, and DML forms are the same root. Reopen only when a different
persisted expression owner consumes session-shaped operands or when a separate consumer bypasses
an independent closure proof.
