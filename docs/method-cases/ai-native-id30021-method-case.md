# Method Case id30021: Interval rows break point-style coarse skip
> 2026-07-03. `information_schema.statements_summary`. This note records the methodology result, not just the bug.

## What Was Being Tested

After id30020, the loop needed to avoid another cache-payload variant unless it added a new dimension. This target returned to S3, but with a different mechanism from the `valueToLower=true` helper:

```text
coarse time-range shortcut
sets skip_request
while original scalar predicates are still needed for correctness
```

## Why This Target Was Picked

The source had a very explicit proof comment:

```text
SELECT ... WHERE summary_begin_time <= endTime
             AND summary_end_time >= startTime
```

Then the executor skipped the request if the derived `startTime > endTime`.

That is a familiar point-range proof, but statement-summary rows are intervals:

```text
row = [summary_begin_time, summary_end_time]
```

For interval rows, `begin <= A AND end >= B` with `B > A` means "the summary window covers [A,B]", not "empty range".

## Tiny Matrix

Red cell:

```sql
WHERE summary_begin_time <= A
  AND summary_end_time >= B
-- A and B are inside one real statement-summary window, A < B
```

Fast path:

```text
EXPLAIN ... skip_request:true
COUNT(*) = 0
```

Reference:

```sql
WHERE CASE WHEN summary_begin_time <= A THEN TRUE ELSE FALSE END
  AND CASE WHEN summary_end_time >= B THEN TRUE ELSE FALSE END
```

Reference result:

```text
n>0, begin_ok=n, end_ok=n
```

Green control:

```sql
WHERE summary_begin_time <= B
  AND summary_end_time >= A
```

This is not skipped and returns the same live window rows.

## Why It Worked

The proof obligation was visible in code, not hidden in a large search space. The only adversarial dimension was:

```text
D_dim = interval semantics
```

Once that was named, the matrix was one red cell plus one green control. CASE wrapping gave a no-shortcut reference and row self-predicate evidence.

## Quality

Medium severity, high methodology value:

- deterministic wrong-result on a SQL-visible information_schema table;
- no failpoint or concurrency required;
- trigger evidence is explicit `skip_request:true`;
- CASE reference returns rows that all satisfy the original predicates;
- the issue is not another `valueToLower=true` blast-radius case.

Severity is medium because it affects observability/diagnostic tables rather than user data, but the predicate semantics are plainly wrong.

## Methodology Improvement

Add this rule to S3:

```text
If a shortcut converts row intervals into a point/range request,
check whether the skip condition proves unsatisfiable predicates
or only an empty point-range abstraction.
```

This is a general AI-search improvement. Comments that spell out a proof such as "start/end range" are high-value targets when the row model is richer than a point.
