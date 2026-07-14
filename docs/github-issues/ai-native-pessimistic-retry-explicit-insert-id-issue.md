## Bug Report

### 1. Minimal reproduce step (Required)

This focused test creates a natural pessimistic-write conflict. The first attempt evaluates an
explicit nonzero `AUTO_INCREMENT` value (`42`) and then conflicts on the unique key. Before TiDB
retries the statement, the competing transaction also inserts a gate row. The successful retry
therefore inserts zero rows.

Add `pkg/executor/test/writetest/pessimistic_retry_insert_id_test.go`:

```go
// Copyright 2026 PingCAP, Inc.

package writetest

import (
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/stretchr/testify/require"
)

func TestPessimisticRetryDoesNotPublishFailedAttemptInsertID(t *testing.T) {
	store := testkit.CreateMockStore(t)
	owner := testkit.NewTestKit(t, store)
	competitor := testkit.NewTestKit(t, store)
	owner.MustExec("use test")
	competitor.MustExec("use test")
	owner.MustQuery("show global variables like 'tidb_enable_metadata_lock'").
		Check(testkit.Rows("tidb_enable_metadata_lock ON"))

	owner.MustExec("create table src (id int primary key, explicit_id bigint, u int)")
	owner.MustExec("create table gate (id int primary key)")
	owner.MustExec("create table dst (id bigint auto_increment primary key, u int unique)")
	owner.MustExec("create table sink (arm varchar(16) primary key, reported_id bigint)")
	owner.MustExec("insert into src values (1, 42, 1)")
	owner.MustExec("set transaction_isolation = 'READ-COMMITTED'")
	competitor.MustExec("set transaction_isolation = 'READ-COMMITTED'")

	insertSQL := `insert into dst(id, u)
		select explicit_id, u + sleep(0.8) * 0
		from src s
		where not exists (select 1 from gate g where g.id = s.id)`

	owner.MustExec("begin pessimistic")
	errCh := make(chan error, 1)
	go func() {
		_, err := owner.Exec(insertSQL)
		errCh <- err
	}()

	time.Sleep(150 * time.Millisecond)
	competitor.MustExec("begin pessimistic")
	competitor.MustExec("insert into dst values (2, 1)")
	competitor.MustExec("insert into gate values (1)")
	competitor.MustExec("commit")

	require.NoError(t, <-errCh)
	require.Greater(t, owner.Session().GetSessionVars().StmtCtx.ExecRetryCount, uint64(0))
	retryAffected := owner.Session().AffectedRows()
	retryInsertID := owner.Session().LastInsertID()
	owner.MustExec("commit")
	owner.MustExec("insert into sink values ('retry', ?)", retryInsertID)

	control := testkit.NewTestKit(t, store)
	control.MustExec("use test")
	control.MustExec("set transaction_isolation = 'READ-COMMITTED'")
	_, err := control.Exec(insertSQL)
	require.NoError(t, err)
	controlAffected := control.Session().AffectedRows()
	controlInsertID := control.Session().LastInsertID()
	control.MustExec("insert into sink values ('control', ?)", controlInsertID)

	owner.MustQuery("select * from dst order by id").Check(testkit.Rows("2 1"))
	owner.MustQuery("select * from sink order by arm").
		Check(testkit.Rows("control 0", "retry 0"))
	require.Equal(t, uint64(0), retryAffected)
	require.Equal(t, uint64(0), controlAffected)
	require.Equal(t, controlInsertID, retryInsertID,
		"a zero-row retry must not publish the explicit ID from its failed attempt")
}
```

Run it from the TiDB repository:

```bash
go test ./pkg/executor/test/writetest \
  -run TestPessimisticRetryDoesNotPublishFailedAttemptInsertID \
  -count=1 -v
```

The same schedule was also reproduced through `database/sql` and
`github.com/go-sql-driver/mysql` against a real TiKV cluster. The values below come from
`sql.Result.RowsAffected()` and `sql.Result.LastInsertId()`, so they are the MySQL OK-packet
result observed by an application, not a later SQL `SELECT LAST_INSERT_ID()` call.

### 2. What did you expect to see? (Required)

The successful retry sees the committed gate row and inserts zero rows. A direct execution from
that same final database state is the control arm and returns:

```text
affected_rows=0, last_insert_id=0
```

Transparent retry should expose only the successful attempt's result. Therefore the retry arm
should return the same result:

```text
retry:   affected_rows=0, last_insert_id=0
control: affected_rows=0, last_insert_id=0
sink:    control=0, retry=0
```

### 3. What did you see instead (Required)

The statement returned success after one internal retry, and its durable table effect was exactly
the same as the control arm, but the OK packet published `42` from the rolled-back attempt:

```json
{
  "retry_affected": 0,
  "retry_insert_id": 42,
  "control_affected": 0,
  "control_insert_id": 0,
  "destination_rows": [[2, 1]],
  "sink_rows": [
    {"arm": "control", "reported_id": 0},
    {"arm": "retry", "reported_id": 42}
  ]
}
```

The real-TiKV slow log recorded `Exec_retry_count: 1`, `Result_rows: 0`, and `Succ: true` for the
retry arm. Persisting `sql.Result.LastInsertId()` into `sink` makes the user-visible wrong result
durable and shows how an application can associate subsequent data with an ID that the successful
attempt never inserted.

The explicit nonzero auto-increment path assigns `StatementContext.InsertID`. On pessimistic
statement retry, `StatementContext.ResetForRetry()` resets affected rows and other per-attempt
state but does not clear `InsertID`. If the successful attempt inserts zero rows, nothing overwrites
the stale value. `session.LastInsertID()` then falls back to `StmtCtx.InsertID`, and the server
serializes it in the OK packet.

Clearing `InsertID` in `ResetForRetry()` makes the exact test pass while preserving the retry and
all durable table rows:

```go
func (sc *StatementContext) ResetForRetry() {
	sc.resetMuForRetry()
	sc.InsertID = 0
	// ...
}
```

This is distinct from #69796. That issue is about `LAST_INSERT_ID(expr)` and the separate
`StatementContext.LastInsertID` / `LastInsertIDSet` state. This reproduction never evaluates
`LAST_INSERT_ID(expr)`; it contaminates the singleton `StatementContext.InsertID` owner through an
explicit auto-increment input. Clearing only the two fields from #69796 does not fix this case.

### 4. What is your TiDB version? (Required)

- Current master used by the focused test: `b8d04e17a2ca61eee1220c5ce2d641a376f75e9b`
- Real-TiKV testbed: `8.0.11-TiDB-v9.0.0-beta.2.pre-1895-g5c9198e948`
- Testbed TiDB commit: `5c9198e9484db852b8477ce0014e0422ff9ec6a9`
- Both the focused test and the real-TiKV reproduction verified
  `tidb_enable_metadata_lock=ON`.
