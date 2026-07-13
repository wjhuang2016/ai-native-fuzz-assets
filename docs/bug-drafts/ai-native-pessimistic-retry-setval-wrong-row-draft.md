# Pessimistic retry can persist NULL instead of SETVAL's documented result

Status: confirmed on current source and real TiKV; filed as
[TiDB #69822](https://github.com/pingcap/tidb/issues/69822); remote `found_bug id2370003`;
high consequence, low trigger frequency.

## Summary

A pessimistic READ COMMITTED `UPDATE` can evaluate `SETVAL(seq, 100)`, encounter a retryable unique
key conflict, and transparently rebuild the statement. The hidden first attempt advances the
sequence and returns `100`. The successful attempt executes the same expression again; because the
sequence is already at 100, `SETVAL` returns `NULL`. TiDB reports success and commits `NULL` into the
row.

A run that starts directly from the state seen by the successful attempt commits `100`. Both runs
leave the sequence at the same next value, 101. The contradiction is therefore a persistent row
value changed by hidden retry, not an expected sequence gap.

## P/Q/F

- **P**: `StmtRollback`, `StmtCtx.ResetForRetry`, and `RetryInfo.ResetOffset` make a rebuilt
  pessimistic statement safe to execute again.
- **Q**: re-executing every expression is observationally equivalent to one execution from the
  state seen by the successful attempt.
- **F**: `SETVAL` changes the table-level sequence owner immediately. Its return value depends on
  that owner, which is not restored or journaled. The failed attempt therefore feeds back into the
  successful attempt's durable row image.

## Minimal reproduction

Metadata locking remains enabled. Create the objects:

```sql
DROP DATABASE IF EXISTS pessimistic_setval_retry;
CREATE DATABASE pessimistic_setval_retry;
USE pessimistic_setval_retry;

CREATE SEQUENCE retry_seq START WITH 1 INCREMENT BY 1;
CREATE SEQUENCE control_seq START WITH 1 INCREMENT BY 1;
CREATE TABLE src(id INT PRIMARY KEY, next_u INT);
CREATE TABLE retry_dst(id INT PRIMARY KEY, u INT UNIQUE, v BIGINT NULL);
CREATE TABLE control_dst LIKE retry_dst;
INSERT INTO src VALUES (1, 1);
INSERT INTO retry_dst VALUES (1, 10, 0);
INSERT INTO control_dst VALUES (1, 10, 0);
SELECT @@global.tidb_enable_metadata_lock;
-- 1
```

Session A:

```sql
USE pessimistic_setval_retry;
SET tidb_txn_mode = 'pessimistic';
SET transaction_isolation = 'READ-COMMITTED';
BEGIN;

UPDATE retry_dst AS d JOIN src AS s ON s.id = d.id
SET d.u = s.next_u,
    d.v = SETVAL(retry_seq, 100) + SLEEP(20)
WHERE d.id = 1;
```

While A is sleeping, session B:

```sql
USE pessimistic_setval_retry;
SET tidb_txn_mode = 'pessimistic';
SET transaction_isolation = 'READ-COMMITTED';
BEGIN;
INSERT INTO retry_dst VALUES (2, 1, 0);
UPDATE src SET next_u = 2 WHERE id = 1;
COMMIT;
```

After A returns:

```sql
COMMIT;
SELECT * FROM retry_dst WHERE id = 1;
SELECT NEXTVAL(retry_seq);
```

Run the no-retry control from the same final source state:

```sql
UPDATE control_dst AS d JOIN src AS s ON s.id = d.id
SET d.u = s.next_u,
    d.v = SETVAL(control_seq, 100)
WHERE d.id = 1;
SELECT * FROM control_dst WHERE id = 1;
SELECT NEXTVAL(control_seq);
```

## Result

```text
retry UPDATE:     success, affected=1, Exec_retry_count=1
retry row:        1,2,NULL
retry nextval:    101

control row:      1,2,100
control nextval:  101
```

The testbed slow log records `Exec_retry_count=1`, `Exec_retry_time=20.005`, `Succ=1`, and an
explicit transaction. The official sequence contract says `SETVAL(seq, 100)` returns 100 when it
sets the current value. No exact TiDB issue was found after the local RED; the finding was filed as
[TiDB #69822](https://github.com/pingcap/tidb/issues/69822).

## Source ownership

- `pkg/expression/builtin_info.go`: `builtinSetValSig.evalInt` calls `SetSequenceVal` immediately.
- `pkg/table/tables/tables.go`: `SetSequenceVal` changes the sequence cache or persistent allocator;
  a repeated value at or below the base returns `NULL`.
- `pkg/executor/adapter.go`: `handlePessimisticLockError` rebuilds the executor and restores only
  statement/KV retry state before executing every expression again.

## Counterfactual

Record that an attempt successfully changed a sequence through `SETVAL`. If a retryable
pessimistic lock error occurs afterward, return the original conflict instead of transparently
re-executing the statement. Under that single gate, the same test returns an error and leaves the
target row unchanged. A more general fix can reject hidden retry for any expression whose result
depends on an external mutation made by the same attempt.

## Versions

- Current-source local RED: TiDB `b8d04e17a2ca61eee1220c5ce2d641a376f75e9b`.
- Real-TiKV SQL-only RED: TiDB `5c9198e9484db852b8477ce0014e0422ff9ec6a9`, testbed 8220955.
- Metadata locking: default enabled throughout.
