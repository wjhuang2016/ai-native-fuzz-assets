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
	"fmt"
	"testing"
	"time"

	"github.com/pingcap/failpoint"
	"github.com/pingcap/tidb/pkg/server"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/util/dbterror/plannererrors"
	"github.com/pingcap/tidb/tests/realtikvtest"
	"github.com/stretchr/testify/require"
)

func TestAINativeFKCascadeRegistersChildMDL(t *testing.T) {
	store, dom := realtikvtest.CreateMockStoreAndDomainAndSetup(t)
	sv := server.CreateMockServer(t, store)
	sv.SetDomain(dom)
	dom.InfoSyncer().SetSessionManager(sv)
	defer sv.Close()

	writerConn := server.CreateMockConn(t, sv)
	writer := testkit.NewTestKitWithSession(t, store, writerConn.Context().Session)
	ddlConn := server.CreateMockConn(t, sv)
	ddlTK := testkit.NewTestKitWithSession(t, store, ddlConn.Context().Session)
	observerConn := server.CreateMockConn(t, sv)
	observer := testkit.NewTestKitWithSession(t, store, observerConn.Context().Session)

	writer.MustExec("use test")
	ddlTK.MustExec("use test")
	observer.MustExec("use test")
	writer.MustQuery("select @@global.tidb_enable_metadata_lock").Check(testkit.Rows("1"))
	writer.MustExec("drop table if exists ai_mdl_child, ai_mdl_parent")
	writer.MustExec("create table ai_mdl_parent(id int primary key)")
	writer.MustExec(`create table ai_mdl_child(
		id int primary key,
		pid int not null,
		v int not null,
		key pid_idx(pid),
		constraint fk_pid foreign key(pid) references ai_mdl_parent(id) on delete cascade
	)`)
	writer.MustExec("insert into ai_mdl_parent values (1)")
	writer.MustExec("insert into ai_mdl_child values (10, 1, 100)")

	writer.MustExec("begin pessimistic")
	writer.MustExec("delete from ai_mdl_parent where id = 1")
	writer.MustQuery("select * from ai_mdl_child").Check(testkit.Rows())

	ddlDone := make(chan error, 1)
	go func() {
		ddlDone <- ddlTK.ExecToErr("alter table ai_mdl_child add index v_idx(v)")
	}()

	select {
	case err := <-ddlDone:
		require.FailNow(t, "DDL bypassed child-table MDL held by FK cascade", "err=%v", err)
	case <-time.After(500 * time.Millisecond):
	}

	writer.MustExec("commit")
	select {
	case err := <-ddlDone:
		require.NoError(t, err)
	case <-time.After(15 * time.Second):
		require.FailNow(t, "DDL remained blocked after cascade transaction committed")
	}

	observer.MustQuery("select * from ai_mdl_parent").Check(testkit.Rows())
	observer.MustQuery("select * from ai_mdl_child").Check(testkit.Rows())
	observer.MustExec("admin check table ai_mdl_child")
}

func resetFKCascadeRetryTables(tk *testkit.TestKit, suffix string) {
	grand := "ai_fk_grand_" + suffix
	child := "ai_fk_child_" + suffix
	parent := "ai_fk_parent_" + suffix
	tk.MustExec(fmt.Sprintf("drop table if exists %s, %s, %s", grand, child, parent))
	tk.MustExec(fmt.Sprintf("create table %s(id int primary key, v int not null)", parent))
	tk.MustExec(fmt.Sprintf(`create table %s(
		id int primary key,
		pid int not null,
		v int not null,
		key pid_idx(pid),
		constraint fk_%s_parent foreign key(pid) references %s(id) on delete cascade
	)`, child, suffix, parent))
	tk.MustExec(fmt.Sprintf(`create table %s(
		id int primary key,
		cid int not null,
		v int not null,
		key cid_idx(cid),
		constraint fk_%s_child foreign key(cid) references %s(id) on delete cascade
	)`, grand, suffix, child))
	tk.MustExec(fmt.Sprintf("insert into %s values (1, 10), (2, 20)", parent))
	tk.MustExec(fmt.Sprintf("insert into %s values (10, 1, 100), (20, 2, 200)", child))
	tk.MustExec(fmt.Sprintf("insert into %s values (100, 10, 1000), (200, 20, 2000)", grand))
}

func fkCascadeRetryState(tk *testkit.TestKit, suffix string) string {
	parent := fmt.Sprint(tk.MustQuery("select id, v from ai_fk_parent_" + suffix + " order by id").Rows())
	child := fmt.Sprint(tk.MustQuery("select id, pid, v from ai_fk_child_" + suffix + " order by id").Rows())
	grand := fmt.Sprint(tk.MustQuery("select id, cid, v from ai_fk_grand_" + suffix + " order by id").Rows())
	return parent + "|" + child + "|" + grand
}

func checkFKCascadeRetryClosure(t *testing.T, tk *testkit.TestKit, suffix string) {
	parent := "ai_fk_parent_" + suffix
	child := "ai_fk_child_" + suffix
	grand := "ai_fk_grand_" + suffix
	tk.MustExec("admin check table " + parent)
	tk.MustExec("admin check table " + child)
	tk.MustExec("admin check table " + grand)
	tk.MustQuery(fmt.Sprintf("select count(*) from %s c left join %s p on c.pid = p.id where p.id is null", child, parent)).Check(testkit.Rows("0"))
	tk.MustQuery(fmt.Sprintf("select count(*) from %s g left join %s c on g.cid = c.id where c.id is null", grand, child)).Check(testkit.Rows("0"))
	recordChild := fmt.Sprint(tk.MustQuery(fmt.Sprintf("select id, pid, v from %s ignore index(primary, pid_idx) order by id", child)).Rows())
	indexChild := fmt.Sprint(tk.MustQuery(fmt.Sprintf("select id, pid, v from %s use index(pid_idx) order by id", child)).Rows())
	require.Equal(t, recordChild, indexChild)
	recordGrand := fmt.Sprint(tk.MustQuery(fmt.Sprintf("select id, cid, v from %s ignore index(primary, cid_idx) order by id", grand)).Rows())
	indexGrand := fmt.Sprint(tk.MustQuery(fmt.Sprintf("select id, cid, v from %s use index(cid_idx) order by id", grand)).Rows())
	require.Equal(t, recordGrand, indexGrand)
}

func TestAINativeAutocommitRetryRebuildsFKCascade(t *testing.T) {
	store := realtikvtest.CreateMockStoreAndSetup(t)
	setup := testkit.NewTestKit(t, store)
	writer := testkit.NewTestKit(t, store)
	blocker := testkit.NewTestKit(t, store)
	for _, tk := range []*testkit.TestKit{setup, writer, blocker} {
		tk.MustExec("use test")
		tk.MustExec("set @@tidb_txn_mode = ''")
	}
	writer.MustQuery("select @@autocommit, @@tidb_disable_txn_auto_retry, @@global.tidb_enable_metadata_lock").Check(testkit.Rows("1 1 1"))

	resetFKCascadeRetryTables(setup, "control")
	resetFKCascadeRetryTables(setup, "probe")
	setup.MustExec("delete from ai_fk_parent_control where id = 1")

	require.NoError(t, failpoint.Enable(
		"github.com/pingcap/tidb/pkg/session/aiNativeBeforeTxnCommit",
		`return(1000)`,
	))
	defer func() {
		_ = failpoint.Disable("github.com/pingcap/tidb/pkg/session/aiNativeBeforeTxnCommit")
	}()
	resultCh := make(chan error, 1)
	go func() {
		resultCh <- writer.ExecToErr("delete from ai_fk_parent_probe where id = 1")
	}()
	time.Sleep(200 * time.Millisecond)
	select {
	case err := <-resultCh:
		require.FailNow(t, "DELETE did not reach the pre-commit pause", "err=%v", err)
	default:
	}
	require.NoError(t, failpoint.Disable("github.com/pingcap/tidb/pkg/session/aiNativeBeforeTxnCommit"))
	blocker.MustExec("update ai_fk_parent_probe set v = 11 where id = 1")
	select {
	case err := <-resultCh:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		require.FailNow(t, "DELETE did not finish after the conflicting parent update")
	}
	require.GreaterOrEqual(t, writer.Session().GetSessionVars().StmtCtx.ExecRetryCount, uint64(1),
		"the probe did not replay after the real TiKV write conflict")

	require.Equal(t, fkCascadeRetryState(setup, "control"), fkCascadeRetryState(setup, "probe"))
	checkFKCascadeRetryClosure(t, setup, "control")
	checkFKCascadeRetryClosure(t, setup, "probe")
}

func TestAINativeAutocommitRetryDropsStaleFKCascadeSet(t *testing.T) {
	store := realtikvtest.CreateMockStoreAndSetup(t)
	setup := testkit.NewTestKit(t, store)
	writer := testkit.NewTestKit(t, store)
	blocker := testkit.NewTestKit(t, store)
	for _, tk := range []*testkit.TestKit{setup, writer, blocker} {
		tk.MustExec("use test")
		tk.MustExec("set @@tidb_txn_mode = ''")
	}
	writer.MustQuery("select @@autocommit, @@global.tidb_enable_metadata_lock").Check(testkit.Rows("1 1"))

	resetFKCascadeRetryTables(setup, "control_set")
	resetFKCascadeRetryTables(setup, "probe_set")
	setup.MustExec("update ai_fk_child_control_set set pid = 2 where id = 10")
	setup.MustExec("delete from ai_fk_parent_control_set where id = 1")

	require.NoError(t, failpoint.Enable(
		"github.com/pingcap/tidb/pkg/session/aiNativeBeforeTxnCommit",
		`return(1000)`,
	))
	defer func() {
		_ = failpoint.Disable("github.com/pingcap/tidb/pkg/session/aiNativeBeforeTxnCommit")
	}()
	resultCh := make(chan error, 1)
	go func() {
		resultCh <- writer.ExecToErr("delete from ai_fk_parent_probe_set where id = 1")
	}()
	time.Sleep(200 * time.Millisecond)
	select {
	case err := <-resultCh:
		require.FailNow(t, "DELETE did not reach the pre-commit pause", "err=%v", err)
	default:
	}
	require.NoError(t, failpoint.Disable("github.com/pingcap/tidb/pkg/session/aiNativeBeforeTxnCommit"))
	blocker.MustExec("update ai_fk_child_probe_set set pid = 2 where id = 10")
	select {
	case err := <-resultCh:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		require.FailNow(t, "DELETE did not finish after the child owner changed")
	}
	require.GreaterOrEqual(t, writer.Session().GetSessionVars().StmtCtx.ExecRetryCount, uint64(1),
		"the changed cascade set did not enter transaction replay")

	require.Equal(t, fkCascadeRetryState(setup, "control_set"), fkCascadeRetryState(setup, "probe_set"))
	checkFKCascadeRetryClosure(t, setup, "control_set")
	checkFKCascadeRetryClosure(t, setup, "probe_set")
}

func resetFKCheckRetryTables(tk *testkit.TestKit, suffix string) {
	child := "ai_fk_check_child_" + suffix
	parent := "ai_fk_check_parent_" + suffix
	tk.MustExec(fmt.Sprintf("drop table if exists %s, %s", child, parent))
	tk.MustExec(fmt.Sprintf("create table %s(id int primary key, v int not null)", parent))
	tk.MustExec(fmt.Sprintf(`create table %s(
		id int primary key,
		pid int not null,
		v int not null,
		key pid_idx(pid),
		constraint fk_check_%s foreign key(pid) references %s(id)
	)`, child, suffix, parent))
	tk.MustExec(fmt.Sprintf("insert into %s values (1, 10)", parent))
}

// The first attempt validates and lock-marks the parent key. Deleting the
// parent before prewrite forces a real TiKV conflict; retry must re-run the FK
// check rather than reuse the first attempt's positive result.
func TestAINativeAutocommitRetryRechecksMissingFKParent(t *testing.T) {
	store := realtikvtest.CreateMockStoreAndSetup(t)
	setup := testkit.NewTestKit(t, store)
	writer := testkit.NewTestKit(t, store)
	blocker := testkit.NewTestKit(t, store)
	for _, tk := range []*testkit.TestKit{setup, writer, blocker} {
		tk.MustExec("use test")
		tk.MustExec("set @@tidb_txn_mode = ''")
	}
	writer.MustQuery("select @@autocommit, @@tidb_disable_txn_auto_retry, @@global.tidb_enable_metadata_lock, @@foreign_key_checks").
		Check(testkit.Rows("1 1 1 1"))

	resetFKCheckRetryTables(setup, "control")
	resetFKCheckRetryTables(setup, "probe")
	setup.MustExec("delete from ai_fk_check_parent_control where id = 1")
	controlErr := setup.ExecToErr("insert into ai_fk_check_child_control values (10, 1, 100)")
	require.Error(t, controlErr)
	require.True(t, plannererrors.ErrNoReferencedRow2.Equal(controlErr), controlErr.Error())

	require.NoError(t, failpoint.Enable(
		"github.com/pingcap/tidb/pkg/session/aiNativeBeforeTxnCommit",
		`return(1000)`,
	))
	defer func() {
		_ = failpoint.Disable("github.com/pingcap/tidb/pkg/session/aiNativeBeforeTxnCommit")
	}()
	resultCh := make(chan error, 1)
	go func() {
		resultCh <- writer.ExecToErr("insert into ai_fk_check_child_probe values (10, 1, 100)")
	}()
	time.Sleep(200 * time.Millisecond)
	select {
	case err := <-resultCh:
		require.FailNow(t, "INSERT did not reach the pre-commit pause", "err=%v", err)
	default:
	}
	require.NoError(t, failpoint.Disable("github.com/pingcap/tidb/pkg/session/aiNativeBeforeTxnCommit"))
	blocker.MustExec("delete from ai_fk_check_parent_probe where id = 1")

	select {
	case err := <-resultCh:
		require.Error(t, err)
		require.True(t, plannererrors.ErrNoReferencedRow2.Equal(err), err.Error())
	case <-time.After(10 * time.Second):
		require.FailNow(t, "INSERT did not finish after the parent was deleted")
	}
	require.GreaterOrEqual(t, writer.Session().GetSessionVars().StmtCtx.ExecRetryCount, uint64(1),
		"the parent lock-only conflict did not enter transaction replay")

	setup.MustQuery("select * from ai_fk_check_parent_probe").Check(testkit.Rows())
	setup.MustQuery("select * from ai_fk_check_child_probe").Check(testkit.Rows())
	setup.MustExec("admin check table ai_fk_check_parent_probe")
	setup.MustExec("admin check table ai_fk_check_child_probe")
	setup.MustQuery(`select count(*) from ai_fk_check_child_probe c
		left join ai_fk_check_parent_probe p on c.pid = p.id where p.id is null`).Check(testkit.Rows("0"))
	setup.MustQuery("select id, pid, v from ai_fk_check_child_probe use index(pid_idx)").Check(testkit.Rows())
}
