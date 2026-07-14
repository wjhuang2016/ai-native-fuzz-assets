## Bug Report

### 1. Minimal reproduce step (Required)

An async-commit transaction can finish all prewrites and establish enough durable state for lock
recovery to commit it, then return the ordinary error `txn takes too much time`. If the best-effort
cleanup does not finish, a later reader recovers the transaction as committed.

The following deterministic test uses client-go's existing async-commit integration suite. It
accelerates only the 24-hour age predicate and uses the existing cleanup-skip failpoint to model a
client/store process that becomes unavailable before asynchronous cleanup completes. Prewrite and
lock recovery are otherwise unmodified.

Add `integration_tests/ai_native_async_commit_expired_probe_test.go` to the client-go checkout used
by TiDB:

```go
package tikv_test

import (
	"context"
	"sync/atomic"

	"github.com/pingcap/failpoint"
	"github.com/tikv/client-go/v2/oracle"
	"github.com/tikv/client-go/v2/txnkv/transaction"
	"github.com/tikv/client-go/v2/util"
)

type aiNativeExpiringOracle struct {
	oracle.Oracle
	expireLocks atomic.Bool
}

func (o *aiNativeExpiringOracle) IsExpired(lockTS, ttl uint64, opt *oracle.Option) bool {
	if ttl == transaction.MaxTxnTimeUse || o.expireLocks.Load() {
		return true
	}
	return o.Oracle.IsExpired(lockTS, ttl, opt)
}

func (s *testAsyncCommitFailSuite) TestAINativeExpiredAsyncPrewriteCanRecoverAsCommitted() {
	baseOracle := s.store.GetOracle()
	expiringOracle := &aiNativeExpiringOracle{Oracle: baseOracle}
	s.store.SetOracle(expiringOracle)
	defer s.store.SetOracle(baseOracle)

	s.Require().NoError(failpoint.Enable("tikvclient/commitFailedSkipCleanup", "return"))
	defer func() {
		s.Require().NoError(failpoint.Disable("tikvclient/commitFailedSkipCleanup"))
	}()

	primary := []byte("ai-native-expired-primary")
	secondary := []byte("ai-native-expired-secondary")
	txn := s.beginAsyncCommit()
	s.Require().NoError(txn.Set(primary, []byte("v1")))
	s.Require().NoError(txn.Set(secondary, []byte("v2")))
	ctx := context.WithValue(context.Background(), util.SessionID, uint64(1))
	err := txn.Commit(ctx)
	s.Require().ErrorContains(err, "txn takes too much time")
	s.Require().True(txn.GetCommitter().IsAsyncCommit())

	// A fresh reader expires and resolves the locks through the normal lock resolver.
	// Both values becoming visible after the ordinary Commit error is the bug witness.
	expiringOracle.expireLocks.Store(true)
	s.mustPointGet(primary, []byte("v1"))
	s.mustPointGet(secondary, []byte("v2"))

	s.store.SetOracle(baseOracle)
	cleanupTxn, err := s.store.Begin()
	s.Require().NoError(err)
	s.Require().NoError(cleanupTxn.Delete(primary))
	s.Require().NoError(cleanupTxn.Delete(secondary))
	s.Require().NoError(cleanupTxn.Commit(context.Background()))
}
```

Run:

```bash
cd integration_tests
go test . \
  -run 'TestAsyncCommitFail/TestAINativeExpiredAsyncPrewriteCanRecoverAsCommitted' \
  -count=1 -v
```

On current client-go, the probe observes this sequence and passes, which is the RED bug witness:

```text
[failpoint] injected skip cleanup secondaries on failure
resolve async commit locks ... commitTS=<nonzero>
txn lock cleanup resolve-region rpc finished action=commit ... keyCount=2
--- PASS: TestAsyncCommitFail/TestAINativeExpiredAsyncPrewriteCanRecoverAsCommitted
```

The same probe was also reproduced against a real one-PD/three-TiKV cluster. No TiKV transaction
status or lock-recovery response was mocked; both raw keys were removed after the observation.

#### Concrete production trigger

All of the following conditions are required:

1. `tidb_enable_async_commit=ON` is explicitly enabled for the session or cluster. It is OFF by
   default on current TiDB.
2. 1PC is disabled or not selected, so the final write set uses async commit.
3. The transaction's start TS is more than 24 hours old. Because TiDB's default `wait_timeout` is
   eight hours, this normally means a workflow that raises `wait_timeout`, keeps the session active,
   or continues issuing work inside one long transaction rather than an untouched idle connection.
4. All final async prewrites succeed.
5. TiDB A's background rollback is delayed beyond lock expiry or never completes, for example
   because A loses its TiKV network path after prewrite, is terminated during scale-in/rolling
   restart, or its store is closing. The cleanup goroutine is started but is not awaited before the
   age error is returned.

A concrete business shape is a long-lived workflow transaction performing a two-account transfer:

```sql
SET SESSION tidb_enable_async_commit = ON;
SET SESSION tidb_enable_1pc = OFF;
SET SESSION wait_timeout = 172800;

BEGIN OPTIMISTIC;
UPDATE accounts SET balance = balance - 100 WHERE account_id = 101;
-- The workflow remains active or suspended for more than 24 hours.
UPDATE accounts SET balance = balance + 100 WHERE account_id = 202;
COMMIT;
```

The production failure schedule is:

1. TiDB A sends successful async prewrites for both account keys.
2. The local 24-hour check then returns `txn takes too much time`; the application treats this as a
   definite failed transfer.
3. A's asynchronous rollback is delayed by an A-to-TiKV network fault or interrupted by A's
   termination. The client connection can remain healthy long enough to receive the age error.
4. The application retries the transfer through TiDB B.
5. B touches the same account keys, encounters the first attempt's expired async locks, and its lock
   resolver sees the complete secondary set. It commits the first attempt with the recovered nonzero
   commit TS.
6. The retry then proceeds and commits the same debit/credit a second time.

Thus the later recovery consumer need not be a special maintenance task: the application's own
retry on another TiDB node can trigger the first attempt's commit. The trigger is low-frequency
because it combines an explicitly enabled feature, a transaction older than 24 hours, and delayed
cleanup, but the resulting double application is a correctness failure.

### 2. What did you expect to see? (Required)

The result returned by `Commit` must agree with the durable transaction outcome. Once all async
prewrites have succeeded and another owner can independently recover the transaction as committed,
the client must not return an ordinary abort error.

Safe outcomes include either:

- reject the over-age transaction before async prewrite, return the age error, and leave both keys
  absent; or
- after crossing the async recovery commit point, return success or an explicit undetermined result
  unless rollback has been synchronously proven complete.

### 3. What did you see instead (Required)

`Commit` returns the ordinary `txn takes too much time` error, but a later point get recovers both
the primary and secondary as committed with a nonzero commit TS. Applications normally treat this
error as a definite failure. Retrying a non-idempotent transaction can therefore apply the logical
operation twice.

A realistic trigger is a transaction held open for more than 24 hours by an ETL job, workflow, or
forgotten connection. Its final async-eligible write set prewrites successfully, but the client then
returns the age error. If the TiDB/client process exits, its store closes, or cleanup cannot reach
TiKV before completion, a later conflicting read can recover the first attempt as committed.

The source ordering is:

1. `twoPhaseCommitter.execute` completes `prewriteMutations` for the full async write set.
2. The primary lock contains the complete secondary list and `minCommitTS` is nonzero.
3. client-go selects and stores the async `commitTS`.
4. Only then, `IsExpired(startTS, MaxTxnTimeUse)` returns `txn takes too much time` as an ordinary
   error.
5. Error cleanup is asynchronous and may be skipped when the store is closed or may otherwise fail.
6. `LockResolver` sees the complete async lock set and resolves it with the nonzero commit TS.

The broken proof obligation is: after successful async prewrite has crossed the recovery commit
point, a later check must not publish an ordinary abort unless compensating rollback is known to be
complete before that error is returned.

### 4. What is your TiDB version? (Required)

TiDB current master:

```text
b8d04e17a2ca61eee1220c5ce2d641a376f75e9b
```

Its `go.mod` uses:

```text
github.com/tikv/client-go/v2 v2.0.8-0.20260708122311-01bd8f99f4da
```

The current client-go commit `01bd8f99f4da23c6fc9d671eecc0166c7b6ceb9b` still reproduces the
problem.
