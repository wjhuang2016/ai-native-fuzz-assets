## Bug Report

### 1. Minimal reproduce step (Required)

Metadata locking remains enabled throughout this reproduction.

Create a source table and two identical target tables:

```sql
DROP DATABASE IF EXISTS pessimistic_cte_retry;
CREATE DATABASE pessimistic_cte_retry;
USE pessimistic_cte_retry;

CREATE TABLE src(id INT PRIMARY KEY, next_u INT, payload INT);
CREATE TABLE retry_dst(id INT PRIMARY KEY, u INT UNIQUE, v INT);
CREATE TABLE control_dst LIKE retry_dst;

INSERT INTO src VALUES (1, 1, 10);
INSERT INTO retry_dst VALUES (1, 10, 0);
INSERT INTO control_dst VALUES (1, 10, 0);
SELECT @@global.tidb_enable_metadata_lock;
-- 1
```

In session A, start a pessimistic READ COMMITTED transaction. The CTE is referenced twice so the
plan contains `CTEFullScan`; `SLEEP` only makes the concurrent schedule easy to reproduce:

```sql
USE pessimistic_cte_retry;
SET tidb_txn_mode = 'pessimistic';
SET transaction_isolation = 'READ-COMMITTED';
BEGIN;

WITH c AS (
  SELECT id, payload + SLEEP(20) * 0 AS v FROM src
)
UPDATE retry_dst d
JOIN src s ON s.id = d.id
JOIN c c1 ON c1.id = d.id
JOIN c c2 ON c2.id = d.id
SET d.u = s.next_u, d.v = c1.v
WHERE d.id = 1;
```

While session A is sleeping, run and commit the following in session B:

```sql
USE pessimistic_cte_retry;
SET tidb_txn_mode = 'pessimistic';
SET transaction_isolation = 'READ-COMMITTED';
BEGIN;

INSERT INTO retry_dst VALUES (2, 1, 0);
INSERT INTO control_dst VALUES (2, 1, 0);
UPDATE src SET next_u = 2, payload = 20 WHERE id = 1;
COMMIT;
```

After the `UPDATE` in session A returns, commit and inspect the retry table:

```sql
COMMIT;
SELECT * FROM retry_dst ORDER BY id;
```

Run the same logical update once from the database state now visible to the successful attempt:

```sql
WITH c AS (
  SELECT id, payload AS v FROM src
)
UPDATE control_dst d
JOIN src s ON s.id = d.id
JOIN c c1 ON c1.id = d.id
JOIN c c2 ON c2.id = d.id
SET d.u = s.next_u, d.v = c1.v
WHERE d.id = 1;

SELECT * FROM control_dst ORDER BY id;
```

The following test provides a deterministic in-repository reproduction. Add it under
`pkg/executor/test/writetest`:

```go
package writetest

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/stretchr/testify/require"
)

func TestPessimisticRetryRefreshesMaterializedCTE(t *testing.T) {
	store := testkit.CreateMockStore(t)
	owner := testkit.NewTestKit(t, store)
	competitor := testkit.NewTestKit(t, store)
	owner.MustExec("use test")
	competitor.MustExec("use test")
	owner.MustQuery("show global variables like 'tidb_enable_metadata_lock'").
		Check(testkit.Rows("tidb_enable_metadata_lock ON"))

	owner.MustExec("create table cte_src (id int primary key, next_u int, payload int)")
	owner.MustExec("create table cte_retry (id int primary key, u int unique, v int)")
	owner.MustExec("create table cte_control like cte_retry")
	owner.MustExec("insert into cte_src values (1, 1, 10)")
	owner.MustExec("insert into cte_retry values (1, 10, 0)")
	owner.MustExec("insert into cte_control values (1, 10, 0)")
	owner.MustExec("set transaction_isolation = 'READ-COMMITTED'")
	competitor.MustExec("set transaction_isolation = 'READ-COMMITTED'")

	updateSQL := `with c as (
		select id, payload + sleep(0.8) * 0 as v from cte_src
	)
	update cte_retry d
	join cte_src s on s.id = d.id
	join c c1 on c1.id = d.id
	join c c2 on c2.id = d.id
	set d.u = s.next_u, d.v = c1.v
	where d.id = 1`
	planRows := owner.MustQuery("explain " + updateSQL).Rows()
	var plan strings.Builder
	for _, row := range planRows {
		fmt.Fprintln(&plan, row)
	}
	require.Contains(t, plan.String(), "CTEFullScan")

	owner.MustExec("begin pessimistic")
	errCh := make(chan error, 1)
	go func() {
		_, err := owner.Exec(updateSQL)
		errCh <- err
	}()

	time.Sleep(150 * time.Millisecond)
	competitor.MustExec("begin pessimistic")
	competitor.MustExec("insert into cte_retry values (2, 1, 0)")
	competitor.MustExec("insert into cte_control values (2, 1, 0)")
	competitor.MustExec("update cte_src set next_u = 2, payload = 20 where id = 1")
	competitor.MustExec("commit")

	require.NoError(t, <-errCh)
	require.Greater(t, owner.Session().GetSessionVars().StmtCtx.ExecRetryCount, uint64(0))
	owner.MustExec("commit")
	retryRows := owner.MustQuery("select * from cte_retry order by id").Rows()

	owner.MustExec(`with c as (select id, payload as v from cte_src)
		update cte_control d
		join cte_src s on s.id = d.id
		join c c1 on c1.id = d.id
		join c c2 on c2.id = d.id
		set d.u = s.next_u, d.v = c1.v
		where d.id = 1`)
	controlRows := owner.MustQuery("select * from cte_control order by id").Rows()

	require.Equal(t, controlRows, retryRows,
		"hidden retry must rebuild materialized CTE from the successful-attempt snapshot")
}
```

Run it with:

```bash
go test -tags=intest ./pkg/executor/test/writetest \
  -run TestPessimisticRetryRefreshesMaterializedCTE -count=1
```

### 2. What did you expect to see? (Required)

The transparent retry should be observationally equivalent to one execution using the state seen by
the successful attempt. After session B commits, both the ordinary `src` read and the materialized
CTE should observe `next_u=2,payload=20`.

The retry and direct-control tables should therefore both contain:

```text
(1, 2, 20)
(2, 1, 0)
```

Returning the original conflict would also avoid publishing a mixed-attempt row.

### 3. What did you see instead (Required)

Session A reports success with one internal retry, but commits a row assembled from two different
attempts:

```text
retry UPDATE: success, affected rows = 1, Exec_retry_count = 1
retry rows:   (1, 2, 10), (2, 1, 0)
control rows: (1, 2, 20), (2, 1, 0)
```

`u=2` comes from the successful attempt's fresh ordinary read, while `v=10` comes from the failed
attempt's completed CTE materialization. On real TiKV, the slow log records
`Exec_retry_count=1`, `Exec_retry_time=20.00218629`, `Query_time=20.005502414`, `Succ=1`, and
`IsExplicitTxn=1`.

`StatementContext.CTEStorageMap` owns each CTE's storage and `sync.Once`. The first attempt completes
the result and `CTEExec.Close` deliberately preserves a completed `resTbl`. During pessimistic retry,
`buildExecutor` runs against the same statement context; `buildCTE` loads the old storage and
`initOnce` suppresses producer reconstruction. `StatementContext.ResetForRetry` does not reset the
CTE map or completed result.

As a counterfactual, closing the old CTE storage and creating an empty CTE map before retry executor
construction makes the exact test commit `(1,2,20)` and match the direct control.

### 4. What is your TiDB version? (Required)

- Real-TiKV SQL reproduction: `8.0.11-TiDB-v9.0.0-beta.2.pre-1895-g5c9198e948`
- TiDB commit: `5c9198e9484db852b8477ce0014e0422ff9ec6a9`
- Current-source local reproduction: `b8d04e17a2ca61eee1220c5ce2d641a376f75e9b`
- Global `tidb_enable_metadata_lock`: `ON`
