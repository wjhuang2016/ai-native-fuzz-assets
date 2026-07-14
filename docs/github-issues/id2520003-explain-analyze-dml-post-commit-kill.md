## Bug Report

### 1. Minimal reproduce step (Required)

An autocommit `EXPLAIN ANALYZE` DML commits its mutation before the server fetches
the first row of the explain result. If `KILL QUERY` reaches the session in this
interval, the first result fetch returns error 1317 even though the mutation is
already durable.

Add `pkg/util/sqlkiller` to the imports of `pkg/server/tidb_test.go`, then add the
following test:

```go
func TestExplainAnalyzeDMLKillAfterCommitReturnsError(t *testing.T) {
	store := testkit.CreateMockStore(t)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("create table t(id int primary key, v int)")
	tk.MustExec("insert into t values (1, 0)")

	// ExecuteStmt commits an autocommit EXPLAIN ANALYZE DML before its result
	// set is fetched.
	rs, err := tk.Exec("explain analyze update t set v = v + 1 where id = 1")
	require.NoError(t, err)
	require.NotNil(t, rs)

	// This is the same SQLKiller signal that KILL QUERY sends. Deliver it at
	// the post-commit/pre-result boundary exposed by ExecuteStmt returning rs.
	tk.Session().GetSessionVars().SQLKiller.SendKillSignal(sqlkiller.QueryInterrupted)
	err = rs.Next(context.Background(), rs.NewChunk(nil))
	require.ErrorContains(t, err, "Query execution was interrupted")
	require.NoError(t, rs.Close())
	tk.Session().GetSessionVars().SQLKiller.Reset()

	// Because the statement returned an interruption error, its DML must not
	// have been published as a successful durable mutation.
	observer := testkit.NewTestKit(t, store)
	observer.MustExec("use test")
	observer.MustQuery("select v from t where id = 1").Check(testkit.Rows("0"))
}
```

Run:

```bash
go test ./pkg/server \
  -run TestExplainAnalyzeDMLKillAfterCommitReturnsError \
  -tags=intest -count=1 -v
```

This is deterministic and fails on current master:

```text
Error:      Not equal:
            expected: [[0]]
            actual  : [[1]]
Messages:   a statement reported as interrupted must not leave its mutation committed
--- FAIL: TestExplainAnalyzeDMLKillAfterCommitReturnsError
```

The test calls `SQLKiller.SendKillSignal` directly only to select the exact
boundary deterministically. Production `KILL QUERY`, max-execution-time expiry,
and connection-liveness cancellation use the same signal and `HandleSignal`
path.

### 2. What did you expect to see? (Required)

The terminal result and the durable transaction result must agree. Either:

- the kill wins before commit, the client receives error 1317, and `v` remains
  `0`; or
- commit wins, the client receives the successful explain result, and `v`
  becomes `1`.

TiDB must not return a definite `Query execution was interrupted` error for a
statement whose mutation has already committed.

### 3. What did you see instead (Required)

The result fetch returns error 1317, but a fresh session observes `v=1`.
Applications and operators normally treat error 1317 as a failed statement. If
they retry a non-idempotent update such as `SET balance = balance + ?`, the same
effect can be applied twice.

The ordering is:

1. `ExecStmt.handleNoDelay` executes the inner UPDATE and sets
   `StmtCtx.IsExplainAnalyzeDML`.
2. `session.ExecuteStmt` sees the non-nil explain record set and calls
   `StmtCommit` followed by `CommitTxn` before returning it.
3. No explain row has been generated yet. The server then calls the record
   set's first `Next` to produce the response.
4. `recordSet.Next` calls `SQLKiller.HandleSignal` before
   `ExplainExec.generateExplainInfo`. A pending kill therefore returns error
   1317 after the commit is irreversible.

Relevant code paths are:

- `pkg/executor/adapter.go`: `handleNoDelay` executes the DML but leaves explain
  rendering lazy.
- `pkg/session/session.go`: the `IsExplainAnalyzeDML` branch commits before
  returning the record set.
- `pkg/executor/adapter.go`: `recordSet.Next` consumes the kill signal before
  rendering the first explain chunk.
- `pkg/server/conn.go`: an error from that first `Next` is returned to the
  client as the statement result.

As an ownership counterfactual, the same test inside an explicit transaction
keeps the mutation uncommitted across the first result fetch. The same kill
error followed by rollback leaves `v=0`. This isolates the autocommit
pre-result commit boundary as the cause, rather than UPDATE or SQLKiller itself.

### 4. What is your TiDB version? (Required)

Current master commit:

```text
b8d04e17a2ca61eee1220c5ce2d641a376f75e9b
```

The test uses the default `tidb_enable_metadata_lock=ON` setting.
