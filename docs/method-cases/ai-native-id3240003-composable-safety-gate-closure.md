# id3240003: safety gates must cover equivalent compositions

Status: confirmed high catalog severity with critical persistent-corruption consequence.

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

## Asset transfer raised the consequence

The first witness varied `time_zone` around `DATE(TIMESTAMP)`. The next round did not enumerate more
offsets or DML forms. It reused a hidden-input asset discovered earlier around
`default_week_format`, then moved that input to the same persistent consumer:

```text
hidden getter:     WEEK(date) reads default_week_format
persistent owner:  indexed virtual generated value
mutation owner:    DELETE reevaluates the value in its current session
```

The minimal matrix fixes the base `DATE` at `2021-01-01` and varies only mode 0 versus mode 3:

| View before DELETE | State |
| --- | --- |
| source rows | `id1:g53,id2:g53` |
| covering unique index | `id1:g0,id2:g53` |

`DELETE WHERE g=0` affects one row while the root-owned twin affects zero. After success, the source
contains `id2:g53`; the index contains stale `id1:g0`. This raises the oracle from a wrong-row
consumer to bidirectional physical parity.

The sibling does not create another bug ID. It shares the same admission owner and generic fix with
the original time-zone witness, so it revises the root fingerprint and severity evidence.

Add one LOOP rule:

1. store hidden inputs independently from the first consumer that exposed them;
2. move each input across cache, remote evaluation, persistence, recovery, and cleanup owners;
3. at each owner, choose the strongest irreversible consumer;
4. merge siblings when one owner-level fix closes every witness;
5. keep the stronger oracle and production scenario as new assets even when the bug count is stable.

## Stop rule

Other functions, settings, storage modes, and DML forms remain the same admission root. A sibling is
worth executing when it reaches a stronger lifecycle consumer, but it upgrades this root instead of
increasing the bug count. Reopen as a new bug only for another safety gate, another owner, or a fix
that is independent of the indexed generated-column admission defect.
