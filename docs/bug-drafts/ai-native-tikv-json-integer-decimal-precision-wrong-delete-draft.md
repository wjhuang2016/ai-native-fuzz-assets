# TiKV JSON-to-DECIMAL pushdown can silently delete rows after integer precision loss

Status: confirmed on official nightly and current TiKV master; not filed upstream.

## Summary

TiDB converts JSON `I64` and `U64` values directly to exact decimal values. TiKV converts every
non-string JSON value through `f64` before constructing the decimal. Integers above `2^53` can
therefore change value only when the expression is evaluated in TiKV.

A pushed `DELETE` can silently remove rows whose JSON ID and DECIMAL mirror are identical. The
same predicate evaluated in TiDB is false and deletes no rows.

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

No concurrency, retry, failpoint, source patch, or configuration change is required.

## Minimal reproduction

```sql
CREATE TABLE events_push(
  id INT PRIMARY KEY,
  payload JSON NOT NULL,
  entity_id DECIMAL(65,0) NOT NULL
);
CREATE TABLE events_root LIKE events_push;

INSERT INTO events_push VALUES
  (1, JSON_OBJECT('entity_id', 9007199254740991), 9007199254740991),
  (2, JSON_OBJECT('entity_id', 9007199254740993), 9007199254740993),
  (3, JSON_OBJECT('entity_id', 9223372036854775807), 9223372036854775807),
  (4, JSON_OBJECT('entity_id', 18446744073709551615), 18446744073709551615);
INSERT INTO events_root SELECT * FROM events_push;
```

The ordinary predicate is pushed to a `cop[tikv] Selection`:

```sql
SELECT GROUP_CONCAT(id ORDER BY id)
FROM events_push
WHERE CAST(payload->'$.entity_id' AS DECIMAL(65,0)) <> entity_id;
-- 2,3,4
```

`SLEEP(0)` keeps the same predicate in TiDB:

```sql
SELECT GROUP_CONCAT(id ORDER BY id)
FROM events_root
WHERE IF(
  SLEEP(0)=0,
  CAST(payload->'$.entity_id' AS DECIMAL(65,0)) <> entity_id,
  NULL
);
-- NULL
```

## Silent wrong deletion

```sql
DELETE FROM events_push
WHERE CAST(payload->'$.entity_id' AS DECIMAL(65,0)) <> entity_id;
-- success; 3 rows deleted; only id=1 remains

DELETE FROM events_root
WHERE IF(
  SLEEP(0)=0,
  CAST(payload->'$.entity_id' AS DECIMAL(65,0)) <> entity_id,
  NULL
);
-- success; 0 rows deleted; ids 1,2,3,4 remain
```

There is no warning or error. `ADMIN CHECK TABLE` is also green because each deletion is
physically consistent; the loss is at the SQL row-selection layer.

## Boundary

The first distinguishing positive value is `9007199254740993`, or `2^53+1`.

```text
value                 pushed result
9007199254740991      exact
9007199254740992      exact
9007199254740993      9007199254740992
9007199254740994      exact
9007199254740995      9007199254740996
```

Large signed and unsigned values are both affected.

## Current-master confirmation

The focused probe in
`scaffolds/rust-probes/tikv_json_integer_decimal_precision_test.patch` fails on TiKV master:

```text
JSON integer 9007199254740993
left:  9007199254740992
right: 9007199254740993
```

The minimal counterfactual handles `JsonType::I64` and `JsonType::U64` with direct
`Decimal::from(...)`. The same focused test then passes. The temporary change was removed.

## Root cause

TiDB `ConvertJSONToDecimal` has exact branches:

```go
case JSONTypeCodeInt64:
    res = res.FromInt(j.GetInt64())
case JSONTypeCodeUint64:
    res = res.FromUint(j.GetUint64())
```

TiKV `ConvertTo<Decimal> for JsonRef` distinguishes only strings. Every other JSON type takes:

```rust
let r: f64 = self.convert(ctx)?;
Decimal::from_f64(r)
```

The intermediate `f64` domain cannot represent every `I64` or `U64` value exactly.

## Production trigger

Applications commonly keep a large entity ID both in a JSON payload and in a typed mirror column.
Reconciliation or cleanup jobs may use:

```sql
DELETE FROM events
WHERE CAST(payload->'$.entity_id' AS DECIMAL(65,0)) <> entity_id;
```

Snowflake-style IDs are normally above `2^53`. Matching rows can therefore be classified as
inconsistent and deleted. The behavior uses default settings and ordinary one-statement DML.

An implicit comparison is an important GREEN control:

```sql
WHERE payload->'$.entity_id' <> entity_id
```

The planner converts the DECIMAL operand to JSON, so both TiDB and TiKV return an empty row set.
The bug requires the explicit JSON-to-DECIMAL conversion shown above.

The consequence is direct silent data loss. The SQL shape requires an explicit JSON-to-DECIMAL
comparison, so formal severity should be decided between high and critical based on deployment
prevalence.

## Expected behavior

TiKV should convert JSON `I64` and `U64` directly to `Decimal`. Until exact conversion is
implemented, `CastJsonAsDecimal` should not be pushed down for integer JSON values.

## Dedup

Post-RED searches found no exact TiDB issue, TiKV issue, PR, or remote `found_bug` root.
TiDB #10461 concerns JSON floating-point comparison and does not cover `CastJsonAsDecimal`,
remote row admission, or silent DML.
