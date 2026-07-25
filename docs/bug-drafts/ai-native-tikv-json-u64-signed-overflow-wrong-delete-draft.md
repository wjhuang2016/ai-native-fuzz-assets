# TiKV can turn JSON-to-SIGNED overflow into negative values and silently delete rows

Status: confirmed on current TiDB and TiKV master with recent real TiKV. No exact upstream issue or
internal root was found.

## Summary

TiDB and TiKV implement `CAST(JSON AS SIGNED)` differently for JSON unsigned integers above
`MaxInt64`.

- TiDB performs a bounded conversion. A `SELECT` returns `MaxInt64` with warning 1690; a strict
  `DELETE` returns error 1690 before changing data.
- TiKV uses Rust `u64 as i64`. Values above `MaxInt64` wrap into ordinary negative integers without
  invoking the evaluation context.

Predicate pushdown therefore changes both row membership and the statement terminal. An ordinary
`DELETE` that should fail can succeed and permanently delete rows. `ADMIN CHECK TABLE` remains
green because the wrong rows were removed consistently.

## Production trigger

A common production shape is an event table containing external identifiers in JSON:

```json
{"account_id": 18446744073709551615, "kind": "external"}
```

The trigger is:

1. A JSON number is encoded as `UNSIGNED INTEGER`, with a value in
   `[9223372036854775808, 18446744073709551615]`.
2. A filter explicitly converts that value to `SIGNED`.
3. The filter is pushed to TiKV, which is the default plan for a table scan.
4. `DELETE`, `UPDATE`, or `INSERT ... SELECT` consumes the remote row set.

For example, a cleanup intended to reject or find invalid negative IDs can delete valid external
IDs:

```sql
DELETE FROM events
WHERE CAST(JSON_EXTRACT(payload, '$.account_id') AS SIGNED) < 0;
```

No failpoint, concurrency, retry, process restart, network or disk fault, disabled MDL, unusual
transaction isolation, or nondefault SQL mode is required. The SQL pattern and full unsigned
64-bit JSON values are specific, but they are plausible for data imported from unsigned-ID systems.

## Reproduction

Environment:

```text
TiDB master: 05b396fb6636f73b3bc06b09107cf43f2c725c35
TiDB nightly: ed2376acc6e0feeff9f3e2c38db489727933aa80
real TiKV: 730be34f959185c934b7d3db730ca1dbeb3949f8
TiKV source master: 91ccfb212677a43fd5255183ccf2afa4e3cec23e
sql_mode: default STRICT_TRANS_TABLES
MDL: ON
fault injection: none
```

Create matched tables:

```sql
CREATE TABLE events_push (
  event_id BIGINT PRIMARY KEY,
  payload JSON NOT NULL,
  preimage VARCHAR(64) NOT NULL
);

INSERT INTO events_push VALUES
  (101, '{"account_id": 42, "kind": "normal"}', 'original-normal'),
  (102, '{"account_id": 9223372036854775808, "kind": "external"}',
        'original-large-id'),
  (103, '{"account_id": 18446744073709551615, "kind": "external"}',
        'original-max-id');

CREATE TABLE events_root LIKE events_push;
INSERT INTO events_root SELECT * FROM events_push;
```

The normal plan contains:

```text
Selection ... cop[tikv]
lt(cast(json_extract(payload, "$.account_id"), bigint BINARY), 0)
```

Run the normal statement:

```sql
DELETE FROM events_push
WHERE CAST(JSON_EXTRACT(payload, '$.account_id') AS SIGNED) < 0;
SELECT ROW_COUNT();
```

Result:

```text
ROW_COUNT() = 2
remaining event_id = 101
ADMIN CHECK TABLE events_push = success
```

Force the same predicate to the TiDB root with a false non-pushable disjunct:

```sql
DELETE FROM events_root
WHERE CAST(JSON_EXTRACT(payload, '$.account_id') AS SIGNED) < 0
   OR RAND() < 0;
```

Result:

```text
ERROR 1690 (22003): constant 9223372036854775808 overflows bigint
ROW_COUNT() = 0
remaining event_id = 101,102,103
```

The root barrier does not alter the predicate's accepted result set: `RAND()` is always in `[0,1)`,
so `RAND() < 0` is false. `EXPLAIN` confirms that only execution altitude changes.

## Boundary matrix

| JSON value | JSON type | TiDB root SIGNED | TiKV SIGNED | Pushed `< 0` |
| --- | --- | ---: | ---: | --- |
| `9223372036854775807` | INTEGER | `9223372036854775807` | same | false |
| `9223372036854775808` | UNSIGNED INTEGER | overflow / capped | `-9223372036854775808` | true |
| `18446744073709551614` | UNSIGNED INTEGER | overflow / capped | `-2` | true |
| `18446744073709551615` | UNSIGNED INTEGER | overflow / capped | `-1` | true |
| `-1` | INTEGER | `-1` | `-1` | true |

The distinguishing domain is derived directly from the source branch and integer boundary; random
JSON generation is unnecessary.

## Root cause

TiDB's `ConvertJSONToInt` handles `JSONTypeCodeUint64` with `ConvertUintToInt`, which enforces the
signed upper bound and routes overflow through statement error handling.

TiKV's `ToInt for JsonRef` instead contains:

```rust
JsonType::U64 => Ok(self.get_u64() as i64),
```

Rust's cast wraps the high bit into the signed domain. The later `i64.to_int` sees an already valid
negative value, so the original overflow is unrecoverable. The remote evaluator returns a row set
instead of the strict-DML error required by TiDB.

## Current-master and counterfactual proof

A focused test added temporarily to TiKV master `91ccfb2126` expected:

- warning mode: `u64::MAX -> i64::MAX` plus one overflow warning;
- strict mode: error code 1690.

Current source was RED with actual value `-1`.

Changing only the U64 branch to:

```rust
JsonType::U64 => self.get_u64().to_int(ctx, tp),
```

made both cells GREEN. The test and change were removed afterward; both TiDB and TiKV worktrees are
clean.

## Expected behavior

A pushed expression must preserve the TiDB evaluator's value, warning/error policy, and row
membership. Under strict DML, an overflowing JSON-to-SIGNED conversion must abort before any row is
modified.

## Fix direction

Reuse TiKV's existing bounded `u64::to_int(ctx, tp)` conversion instead of `as i64`. Add conformance
tests for every JSON numeric variant under both warning and strict evaluation flags, then exercise
the expression under pushed `Selection` and persistent DML.

## Impact and severity

This is direct, successful, persistent data loss under default configuration. The catalog records
it as `high`, with critical-class consequence. Trigger frequency is lower than a generic numeric
predicate because it requires a JSON unsigned integer above `MaxInt64` and an explicit SIGNED cast,
but no timing or infrastructure failure is involved.
