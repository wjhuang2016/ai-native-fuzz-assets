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

package sessiontxn_test

import (
	"testing"
	"time"

	"github.com/pingcap/failpoint"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/util/breakpoint"
	"github.com/stretchr/testify/require"
)

func TestAILastInsertIDFromFailedPessimisticAttemptIsNotPublished(t *testing.T) {
	store, _ := setupTxnContextTest(t)

	coordinator := testkit.NewTestKit(t, store)
	coordinator.MustExec("use test")
	coordinator.MustExec("create table retry_last_id (id int primary key, u int unique, v bigint)")
	coordinator.MustExec("create table retry_last_id_gate (id int primary key)")
	coordinator.MustExec("create table retry_last_id_sink (v bigint)")
	coordinator.MustExec("insert into retry_last_id values (1, 10, 0)")

	retrying := testkit.NewTestKit(t, store)
	retrying.MustExec("use test")
	retrying.MustExec("set tidb_txn_mode = 'pessimistic'")
	retrying.MustExec("set tx_isolation = 'READ-COMMITTED'")
	retrying.MustExec("set tidb_pessimistic_txn_fair_locking = 0")
	retrying.MustQuery("select last_insert_id(7)").Check(testkit.Rows("7"))
	retrying.MustExec("begin")

	const breakPointName = "aiAfterDMLExecutionBeforeLock"
	const breakPointPath = "github.com/pingcap/tidb/pkg/util/breakpoint/" + breakPointName
	require.NoError(t, failpoint.Enable(breakPointPath, "return(true)"))
	t.Cleanup(func() {
		_ = failpoint.Disable(breakPointPath)
	})

	stopped := make(chan string, 2)
	resume := make(chan struct{})
	retrying.Session().SetValue(breakpoint.NotifyBreakPointFuncKey, func(name string) {
		stopped <- name
		<-resume
	})

	stmtErr := make(chan error, 1)
	go func() {
		rs, err := retrying.Exec("update retry_last_id as t set u = 1, v = last_insert_id(99) where id = 1 and not exists (select 1 from retry_last_id_gate as g where g.id = t.id)")
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
	coordinator.MustExec("begin")
	coordinator.MustExec("insert into retry_last_id values (2, 1, 0)")
	coordinator.MustExec("insert into retry_last_id_gate values (1)")
	coordinator.MustExec("commit")
	close(resume)

	var updateErr error
	select {
	case updateErr = <-stmtErr:
	case <-time.After(10 * time.Second):
		require.FailNow(t, "retried DML did not finish")
	}
	t.Logf("retry breakpoint hits after resume=%d updateErr=%v", len(stopped), updateErr)
	require.NoError(t, updateErr)
	require.NoError(t, failpoint.Disable(breakPointPath))
	retrying.MustExec("commit")

	coordinator.MustQuery("select id, u, v from retry_last_id order by id").Check(testkit.Rows("1 10 0", "2 1 0"))
	t.Logf("published last_insert_id after zero-match retry=%v", retrying.MustQuery("select last_insert_id()").Rows())
	retrying.MustExec("insert into retry_last_id_sink values (last_insert_id())")
	coordinator.MustQuery("select v from retry_last_id_sink").Check(testkit.Rows("7"))
}
