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
// See the License for the specific language governing permissions and
// limitations under the License.

package addindextest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pingcap/failpoint"
	"github.com/pingcap/tidb/pkg/ddl"
	"github.com/pingcap/tidb/pkg/domain"
	"github.com/pingcap/tidb/pkg/meta/model"
	"github.com/pingcap/tidb/pkg/session/txninfo"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/testkit/testfailpoint"
	"github.com/pingcap/tidb/tests/realtikvtest"
	"github.com/stretchr/testify/require"
	"github.com/tikv/client-go/v2/txnkv/transaction"
)

// TestPessimisticLockNotFoundDuringFastAddIndexMerge is a deterministic
// mechanism proof for the conflict fixed by #62387. It explicitly models a
// missing DDL row guard and a retried prewrite; it does not by itself prove that
// both conditions are naturally reachable in this exact version. The user
// transaction gets its forUpdateTS before the DDL merge commits the normal
// non-unique index key, then retries its prewrite.
func TestPessimisticLockNotFoundDuringFastAddIndexMerge(t *testing.T) {
	store := realtikvtest.CreateMockStoreAndSetup(t)
	tkDDL := testkit.NewTestKit(t, store)
	tkSeed := testkit.NewTestKit(t, store)
	tkDML := testkit.NewTestKit(t, store)
	for _, tk := range []*testkit.TestKit{tkDDL, tkSeed, tkDML} {
		tk.MustExec("use test")
	}
	tkDDL.MustExec("set global tidb_enable_metadata_lock = on")
	tkDDL.MustExec("set global tidb_ddl_enable_fast_reorg = on")
	tkDDL.MustExec("set global tidb_enable_dist_task = off")
	tkDML.MustExec("set tidb_txn_mode = 'pessimistic'")
	tkDDL.MustExec("drop table if exists t_pess_nf")
	tkDDL.MustExec("create table t_pess_nf (id int primary key, k int not null, payload int, key existing_k(k))")
	tkDDL.MustExec("insert into t_pess_nf values (1, 0, 0)")

	var seeded atomic.Bool
	var targetStarted atomic.Bool
	targetPaused := make(chan struct{})
	targetErrCh := make(chan error, 1)
	hookErrCh := make(chan error, 2)
	mergeCommitted := make(chan struct{})
	allowMergeToContinue := make(chan struct{})
	var targetPauseOnce sync.Once
	var allowMergeOnce sync.Once
	var retryInjected atomic.Bool
	var targetTxnStartTS atomic.Uint64
	var targetLockForUpdateTS atomic.Uint64
	var targetForUpdateTS atomic.Uint64
	allowMerge := func() { allowMergeOnce.Do(func() { close(allowMergeToContinue) }) }

	defer func() {
		allowMerge()
		_ = failpoint.Disable("tikvclient/beforePessimisticLock")
		_ = failpoint.Disable("tikvclient/beforePrewrite")
		transaction.MockBeforePessimisticLock = nil
		transaction.MockForcePrewriteRetryRequest = nil
		ddl.MockDMLExecutionStateBeforeImport = nil
		ddl.MockDMLExecutionMerging = nil
		ddl.MockSkipMergeRowLock = nil
	}()

	testfailpoint.Enable(t, "github.com/pingcap/tidb/pkg/ddl/skipReorgWorkForTempIndex", "return(false)")
	testfailpoint.Enable(t, "github.com/pingcap/tidb/pkg/ddl/ingest/skipDiskFullCheckForTest", "return(true)")
	// Model the protection gap described by #62337: merge writes only the
	// secondary index key, so locking the row cannot refresh forUpdateTS.
	ddl.MockSkipMergeRowLock = func() bool { return true }
	ddl.MockDMLExecutionStateBeforeImport = func() {
		if !seeded.CompareAndSwap(false, true) {
			return
		}
		if _, err := tkSeed.Exec("update t_pess_nf set k = 1 where id = 1"); err != nil {
			hookErrCh <- fmt.Errorf("seed temp index record: %w", err)
		}
	}
	testfailpoint.Enable(t, "github.com/pingcap/tidb/pkg/ddl/mockDMLExecutionStateBeforeImport", "return(true)")

	dom := domain.GetDomain(tkDDL.Session())
	testfailpoint.EnableCall(t, "github.com/pingcap/tidb/pkg/ddl/onJobUpdated", func(job *model.Job) {
		if job.TableName != "t_pess_nf" || job.SchemaState != model.StateWriteReorganization {
			return
		}
		tbl, ok := dom.InfoSchema().TableByID(context.Background(), job.TableID)
		if !ok {
			return
		}
		idx := tbl.Meta().FindIndexByName("idx_k")
		if idx == nil {
			return
		}

		if idx.BackfillState != model.BackfillStateMerging || !targetStarted.CompareAndSwap(false, true) {
			return
		}
		if !seeded.Load() {
			hookErrCh <- fmt.Errorf("seed DML did not run before merge")
			return
		}
		if err := failpoint.Enable("tikvclient/beforePessimisticLock", "pause"); err != nil {
			hookErrCh <- err
			return
		}
		if err := failpoint.Enable("tikvclient/beforePrewrite", "pause"); err != nil {
			hookErrCh <- err
			return
		}
		if _, err := tkDML.Exec("begin pessimistic"); err != nil {
			hookErrCh <- fmt.Errorf("begin target transaction: %w", err)
			return
		}
		txnInfo := tkDML.Session().TxnInfo()
		if txnInfo == nil {
			hookErrCh <- fmt.Errorf("target transaction has no transaction info")
			return
		}
		targetTxnStartTS.Store(txnInfo.StartTS)
		transaction.MockBeforePessimisticLock = func(startTS, forUpdateTS uint64) {
			if startTS != targetTxnStartTS.Load() {
				return
			}
			targetLockForUpdateTS.Store(forUpdateTS)
			targetPauseOnce.Do(func() { close(targetPaused) })
		}
		go func() {
			_, err := tkDML.Exec("update t_pess_nf set k = 2, payload = payload + 1 where id = 1")
			if err == nil {
				_, err = tkDML.Exec("commit")
			}
			targetErrCh <- err
		}()
	})

	var mergeOnce sync.Once
	ddl.MockDMLExecutionMerging = func() {
		mergeOnce.Do(func() {
			close(mergeCommitted)
			<-allowMergeToContinue
		})
	}
	testfailpoint.Enable(t, "github.com/pingcap/tidb/pkg/ddl/mockDMLExecutionMerging", "return(true)")

	ddlErrCh := make(chan error, 1)
	go func() {
		_, err := tkDDL.Exec("alter table t_pess_nf add index idx_k(k)")
		ddlErrCh <- err
	}()

	select {
	case <-targetPaused:
	case err := <-hookErrCh:
		require.NoError(t, err)
	case <-time.After(20 * time.Second):
		require.FailNow(t, "target DML did not start in merging state")
	}
	targetStartTS := targetTxnStartTS.Load()
	require.NotZero(t, targetStartTS)
	require.NotZero(t, targetLockForUpdateTS.Load())
	select {
	case <-mergeCommitted:
	case err := <-hookErrCh:
		require.NoError(t, err)
	case <-time.After(20 * time.Second):
		require.FailNow(t, "DDL merge transaction did not commit")
	}

	require.NoError(t, failpoint.Disable("tikvclient/beforePessimisticLock"))
	require.Eventually(t, func() bool {
		info := tkDML.Session().TxnInfo()
		return info != nil && info.State == txninfo.TxnCommitting
	}, 10*time.Second, 10*time.Millisecond)

	// A retried prewrite asks TiKV to distinguish an idempotent retry from a
	// first attempt; NonLockKeyConflict is emitted only on this retry path.
	transaction.MockForcePrewriteRetryRequest = func(startTS, forUpdateTS uint64) bool {
		if startTS != targetStartTS {
			return false
		}
		targetForUpdateTS.Store(forUpdateTS)
		retryInjected.Store(true)
		return true
	}
	require.NoError(t, failpoint.Disable("tikvclient/beforePrewrite"))
	targetErr := <-targetErrCh
	require.True(t, retryInjected.Load())
	t.Logf("target startTS=%d lockForUpdateTS=%d prewriteForUpdateTS=%d",
		targetStartTS, targetLockForUpdateTS.Load(), targetForUpdateTS.Load())
	require.Error(t, targetErr)
	require.True(t, strings.Contains(targetErr.Error(), "PessimisticLockNotFound"), targetErr.Error())
	require.True(t, strings.Contains(targetErr.Error(), "NonLockKeyConflict"), targetErr.Error())

	allowMerge()
	require.NoError(t, <-ddlErrCh)
	tkDDL.MustExec("admin check table t_pess_nf")
}
