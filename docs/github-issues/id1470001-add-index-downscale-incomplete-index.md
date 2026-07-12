# ADD INDEX can publish an incomplete index after DDL worker downscale drops a tail-worker error

## Bug Report

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

A production cluster is adding a UNIQUE index to a large table after historical data cleanup, and the target column still contains an unexpected duplicate in a later key range.

```sql
ALTER TABLE users ADD UNIQUE INDEX uk_email(email);
```

The job starts with multiple DDL reorg workers, for example `tidb_ddl_reorg_worker_cnt = 8` or higher. While the backfill is in `write reorganization`, the cluster load becomes visible, so an operator reduces the running DDL job's thread count:

```sql
ADMIN SHOW DDL JOBS;
ADMIN ALTER DDL JOBS <add-unique-index-job-id> THREAD = 1;
```

If one of the removed tail workers is processing the range that contains the duplicate value, `batchCheckUniqueKey(...)` or `index.Create(...)` can return the real duplicate-key error after that worker has already been canceled by the downscale. That error is load-bearing: the ADD UNIQUE INDEX job should fail or roll back. In the buggy path, the canceled worker's result can be dropped, and the parent may continue as if all workers succeeded.

The same race can also be hit by non-unique ADD INDEX if the removed tail worker returns a real storage/transaction error during scan, lock, write, or commit. The failpoint reproduction above makes this timing deterministic; it is not relying on a fake semantic class of error.

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
