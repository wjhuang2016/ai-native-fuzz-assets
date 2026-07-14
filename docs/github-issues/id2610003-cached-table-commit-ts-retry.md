# [txn] CommitTsExpired retry can commit cached-table writes after the write lease expires

## Bug Report

### 1. Minimal reproduce step (Required)

#### Concrete production trigger

This needs an explicitly cached table, but does not require concurrent DDL, disabled MDL, async
commit, 1PC, or a fabricated TiKV error. A concrete production schedule is:

1. A frequently read reference table is enabled with `ALTER TABLE ... CACHE`. TiDB A starts an
   ordinary optimistic transaction that updates the table.
2. The write is large enough that the primary lock remains live beyond the cached-table WRITE lease.
   Current client-go uses `6000 * sqrt(write-size-MiB)` milliseconds, capped by the production
   `ManagedLockTTL=20s`. A roughly 4 MiB write therefore receives about 12 seconds, longer than the
   fixed five-second cached-table WRITE lease. The exact condition is `primary lock TTL > 5s`, not
   exactly 4 MiB.
3. TiDB A acquires the WRITE lease, prewrites, and validates the initial commitTS. Immediately
   afterward, only A loses progress for more than five seconds. Real examples are a node-specific
   TiDB-to-TiKV/PD network interruption, a long stop-the-world runtime pause, severe CPU starvation,
   or an OS/container scheduling stall. Its primary lock remains live.
4. TiDB B stays healthy. After A's WRITE lease expires, B executes an ordinary SELECT, acquires a
   READ lease, and encounters A's primary lock. TiKV `CheckTxnStatus` pushes that lock's
   `minCommitTS`, allowing B to load the pre-commit value into its cache.
5. A resumes and sends its original primary Commit. TiKV correctly returns `CommitTsExpired`
   because that commitTS is below the pushed `minCommitTS`.
6. client-go requests a replacement TSO. The replacement is later than A's expired WRITE lease, but
   current code does not run the cached-table upper-bound checker again. It sends a second Commit and
   reports SQL success.
7. B can continue serving the old cached value. An ordinary
   `INSERT INTO sink SELECT ... FROM cached_table` can persist it into a regular table.

Small transactions are a negative control: their default primary lock TTL is around three seconds,
so the reader can roll back the lock before this five-second window. This is why the trigger must
state the transaction-size/TTL condition.

The following deterministic test stops lease renewal and holds the first primary Commit only to
compress step 3. TiKV itself performs the prewrite, reader-driven `CheckTxnStatus`, minCommitTS
push, `CommitTsExpired` rejection, replacement commit, and fresh reads.

Apply this test-only hook to `pkg/table/tables/cache.go`:

```diff
diff --git a/pkg/table/tables/cache.go b/pkg/table/tables/cache.go
index c8fe53a824..7e7c02ac66 100644
--- a/pkg/table/tables/cache.go
+++ b/pkg/table/tables/cache.go
@@ -305,6 +305,13 @@ func (c *cachedTable) renewLease(handle StateRemote, ts uint64, data *cacheData,
 
 const cacheTableWriteLease = 5 * time.Second
 
+// TestAINativeStopWriteLeaseRenewal and TestAINativeWriteLeaseAcquired expose
+// the cached-table lease boundary to the focused transaction safety probe.
+var (
+	TestAINativeStopWriteLeaseRenewal atomic.Bool
+	TestAINativeWriteLeaseAcquired    chan uint64
+)
+
 func (c *cachedTable) WriteLockAndKeepAlive(ctx context.Context, exit chan struct{}, leasePtr *uint64, wg chan error) {
 	writeLockLease, err := c.lockForWrite(ctx)
 	atomic.StoreUint64(leasePtr, writeLockLease)
@@ -313,6 +320,12 @@ func (c *cachedTable) WriteLockAndKeepAlive(ctx context.Context, exit chan struc
 		logutil.Logger(ctx).Warn("lock for write lock fail", zap.String("category", "cached table"), zap.Error(err))
 		return
 	}
+	if TestAINativeWriteLeaseAcquired != nil {
+		TestAINativeWriteLeaseAcquired <- writeLockLease
+	}
+	if TestAINativeStopWriteLeaseRenewal.Load() {
+		return
+	}
 
 	t := time.NewTicker(cacheTableWriteLease / 2)
 	defer t.Stop()
```

Add `pkg/table/tables/ai_native_commit_ts_upper_bound_retry_test.go`:

```go
// Copyright 2026 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tables_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pingcap/kvproto/pkg/kvrpcpb"
	"github.com/pingcap/tidb/pkg/infoschema"
	"github.com/pingcap/tidb/pkg/kv"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/store/mockstore"
	"github.com/pingcap/tidb/pkg/table/tables"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/tests/realtikvtest"
	"github.com/stretchr/testify/require"
	"github.com/tikv/client-go/v2/oracle"
	"github.com/tikv/client-go/v2/tikv"
	"github.com/tikv/client-go/v2/tikvrpc"
	"github.com/tikv/client-go/v2/txnkv/transaction"
)

type cachedLeaseCommitHoldClient struct {
	tikv.Client
	targetStartTS atomic.Uint64
	blocked       chan struct{}
	release       chan struct{}
	minTSPushed   chan struct{}
	naturalExpiry chan struct{}
	blockOnce     sync.Once
	pushOnce      sync.Once
	expiryOnce    sync.Once
	commitCalls   atomic.Int32
}

func newCachedLeaseCommitHoldClient() *cachedLeaseCommitHoldClient {
	return &cachedLeaseCommitHoldClient{
		blocked:       make(chan struct{}),
		release:       make(chan struct{}),
		minTSPushed:   make(chan struct{}),
		naturalExpiry: make(chan struct{}),
	}
}

func (c *cachedLeaseCommitHoldClient) SendRequest(
	ctx context.Context,
	addr string,
	req *tikvrpc.Request,
	timeout time.Duration,
) (*tikvrpc.Response, error) {
	target := c.targetStartTS.Load()
	isTargetCommit := req.Type == tikvrpc.CmdCommit && target != 0 && req.Commit().StartVersion == target
	if isTargetCommit {
		c.commitCalls.Add(1)
		c.blockOnce.Do(func() {
			close(c.blocked)
			<-c.release
		})
	}

	resp, err := c.Client.SendRequest(ctx, addr, req, timeout)
	if err != nil || resp == nil || resp.Resp == nil || target == 0 {
		return resp, err
	}
	if req.Type == tikvrpc.CmdCheckTxnStatus && req.CheckTxnStatus().LockTs == target {
		statusResp := resp.Resp.(*kvrpcpb.CheckTxnStatusResponse)
		if statusResp.Action == kvrpcpb.Action_MinCommitTSPushed {
			c.pushOnce.Do(func() { close(c.minTSPushed) })
		}
	}
	if isTargetCommit {
		commitResp := resp.Resp.(*kvrpcpb.CommitResponse)
		if commitResp.GetError().GetCommitTsExpired() != nil {
			c.expiryOnce.Do(func() { close(c.naturalExpiry) })
		}
	}
	return resp, err
}

func TestAINativeCommitTSRetryCannotCrossCachedTableWriteLease(t *testing.T) {
	expectFixed := os.Getenv("AI_NATIVE_EXPECT_COMMIT_TS_UPPER_BOUND_FIX") == "1"
	realTiKV := os.Getenv("AI_NATIVE_REAL_TIKV") == "1"
	client := newCachedLeaseCommitHoldClient()
	var store kv.Storage
	if realTiKV {
		*realtikvtest.WithRealTiKV = true
		store = realtikvtest.CreateMockStoreAndSetup(t)
		atomic.StoreUint64(&transaction.ManagedLockTTL, 20_000)
		clientStore, ok := store.(interface {
			GetTiKVClient() tikv.Client
			SetTiKVClient(tikv.Client)
		})
		require.True(t, ok)
		inner := clientStore.GetTiKVClient()
		client.Client = inner
		clientStore.SetTiKVClient(client)
		t.Cleanup(func() { clientStore.SetTiKVClient(inner) })
	} else {
		store = testkit.CreateMockStore(t, mockstore.WithClientHijacker(func(inner tikv.Client) tikv.Client {
			client.Client = inner
			return client
		}))
	}

	setup := testkit.NewTestKit(t, store)
	setup.MustExec("use test")
	setup.MustExec("set global tidb_table_cache_lease = 10")
	setup.MustExec("create table cached_lease_retry (id int primary key, v int, pad longblob)")
	setup.MustExec("create table cached_lease_retry_sink (id int primary key, copied_v int)")
	setup.MustExec("insert into cached_lease_retry values (1, 0, repeat('x', 4 * 1024 * 1024))")
	setup.MustExec("alter table cached_lease_retry cache")
	setup.MustQuery("select @@tidb_enable_metadata_lock").Check(testkit.Rows("1"))

	is := setup.Session().GetInfoSchema().(infoschema.InfoSchema)
	tbl, err := is.TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("cached_lease_retry"))
	require.NoError(t, err)
	remote := tables.NewStateRemote(setup.Session())

	leaseAcquired := make(chan uint64, 1)
	tables.TestAINativeWriteLeaseAcquired = leaseAcquired
	tables.TestAINativeStopWriteLeaseRenewal.Store(true)
	t.Cleanup(func() {
		tables.TestAINativeStopWriteLeaseRenewal.Store(false)
		tables.TestAINativeWriteLeaseAcquired = nil
	})

	writer := testkit.NewTestKit(t, store)
	reader := testkit.NewTestKit(t, store)
	observer := testkit.NewTestKit(t, store)
	writer.MustExec("use test")
	reader.MustExec("use test")
	observer.MustExec("use test")

	writer.MustExec("begin optimistic")
	writer.MustExec("update cached_lease_retry set v = 1 where id = 1")
	startTS := writer.Session().GetSessionVars().TxnCtx.StartTS
	require.NotZero(t, startTS)
	client.targetStartTS.Store(startTS)

	commitDone := make(chan error, 1)
	go func() { commitDone <- writer.ExecToErr("commit") }()
	var writeLease uint64
	select {
	case writeLease = <-leaseAcquired:
		require.NotZero(t, writeLease)
	case <-time.After(10 * time.Second):
		t.Fatal("cached-table WRITE lease was not acquired")
	}
	select {
	case <-client.blocked:
	case <-time.After(10 * time.Second):
		t.Fatal("target primary Commit did not reach the hold point")
	}

	// The large row keeps the TiKV lock alive beyond the fixed five-second
	// cached-table WRITE lease. Wait for the observed lease, not a guessed delay.
	require.Eventually(t, func() bool {
		return time.Now().After(oracle.GetTimeFromTS(writeLease).Add(100 * time.Millisecond))
	}, 7*time.Second, 25*time.Millisecond, "the observed cached-table WRITE lease must expire")
	lockType, remoteLease, err := remote.Load(context.Background(), tbl.Meta().ID)
	require.NoError(t, err)
	require.Equal(t, tables.CachedTableLockWrite, lockType)
	require.Equal(t, writeLease, remoteLease, "the stopped writer must leave its original lease in remote state")

	readerDone := make(chan string, 1)
	go func() {
		readerDone <- fmt.Sprint(reader.MustQuery("select v from cached_lease_retry where id = 1").Rows())
	}()

	require.Eventually(t, func() bool {
		lockType, _, loadErr := remote.Load(context.Background(), tbl.Meta().ID)
		return loadErr == nil && lockType == tables.CachedTableLockRead
	}, 5*time.Second, 50*time.Millisecond, "a reader must take the expired cached-table write lease")
	select {
	case <-client.minTSPushed:
	case <-time.After(10 * time.Second):
		t.Fatal("reader did not push the target primary minCommitTS")
	}

	close(client.release)
	select {
	case err = <-commitDone:
		if expectFixed {
			require.ErrorContains(t, err, "check commit ts upper bound fail")
		} else {
			require.NoError(t, err, "current source retries with a new commitTS and reports success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("writer Commit did not finish")
	}
	select {
	case <-client.naturalExpiry:
	case <-time.After(5 * time.Second):
		t.Fatal("TiKV did not reject the first Commit with CommitTsExpired")
	}
	require.Equal(t, "[[0]]", <-readerDone)
	if expectFixed {
		require.Equal(t, int32(1), client.commitCalls.Load(), "the rejected replacement commitTS must not reach TiKV")
		observer.MustQuery("select v from cached_lease_retry where id = 1").Check(testkit.Rows("0"))
		observer.MustExec("alter table cached_lease_retry nocache")
		observer.MustQuery("select v from cached_lease_retry where id = 1").Check(testkit.Rows("0"))
		return
	}
	require.Equal(t, int32(2), client.commitCalls.Load())

	var staleRows string
	require.Eventually(t, func() bool {
		staleRows = fmt.Sprint(observer.MustQuery("select v from cached_lease_retry where id = 1").Rows())
		return lastReadFromCache(observer)
	}, 5*time.Second, 50*time.Millisecond, "the pre-commit snapshot must become the active table cache")
	require.Equal(t, "[[0]]", staleRows, "a post-commit cached read exposes the old value")
	observer.MustExec("insert into cached_lease_retry_sink select id, v from cached_lease_retry")
	require.True(t, lastReadFromCache(observer), "the durable copy must consume the stale table cache")

	observer.MustExec("alter table cached_lease_retry nocache")
	observer.MustQuery("select v from cached_lease_retry where id = 1").Check(testkit.Rows("1"))
	observer.MustQuery("select copied_v from cached_lease_retry_sink where id = 1").Check(testkit.Rows("0"))
}
```

Run the local TiKV-backed RED evidence test:

```bash
go test -tags=intest ./pkg/table/tables \
  -run '^TestAINativeCommitTSRetryCannotCrossCachedTableWriteLease$' \
  -count=1 -v
```

The default mode asserts the complete unsafe outcome, including durable sink divergence, and passes
on current source. To run it as a conventional regression expectation that must fail on current
source:

```bash
AI_NATIVE_EXPECT_COMMIT_TS_UPPER_BOUND_FIX=1 \
go test -tags=intest ./pkg/table/tables \
  -run '^TestAINativeCommitTSRetryCannotCrossCachedTableWriteLease$' \
  -count=1 -v
```

For a real TiKV run, start a one-TiKV playground and run the same test:

```bash
tiup playground nightly --db=0 --kv=1 --tiflash=0

AI_NATIVE_REAL_TIKV=1 \
go test -tags=intest ./pkg/table/tables \
  -run '^TestAINativeCommitTSRetryCannotCrossCachedTableWriteLease$' \
  -count=1 -v
```

The test restores the production client-go `ManagedLockTTL=20s` because realtikvtest otherwise
shortens it to five seconds. That shortened test setting makes the reader roll back the lock and is
not the production path.

### 2. What did you expect to see? (Required)

Every commitTS candidate must pass the cached-table WRITE-lease upper-bound checker. If TiKV rejects
the initial Commit and client-go obtains a replacement commitTS outside the lease, client-go must
reject that candidate before sending another Commit. SQL COMMIT must fail, and a successful cached
read must not coexist with a different fresh source value.

### 3. What did you see instead (Required)

On pinned real TiKV, the first Commit naturally returned:

```text
CommitTsExpired:
  StartTs           = 467666559668060171
  AttemptedCommitTs = 467666559668060175
  MinCommitTs       = 467666561005780998
```

Current client-go then sent a second Commit and SQL COMMIT returned success. The final observations
were:

```text
Commit RPC count:                  2
post-commit cached source value:   0
post-NOCACHE fresh source value:   1
regular sink copied from cache:    0
```

This is not only a transient stale read. The regular sink durably contains a value copied from the
stale cache while the source contains the committed new value.

The source ownership gap is:

1. TiDB `session.commitTxn` acquires/renews cached-table WRITE leases and installs
   `cachedTableRenewLease.commitTSCheck`.
2. client-go `twoPhaseCommitter.execute` runs `commitTSUpperBoundCheck` for the initial commitTS.
3. A reader's TiKV `CheckTxnStatus` can push a still-live normal primary lock's `minCommitTS`.
4. TiKV Commit rejects `commitTS < lock.minCommitTS` with `CommitTsExpired`.
5. client-go's `CommitTsExpired` branch gets a replacement TSO, updates `c.commitTS` and the
   request, and retries without rerunning `commitTSUpperBoundCheck`.

The initial proof is value-scoped: it proves `commitTS1 < lease`. It does not prove
`commitTS2 < lease` after replacement.

As an exact counterfactual, rerun the existing checker immediately after obtaining the replacement
TSO and before updating `c.commitTS` or the request. The identical local and real-TiKV schedules
still receive the first natural `CommitTsExpired`, but the checker is called twice, only one Commit
RPC reaches TiKV, SQL COMMIT fails, and cached/fresh source values both remain 0.

### 4. What is your TiDB version? (Required)

```text
TiDB:           b8d04e17a2ca61eee1220c5ce2d641a376f75e9b
tikv/client-go: 01bd8f99f4da23c6fc9d671eecc0166c7b6ceb9b
TiKV:           7ecce12e7573f7d4a392877b994fa6af80606369
```

The SQL-level run used ordinary optimistic 2PC with
`@@tidb_enable_metadata_lock=1`. Setting the commitTS upper-bound checker disables 1PC and async
commit for this transaction.
