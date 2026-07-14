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

package txntest

import (
	"context"
	"testing"
	"time"

	"github.com/pingcap/kvproto/pkg/debugpb"
	"github.com/pingcap/tidb/pkg/kv"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/tests/realtikvtest"
	"github.com/stretchr/testify/require"
	"github.com/tikv/client-go/v2/tikv"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type gcControllableStore interface {
	GC(context.Context, uint64, ...tikv.GCOpt) (uint64, error)
	UpdateTxnSafePointCache(uint64, time.Time)
}

func forceTiKVGCCompaction(t *testing.T, store kv.Storage) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stores, err := store.(kv.StorageWithPD).GetPDClient().GetAllStores(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, stores)
	for _, storeMeta := range stores {
		if storeMeta.GetAddress() == "" {
			continue
		}
		conn, err := grpc.NewClient(
			storeMeta.GetAddress(),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		require.NoError(t, err)
		client := debugpb.NewDebugClient(conn)
		for _, cf := range []string{"write", "default"} {
			_, err = client.Compact(ctx, &debugpb.CompactRequest{
				Db:                        debugpb.DB_KV,
				Cf:                        cf,
				Threads:                   1,
				BottommostLevelCompaction: debugpb.BottommostLevelCompaction_Force,
			})
			require.NoError(t, err)
		}
		require.NoError(t, conn.Close())
	}
}

func TestExpiredOptimisticTxnCannotResurrectDeletedRow(t *testing.T) {
	store := realtikvtest.CreateMockStoreAndSetup(t)
	tk1 := testkit.NewTestKit(t, store)
	tk2 := testkit.NewTestKit(t, store)
	tk3 := testkit.NewTestKit(t, store)
	for _, tk := range []*testkit.TestKit{tk1, tk2, tk3} {
		tk.MustExec("use test")
		tk.MustExec("set @@tidb_enable_1pc = off")
		tk.MustExec("set @@tidb_enable_async_commit = off")
	}
	// Classic clusters upgraded from before DML assertions may retain OFF.
	tk1.MustExec("set @@tidb_txn_assertion_level = off")
	tk1.MustQuery("select @@tidb_enable_metadata_lock").Check(testkit.Rows("1"))
	tk1.MustQuery("select @@tidb_enable_1pc, @@tidb_enable_async_commit").Check(testkit.Rows("0 0"))
	tk1.MustQuery("select @@tidb_txn_assertion_level").Check(testkit.Rows("OFF"))

	tk1.MustExec("create table gc_resurrection (id bigint primary key, v bigint)")
	tk1.MustExec("insert into gc_resurrection values (1, 10)")
	tk1.MustExec("begin optimistic")
	tk1.MustExec("update gc_resurrection set v = 11 where id = 1")
	tk2.MustExec("delete from gc_resurrection where id = 1")
	deleteCommitTS := tk2.Session().GetSessionVars().LastCommitTS
	require.NotZero(t, deleteCommitTS)

	// This compresses only the production wait for tidb_gc_max_wait_time and the
	// next GC round. GC, TiKV's safe-point poll, and compaction are real.
	gcStore := store.(gcControllableStore)
	newSafePoint, err := gcStore.GC(context.Background(), deleteCommitTS)
	require.NoError(t, err)
	require.GreaterOrEqual(t, newSafePoint, deleteCommitTS)
	time.Sleep(12 * time.Second)
	forceTiKVGCCompaction(t, store)
	gcStore.UpdateTxnSafePointCache(newSafePoint, time.Now())

	err = tk1.ExecToErr("commit")
	t.Logf("expired transaction commit result: %v", err)
	result := tk3.MustQuery("select id, v from gc_resurrection")
	t.Logf("fresh row state after commit: %v", result.Rows())
	require.ErrorContains(t, err, "GC life time is shorter than transaction duration")
	result.Check(testkit.Rows())
}
