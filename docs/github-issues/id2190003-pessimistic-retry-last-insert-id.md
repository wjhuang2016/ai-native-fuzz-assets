## Bug Report

### 1. Minimal reproduce step

Create the tables:

```sql
DROP DATABASE IF EXISTS pessimistic_last_id_retry;
CREATE DATABASE pessimistic_last_id_retry;
USE pessimistic_last_id_retry;

CREATE TABLE t(id INT PRIMARY KEY, u INT UNIQUE, v BIGINT);
CREATE TABLE gate(id INT PRIMARY KEY);
CREATE TABLE sink(v BIGINT);
INSERT INTO t VALUES (1, 10, 0);
```

Start the following statements in session A. The `SLEEP(20)` opens the concurrency window:

```sql
USE pessimistic_last_id_retry;
SET tidb_txn_mode = 'pessimistic';
SET tx_isolation = 'READ-COMMITTED';
SET tidb_pessimistic_txn_fair_locking = OFF;
SELECT LAST_INSERT_ID(7);
BEGIN;

UPDATE t AS x
SET u = 1, v = LAST_INSERT_ID(99 + SLEEP(20))
WHERE id = 1
  AND NOT EXISTS (SELECT 1 FROM gate AS g WHERE g.id = x.id);
```

While session A is sleeping, run session B:

```sql
USE pessimistic_last_id_retry;
BEGIN;
INSERT INTO t VALUES (2, 1, 0);
INSERT INTO gate VALUES (1);
COMMIT;
```

After session A's UPDATE returns, continue in session A:

```sql
SELECT ROW_COUNT() AS affected, LAST_INSERT_ID() AS published_after_update;
COMMIT;
INSERT INTO sink VALUES (LAST_INSERT_ID());

SELECT id, u, v FROM t ORDER BY id;
SELECT * FROM gate;
SELECT * FROM sink;
```

This reproduces without failpoints on a real TiKV cluster.

### 2. What did you expect to see?

The successful retry sees the newly committed gate and matches zero rows. It never executes
`LAST_INSERT_ID(99)`, so transparent retry should preserve the statement-entry value `7`:

```text
affected=0
published_after_update=7
sink=(7)
```

This is also what happens when the same gate and unique-key rows already exist before session A
starts: the zero-match UPDATE keeps and persists `7`.

### 3. What did you see instead?

Session A returned success and reported zero matched/changed rows, but published and persisted the
value from its rolled-back attempt:

```text
affected  published_after_update
0         99

t:    (1,10,0), (2,1,0)
gate: (1)
sink: (99)
```

`LAST_INSERT_ID(expr)` writes `StatementContext.LastInsertID` and `LastInsertIDSet` during
expression evaluation. The later unique-key lock conflict is accepted for transparent pessimistic
statement retry. `handlePessimisticLockError` rolls back statement KV changes, rebuilds the
executor, and calls `StatementContext.ResetForRetry()`, but that reset does not clear either field.

The rebuilt READ COMMITTED executor sees `gate(1)` and matches zero rows, so no successful-attempt
setter overwrites `99`. Statement completion then publishes it, and the next INSERT makes the
wrong value durable.

A focused natural-conflict unit test observed one retry after the first evaluation and the same
`business row unchanged / LAST_INSERT_ID=99 / sink=99` result. Clearing `LastInsertID` and
`LastInsertIDSet` in `ResetForRetry()` made the exact schedule return and persist `7`; existing
`TestLastInsertID` expression tests also passed.

### 4. What is your TiDB version?

- Current master: `13282a8bd06bd33324a4dbfd3c1c03685f3cd9aa`
- SQL-only real-TiKV reproduction:
  `8.0.11-TiDB-v9.0.0-beta.2.pre-1895-g5c9198e948`
