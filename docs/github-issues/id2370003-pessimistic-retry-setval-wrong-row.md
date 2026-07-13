## Bug Report

### 1. Minimal reproduce step (Required)

Metadata locking remains enabled throughout this reproduction.

Create the objects:

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

In session A, start a pessimistic READ COMMITTED transaction. `SLEEP` makes the concurrent schedule
easy to reproduce:

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

While session A is sleeping, run and commit the following in session B:

```sql
USE pessimistic_setval_retry;
SET tidb_txn_mode = 'pessimistic';
SET transaction_isolation = 'READ-COMMITTED';
BEGIN;
INSERT INTO retry_dst VALUES (2, 1, 0);
UPDATE src SET next_u = 2 WHERE id = 1;
COMMIT;
```

After the `UPDATE` in session A returns successfully:

```sql
COMMIT;
SELECT * FROM retry_dst WHERE id = 1;
SELECT NEXTVAL(retry_seq);
```

Run a no-retry control from the same final `src` state, using a fresh identical sequence:

```sql
UPDATE control_dst AS d JOIN src AS s ON s.id = d.id
SET d.u = s.next_u,
    d.v = SETVAL(control_seq, 100)
WHERE d.id = 1;
SELECT * FROM control_dst WHERE id = 1;
SELECT NEXTVAL(control_seq);
```

The following test provides a deterministic in-repository reproduction. Add it under
`pkg/executor/test/writetest`:

```go
package writetest

import (
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/stretchr/testify/require"
)

func TestPessimisticRetryDoesNotChangeSetvalResult(t *testing.T) {
	store := testkit.CreateMockStore(t)
	owner := testkit.NewTestKit(t, store)
	competitor := testkit.NewTestKit(t, store)
	owner.MustExec("use test")
	competitor.MustExec("use test")
	owner.MustQuery("show global variables like 'tidb_enable_metadata_lock'").
		Check(testkit.Rows("tidb_enable_metadata_lock ON"))

	owner.MustExec("create sequence retry_seq start with 1 increment by 1")
	owner.MustExec("create sequence control_seq start with 1 increment by 1")
	owner.MustExec("create table src (id int primary key, next_u int)")
	owner.MustExec("create table retry_dst (id int primary key, u int unique, v bigint null)")
	owner.MustExec("create table control_dst like retry_dst")
	owner.MustExec("insert into src values (1, 1)")
	owner.MustExec("insert into retry_dst values (1, 10, 0)")
	owner.MustExec("insert into control_dst values (1, 10, 0)")
	owner.MustExec("set transaction_isolation = 'READ-COMMITTED'")
	competitor.MustExec("set transaction_isolation = 'READ-COMMITTED'")

	owner.MustExec("begin pessimistic")
	errCh := make(chan error, 1)
	go func() {
		_, err := owner.Exec(`update retry_dst as d join src as s on s.id = d.id
			set d.u = s.next_u, d.v = setval(retry_seq, 100) + sleep(0.8)
			where d.id = 1`)
		errCh <- err
	}()

	time.Sleep(150 * time.Millisecond)
	competitor.MustExec("begin pessimistic")
	competitor.MustExec("insert into retry_dst values (2, 1, 0)")
	competitor.MustExec("update src set next_u = 2 where id = 1")
	competitor.MustExec("commit")

	err := <-errCh
	require.NoError(t, err)
	require.Greater(t, owner.Session().GetSessionVars().StmtCtx.ExecRetryCount, uint64(0))
	owner.MustExec("commit")
	retryRow := owner.MustQuery("select * from retry_dst where id = 1").Rows()
	retryNextVal := owner.MustQuery("select nextval(retry_seq)").Rows()

	owner.MustExec(`update control_dst as d join src as s on s.id = d.id
		set d.u = s.next_u, d.v = setval(control_seq, 100)
		where d.id = 1`)
	controlRow := owner.MustQuery("select * from control_dst where id = 1").Rows()
	controlNextVal := owner.MustQuery("select nextval(control_seq)").Rows()

	require.Equal(t, controlNextVal, retryNextVal)
	require.Equal(t, controlRow, retryRow,
		"transparent retry must equal one execution from the successful attempt's state")
}
```

Run it with:

```bash
go test -tags=intest ./pkg/executor/test/writetest \
  -run TestPessimisticRetryDoesNotChangeSetvalResult -count=1
```

### 2. What did you expect to see? (Required)

A successful transparent retry should be observationally equivalent to executing the statement
once from the state seen by the successful attempt. In this case, both the retried statement and
the no-retry control should persist `(1, 2, 100)`.

If TiDB cannot preserve that equivalence after `SETVAL` changes the sequence owner, it should return
the retryable conflict instead of silently re-executing the expression and committing a different
row value.

### 3. What did you see instead (Required)

The retried `UPDATE` reports success, but the two runs produce different durable rows:

```text
retried UPDATE:   success, affected rows = 1, Exec_retry_count = 1
retried row:      1,2,NULL
retried nextval:  101

control row:      1,2,100
control nextval:  101
```

The in-repository test fails with:

```text
expected: [[1 2 100]]
actual:   [[1 2 <nil>]]
transparent retry must equal one execution from the successful attempt's state
```

The equal `NEXTVAL` results are important: this is not an expected sequence gap caused by a
non-transactional allocator. The hidden failed attempt changes the value later persisted into an
ordinary table row, even though the final sequence state is identical to the control.

The sequence mutation happens in `builtinSetValSig.evalInt` before the statement encounters the
retryable unique-key conflict. `SetSequenceVal` changes the sequence owner immediately. The
pessimistic retry path rebuilds the executor and restores statement/KV retry state, but it neither
restores that sequence owner nor preserves the first `SETVAL` result. The second execution of
`SETVAL(retry_seq, 100)` therefore returns `NULL`, and that `NULL` reaches the committed row.

As a counterfactual, recording an effective `SETVAL` in the statement context and declining the
transparent retry after it makes the same test return the original conflict and leave the target
row unchanged. This isolates the problem to the retry eligibility decision; a complete fix may
generalize the gate to expressions whose result depends on an external mutation made by the failed
attempt.

### 4. What is your TiDB version? (Required)

- Real-TiKV SQL reproduction: `8.0.11-TiDB-v9.0.0-beta.2.pre-1895-g5c9198e948`
- TiDB commit: `5c9198e9484db852b8477ce0014e0422ff9ec6a9`
- Current-source local reproduction: `b8d04e17a2ca61eee1220c5ce2d641a376f75e9b`
- Global and session `tidb_enable_metadata_lock`: `ON`
