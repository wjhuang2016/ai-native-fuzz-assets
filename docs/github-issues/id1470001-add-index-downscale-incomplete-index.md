# ADD INDEX can publish an incomplete index after DDL worker downscale drops a tail-worker error

## Bug Report

### Real-world trigger at a glance (no test fault)

The easiest production-shaped trigger is an online uniqueness migration on a table with
historical duplicate business keys. For example, an `orders` table can contain two rows
with different auto-increment `id` values but the same `order_no` after an importer
timeout/retry. The team then adds the missing constraint during a release:

```sql
-- The duplicate existed before the DDL; it is not a synthetic storage error.
SELECT order_no, COUNT(*)
FROM orders
GROUP BY order_no
HAVING COUNT(*) > 1;

ALTER TABLE orders ADD UNIQUE INDEX uk_order_no(order_no);
```

To make the normal race likely, the table should be large enough for a multi-worker
backfill, the duplicate should be in a later primary-key range, and the job should use
the transaction backfill path (`tidb_ddl_enable_fast_reorg=OFF`, or ingest unavailable).
During a daytime latency spike, a DBA or resource-control automation can issue the
supported operational action:

```sql
-- Session B, after SHOW DDL JOBS shows write reorganization and a growing row_count.
ADMIN ALTER DDL JOBS <job_id> THREAD = 1;
```

The concrete in-flight event is then `batchCheckUniqueKey` seeing the two handles for
the same `order_no` and returning `kv.ErrKeyExists`, surfaced to the client as:

```text
ERROR 1062 (23000): Duplicate entry '<order_no>' for key 'orders.uk_order_no'
```

The canceled tail worker can produce this result after the downscale. A bug hit is not
the ordinary duplicate-key rollback. It is `synced/public` followed by a table-scan
versus index-scan mismatch, or `ADMIN CHECK TABLE orders` reporting `8223`:

```sql
SELECT * FROM orders IGNORE INDEX(uk_order_no)
WHERE order_no = '<duplicate-order-no>';
SELECT * FROM orders FORCE INDEX(uk_order_no)
WHERE order_no = '<duplicate-order-no>';
ADMIN CHECK TABLE orders;
```

This is a high-probability production shape, not a deterministic no-failpoint
reproducer: if the duplicate is detected before the downscale, rollback is the
correct control. The failpoint reproducer below fixes only this ordering; it does not
invent a different error class.

### 1. Minimal reproduce step (Required)

This was reproduced on current master with a test-only failpoint build.

The failpoints are only used to make the race deterministic. They inject a real backfill worker error after one worker has already finished a batch, then delay the error long enough for `ADMIN ALTER DDL JOBS ... THREAD = 1` to downscale the DDL job.

Test-only failpoint semantics:

- `github.com/pingcap/tidb/pkg/ddl/mockBackfillPostBatchErrForWorker=return(<worker-id>)`
  - after a backfill worker's `BackfillData(...)` returns, if the worker id matches, return `mock backfill post-batch error on worker <id>`
- `github.com/pingcap/tidb/pkg/ddl/mockBackfillPostBatchErrSleepMs=return(<ms>)`
  - before returning that injected post-batch error, sleep for the configured number of milliseconds

Environment/session setup:

```sql
SET GLOBAL tidb_enable_dist_task = OFF;
SET GLOBAL tidb_ddl_enable_fast_reorg = OFF;
SET GLOBAL tidb_ddl_reorg_worker_cnt = 4;

DROP DATABASE IF EXISTS ai_native_shrink_test2;
CREATE DATABASE ai_native_shrink_test2;
USE ai_native_shrink_test2;

CREATE TABLE t (
  id BIGINT PRIMARY KEY,
  a BIGINT,
  pad VARCHAR(64)
);

CREATE TEMPORARY TABLE seq(n INT PRIMARY KEY);
INSERT INTO seq VALUES (0),(1),(2),(3),(4),(5),(6),(7);

INSERT INTO t
SELECT
  s0.n + s1.n * 8 + s2.n * 64 + s3.n * 512 + s4.n * 4096 + 1 AS id,
  s0.n + s1.n * 8 + s2.n * 64 + s3.n * 512 + s4.n * 4096 + 1 AS a,
  REPEAT('x', 32)
FROM seq s0, seq s1, seq s2, seq s3, seq s4;

SPLIT TABLE t BETWEEN (0) AND (32768) REGIONS 16;
```

Enable the failpoints on the current DDL owner:

```bash
curl -X PUT "http://<ddl-owner-status-addr>/fail/github.com/pingcap/tidb/pkg/ddl/mockBackfillPostBatchErrForWorker" \
  -d 'return(3)'

curl -X PUT "http://<ddl-owner-status-addr>/fail/github.com/pingcap/tidb/pkg/ddl/mockBackfillPostBatchErrSleepMs" \
  -d 'return(10000)'
```

Session A:

```sql
USE ai_native_shrink_test2;
ALTER TABLE t ADD INDEX idx_a(a);
```

Session B, while the `ADD INDEX` job is in `write reorganization`:

```sql
ADMIN SHOW DDL JOBS;
ADMIN ALTER DDL JOBS <add-index-job-id> THREAD = 1;
```

Wait until Session A returns, then validate the table:

```sql
USE ai_native_shrink_test2;

ADMIN SHOW DDL JOBS;

SELECT COUNT(*) FROM t;
SELECT COUNT(*) FROM t IGNORE INDEX(idx_a);
SELECT COUNT(*) FROM t FORCE INDEX(idx_a);

SELECT COUNT(*) FROM t IGNORE INDEX(idx_a) WHERE a = 5676;
SELECT COUNT(*) FROM t FORCE INDEX(idx_a) WHERE a = 5676;
SELECT COUNT(*) FROM t WHERE a = 5676;

EXPLAIN FORMAT='brief' SELECT COUNT(*) FROM t;

ADMIN CHECK TABLE t;
```

Control run:

Run the same injected post-batch worker error without `ADMIN ALTER DDL JOBS ... THREAD = 1`.

In the control run, `ALTER TABLE ... ADD INDEX` returns the injected error and the job rolls back:

```text
ERROR 1105 (HY000): mock backfill post-batch error on worker 0
```

That control is important: the bug is not "an injected worker error can fail ADD INDEX". The bug is that dynamic downscale can make a removed worker's real error disappear, allowing the DDL job to publish success.

Realistic non-failpoint trigger example:

The ordinary error source can be dirty historical data, not a synthetic storage fault. A common production operation is adding a uniqueness constraint after several import jobs or application versions have written a large table. This must use the same transaction backfill path as the reproducer: for example, fast reorg is disabled for operational reasons, or the ingest path is unavailable. On the testbed, the following control used no error-injection failpoint and returned the normal duplicate-key error:

```sql
SET GLOBAL tidb_enable_dist_task = OFF;
SET GLOBAL tidb_ddl_enable_fast_reorg = OFF;
SET GLOBAL tidb_ddl_reorg_worker_cnt = 4;

CREATE DATABASE ai_native_real_duplicate;
USE ai_native_real_duplicate;

CREATE TABLE users (
  id BIGINT PRIMARY KEY,
  email VARCHAR(255) NOT NULL,
  payload VARCHAR(32)
);

-- These rows represent a duplicate left by an old import or retry path.
INSERT INTO users VALUES
  (81002344, 'a@example.com', 'import-a'),
  (81072319, 'a@example.com', 'import-b');

SELECT email, COUNT(*)
FROM users
GROUP BY email
HAVING COUNT(*) > 1;

ALTER TABLE users ADD UNIQUE INDEX uk_email(email);
-- ERROR 1062 (23000): Duplicate entry 'a@example.com' for key 'users.uk_email'
```

### More realistic non-failpoint trigger

The four-row table above only proves that a normal `ERROR 1062` can come from the transaction backfill path. It is too small: the job can finish before an operator has a chance to downscale it. The production-shaped trigger is a million-row table with a duplicate left by an import/application retry, with the duplicate in a later primary-key range. The backfill then runs long enough to have multiple workers, and an operator reduces the concurrency while the job is still active.

The important point is what the in-flight operation actually is. It is the normal unique-key check performed by `addIndexTxnWorker.BackfillData`: while processing the batch that contains the two rows below, `batchCheckUniqueKey` sees the same `email` value attached to two different handles and returns the ordinary duplicate-key error (`ErrKeyExists`, surfaced to SQL as `ERROR 1062`). This is not a TiKV outage, a panic, or a test-only error. The bug is that this result can be produced after the worker has been canceled by the downscale and then be dropped by `sendResult`.

The following SQL constructs that shape. In production, the first million rows would normally already exist:

```sql
SET GLOBAL tidb_enable_dist_task = OFF;
SET GLOBAL tidb_ddl_enable_fast_reorg = OFF;
SET GLOBAL tidb_ddl_reorg_worker_cnt = 8;

CREATE DATABASE ai_native_realistic_trigger_20260712;
USE ai_native_realistic_trigger_20260712;

CREATE TABLE users (
  id BIGINT PRIMARY KEY,
  email VARCHAR(255) NOT NULL,
  payload VARCHAR(128)
);

CREATE TEMPORARY TABLE digit(n INT PRIMARY KEY);
INSERT INTO digit VALUES (0),(1),(2),(3),(4),(5),(6),(7),(8),(9);

-- One million historical rows: long enough for multiple regions/tasks.
INSERT INTO users
SELECT
  d0.n + d1.n * 10 + d2.n * 100 + d3.n * 1000 +
  d4.n * 10000 + d5.n * 100000 + 1 AS id,
  CONCAT('user-', d0.n + d1.n * 10 + d2.n * 100 + d3.n * 1000 +
         d4.n * 10000 + d5.n * 100000 + 1, '@example.com') AS email,
  REPEAT('x', 128)
FROM digit d0, digit d1, digit d2, digit d3, digit d4, digit d5;

-- A duplicate left by an old import or retry, deliberately in a late PK range.
INSERT INTO users VALUES
  (900000001, 'dupe@example.com', REPEAT('x', 128)),
  (900100001, 'dupe@example.com', REPEAT('x', 128));

SPLIT TABLE users BETWEEN (0) AND (1000000000) REGIONS 32;
```

Session A starts the ordinary online operation:

```sql
USE ai_native_realistic_trigger_20260712;
ALTER TABLE users ADD UNIQUE INDEX uk_email(email);
```

In Session B, poll `ADMIN SHOW DDL JOBS`. While the job is still in `write reorganization` and `row_count` is increasing, reduce the job concurrency once:

```sql
ADMIN SHOW DDL JOBS;
ADMIN ALTER DDL JOBS <job_id> THREAD = 1;
```

The required timing is concrete:

1. the worker handling the later primary-key range is still inside the transaction backfill;
2. that range contains the ordinary duplicate `dupe@example.com`;
3. a DBA or resource-control automation temporarily reduces the job from 8 workers to 1 to protect foreground latency, so the busy worker is in the canceled tail;
4. `batchCheckUniqueKey` compares the two handles for `dupe@example.com` and returns the normal `ERROR 1062` condition while that worker's batch is in flight;
5. `sendResult` can choose the canceled-context branch and drop that terminal result instead of delivering it to the collector;
6. the parent collector sees no worker error and can publish the index as if all ranges succeeded.

After the job finishes, do not validate only the `ALTER TABLE` return. Run:

```sql
ADMIN SHOW DDL JOBS;
ADMIN CHECK TABLE users;

SELECT COUNT(*) FROM users IGNORE INDEX(uk_email);
SELECT COUNT(*) FROM users FORCE INDEX(uk_email);
SELECT COUNT(*) FROM users IGNORE INDEX(uk_email)
  WHERE email = 'dupe@example.com';
SELECT COUNT(*) FROM users FORCE INDEX(uk_email)
  WHERE email = 'dupe@example.com';
```

The correct result is `ERROR 1062` followed by rollback, with no published `uk_email`. A bug hit is a `synced/public` job followed by inconsistent table-scan versus `FORCE INDEX(uk_email)` results, or `ADMIN CHECK TABLE` reporting `8223`. This needs no TiKV failure and no test-only failpoint; it only combines dirty historical data, an online unique-index build, and the supported operational action of temporarily reducing DDL concurrency during resource pressure. If the job detects the duplicate before the downscale and rolls back, that is the expected control, not a bug; the large table and late duplicate are what widen the race window.

#### Concrete production-shaped example

For example, an `orders` table has been written by two application versions. The old importer retried a batch after a timeout, so the same business `order_no` exists under two different auto-increment `id` values. The team wants to enforce the invariant before the next release:

```sql
-- The duplicate is a data-quality problem that already exists before the DDL.
SELECT order_no, COUNT(*)
FROM orders
GROUP BY order_no
HAVING COUNT(*) > 1;

ALTER TABLE orders ADD UNIQUE INDEX uk_order_no(order_no);
```

The table is large enough that this is an online operation rather than a short metadata change. During a daytime traffic spike, TiDB latency rises and the DBA or resource-control automation uses the supported DDL control statement to reduce background work:

```sql
-- Session B: issued after SHOW DDL JOBS reports write reorganization and a growing row_count.
ADMIN ALTER DDL JOBS <job_id> THREAD = 1;
```

To make the natural race more likely, the duplicate `order_no` should be in a later primary-key range, while earlier ranges have already been processed. The likely sequence is then:

1. the `ADD UNIQUE INDEX` job has several txn backfill workers;
2. the worker covering the late range is still processing the batch containing the two `order_no` rows;
3. the supported downscale cancels that worker because it is in the removed tail;
4. the normal unique-key check returns `Duplicate entry '<order_no>'` after the cancellation;
5. the canceled worker loses the terminal result at `sendResult`, and the parent can publish a partial `uk_order_no`.

The production symptom is not merely an `ALTER TABLE` error. The job may report `synced/public`, while a point lookup or a normal plan using `uk_order_no` misses one of the pre-existing rows. Validate both paths explicitly:

```sql
SELECT COUNT(*) FROM orders IGNORE INDEX(uk_order_no);
SELECT COUNT(*) FROM orders FORCE INDEX(uk_order_no);
SELECT * FROM orders IGNORE INDEX(uk_order_no) WHERE order_no = '<duplicate-order-no>';
SELECT * FROM orders FORCE INDEX(uk_order_no) WHERE order_no = '<duplicate-order-no>';
ADMIN CHECK TABLE orders;
```

This example is a high-probability production shape, not a deterministic no-failpoint reproducer: the duplicate may be detected before the downscale, in which case rollback is correct. The failpoint reproducer is still required to prove the specific ordering and to avoid confusing a normal duplicate-key rollback with this silent-publish bug.

This non-failpoint scenario remains timing-sensitive and is not a deterministic replacement for the failpoint reproducer. The failpoint fixes the same ordering without inventing a new error class: it holds the worker so the real terminal error arrives after downscale cancellation.

The same result-delivery window can also be entered by a real non-duplicate TiKV/transaction error from a non-unique `ADD INDEX`; the unique-index example is easier to explain because the error source is ordinary application data.

The failpoint reproduction above makes this timing deterministic. It does not invent a fake semantic class of error; it only holds the real worker at the exact point where a production duplicate-key result can arrive after downscale cancellation.

### 2. What did you expect to see? (Required)

The DDL job must not publish a partial index after any in-flight backfill worker returns a real error.

Expected behavior:

- the removed worker's terminal result/error is still collected, or
- the DDL job rolls back/retries, and
- `idx_a` is not published unless all required rows have been indexed.

At minimum, `ADMIN CHECK TABLE t`, table scan, index scan, and ordinary query results must stay consistent after the DDL finishes.

### 3. What did you see instead (Required)

The `ADD INDEX` job reached `synced/public`, but the published index was missing rows.

Observed live result:

```sql
SELECT COUNT(*) FROM t;                     -- 30301
SELECT COUNT(*) FROM t IGNORE INDEX(idx_a); -- 32768
SELECT COUNT(*) FROM t FORCE INDEX(idx_a);  -- 30301

ADMIN CHECK TABLE t;
-- ERROR 8223 (HY000): data inconsistency in table: t, index: idx_a, handle: 5676, ...
```

Concrete witness:

```sql
SELECT COUNT(*) FROM t IGNORE INDEX(idx_a) WHERE a = 5676; -- 1
SELECT COUNT(*) FROM t FORCE INDEX(idx_a)  WHERE a = 5676; -- 0
SELECT COUNT(*) FROM t WHERE a = 5676;                     -- 0
```

The ordinary query can already choose the bad index:

```sql
EXPLAIN FORMAT='brief' SELECT COUNT(*) FROM t;
-- IndexFullScan(idx_a)
```

The owner log showed this sequence:

```text
adjust ddl job config success    current worker count=1
mock backfill post-batch error injected workerID=3 taskID=2
backfill worker exit on error    worker 3
backfill workers successfully processed total added count=30269
run reorg job done               jobID=4452 handled rows=30269
finish DDL job                   state=synced/public
```

The missing signal is also important: unlike the control run, the failing run did not log the normal parent-side `backfill worker failed` handling before publishing success.

### 4. What is your TiDB version? (Required)

Reproduced on a current-master test build based on:

```text
Git commit: 13282a8bd06bd33324a4dbfd3c1c03685f3cd9aa
```

The binary included only test-only failpoints to make the post-batch worker error and timing deterministic.

### Suspected root cause

The current evidence points to a result-delivery race during DDL worker downscale.

`txnBackfillExecutor.adjustWorkerSize()` keeps the worker slice prefix when shrinking the worker count and cancels the tail workers. If the busy worker is in the canceled tail, it can still produce a real post-batch error after cancellation.

Then `backfillWorker.sendResult(...)` can drop that result if the worker context is already canceled:

```go
select {
case <-w.ctx.Done():
case w.resultCh <- result:
}
```

If the `<-w.ctx.Done()` arm wins, the parent result collector never sees the worker error. It then observes a clean channel close and treats the partial backfill as successfully processed, allowing the DDL job to publish an incomplete index.

Found by AI-assisted testing.
