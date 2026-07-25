# TiKV ignores `default_week_format` for pushed `WEEK(date)` and can delete the wrong rows

Status: confirmed on official nightly and present in current TiDB/TiKV master; not filed upstream.

## Summary

TiDB evaluates single-argument `WEEK(date)` using the session variable
`default_week_format`. TiKV always evaluates the pushed `WeekWithoutMode` signature with mode 0.

When a session uses a nonzero week format, a pushed filter selects rows under mode 0 and returns
their handles to `UPDATE` or `DELETE`. TiDB does not recheck the predicate before mutating them.
An ordinary strict-mode `DELETE` can therefore remove dates that do not belong to the requested
week under the session's configured semantics.

## Environment

```text
TiDB nightly: ed2376acc6e0feeff9f3e2c38db489727933aa80
TiKV nightly: 730be34f959185c934b7d3db730ca1dbeb3949f8
TiDB master:  05b396fb6636f73b3bc06b09107cf43f2c725c35
TiKV master:  91ccfb212677a43fd5255183ccf2afa4e3cec23e
Topology:     one TiDB, one PD, one real TiKV
MDL:          enabled
sql_mode:     default strict mode
```

The only nondefault setting is `default_week_format=3`, a normal MySQL setting for ISO-style
weeks. No concurrency, retry, failpoint, source patch, pause, or infrastructure fault is needed.

## Minimal reproduction

```sql
CREATE TABLE t(id INT PRIMARY KEY, d DATE NOT NULL);
INSERT INTO t VALUES
  (1,'2015-12-31'),(2,'2016-01-01'),(3,'2016-01-03'),(4,'2016-01-04'),
  (5,'2019-12-29'),(6,'2019-12-30'),(7,'2020-01-01'),(8,'2020-01-05'),
  (9,'2020-12-31'),(10,'2021-01-01'),(11,'2021-01-03'),(12,'2021-01-04');

SET @@session.default_week_format=3;

SELECT id,d,WEEK(d),WEEK(d,@@default_week_format),
       WEEK(d)<>WEEK(d,@@default_week_format) AS predicate_holds
FROM t
WHERE WEEK(d)<>WEEK(d,@@default_week_format)
ORDER BY id;
```

The predicate is semantically impossible: single-argument `WEEK(d)` must use the value of
`@@default_week_format`, so both sides are the same expression. TiKV nevertheless returns:

```text
ids: 1,2,3,6,7,9,10,11
```

After the rows reach TiDB, projection evaluates both expressions locally. Every returned row shows
the same week number on both sides and `predicate_holds=0`.

`EXPLAIN` localizes the mismatch:

```text
Selection cop[tikv]
  ne(week(t.d), week(t.d, 3))
```

Adding a zero-delay `SLEEP(0)` barrier keeps the predicate in TiDB and returns no rows.

## Ordinary wrong deletion

Under mode 3, only `2019-12-29` in the matrix is week 52:

```sql
SELECT GROUP_CONCAT(id ORDER BY id) FROM t WHERE WEEK(d,3)=52;
-- 5
```

The ordinary one-argument filter is pushed and selects mode-0 rows:

```sql
SELECT GROUP_CONCAT(id ORDER BY id) FROM t WHERE WEEK(d)=52;
-- 1,5,6,9
```

Consequently:

```sql
DELETE FROM t WHERE WEEK(d)=52;
-- success, 4 rows deleted
```

Ids 1, 6, and 9 are silently deleted even though their mode-3 week numbers are 53, 1, and 53.
The matched root-owned `DELETE`, using the same single-argument expression behind a `SLEEP(0)`
barrier, deletes only id 5. `ADMIN CHECK TABLE` passes on both copies because this is logically
wrong data loss rather than record/index structural corruption.

## Root cause

TiDB's local evaluator reads the hidden session input:

```go
mode := 0
if modeStr := ctx.GetDefaultWeekFormatMode(); modeStr != "" {
    mode, err = strconv.Atoi(modeStr)
}
week := date.Week(mode)
```

TiKV's current `week_without_mode` implementation ignores that input:

```rust
let week = t.week(WeekMode::from_bits_truncate(0u32));
```

The protobuf signature carries only the date argument. TiKV's `EvalContext` does not contain
`default_week_format`, so capturing `ctx` cannot reproduce TiDB semantics.

## Production trigger

A service configures ISO-style weeks globally or per connection:

```sql
SET GLOBAL default_week_format=3;
```

Application cleanup, retention, or rollup code then uses the standard one-argument form:

```sql
DELETE FROM weekly_events WHERE WEEK(event_date)=52;
```

Near year boundaries, mode 0 and mode 3 assign different week numbers. The TiKV filter uses mode 0
and can delete events outside the application's intended ISO week. The same mismatch affects
`SELECT`, `UPDATE`, and any other pushed row-set consumer.

## Expected behavior and fix direction

Before pushdown, TiDB should make the hidden input explicit by rewriting `WEEK(date)` to
`WEEK(date, <current default_week_format>)`, or the coprocessor request must carry the session
setting and TiKV must use it. Until the semantic input reaches TiKV, `WeekWithoutMode` should not
be pushed down.

## Severity and dedup

The consequence is direct silent data loss through a common date function. The trigger requires a
nonzero `default_week_format`, so the catalog grade remains high rather than critical.

The existing id30034 and TiDB issue #69650 concern prepared plan cache reusing a value after the
session variable changes. This bug needs no prepared statement or variable change and has a
different owner: the TiKV remote evaluator. Older issues #9669 and #21510 concern local/session
loading. Post-RED searches found no issue for the current pushdown root.
