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

package addindextest_test

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pingcap/failpoint"
	"github.com/pingcap/tidb/pkg/ddl"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/tests/realtikvtest"
	"github.com/stretchr/testify/require"
)

// This test is not product validation coverage for its own sake; it is a deterministic bug-hunt harness
// for the source obligation around global-index locking in txn backfill with async commit.
func TestAINativeAddUniqueGlobalIndexWithAsyncCommitPartitionMove(t *testing.T) {
	store := realtikvtest.CreateMockStoreAndSetup(t)

	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t_ai_global_async")
	tk.MustExec("set @@global.tidb_enable_dist_task = off")
	tk.MustExec("set @@global.tidb_ddl_enable_fast_reorg = off")
	tk.MustExec("set @@global.tidb_ddl_reorg_worker_cnt = 1")
	tk.MustExec("set @@global.tidb_ddl_reorg_batch_size = 16")
	tk.MustExec(`create table t_ai_global_async (
		a int not null,
		b int not null,
		pad varchar(64) not null default '',
		key(a)
	) partition by hash(a) partitions 5`)

	const initialRows = 2000
	values := make([]string, 0, initialRows)
	for i := 1; i <= initialRows; i++ {
		values = append(values, fmt.Sprintf("(%d,%d,repeat('x',64))", i, i))
	}
	tk.MustExec("insert into t_ai_global_async values " + join(values))

	tkDML := testkit.NewTestKit(t, store)
	tkDML.MustExec("use test")
	tkDML.MustExec("set @@tidb_enable_async_commit = 1")
	tkDML.MustExec("set @@tidb_enable_1pc = 0")
	tkDML.MustExec("set @@tidb_txn_mode = 'pessimistic'")

	var (
		nextVal int64 = initialRows
		callCnt atomic.Int64
		errMu   sync.Mutex
		dmlErr  error
	)
	recordErr := func(err error) {
		if err == nil {
			return
		}
		errMu.Lock()
		defer errMu.Unlock()
		if dmlErr == nil {
			dmlErr = err
		}
	}

	ddl.MockDMLExecution = func() {
		tmp := int(atomic.AddInt64(&nextVal, 1))
		callCnt.Add(1)

		if _, err := tkDML.Exec("begin pessimistic"); err != nil {
			recordErr(fmt.Errorf("begin pessimistic tmp=%d: %w", tmp, err))
			return
		}
		if _, err := tkDML.Exec(fmt.Sprintf("insert into t_ai_global_async values (%d,%d,repeat('x',64))", tmp, tmp)); err != nil {
			_, _ = tkDML.Exec("rollback")
			recordErr(fmt.Errorf("insert tmp=%d: %w", tmp, err))
			return
		}
		if _, err := tkDML.Exec(fmt.Sprintf("update t_ai_global_async set b = b + 1000000, a = b where b = %d", tmp-1)); err != nil {
			_, _ = tkDML.Exec("rollback")
			recordErr(fmt.Errorf("update prev=%d tmp=%d: %w", tmp-1, tmp, err))
			return
		}
		if _, err := tkDML.Exec("commit"); err != nil {
			_, _ = tkDML.Exec("rollback")
			recordErr(fmt.Errorf("commit tmp=%d: %w", tmp, err))
			return
		}
	}
	require.NoError(t, failpoint.Enable("github.com/pingcap/tidb/pkg/ddl/mockDMLExecution", "return(true)"))
	t.Cleanup(func() {
		ddl.MockDMLExecution = nil
		require.NoError(t, failpoint.Disable("github.com/pingcap/tidb/pkg/ddl/mockDMLExecution"))
	})

	tk.MustExec("alter table t_ai_global_async add unique index idx_b(b) global")

	errMu.Lock()
	require.NoError(t, dmlErr)
	errMu.Unlock()

	tk.MustExec("admin check table t_ai_global_async")
	expectedRows := initialRows + int(callCnt.Load())
	tk.MustQuery("select count(*) from t_ai_global_async").Check(testkit.Rows(fmt.Sprintf("%d", expectedRows)))
	tk.MustQuery("select count(*) from (select b from t_ai_global_async group by b having count(*) > 1) x").Check(testkit.Rows("0"))

	rsIndex := tk.MustQuery("select a,b from t_ai_global_async use index(idx_b) order by b, a").Rows()
	rsTable := tk.MustQuery("select a,b from t_ai_global_async ignore index(idx_b) order by b, a").Rows()
	require.Equal(t, rsTable, rsIndex)
}

// Mixed global/local multi-schema change exercises a higher-risk obligation than the
// single-index case: concurrent async-commit DML must keep both the new global unique
// index and the co-created local index consistent while rows move partitions.
func TestAINativeAddMixedGlobalLocalIndexesWithAsyncCommitPartitionMove(t *testing.T) {
	store := realtikvtest.CreateMockStoreAndSetup(t)

	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t_ai_mixed_global_async")
	tk.MustExec("set @@global.tidb_enable_dist_task = off")
	tk.MustExec("set @@global.tidb_ddl_enable_fast_reorg = off")
	tk.MustExec("set @@global.tidb_ddl_reorg_worker_cnt = 1")
	tk.MustExec("set @@global.tidb_ddl_reorg_batch_size = 16")
	tk.MustExec(`create table t_ai_mixed_global_async (
		a int not null,
		b int not null,
		c int not null,
		pad varchar(64) not null default '',
		key(a)
	) partition by hash(a) partitions 5`)

	const initialRows = 2000
	values := make([]string, 0, initialRows)
	for i := 1; i <= initialRows; i++ {
		values = append(values, fmt.Sprintf("(%d,%d,%d,repeat('x',64))", i, i, i))
	}
	tk.MustExec("insert into t_ai_mixed_global_async values " + join(values))

	tkDML := testkit.NewTestKit(t, store)
	tkDML.MustExec("use test")
	tkDML.MustExec("set @@tidb_enable_async_commit = 1")
	tkDML.MustExec("set @@tidb_enable_1pc = 0")
	tkDML.MustExec("set @@tidb_txn_mode = 'pessimistic'")

	var (
		nextVal int64 = initialRows
		callCnt atomic.Int64
		errMu   sync.Mutex
		dmlErr  error
	)
	recordErr := func(err error) {
		if err == nil {
			return
		}
		errMu.Lock()
		defer errMu.Unlock()
		if dmlErr == nil {
			dmlErr = err
		}
	}

	ddl.MockDMLExecution = func() {
		tmp := int(atomic.AddInt64(&nextVal, 1))
		callCnt.Add(1)

		if _, err := tkDML.Exec("begin pessimistic"); err != nil {
			recordErr(fmt.Errorf("begin pessimistic tmp=%d: %w", tmp, err))
			return
		}
		if _, err := tkDML.Exec(fmt.Sprintf("insert into t_ai_mixed_global_async values (%d,%d,%d,repeat('x',64))", tmp, tmp, tmp)); err != nil {
			_, _ = tkDML.Exec("rollback")
			recordErr(fmt.Errorf("insert tmp=%d: %w", tmp, err))
			return
		}
		if _, err := tkDML.Exec(fmt.Sprintf(
			"update t_ai_mixed_global_async set c = c + 1000000, b = b + 1000000, a = c where c = %d",
			tmp-1,
		)); err != nil {
			_, _ = tkDML.Exec("rollback")
			recordErr(fmt.Errorf("update prev=%d tmp=%d: %w", tmp-1, tmp, err))
			return
		}
		if _, err := tkDML.Exec("commit"); err != nil {
			_, _ = tkDML.Exec("rollback")
			recordErr(fmt.Errorf("commit tmp=%d: %w", tmp, err))
			return
		}
	}
	require.NoError(t, failpoint.Enable("github.com/pingcap/tidb/pkg/ddl/mockDMLExecution", "return(true)"))
	t.Cleanup(func() {
		ddl.MockDMLExecution = nil
		require.NoError(t, failpoint.Disable("github.com/pingcap/tidb/pkg/ddl/mockDMLExecution"))
	})

	tk.MustExec("alter table t_ai_mixed_global_async add unique index idx_b(b) global, add index idx_c(c)")

	errMu.Lock()
	require.NoError(t, dmlErr)
	errMu.Unlock()

	tk.MustExec("admin check table t_ai_mixed_global_async")
	expectedRows := initialRows + int(callCnt.Load())
	tk.MustQuery("select count(*) from t_ai_mixed_global_async").Check(testkit.Rows(fmt.Sprintf("%d", expectedRows)))
	tk.MustQuery("select count(*) from (select b from t_ai_mixed_global_async group by b having count(*) > 1) x").Check(testkit.Rows("0"))

	rsByBIndex := tk.MustQuery("select a,b,c from t_ai_mixed_global_async use index(idx_b) order by b, a, c").Rows()
	rsByBTable := tk.MustQuery("select a,b,c from t_ai_mixed_global_async ignore index(idx_b, idx_c) order by b, a, c").Rows()
	require.Equal(t, rsByBTable, rsByBIndex)

	rsByCIndex := tk.MustQuery("select a,b,c from t_ai_mixed_global_async use index(idx_c) order by c, a, b").Rows()
	rsByCTable := tk.MustQuery("select a,b,c from t_ai_mixed_global_async ignore index(idx_b, idx_c) order by c, a, b").Rows()
	require.Equal(t, rsByCTable, rsByCIndex)
}

func join(items []string) string {
	if len(items) == 0 {
		return ""
	}
	out := items[0]
	for i := 1; i < len(items); i++ {
		out += "," + items[i]
	}
	return out
}
