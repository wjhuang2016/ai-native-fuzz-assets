# id3180003: hidden session inputs must become explicit before pushdown

Status: confirmed high severity with ordinary wrong-row deletion.

## Proof obligation

```text
P: TiDB and TiKV both implement single-argument WEEK(date).
Q: both evaluators use the session's default_week_format.
F: TiDB reads the hidden input; TiKV fixes mode 0; pushed DELETE consumes TiKV's row set.
```

The useful comparison is not the function name or explicit SQL arguments. It is the complete set
of semantic inputs read while evaluating the function.

## Asset reuse

id30034 had already recorded that `WEEK(date)` reads a hidden session input. That bug stopped at the
prepared plan-cache boundary. Reusing the getter inventory against a different representation
boundary exposed a new root:

```text
local getter inventory
  -> pushdown signatures
  -> remote request/context fields
  -> set difference
  -> persistent row-set consumer
```

This is incremental discovery: the expensive semantic fact was learned once, then checked against
another owner without restarting from broad expression fuzzing.

## Small matrix

The strongest oracle is an algebraic identity:

```sql
WEEK(d) = WEEK(d, @@default_week_format)
```

With `default_week_format=3`, the pushed negation returned eight rows. Root evaluation returned
none, and projecting the predicate on every pushed row produced `predicate_holds=0`.

The production-shaped cell used `WEEK(d)=52`:

| Evaluator | matched ids | meaning |
| --- | --- | --- |
| TiKV pushed single-argument | 1,5,6,9 | mode 0 |
| TiDB root single-argument | 5 | current mode 3 |
| explicit `WEEK(d,3)` | 5 | semantic reference |

Pushed `DELETE` removed four rows. Root-owned `DELETE` removed one.

## Selector refinement

Extend `REMOTE_EVALUATOR_CONTEXT_CLOSURE` with hidden getter provenance:

1. inventory local evaluator getters for session, statement, return type, and config state;
2. trace each getter's value to protobuf arguments or remote `EvalContext` fields;
3. flag signatures where the remote function substitutes a literal or default;
4. build an algebraic self-equivalence when an explicit-argument sibling exists;
5. use year/precision/length boundaries only after the missing input is proven;
6. lift exact row-set drift through ordinary DML;
7. deduplicate by omitted input plus remote owner, not by the SQL function alone.

Capturing a generic `ctx` is not proof of closure. The context schema must contain the specific
semantic field.

## Why this worked

The search space collapsed along three dimensions:

- id30034 supplied the hidden input and known boundary dates;
- source diff supplied the missing transport edge before any test data was generated;
- `WEEK(d)` versus `WEEK(d, @@default_week_format)` supplied a reference with no hand-written
  expected week calculations.

Only one 12-row date matrix was needed to prove row admission and persistent consequence.

## Stop rule

Modes 1 through 7 and additional year-boundary dates are the same root. Reopen only for another
hidden input, another remote context field, or a different irreversible consumer that bypasses a
separate proof.
