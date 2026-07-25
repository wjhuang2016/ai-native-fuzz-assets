# TiKV duration-cast pushdown can make DML modify a row that fails its predicate

Status: confirmed on official nightly and current TiKV master; not filed upstream.

## Summary

TiDB and TiKV disagree when `TIME(6)` is a negative exact half-second and is cast to `SIGNED`.
TiDB returns `0` for `-00:00:00.500000`; TiKV returns `-1`.

The cast is pushed into TiKV for a table predicate. A query can therefore return a row and show
that the same row does not satisfy its own predicate. `UPDATE` and `DELETE` use the same pushed
selection, so the mismatch can persistently mutate the wrong row.

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

No failpoint, source patch, retry, concurrency, process pause, or node/network/disk fault is needed.

## Minimal reproduction

```sql
CREATE DATABASE ai_duration_cast_diff;
USE ai_duration_cast_diff;

CREATE TABLE t(
  id INT PRIMARY KEY,
  dur TIME(6) NOT NULL,
  marker INT NOT NULL DEFAULT 0
);

INSERT INTO t(id,dur) VALUES
  (1,'-00:00:00.499999'),
  (2,'-00:00:00.500000'),
  (3,'-00:00:00.500001'),
  (4,'00:00:00.500000');
```

The ordinary predicate is pushed into TiKV:

```sql
EXPLAIN FORMAT='brief'
SELECT id FROM t WHERE CAST(dur AS SIGNED)<0;
```

```text
Selection ... cop[tikv] lt(cast(t.dur, bigint BINARY), 0)
```

Compare exact row membership:

```sql
SELECT GROUP_CONCAT(id ORDER BY id)
FROM t
WHERE CAST(dur AS SIGNED)<0;
-- 2,3

SELECT GROUP_CONCAT(id ORDER BY id)
FROM t
WHERE IF(SLEEP(0)=0,CAST(dur AS SIGNED)<0,NULL);
-- 3
```

`SLEEP(0)` is only a root-evaluation barrier. It does not delay this four-row query.

The ordinary query contains its own contradiction:

```sql
SELECT id,dur,CAST(dur AS SIGNED) AS cast_value,
       CAST(dur AS SIGNED)<0 AS predicate_holds
FROM t
WHERE CAST(dur AS SIGNED)<0
ORDER BY id;
```

```text
id  dur                cast_value  predicate_holds
2   -00:00:00.500000   0           0
3   -00:00:00.500001  -1           1
```

The selection was evaluated in TiKV, while the projected values were evaluated in TiDB.

## Persistent wrong DML

```sql
EXPLAIN FORMAT='brief'
UPDATE t SET marker=1 WHERE CAST(dur AS SIGNED)<0;

UPDATE t SET marker=1 WHERE CAST(dur AS SIGNED)<0;
SELECT id,dur,marker FROM t WHERE marker=1 ORDER BY id;
```

The plan has a `cop[tikv]` Selection and updates ids `2,3`. Id `2` is a persistent wrong mutation:
TiDB evaluates its predicate to false.

Matched root-only control:

```sql
UPDATE t SET marker=0;
UPDATE t
SET marker=2
WHERE IF(SLEEP(0)=0,CAST(dur AS SIGNED)<0,NULL);
```

This affects only id `3`. Adjacent values `.499999` and `.500001` also bound the mismatch to the
negative exact-half tie.

## Current-master confirmation

A temporary assertion was added to TiKV's existing `test_duration_as_int`:

```rust
(
    Duration::parse(&mut ctx, "-00:00:00.50", 2).unwrap(),
    0,
),
```

Current master failed:

```text
input: -00:00:00.50, expect: 0, output: Ok(Some(-1))
```

The probe was removed after the run. There is no relevant change between the nightly TiKV revision
and current master in the cast implementation.

## Root cause

TiDB's `builtinCastDurationAsIntSig` calls `types.Duration.RoundFrac(0)`. That implementation
anchors the duration on a Go time value before rounding; a negative exact-half tie is rounded
toward zero.

TiKV's `Duration::to_int` calls `Duration::round_frac(0)` directly. Its tie behavior rounds the
negative duration away from zero. `ScalarFuncSig_CastDurationAsInt` remains eligible for pushdown,
so the optimizer treats the two evaluators as interchangeable.

## Production trigger

An application stores signed elapsed-time or schedule offsets in `TIME(6)` and uses an explicit
numeric cast in a filter, cleanup, or state-transition statement. Values exactly on half-second
boundaries are natural when durations are quantized by a 500 ms timer. A negative value can
represent early completion, clock offset, debit time, or a countdown before zero.

The trigger is narrower than a general TIME predicate because it needs a negative `.500000` tie and
an explicit cast. The consequence is nevertheless persistent wrong `UPDATE`/`DELETE`, so the bug is
cataloged as high severity rather than critical.

## Expected behavior

A deterministic pushed expression must preserve row membership across TiDB and TiKV. The two
duration rounding implementations should share one negative-tie rule. Until they do,
`CastDurationAsInt` should not be pushed down.

## Dedup

Post-RED searches across `pingcap/tidb`, `tikv/tikv`, and the remote `found_bug` table found no
issue with this negative-duration half-tie root.
