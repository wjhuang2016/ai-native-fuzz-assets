# TiKV pushdown can silently delete rows because DOUBLE-to-UNSIGNED uses a different tie rule

Status: confirmed on official nightly and current TiKV master; no exact upstream issue found.

## Summary

TiDB and TiKV disagree when a `DOUBLE` exact half value is cast to `UNSIGNED`.
TiDB uses round-to-nearest-even, while TiKV uses Rust's `round()`, which rounds a half away from
zero.

For example, TiDB evaluates `CAST(0.5 AS UNSIGNED)` as `0`; TiKV evaluates it as `1`. The cast is
eligible for pushdown, so an ordinary `DELETE` can remove a row that TiDB says does not satisfy the
statement predicate.

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

No failpoint, source patch, retry, concurrency, or node/network/disk fault is required.

## Minimal reproduction

```sql
CREATE DATABASE ai_float_uint_half_tie;
CREATE TABLE ai_float_uint_half_tie.t(
  id INT PRIMARY KEY,
  x DOUBLE NOT NULL
);
INSERT INTO ai_float_uint_half_tie.t VALUES
  (1,0.5),
  (2,0.4),
  (3,1.4);

EXPLAIN FORMAT='brief'
DELETE FROM ai_float_uint_half_tie.t
WHERE CAST(x AS UNSIGNED)=1;
```

The plan contains:

```text
Selection ... cop[tikv] eq(cast(t.x, bigint UNSIGNED BINARY), 1)
```

Compare exact row membership:

```sql
SELECT GROUP_CONCAT(id ORDER BY id)
FROM ai_float_uint_half_tie.t
WHERE CAST(x AS UNSIGNED)=1;
-- 1,3

SELECT GROUP_CONCAT(id ORDER BY id)
FROM ai_float_uint_half_tie.t
WHERE IF(SLEEP(0)=0,CAST(x AS UNSIGNED)=1,NULL);
-- 3
```

The pushed result contains its own contradiction:

```sql
SELECT id,x,CAST(x AS UNSIGNED),
       CAST(x AS UNSIGNED)=1 AS predicate_holds
FROM ai_float_uint_half_tie.t
WHERE CAST(x AS UNSIGNED)=1
ORDER BY id;
```

```text
id  x    cast_value  predicate_holds
1   0.5  0           0
3   1.4  1           1
```

## Persistent data loss

On reset copies:

```sql
DELETE FROM push_t WHERE CAST(x AS UNSIGNED)=1;
-- affected_rows = 2; remaining id = 2

DELETE FROM root_t
WHERE IF(SLEEP(0)=0,CAST(x AS UNSIGNED)=1,NULL);
-- affected_rows = 1; remaining ids = 1,2
```

The pushed statement silently deletes id `1`; the root evaluator preserves it. `ADMIN CHECK TABLE`
passes because this is a logically wrong but physically consistent deletion.

The matched GREEN uses values `(1.5, 1.4, 1.6)` and predicate
`CAST(x AS UNSIGNED)=2`. Both evaluators select and delete ids `1,3`. This isolates the parity of
the exact-half tie: ties whose lower integer is even expose the mismatch; ties whose rounded result
is already even agree.

## Root cause

TiDB current master:

```go
func ConvertFloatToUint(...) {
    val := RoundFloat(fval)
}

func RoundFloat(f float64) float64 {
    return math.RoundToEven(f)
}
```

TiKV current master:

```rust
fn to_uint(...) -> Result<u64> {
    let val = self.round();
}
```

TiKV's existing `test_float_to_uint` also expects values such as `65536.5` to become `65537`.
A temporary current-master assertion requiring `0.5 -> 0` failed with actual value `1`.

## Production trigger

The trigger is an ordinary predicate over a floating-point column with an explicit unsigned cast.
Exact `.5` values are common in measurements, ratings, prices imported through floating-point
pipelines, and values produced by division. `SELECT`, `UPDATE`, and `DELETE` can all consume the
wrong pushed row set. The demonstrated terminal effect is silent permanent deletion under default
configuration.

## Expected behavior

A deterministic cast must preserve exact row membership across TiDB and TiKV. TiKV should use the
same nearest-even primitive as TiDB, or TiDB should stop pushing this cast until the semantics are
aligned.

## Dedup

Post-RED searches across open and closed `pingcap/tidb` and `tikv/tikv` issues and pull requests
found no exact DOUBLE-to-UNSIGNED half-tie pushdown root. The remote bug asset database also had no
matching rounding or unsigned-cast entry.
