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
	"fmt"
	"testing"
	"time"

	"github.com/pingcap/failpoint"
	"github.com/pingcap/tidb/pkg/domain/serverinfo"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/tests/realtikvtest"
	"github.com/stretchr/testify/require"
)

func TestAINativeRealTiKVMDLServerInfoRestartPutFailure(t *testing.T) {
	store, dom := realtikvtest.CreateMockStoreAndDomainAndSetup(t)
	oldTxn := testkit.NewTestKit(t, store)
	ddlTK := testkit.NewTestKit(t, store)

	oldTxn.MustExec("use test")
	oldTxn.MustExec("create table ai_mdl_server_info_restart (id int primary key, v int)")
	oldTxn.MustQuery("show global variables like 'tidb_enable_metadata_lock'").Check(
		testkit.Rows("tidb_enable_metadata_lock ON"),
	)
	oldTxn.MustExec("begin pessimistic")
	oldTxn.MustExec("insert into ai_mdl_server_info_restart values (1, 10)")

	infoSyncer := dom.InfoSyncer().ServerInfoSyncer()
	serverInfoKey := fmt.Sprintf("%s/%s", serverinfo.ServerInformationPath, infoSyncer.GetLocalServerInfo().ID)
	etcdCli := dom.GetEtcdClient()
	resp, err := etcdCli.Get(context.Background(), serverInfoKey)
	require.NoError(t, err)
	require.Len(t, resp.Kvs, 1)
	require.NotZero(t, resp.Kvs[0].Lease)

	pauseFailpoint := "github.com/pingcap/tidb/pkg/domain/serverinfo/beforeServerInfoRestart"
	require.NoError(t, failpoint.Enable(pauseFailpoint, "pause"))
	t.Cleanup(func() { _ = failpoint.Disable(pauseFailpoint) })
	closeFailpoint := "github.com/pingcap/tidb/pkg/domain/serverinfo/mockCloseServerInfoSession"
	require.NoError(t, failpoint.Enable(closeFailpoint, "1*return()"))
	t.Cleanup(func() { _ = failpoint.Disable(closeFailpoint) })
	select {
	case <-infoSyncer.Done():
	case <-time.After(10 * time.Second):
		require.FailNow(t, "server info session did not close")
	}

	require.Eventually(t, func() bool {
		resp, err = etcdCli.Get(context.Background(), serverInfoKey)
		return err == nil && len(resp.Kvs) == 0
	}, 10*time.Second, 50*time.Millisecond)
	failpointName := "github.com/pingcap/tidb/pkg/domain/serverinfo/mockStoreServerInfoError"
	require.NoError(t, failpoint.Enable(failpointName, "1*return()"))
	t.Cleanup(func() { _ = failpoint.Disable(failpointName) })
	require.NoError(t, failpoint.Disable(pauseFailpoint))

	// The one-shot failure is over, but the live unpublished replacement suppresses another restart.
	time.Sleep(500 * time.Millisecond)
	resp, err = etcdCli.Get(context.Background(), serverInfoKey)
	require.NoError(t, err)
	require.Empty(t, resp.Kvs)

	ddlDone := make(chan error, 1)
	go func() {
		ddlDone <- ddlTK.ExecToErr("alter table test.ai_mdl_server_info_restart add index idx_v(v)")
	}()

	// A broken membership snapshot lets DDL finish before the old transaction.
	// A correct implementation keeps DDL waiting; commit then releases it.
	var ddlErr error
	ddlFinishedBeforeCommit := false
	select {
	case ddlErr = <-ddlDone:
		ddlFinishedBeforeCommit = true
	case <-time.After(10 * time.Second):
	}

	commitErr := oldTxn.ExecToErr("commit")
	if !ddlFinishedBeforeCommit {
		select {
		case ddlErr = <-ddlDone:
		case <-time.After(30 * time.Second):
			require.FailNow(t, "DDL did not finish after the old transaction committed")
		}
	}
	require.NoError(t, ddlErr)
	require.NoError(t, commitErr)
	tableRows := oldTxn.MustQuery("select id, v from ai_mdl_server_info_restart order by id").Rows()
	indexRows := oldTxn.MustQuery("select /*+ use_index(ai_mdl_server_info_restart, idx_v) */ id, v from ai_mdl_server_info_restart order by id").Rows()
	adminErr := oldTxn.ExecToErr("admin check table ai_mdl_server_info_restart")
	t.Logf("ddlFinishedBeforeCommit=%v tableRows=%v indexRows=%v adminErr=%v", ddlFinishedBeforeCommit, tableRows, indexRows, adminErr)

	require.Equal(t, tableRows, indexRows, "a successful old-schema commit must be represented in the new index")
	require.NoError(t, adminErr)
}
