# TiKV JSON-to-CHAR pushdown can silently delete a row after TiDB would abort

Status: confirmed on official nightly and current TiKV master; not filed upstream.

## Summary

TiDB applies the declared length in `CAST(json_col AS CHAR(n))`; TiKV returns the full serialized
JSON value. Under default strict mode, root TiDB raises `ERROR 1406 Data Too Long` during DML.
The pushed TiKV predicate neither truncates nor raises the error.

As a result, the same `DELETE` can silently remove rows when pushed into TiKV and abort without
changing any row when evaluated in TiDB.

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

No concurrency, retry, failpoint, source patch, pause, or node/network/disk fault is needed.

## Minimal reproduction

Create identical tables:

```sql
CREATE TABLE push_t(id INT PRIMARY KEY,j JSON);
CREATE TABLE root_t LIKE push_t;

INSERT INTO push_t VALUES
  (1,CAST('1234.5' AS JSON)),
  (2,CAST('1234' AS JSON)),
  (3,CAST('12' AS JSON));
INSERT INTO root_t SELECT * FROM push_t;
```

Compare row membership:

```sql
SELECT GROUP_CONCAT(id ORDER BY id)
FROM push_t
WHERE CAST(j AS CHAR(4))<>'1234';
-- 1,3

SELECT GROUP_CONCAT(id ORDER BY id)
FROM root_t
WHERE IF(SLEEP(0)=0,CAST(j AS CHAR(4))<>'1234',NULL);
-- 3
```

`SLEEP(0)` is a zero-delay barrier that keeps the full predicate in TiDB. `EXPLAIN` confirms that
the ordinary predicate is a `cop[tikv] Selection`.

The pushed query proves its own row-set error:

```sql
SELECT id,j,CAST(j AS CHAR(4)) AS cast_value,
       CAST(j AS CHAR(4))<>'1234' AS predicate_holds
FROM push_t
WHERE CAST(j AS CHAR(4))<>'1234'
ORDER BY id;
```

```text
id  j       cast_value  predicate_holds
1   1234.5  1234        0
3   12      12          1
```

## Silent wrong deletion

```sql
DELETE FROM push_t WHERE CAST(j AS CHAR(4))<>'1234';
-- success, 2 rows deleted; only id=2 remains
```

Matched root control:

```sql
DELETE FROM root_t
WHERE IF(SLEEP(0)=0,CAST(j AS CHAR(4))<>'1234',NULL);
-- ERROR 1406 (22001): Data Too Long, field len 4, data len 6

SELECT GROUP_CONCAT(id ORDER BY id) FROM root_t;
-- 1,2,3
```

The under-length value `12` is an embedded GREEN. Both engines agree that it satisfies the
predicate. The exact-length value `1234` is another GREEN.

## Current-master confirmation

TiKV current master was tested by temporarily asserting that `cast_json_as_bytes(1234.5)` must
produce the `CHAR(4)` value `1234`. The existing function returned all six bytes:

```text
left:  Some([49, 50, 51, 52, 46, 53])  # 1234.5
right: Some([49, 50, 51, 52])          # 1234
```

The probe was removed. The nightly-to-master range contains no relevant cast change.

## Root cause

TiDB's `builtinCastJSONAsStringSig` serializes the JSON and calls:

```go
types.ProduceStrWithSpecifiedTp(val.String(), b.tp, typeCtx(ctx), false)
```

This applies `flen`, charset, and truncation policy.

TiKV's `cast_json_as_bytes` captures only `EvalContext` and returns `JsonRef.convert(ctx)`. The
`ConvertTo<String> for JsonRef` implementation explicitly notes that TiDB has an additional
`ProduceStrWithSpecifiedTp` step. The remote function has no `RpnFnCallExtra` or return `FieldType`,
so it cannot enforce `CHAR(n)`.

## Production trigger

A cleanup or state-transition query extracts JSON data by casting it to a bounded character field:

```sql
DELETE FROM events
WHERE CAST(payload->'$.code' AS CHAR(4)) <> '1234';
```

When a producer begins sending `1234.5`, TiDB semantics truncate it to `1234` and strict DML
rejects the overlong value. TiKV instead compares the full text, selects the row, and deletes it.
The boundary applies to JSON numbers, strings, arrays, and objects whose serialized length exceeds
the declared `CHAR(n)`.

The consequence is direct silent data loss under default settings. The SQL pattern still requires
an explicit undersized JSON cast, so the catalog severity remains high rather than critical.

## Expected behavior

Pushdown must preserve return field length and strict truncation behavior. TiKV should receive the
full return `FieldType`, apply the same string-production routine, and propagate the same error.
Until that is implemented, `CastJsonAsString` should not be pushed down.

## Dedup

Post-RED searches of TiDB issues, TiKV issues, and the remote bug library found no exact
JSON-to-CHAR return-context omission root.
