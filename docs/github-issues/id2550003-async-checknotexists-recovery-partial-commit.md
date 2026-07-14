# [txn] Async recovery can commit a business write after duplicate-key failure

## Bug Report

### 1. Minimal reproduce step (Required)

#### Concrete production scenario

This can occur in an optimistic unit-of-work or ORM transaction that:

1. creates a tentative row carrying a unique business identity, such as an email, request ID, reservation ID, or idempotency key;
2. removes that tentative row later in the same transaction after another business rule rejects it; and
3. still updates durable business state in that transaction, such as an account balance, tenant quota, inventory counter, or ledger row.

For example, an application may flush a tentative `candidates` entity, revoke it later in the same unit of work, and charge an account for the attempted operation:

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

The full production trigger is:

1. `tidb_enable_async_commit=ON`, `tidb_enable_1pc=OFF`, optimistic transaction, and lazy uniqueness checking.
2. The proof keys and business key are in different Region batches. Separate tables normally satisfy this after ordinary Region splitting.
3. The business primary prewrite succeeds before the proof Region returns `AlreadyExist`. Prewrite batches run concurrently; the test below reproduced this ordering 3/3 without adding a Region delay.
4. TiDB returns a definite duplicate-key error, but its asynchronous cleanup is interrupted. Real examples include a TiDB pod restart/OOM or rolling upgrade immediately after the error, a temporary loss of the TiKV path, or cleanup RPCs exhausting their retry budget.
5. After lock expiry, another request reads or writes the business key. Lock resolution sees an async primary whose recovery set omits the failed proof, and commits the business write.

Thus the application is told that the transaction failed, but the business write becomes durable later. A normal application retry can apply the write a second time.

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

The failpoint models the real cleanup-interruption conditions listed above. It does not control Region
ordering: no Region-delay injection is used.

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
can deduct another 100.

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
