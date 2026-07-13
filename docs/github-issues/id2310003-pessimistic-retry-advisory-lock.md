## Bug Report

### 1. Minimal reproduce step

Create the tables. Metadata locking is enabled throughout this reproduction.

```sql
DROP DATABASE IF EXISTS pessimistic_advisory_retry;
CREATE DATABASE pessimistic_advisory_retry;
USE pessimistic_advisory_retry;

CREATE TABLE t(id INT PRIMARY KEY, u INT UNIQUE, v BIGINT);
CREATE TABLE gate(id INT PRIMARY KEY);
INSERT INTO t VALUES (1, 10, 0);

SELECT @@global.tidb_enable_metadata_lock;
-- 1
```

Start the following statements in session A. `SLEEP(20)` opens the concurrency window:

```sql
USE pessimistic_advisory_retry;
SET tidb_txn_mode = 'pessimistic';
SET tx_isolation = 'READ-COMMITTED';
BEGIN;

UPDATE t AS x
SET u = 1,
    v = GET_LOCK(CONCAT('retry_job_', x.id), 0) + SLEEP(20)
WHERE id = 1
  AND NOT EXISTS (SELECT 1 FROM gate AS g WHERE g.id = x.id);
```

While session A is sleeping, run session B:

```sql
USE pessimistic_advisory_retry;
BEGIN;
INSERT INTO t VALUES (2, 1, 0);
INSERT INTO gate VALUES (1);
COMMIT;
```

After session A's UPDATE returns, continue in session A and keep the connection open:

```sql
SELECT ROW_COUNT() AS affected;
COMMIT;
SELECT CONNECTION_ID() AS owner_conn,
       IS_USED_LOCK('retry_job_1') AS lock_owner;
SELECT * FROM t ORDER BY id;
```

In session C:

```sql
SELECT GET_LOCK('retry_job_1', 0) AS competitor;
```

This reproduces without failpoints on a real TiKV cluster. The slow log for the UPDATE records
`Exec_retry_count: 1` and `Succ: true`.

### 2. What did you expect to see?

The successful retry sees `gate(1)` and matches zero rows. It does not evaluate the row-dependent
`GET_LOCK`, so it should not leave an advisory lock behind:

```text
affected=0
lock_owner=NULL
competitor=1
```

This is what happens in the control where `(2,1,0)` and `gate(1)` exist before session A starts.

### 3. What did you see instead?

The UPDATE returned success with zero matched/changed rows, but the lock belonged to session A and
the competitor was denied:

```text
affected=0
owner_conn=3397407484
lock_owner=3397407484
competitor=0

t: (1,10,0), (2,1,0)
```

Calling `RELEASE_LOCK('retry_job_1')` once in session A changed the competing result to `1`.

`GET_LOCK` creates an entry in `session.advisoryLocks` backed by a dedicated internal pessimistic
transaction. The later unique-key conflict enters transparent pessimistic statement retry.
`handlePessimisticLockError` rebuilds the executor, calls `StmtRollback`, and resets statement
context, but none of those owners restore the advisory-lock map or close its internal transaction.
The rebuilt RC executor sees the gate and performs zero work, so the failed-attempt capability
survives the successful statement.

### 4. What is your TiDB version?

- Current master: `5c9198e9484db852b8477ce0014e0422ff9ec6a9`
- SQL-only real-TiKV reproduction:
  `8.0.11-TiDB-v9.0.0-beta.2.pre-1895-g5c9198e948`

