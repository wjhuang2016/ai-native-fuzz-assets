# id2370003: hidden-attempt feedback changes the successful retry output

Remote bug DB: `found_bug id2370003`, confirmed high consequence / low frequency.
Upstream: [TiDB #69822](https://github.com/pingcap/tidb/issues/69822).

## Starting proof obligation

Transparent statement retry must be observationally equivalent to one execution from the state
seen by the successful attempt, unless TiDB exposes the original error. Earlier S45 cases looked
for state that simply survived a failed attempt. This case asks a stronger question:

```text
failed attempt writes external owner E
successful retry re-executes expression R
R reads E, or R's return value depends on E
R's changed result enters the durable row image
```

## How the candidate was generated

1. Start from the proven pessimistic RC retry owner, not from issue history.
2. Enumerate expression functions marked mutable or side-effecting.
3. Keep only a different owner from prior roots and require a C3 consumer.
4. `SETVAL` survived because it both mutates a sequence owner and returns a value determined by
   that owner's prior state.
5. Use a changing unique-key assignment only to force one natural retry. Keep the `SETVAL` argument
   constant so the output difference cannot be blamed on changed input rows.

## Compressed matrix

| Cell | Hidden retry | Final row | Next sequence value |
| --- | ---: | --- | ---: |
| Natural unique conflict | 1 | `1,2,NULL` | 101 |
| Same successful-attempt state, direct execution | 0 | `1,2,100` | 101 |
| Counterfactual: decline retry after effective SETVAL | 0 | original conflict; row `1,10,0` | 101 |

The sequence-state equality is an important anti-oracle: this is not a complaint about gaps or
nontransactional sequence allocation. The public error and durable row image disagree with both
legal outcomes: an exposed failure or one successful execution.

## Selector improvement

New child selector: `HIDDEN_ATTEMPT_FEEDBACK_INTO_RETRY_OUTPUT`.

```text
external writes by failed attempt
intersect
owners read by re-executed expressions
intersect
values that reach durable output or public terminal truth
minus
journal, restore, idempotency, or retry-decline guards
```

This is more precise than searching for omitted reset fields. A side effect can be intentionally
nontransactional and still be unsafe for hidden retry when the retry observes it and changes a
committed value. Candidate generation should therefore build write-read feedback edges across the
retry boundary, not only a list of survivor fields.

## Quality assessment

The consequence is silent persistent wrong data, so it is C3/high. Trigger frequency is low:
applications must call `SETVAL` inside retryable pessimistic RC DML and hit a conflict after the
first evaluation. Do not label it critical or enumerate sequence values, sleep durations, DML
forms, or conflict shapes. Reopen only for a different external owner or a higher-frequency default
path.
