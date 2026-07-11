# Method Case id30023: Request timezone and row timezone are separate obligations

## Why This Hit Was Fast

The selector did not start from random `information_schema` predicates. It started from a source-level proof obligation:

```text
Code converts UPDATE_TIME predicates to a backend time range using session timezone.
System believes returned rows are already SQL-visible under that same timezone.
So it drops the original predicate and skips scalar recheck.
```

The source red flag was narrow: `updateTimestamp.In(tz)` was called without assigning its return
value. That made the counterexample almost forced: use a non-UTC session timezone and compare the
plain shortcut with a CASE self-recheck.

## Oracle

The fast arm:

```sql
SET time_zone='+14:00';
SELECT COUNT(*), MIN(update_time), MAX(update_time),
       SUM(update_time >= lo AND update_time < hi)
FROM information_schema.tidb_hot_regions_history
WHERE update_time >= lo AND update_time < hi;
```

The reference arm:

```sql
SELECT COUNT(*)
FROM information_schema.tidb_hot_regions_history
WHERE update_time >= lo AND update_time < hi
  AND CASE WHEN update_time >= lo AND update_time < hi THEN 1 ELSE 0 END = 1;
```

Trigger evidence comes from `EXPLAIN`: `MemTableScan` contains `start_time` and `end_time`, so the
extractor consumed the predicate.

## Result

Fast path returned 69 rows under `+14:00`, but every returned row evaluated the projected predicate
as false. CASE reference returned 0 rows.

## Method Lesson

This is S3 again, but the useful refinement is not "test more timezone variants." The reusable
rule is:

```text
If a shortcut translates a predicate into a backend request and also constructs SQL-visible rows,
the request context and row-construction context are two separate proof obligations.
```

For time data, assigning or not assigning a conversion result can be enough to split them.

## Stop Rule

Do not enumerate more time columns just because this hit. Reopen only when source shows a distinct
request/render context split, or when a different backend table has a separate low-noise CASE oracle.
