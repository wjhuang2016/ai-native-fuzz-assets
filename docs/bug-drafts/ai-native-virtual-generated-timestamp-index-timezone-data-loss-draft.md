# Indexed virtual generated columns can drift across session contexts and corrupt indexes

## Summary

Under the default TiDB configuration, direct context-sensitive expression indexes are rejected as
unsafe. The equivalent structure can be created through a virtual generated column plus an ordinary
secondary index.

The index key is evaluated in the writer session time zone, while the virtual column is evaluated
again in the reader session time zone. After the time zone changes across a calendar boundary, the
index can return a row whose current generated value and predicate are both false. Ordinary indexed
`DELETE` then silently removes that row. A second hidden input, `default_week_format`, reaches a
stronger terminal: a successful DELETE leaves one live row without its unique-index entry and one
stale index entry for a deleted row.

## Reproduction

```sql
DROP DATABASE IF EXISTS vg_tz_repro;
CREATE DATABASE vg_tz_repro;
USE vg_tz_repro;

CREATE TABLE t (
  id INT PRIMARY KEY,
  ts TIMESTAMP,
  d DATE AS (DATE(ts)) VIRTUAL,
  INDEX idx_d(d)
);

CREATE TABLE root_t LIKE t;

SET time_zone='+08:00';
INSERT INTO t(id,ts) VALUES (1,'2025-01-01 04:00:00');
INSERT INTO root_t(id,ts) VALUES (1,'2025-01-01 04:00:00');

SET time_zone='-08:00';

SELECT id,ts,d,DATE(ts),d='2025-01-01' AS predicate_holds
FROM t IGNORE INDEX(idx_d)
WHERE d='2025-01-01';

SELECT id,ts,d,DATE(ts),d='2025-01-01' AS predicate_holds
FROM t USE INDEX(idx_d)
WHERE d='2025-01-01';

EXPLAIN FORMAT='brief'
DELETE FROM t WHERE d='2025-01-01';

DELETE FROM t WHERE d='2025-01-01';
SELECT ROW_COUNT();

DELETE FROM root_t IGNORE INDEX(idx_d) WHERE d='2025-01-01';
SELECT ROW_COUNT();

SELECT * FROM t;
SELECT id,ts,d,d='2025-01-01' AS predicate_holds FROM root_t;
```

## Actual result

- The root/table path returns no rows.
- The index path returns id 1.
- The returned row shows `ts=2024-12-31 12:00:00`, `d=2024-12-31`,
  `DATE(ts)=2024-12-31`, and `predicate_holds=0`.
- The default plan is an `IndexRangeScan` over key `2025-01-01`.
- Indexed DELETE returns success with one affected row and permanently removes id 1.
- The matched root DELETE affects zero and preserves id 1.
- Default fast `ADMIN CHECK TABLE` does not report the stale key in this direction.

## Expected result

An index on a virtual generated column must represent the value that the virtual expression exposes
to the current query. A row whose current generated value fails the `WHERE` predicate must never be
returned or deleted.

## Stronger physical-corruption witness

Use a plain `DATE`, so this path is independent of TIMESTAMP representation:

```sql
CREATE TABLE week_t (
  id INT PRIMARY KEY,
  d DATE NOT NULL,
  g INT AS (WEEK(d)) VIRTUAL,
  UNIQUE KEY ug(g)
);

SET default_week_format=0;
INSERT INTO week_t VALUES (1,'2021-01-01',DEFAULT);

SET default_week_format=3;
INSERT INTO week_t VALUES (2,'2021-01-01',DEFAULT);
```

Under mode 3, a source scan projects both rows as `g=53`. A covering index scan exposes physical
entries `id1:g0,id2:g53`, so logical uniqueness has already been violated.

```sql
DELETE FROM week_t WHERE g=0;
```

The statement succeeds and affects one row. Its index lookup selects stale key `g=0 -> id1`, then
index maintenance reevaluates `WEEK(d)` under mode 3 and removes key `g=53`, which belongs to id2.
The terminal state is:

```text
record scan:         id2:g53
covering index scan: id1:g0
ADMIN CHECK TABLE:   ERROR 8223
```

The matched `IGNORE INDEX` DELETE affects zero. With `g AS (WEEK(d,3))`, the second insert correctly
returns duplicate-key error 1062 and `ADMIN CHECK TABLE` passes.

## Default safety-gate bypass

The direct equivalent is rejected:

```sql
CREATE TABLE direct_t(id INT PRIMARY KEY, ts TIMESTAMP);
CREATE INDEX idx ON direct_t((DATE(ts)));
```

Result:

```text
ERROR 8200: Unsupported creating expression index containing unsafe functions
without allow-expression-index in config
```

`checkIllegalFn4Generated` computes `hasNotGAFunc4ExprIdx` for both forms, but enforces it only when
the current object is a direct expression index. A user-defined generated column is admitted as
`typeColumn`; a later ordinary index does not revalidate the generated expression.

## Source chain

- `pkg/ddl/generated_column.go`: non-GA functions are rejected only for `typeIndex`.
- `pkg/sessionctx/variable/varsutil.go`: `DATE` and `WEEK` are absent from
  `GAFunction4ExpressionIndex`.
- `pkg/expression/builtin_time.go`: single-argument `WEEK` reads
  `GetDefaultWeekFormatMode`.
- `pkg/executor/insert_common.go`: generated expressions are evaluated with the writer session
  `EvalContext` before table mutation builds index keys.
- `pkg/table/column.go`: virtual generated values are reevaluated with the reader session
  `EvalContext`.
- The index range is trusted as a complete predicate proof; DELETE does not recheck the virtual
  value from the base TIMESTAMP.

## Production trigger

A common schema workaround for date-part lookup uses:

```sql
event_date DATE AS (DATE(event_ts)) VIRTUAL,
INDEX idx_event_date(event_date)
```

An ingestion service writes in one session time zone. A regional service, reporting job, retention
task, or connection pool later reads or deletes in another time zone. A TIMESTAMP near midnight
crosses the calendar boundary between the zones.

Another ordinary schema stores a weekly bucket:

```sql
week_no INT AS (WEEK(event_date)) VIRTUAL,
UNIQUE KEY uk_week(week_no)
```

Two services or connection pools use different `default_week_format` values, commonly mode 0 and
ISO-style mode 3. Inserts can bypass logical uniqueness; a later update or cleanup reevaluates the
key under its own session mode.

The trigger needs no partial index, concurrency, retry, failpoint, source change, nondefault SQL
mode, disabled MDL, or infrastructure failure.

## Fix direction

Apply expression-index safety admission to every indexed generated-column composition. For existing
schemas, either reject context-sensitive virtual generated indexes, freeze and persist a canonical
expression context, or require a base-row recheck before an index result can feed reads or DML.
`ADMIN CHECK` should compare generated index keys using the same canonical contract.

## Severity

Critical impact. A common indexed generated-column pattern can make ordinary DELETE silently remove
a predicate-false row or leave bidirectional physical index corruption. The local bug catalog stores
it as `high`, following the existing catalog convention for critical-consequence findings.
