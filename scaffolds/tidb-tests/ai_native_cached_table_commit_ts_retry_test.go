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
