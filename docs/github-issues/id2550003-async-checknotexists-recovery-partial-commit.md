# [txn] Async recovery can commit a business write after duplicate-key failure

## Bug Report

### 1. Minimal reproduce step (Required)

#### Concrete production scenario

One concrete production shape is an at-least-once import/reconciliation consumer. A common two-stage
flow first materializes a provisional entity, then merges it into an existing canonical entity after
matching has completed. The same database transaction also updates the canonical balance and advances
the import cursor. Explicit flush boundaries make this independent of ORM statement reordering.

```java
@Transactional
void importCustomer(Event event) {
    Candidate c = candidates.saveAndFlush(
        new Candidate(nextID(), event.externalCustomerID())); // sends INSERT now

    Match match = matchingService.match(event);
    if (match.mergeIntoExisting()) {
        candidates.delete(c);
        candidates.flush();                                  // sends DELETE now
        accounts.applyDelta(match.accountID(), event.balanceDelta());
    }

    importJobs.advanceCursor(event.partition(), event.offset());
}                                                            // JDBC COMMIT here
```

Suppose this event is a redelivery after the first consumer timed out, or two import workers received the
same external customer concurrently. A committed row therefore already owns the unique external customer
ID. With lazy uniqueness checking, `saveAndFlush` can still return success. The matcher then decides to
merge the provisional entity into the existing customer, deletes the provisional row, adjusts the
canonical account, and advances the consumer cursor. `COMMIT` is the first operation that reports
`Duplicate entry`. The transaction manager reports the whole unit of work as aborted, so the message is
normally retried and neither the balance adjustment nor the cursor advance is expected to persist.

The bug violates that expectation after an ordinary TiDB failure window: the account/cursor prewrite has
already succeeded, the duplicate proof in another Region fails, and TiDB returns the definite duplicate
error before its background rollback completes. If that TiDB pod is then terminated by an OOM, node loss,
or rolling deployment, or if the primary Region remains unavailable through the cleanup backoff budget,
the primary lock survives. A later read or update of the account/cursor resolves that lock. Async recovery
does not know about the failed `CheckNotExists` proof and commits the business write. The redelivered event
can then apply the balance delta a second time. If the cursor is the surviving write, the consumer can skip
an event it believes was rolled back.

The compact SQL below uses `candidates` for the provisional entity and `accounts` for the canonical
balance/cursor write:

```sql
SET tidb_enable_async_commit = ON;
SET tidb_enable_1pc = OFF;

BEGIN OPTIMISTIC;
INSERT INTO candidates VALUES (200, 'used@example.com');
DELETE FROM candidates WHERE id = 200;
UPDATE accounts SET balance = balance - 100 WHERE id = 1;
COMMIT; -- returns Duplicate entry 'used@example.com'
```

The unique value already exists in another committed row. With the current default
`tidb_constraint_check_in_place=OFF`, the insert is checked lazily. The insert-then-delete keys become
proof-only `CheckNotExists` mutations, while the account update is a real mutation.

The exact production trigger is:

1. Async commit has been explicitly enabled at cluster or session scope. It is not enabled by default in
   current TiDB. `tidb_enable_1pc` may be OFF, or it may be ON and fall back because the transaction needs
   more than one prewrite batch.
2. The service uses optimistic transactions, for example through a session/global
   `tidb_txn_mode=optimistic` setting, and lazy uniqueness checking is active
   (`tidb_constraint_check_in_place=OFF`, the current default).
3. The transaction inserts and then deletes a new row carrying a unique value that is already committed
   elsewhere. Both SQL statements can return success; TiDB retains row/index `CheckNotExists` operations
   as commit-time proofs.
4. The transaction also changes at least one real business key. In the reproducer the account update is
   the only lock-bearing mutation, so it becomes the transaction primary; `CheckNotExists` keys are
   explicitly skipped during primary selection. No special table-creation order is required.
5. The business primary and the failed uniqueness proof are in different Region/batch requests. This is
   an ordinary layout once the tables have separate Regions; it does not require a DDL or MDL race.
6. The primary prewrite succeeds before the other batch returns `AlreadyExist`. Prewrite batches run
   concurrently; the real-TiKV SQL test reproduced this ordering 3/3 without Region-delay injection.
7. TiDB starts best-effort cleanup in a background goroutine and returns the definite duplicate error
   without waiting for that cleanup. The cleanup does not reach the primary. A concrete production case is
   the TiDB pod exiting because of OOM, node loss, or a rolling deployment after the error response is sent
   but before `BatchRollback` reaches the primary. Another is the primary Region remaining unreachable or
   server-busy through client-go's cleanup backoff budget.
8. After the lock expires, a later request touches the business key. Lock resolution reads the async
   primary, whose recovery set omits the failed proof, and incorrectly commits the business mutation.

The required ordering is:

```text
T(primary Prewrite succeeds)
  < T(proof Region returns AlreadyExist)
  < T(COMMIT returns Duplicate entry)

T(TiDB exits or cleanup exhausts its retries)
  < T(BatchRollback would reach the primary), so that rollback never happens

T(primary lock TTL expires)
  < T(a later request touches the business key and invokes lock resolution)
```

Thus the application is told that the transaction failed, but the business write becomes durable later.
A normal queue redelivery or transaction retry can apply the write a second time.

To reproduce with real TiKV, add the following file as
`tests/realtikvtest/txntest/ai_native_async_checknotexists_sql_test.go`:

```go
package txntest

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pingcap/failpoint"
	"github.com/pingcap/tidb/pkg/config"
	"github.com/pingcap/tidb/pkg/tablecodec"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/tests/realtikvtest"
	"github.com/stretchr/testify/require"
	"github.com/tikv/client-go/v2/oracle"
)

type expiringOracle struct {
	oracle.Oracle
	expireLocks atomic.Bool
}

func (o *expiringOracle) IsExpired(lockTS, ttl uint64, opt *oracle.Option) bool {
	if o.expireLocks.Load() {
		return true
	}
	return o.Oracle.IsExpired(lockTS, ttl, opt)
}

func TestAsyncCheckNotExistsSQLRecovery(t *testing.T) {
	defer config.RestoreFunc()()
	config.UpdateGlobal(func(conf *config.Config) {
		conf.TiKVClient.AsyncCommit.SafeWindow = 10 * time.Second
		conf.TiKVClient.AsyncCommit.AllowedClockDrift = 500 * time.Millisecond
	})

	store, domain := realtikvtest.CreateMockStoreAndDomainAndSetup(t)
	tk := testkit.NewTestKit(t, store)
	tk.Session().SetConnectionID(1)
	tk.MustExec("use test")
	tk.MustQuery("select @@tidb_enable_metadata_lock").Check(testkit.Rows("1"))
	tk.MustQuery("select @@tidb_constraint_check_in_place").Check(testkit.Rows("0"))
	tk.MustExec("set @@tidb_enable_async_commit = on")
	tk.MustExec("set @@tidb_enable_1pc = off")
	tk.MustExec("drop table if exists ai_accounts, ai_candidates")
	tk.MustExec("create table ai_accounts (id bigint primary key, balance bigint not null)")
	tk.MustExec("create table ai_candidates (id bigint primary key, email varchar(128) not null, unique key uk_email(email))")
	tk.MustExec("insert into ai_accounts values (1, 0)")
	tk.MustExec("insert into ai_candidates values (100, 'used@example.com')")

	tableID, err := strconv.ParseInt(tk.MustQuery(
		"select tidb_table_id from information_schema.tables where table_schema='test' and table_name='ai_candidates'",
	).Rows()[0][0].(string), 10, 64)
	require.NoError(t, err)
	_, err = domain.GetPDClient().SplitRegions(context.Background(), [][]byte{tablecodec.EncodeTablePrefix(tableID)})
	require.NoError(t, err)

	type oracleStore interface {
		GetOracle() oracle.Oracle
		SetOracle(oracle.Oracle)
	}
	tikvStore, ok := store.(oracleStore)
	require.True(t, ok)
	baseOracle := tikvStore.GetOracle()
	o := &expiringOracle{Oracle: baseOracle}
	tikvStore.SetOracle(o)
	defer tikvStore.SetOracle(baseOracle)

	require.NoError(t, failpoint.Enable("tikvclient/commitFailedSkipCleanup", "return"))
	defer func() { require.NoError(t, failpoint.Disable("tikvclient/commitFailedSkipCleanup")) }()

	tk.MustExec("begin optimistic")
	tk.MustExec("insert into ai_candidates values (200, 'used@example.com')")
	tk.MustExec("delete from ai_candidates where id = 200")
	tk.MustExec("update ai_accounts set balance = balance - 100 where id = 1")
	commitErr := tk.ExecToErr("commit")
	require.ErrorContains(t, commitErr, "Duplicate entry 'used@example.com'")

	o.expireLocks.Store(true)
	tk2 := testkit.NewTestKit(t, store)
	tk2.Session().SetConnectionID(2)
	tk2.MustExec("use test")
	tk2.MustQuery("select id, email from ai_candidates order by id").Check(
		testkit.Rows("100 used@example.com"),
	)
	tk2.MustQuery("select id, balance from ai_accounts").Check(testkit.Rows("1 0"))
}
```

Start a one-TiKV playground and run:

```bash
tiup playground nightly --db=0 --kv=1 --tiflash=0
go test -v -count=3 -run '^TestAsyncCheckNotExistsSQLRecovery$' --tags=intest ./tests/realtikvtest/txntest/...
```

The cleanup failpoint models only step 7: loss of the best-effort cleanup after TiDB has returned the
duplicate error. It does not create the duplicate, choose the primary, alter Region grouping, control
prewrite ordering, or force recovery to commit. The oracle wrapper only avoids waiting for the real lock
TTL before invoking normal lock resolution. No Region-delay injection is used.

### 2. What did you expect to see? (Required)

Because `COMMIT` returns a definite duplicate-key error, no mutation from the transaction should ever
become visible. A fresh session should read `accounts(1).balance = 0`.

### 3. What did you see instead (Required)

All three runs failed the final assertion:

```text
expected: [["1", "0"]]
actual:   [["1", "-100"]]
```

The candidate table still contains only the original `used@example.com` row, but lock recovery commits
the account update from the failed transaction. This is a durable partial commit, and an application retry
can apply the same balance delta again or advance a business cursor twice.

The root cause is that `Op_CheckNotExists` keys are commit prerequisites but do not write locks.
`checkAsyncCommit` admits the transaction, while `asyncSecondaries` excludes these proof-only keys. The
primary can therefore carry an empty async-recovery set. When another Region returns `AlreadyExist`, the
failure is not represented in durable recovery state, so later lock resolution can commit the primary.

As a minimal counterfactual, rejecting async commit when `hasNoNeedCommitKeys` is true makes the identical
test pass and keeps the balance at `0`.

### 4. What is your TiDB version? (Required)

Reproduced on TiDB master:

```text
b8d04e17a2ca61eee1220c5ce2d641a376f75e9b
```

with tikv/client-go:

```text
01bd8f99f4da23c6fc9d671eecc0166c7b6ceb9b
```

MDL was enabled (`@@tidb_enable_metadata_lock = 1`) throughout the test.
