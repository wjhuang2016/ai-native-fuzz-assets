# Pessimistic RC retry can commit a stale scalar aggregate with fresh DML input

Status: confirmed on current TiDB source and a one-TiDB/three-TiKV testbed. Proposed severity is
major/high because the statement and COMMIT succeed while durable business values are silently
wrong. No exact upstream issue was found after the independent RED.

## Production trigger card

- `workload`: a pessimistic READ COMMITTED batch UPDATE joins a configuration or allocation table
  to choose a unique routing value and also stores a non-correlated scalar aggregate, such as a
  ledger total, inventory total, or account balance snapshot.
- `natural producer`: another allocator commits a new business row that claims the old route and is
  included by the aggregate, then advances the configuration to the next route. A large batch scan,
  cold/hot Region, storage backoff, CPU work, or expression evaluation lets that commit land before
  the first attempt reaches final pessimistic locking.
- `ordering`: A evaluates scalar at RC statement TS `t1` < B claims route 200, inserts value 999,
  advances config to 300, and commits < A locks the first-attempt route 200 and receives a retryable
  conflict < TiDB refreshes statement TS to `t2` < executor reads config 300 but plan retains scalar
  30 < UPDATE success < COMMIT success.
- `settings`: MDL ON, default pessimistic transaction mode, default
  `pessimistic-txn.max-retry-count=256`, and ordinary one-phase/async-commit settings. READ COMMITTED
  is common but not TiDB's default RR isolation. No failpoint, DDL, node failure, or TiKV tuning is
  required.
- `topology`: one TiDB, three TiKV, two SQL sessions; all components remain healthy.
- `durable consequence`: fresh reads show a new route with an old aggregate; `ADMIN CHECK TABLE`
  passes because this is logical corruption rather than physical row/index divergence.
- `control`: remove the unique conflict while keeping B's commit after statement start. The same RC
  statement has zero retries and reads old scalar/old config. Run after B and it reads new/new.

`SLEEP` below only widens the interval that production obtains from scan and storage latency.

## Reproduction

Create three tables:

```sql
DROP DATABASE IF EXISTS ai_txn_retry_scalar_ghost;
CREATE DATABASE ai_txn_retry_scalar_ghost;
USE ai_txn_retry_scalar_ghost;

CREATE TABLE target(id INT PRIMARY KEY, u INT NOT NULL UNIQUE, v INT NOT NULL);
CREATE TABLE control LIKE target;
CREATE TABLE src(id INT PRIMARY KEY, next_u INT NOT NULL);
INSERT INTO src VALUES(1,200);
INSERT INTO target VALUES(1,10,10),(2,20,20);
INSERT INTO control VALUES(1,10,10),(2,20,20),(3,200,999);
SELECT @@global.tidb_enable_metadata_lock;
```

Session A:

```sql
SET transaction_isolation='READ-COMMITTED';
SET tidb_txn_mode='pessimistic';
BEGIN PESSIMISTIC;

UPDATE target d
JOIN src ON src.id=1
SET d.u=IF(d.id=1,100,src.next_u+SLEEP(20)*0),
    d.v=(SELECT SUM(s.v) FROM target s)+d.id
WHERE d.id IN (1,2);
```

While A is in the first attempt, session B commits:

```sql
BEGIN;
UPDATE src SET next_u=300 WHERE id=1;
INSERT target VALUES(3,200,999);
COMMIT;
```

After A's UPDATE returns:

```sql
COMMIT;
SELECT * FROM target ORDER BY id;

UPDATE control d
JOIN src ON src.id=1
SET d.u=IF(d.id=1,100,src.next_u),
    d.v=(SELECT SUM(s.v) FROM control s)+d.id
WHERE d.id IN (1,2);
SELECT * FROM control ORDER BY id;
ADMIN CHECK TABLE target;
```

Observed on testbed `8196300`:

```text
UPDATE affected rows = 2; COMMIT = success
target:  (1,100,31),   (2,300,32),   (3,200,999)
control: (1,100,1030), (2,300,1031), (3,200,999)
ADMIN CHECK TABLE target = success
```

The reusable Go probe is
`scaffolds/go-probes/txn_retry_scalar_subquery_ghost_probe.go`. The accompanying allowed-outcome
probe removes the target-key conflict and observes old/old with no retry.

## Source chain

- `pkg/planner/core/expression_rewriter.go:1602-1629` registers the scalar subquery, executes it with
  `EvalSubqueryFirstRow`, and embeds each result as `expression.Constant` with `SubqueryRefID`.
- `pkg/sessiontxn/isolation/readcommitted.go:64-89` assigns both RC statement read and for-update TS
  acquisition to `getStmtTS`; `OnStmtRetry` at 131-141 clears the old statement generation.
- `pkg/sessiontxn/isolation/readcommitted.go:180-199` caches one `stmtTS` and installs it as the TiKV
  snapshot TS for one attempt.
- `pkg/executor/adapter.go:1437-1469` accepts the retry, refreshes statement state, and calls
  `buildExecutor` from the existing plan. It does not call `RebuildPlan`.

## Counterfactual

After rolling back the failed attempt, rebuilding the plan before rebuilding the executor makes the
same test pass. The slow log still reports `Exec_retry_count=1`, while `plan_cnt` changes from 1 to 2
and the durable rows become new/new. This proves the stale owner is the preprocessed constant in the
reused plan; it is not proposed as the final performance-sensitive fix.

## Version

- Real TiKV: TiDB `d573e284da773c820c1c313105b73d587378381b`, TiKV
  `67fccdb16f5517e96a53c968879f8e5d99bcf1b3` x3.
- Local RED/GREEN: TiDB `531e40cd25989404f9fd1f51cddf278326983af1`.
- The relevant executor, expression-rewriter, and RC-provider files are identical between those two
  TiDB commits.
