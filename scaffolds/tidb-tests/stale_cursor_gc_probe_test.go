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

package staticrecordset_test

import (
	"testing"

	"github.com/pingcap/tidb/pkg/parser/mysql"
	"github.com/pingcap/tidb/pkg/session/cursor"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/util/sqlexec"
	"github.com/stretchr/testify/require"
)

func TestProbeStaleReadCursorProtectsItsReadTS(t *testing.T) {
	store, dom := testkit.CreateMockStoreAndDomain(t)
	tk := testkit.NewTestKit(t, store)
	tk.Session().GetSessionVars().SetStatusFlag(mysql.ServerStatusCursorExists, true)

	tk.MustExec("use test")
	tk.MustExec("create table stale_cursor_probe(id int primary key, v int)")
	tk.MustExec("insert into stale_cursor_probe values (1, 1), (2, 2), (3, 3)")
	tk.MustExec("begin")
	externalTS := tk.MustQuery("select @@tidb_current_ts").Rows()[0][0]
	tk.MustExec("set global tidb_external_ts = @@tidb_current_ts")
	tk.MustExec("commit")
	tk.MustExec("set tidb_enable_external_ts_read = ON")
	require.Equal(t, externalTS, tk.MustQuery("select @@global.tidb_external_ts").Rows()[0][0])

	rs, err := tk.Exec("select * from stale_cursor_probe order by id")
	require.NoError(t, err)
	vars := tk.Session().GetSessionVars()
	readTS := vars.TxnCtx.StaleReadTs
	require.NotZero(t, readTS)
	require.Zero(t, vars.TxnCtx.StartTS)

	detached, ok, err := rs.(sqlexec.DetachableRecordSet).TryDetach()
	require.True(t, ok)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, detached.Close()) })

	var cursorTS uint64
	tk.Session().GetCursorTracker().RangeCursor(func(handle cursor.Handle) bool {
		cursorTS = handle.GetState().StartTS
		return true
	})

	// Replace the active statement owner. The detached cursor must protect its
	// snapshot independently of later activity on the same session.
	tk.MustExec("set tidb_enable_external_ts_read = OFF")
	tk.MustExec("begin")
	t.Cleanup(func() { tk.MustExec("rollback") })
	secondStartTS := vars.TxnCtx.StartTS
	require.Greater(t, secondStartTS, readTS)
	dom.InfoSyncer().ReportMinStartTS(store, nil)
	reportedMinStartTS := dom.InfoSyncer().GetMinStartTS()
	t.Logf("read_ts=%d cursor_start_ts=%d second_txn_start_ts=%d reported_min_start_ts=%d",
		readTS, cursorTS, secondStartTS, reportedMinStartTS)

	require.Equal(t, readTS, cursorTS, "detached stale-read cursor must keep its actual read TS alive for GC")
	require.LessOrEqual(t, reportedMinStartTS, readTS, "reported minStartTS must protect the cursor's read TS")
}
