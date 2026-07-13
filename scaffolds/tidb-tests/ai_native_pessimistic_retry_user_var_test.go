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

package sessiontxn_test

import (
	"testing"
	"time"

	"github.com/pingcap/failpoint"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/util/breakpoint"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAIPessimisticRetryReplaysSetVarSideEffect(t *testing.T) {
	store, _ := setupTxnContextTest(t)

	control := testkit.NewTestKit(t, store)
	control.MustExec("use test")
	control.MustExec("set tidb_txn_mode = 'pessimistic'")
	control.MustExec("set @x = 0")
	control.MustExec("begin")
	control.MustExec("update t1 set v = (@x := @x + 1) where id = 1")
	control.MustExec("commit")
	control.MustQuery("select v from t1 where id = 1").Check(testkit.Rows("1"))
	control.MustQuery("select @x").Check(testkit.Rows("1"))

	control.MustExec("update t1 set v = 10 where id = 1")

	retrying := testkit.NewTestKit(t, store)
	retrying.MustExec("use test")
	retrying.MustExec("set tidb_txn_mode = 'pessimistic'")
	retrying.MustExec("set tidb_pessimistic_txn_fair_locking = 0")
	retrying.MustExec("set @x = 0")
	retrying.MustExec("begin")

	const lockConflict = "github.com/pingcap/tidb/pkg/executor/aiPessimisticLockErrorAfterDMLExecution"
	require.NoError(t, failpoint.Enable(lockConflict, "1*return(true)"))
	t.Cleanup(func() {
		_ = failpoint.Disable(lockConflict)
	})
	retrying.MustExec("update t1 set v = (@x := @x + 1) where id = 1")
	require.NoError(t, failpoint.Disable(lockConflict))
	retrying.MustExec("commit")

	retrying.MustQuery("select v, @x from t1 where id = 1").Check(testkit.Rows("1 1"))
}

func TestAIPessimisticRetrySetVarControls(t *testing.T) {
	store, _ := setupTxnContextTest(t)

	const afterExecutionConflict = "github.com/pingcap/tidb/pkg/executor/aiPessimisticLockErrorAfterDMLExecution"
	const beforeEvaluationConflict = "github.com/pingcap/tidb/pkg/store/mockstore/unistore/tikv/pessimisticLockReturnWriteConflict"
	t.Cleanup(func() {
		_ = failpoint.Disable(afterExecutionConflict)
		_ = failpoint.Disable(beforeEvaluationConflict)
	})

	idempotent := testkit.NewTestKit(t, store)
	idempotent.MustExec("use test")
	idempotent.MustExec("set tidb_txn_mode = 'pessimistic'")
	idempotent.MustExec("set @x = 0")
	idempotent.MustExec("begin")
	require.NoError(t, failpoint.Enable(afterExecutionConflict, "1*return(true)"))
	idempotent.MustExec("update t1 set v = (@x := 7) where id = 1")
	require.NoError(t, failpoint.Disable(afterExecutionConflict))
	idempotent.MustExec("commit")
	idempotent.MustQuery("select v, @x from t1 where id = 1").Check(testkit.Rows("7 7"))

	idempotent.MustExec("update t1 set v = 10 where id = 1")

	preEvaluation := testkit.NewTestKit(t, store)
	preEvaluation.MustExec("use test")
	preEvaluation.MustExec("set tidb_txn_mode = 'pessimistic'")
	preEvaluation.MustExec("set @x = 0")
	preEvaluation.MustExec("begin")
	require.NoError(t, failpoint.Enable(beforeEvaluationConflict, "1*return(true)"))
	preEvaluation.MustExec("update t1 set v = (@x := @x + 1) where id = 1")
	require.NoError(t, failpoint.Disable(beforeEvaluationConflict))
	preEvaluation.MustExec("commit")
	preEvaluation.MustQuery("select v, @x from t1 where id = 1").Check(testkit.Rows("1 1"))
}

func TestAINaturalPessimisticRetryChangesUniqueKeyExpression(t *testing.T) {
	store, _ := setupTxnContextTest(t)

	coordinator := testkit.NewTestKit(t, store)
	coordinator.MustExec("use test")
	coordinator.MustExec("create table side_effect_retry (id int primary key, u int unique)")
	coordinator.MustExec("insert into side_effect_retry values (1, 10)")

	retrying := testkit.NewTestKit(t, store)
	retrying.MustExec("use test")
	retrying.MustExec("set tidb_txn_mode = 'pessimistic'")
	retrying.MustExec("set tidb_pessimistic_txn_fair_locking = 0")
	retrying.MustExec("set @x = 0")
	retrying.MustExec("begin")

	const breakPointName = "aiAfterDMLExecutionBeforeLock"
	const breakPointPath = "github.com/pingcap/tidb/pkg/util/breakpoint/" + breakPointName
	require.NoError(t, failpoint.Enable(breakPointPath, "return(true)"))
	t.Cleanup(func() {
		_ = failpoint.Disable(breakPointPath)
	})

	stopped := make(chan string, 1)
	resume := make(chan struct{})
	retrying.Session().SetValue(breakpoint.NotifyBreakPointFuncKey, func(name string) {
		stopped <- name
		<-resume
	})

	stmtErr := make(chan error, 1)
	go func() {
		rs, err := retrying.Exec("update side_effect_retry set u = (@x := @x + 1) where id = 1")
		if rs != nil {
			_ = rs.Close()
		}
		stmtErr <- err
	}()

	select {
	case name := <-stopped:
		require.Equal(t, breakPointName, name)
	case <-time.After(10 * time.Second):
		require.FailNow(t, "DML did not reach the post-execution lock boundary")
	}
	coordinator.MustExec("insert into side_effect_retry values (2, 1)")
	close(resume)

	var err error
	select {
	case err = <-stmtErr:
	case <-time.After(10 * time.Second):
		require.FailNow(t, "retried DML did not finish")
	}
	require.NoError(t, failpoint.Disable(breakPointPath))
	retrying.MustExec("commit")

	assert.Error(t, err, "the concurrent unique key must still cause a duplicate-key error after retry")
	coordinator.MustQuery("select id, u from side_effect_retry order by id").Check(testkit.Rows("1 10", "2 1"))
}
