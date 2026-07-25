# id3240003: safety gates must cover equivalent compositions

Status: confirmed high catalog severity with critical silent-data-loss consequence.

## Starting proof

TiDB rejects this under its default configuration:

```sql
CREATE INDEX idx ON t((DATE(ts)));
```

The error says the expression contains an unsafe function and requires
`allow-expression-index`.

The same semantic structure is accepted when split into two DDL primitives:

```sql
d DATE AS (DATE(ts)) VIRTUAL,
INDEX idx_d(d)
```

The admission code records that `DATE` is not GA for expression indexes, but only enforces that fact
for `genType == typeIndex`. A manually declared generated column enters as `typeColumn`; the later
secondary-index creation does not revisit the source expression.

## P/Q/F

```text
P: direct expression-index syntax blocks DATE(TIMESTAMP) as unsafe.
Q: accepted compositions cannot recreate the same unsafe derived key.
F: virtual generated column + ordinary index bypasses the gate.
```

At mutation time, the index key uses the writer session expression context. At read time, a virtual
column is recomputed with the reader session context. A persisted key can therefore disagree with
the current value it claims to index.

## Critical RED

Write in `+08:00`:

```sql
INSERT INTO t(id,ts) VALUES (1,'2025-01-01 04:00:00');
```

Read in `-08:00`. The stored TIMESTAMP is now rendered as `2024-12-31 12:00:00`, so both `d` and
`DATE(ts)` are `2024-12-31`.

| Path | `WHERE d='2025-01-01'` |
| --- | --- |
| `IGNORE INDEX(idx_d)` | no rows |
| `USE INDEX(idx_d)` | id 1 |

The indexed row projects:

```text
d=2024-12-31
DATE(ts)=2024-12-31
predicate_holds=0
```

An ordinary default-plan `DELETE WHERE d='2025-01-01'` deletes id 1 and succeeds. The matched
root-owned DELETE affects zero and preserves the row. Default `ADMIN CHECK TABLE` does not report
the stale key in this direction.

Controls:

- write and read in the same time zone: root and index both return id 1;
- replace TIMESTAMP with DATETIME: changing session time zone does not change the generated date,
  and both paths return id 1;
- use direct expression-index syntax: the default safety gate rejects it.

## Selector

`COMPOSABLE_SAFETY_GATE_CLOSURE`:

1. capture a direct syntax rejected for a concrete safety reason;
2. normalize it into expression, derived state, evaluator context, and consumer;
3. compose accepted lower-level features into the same semantic graph;
4. check whether the original admission predicate is revalidated;
5. vary exactly the semantic dimension named by the rejection;
6. compare root/source truth with the derived fast path;
7. lift a self-contradictory row through the highest irreversible consumer.

The selector applies beyond indexes: foreign-key checks versus generated dependencies, restore
prechecks versus idempotent create, direct DDL restrictions versus multi-action DDL, and validated
configuration versus equivalent runtime mutation.

## Why this worked

The product supplied both the hypothesis and the negative control. The direct rejection identified
the dangerous function family before data generation. Source inspection found the alternate syntax
whose semantic graph was equivalent but whose `genType` escaped the guard. A two-time-zone,
one-row matrix then reached direct data loss.

This is much more efficient than fuzzing arbitrary generated expressions because the rejected
operation acts as a compact specification of what the system already considers unsafe.

## Stop rule

Other date functions, offsets, generated-column storage modes, and DML forms are the same admission
root. Reopen only for another safety gate, another equivalent construction, or a separate derived
consumer with its own missing revalidation.
