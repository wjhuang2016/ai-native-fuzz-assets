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

package session

import (
	"context"
	"testing"

	"github.com/pingcap/failpoint"
	"github.com/stretchr/testify/require"
)

func TestAINativeSavepointRollbackTruncatesRetryHistory(t *testing.T) {
	store, dom := CreateStoreAndBootstrap(t)
	t.Cleanup(func() {
		dom.Close()
		require.NoError(t, store.Close())
	})

	se, err := createSession(store)
	require.NoError(t, err)
	t.Cleanup(func() { se.Close() })

	MustExec(t, se, "use test")
	MustExec(t, se, "set @@session.tidb_txn_mode = 'optimistic'")
	MustExec(t, se, "set @@session.tidb_disable_txn_auto_retry = off")
	MustExec(t, se, "set @@session.tidb_retry_limit = 3")
	MustExec(t, se, "create table ai_savepoint_retry (id int primary key, v int)")

	runSchedule := func(t *testing.T, forceOneRetry bool) {
		require.NoError(t, failpoint.Enable(
			"github.com/pingcap/tidb/pkg/sessiontxn/isolation/injectOptimisticTxnRetryable",
			`return(true)`,
		))
		t.Cleanup(func() {
			require.NoError(t, failpoint.Disable("github.com/pingcap/tidb/pkg/sessiontxn/isolation/injectOptimisticTxnRetryable"))
		})

		MustExec(t, se, "truncate table ai_savepoint_retry")
		MustExec(t, se, "begin optimistic")
		t.Logf("after BEGIN: couldRetry=%t history=%d", se.sessionVars.TxnCtx.CouldRetry, GetHistory(se).Count())
		MustExec(t, se, "savepoint s")
		t.Logf("after SAVEPOINT: couldRetry=%t history=%d", se.sessionVars.TxnCtx.CouldRetry, GetHistory(se).Count())
		MustExec(t, se, "insert into ai_savepoint_retry values (1, 10)")
		t.Logf("after rolled-back INSERT: couldRetry=%t history=%d", se.sessionVars.TxnCtx.CouldRetry, GetHistory(se).Count())
		MustExec(t, se, "rollback to savepoint s")
		t.Logf("after ROLLBACK TO: couldRetry=%t history=%d", se.sessionVars.TxnCtx.CouldRetry, GetHistory(se).Count())
		MustExec(t, se, "insert into ai_savepoint_retry values (2, 20)")
		t.Logf("after surviving INSERT: couldRetry=%t history=%d", se.sessionVars.TxnCtx.CouldRetry, GetHistory(se).Count())

		if forceOneRetry {
			require.NoError(t, failpoint.Enable(
				"github.com/pingcap/tidb/pkg/session/mockCommitError8942",
				`1*return(true)->return(false)`,
			))
			t.Cleanup(func() {
				require.NoError(t, failpoint.Disable("github.com/pingcap/tidb/pkg/session/mockCommitError8942"))
			})
		}

		MustExec(t, se, "commit")
		t.Logf("after COMMIT: execRetryCount=%d", se.sessionVars.StmtCtx.ExecRetryCount)
		rs := MustExecToRecodeSet(t, se, "select id, v from ai_savepoint_retry order by id")
		rows, err := ResultSetToStringSlice(context.Background(), se, rs)
		require.NoError(t, err)
		require.Equal(t, [][]string{{"2", "20"}}, rows)
	}

	t.Run("no retry", func(t *testing.T) {
		runSchedule(t, false)
	})
	t.Run("one transparent commit retry", func(t *testing.T) {
		runSchedule(t, true)
	})
}
