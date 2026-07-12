package ddl_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/ddl/placement"
	"github.com/pingcap/tidb/pkg/domain"
	"github.com/pingcap/tidb/pkg/domain/infosync"
	"github.com/pingcap/tidb/pkg/meta/model"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/testkit/testfailpoint"
	"github.com/stretchr/testify/require"
)

const alterTablePlacementExternalHook = "github.com/pingcap/tidb/pkg/ddl/afterAlterTablePlacementExternal"

func placementBundleViews(t *testing.T, tk *testkit.TestKit, tableID int64) (string, string) {
	t.Helper()
	metaBundle, ok := domain.GetDomain(tk.Session()).InfoSchema().PlacementBundleByPhysicalTableID(tableID)
	require.True(t, ok)
	pdBundle, err := infosync.GetRuleBundle(context.Background(), placement.GroupID(tableID))
	require.NoError(t, err)
	metaJSON, err := json.Marshal(metaBundle)
	require.NoError(t, err)
	pdJSON, err := json.Marshal(pdBundle)
	require.NoError(t, err)
	return string(metaJSON), string(pdJSON)
}

func setupPlacementTable(t *testing.T) (*testkit.TestKit, *testkit.TestKit, *testkit.TestKit, int64) {
	t.Helper()
	store, dom := testkit.CreateMockStoreAndDomain(t)
	tkSetup := testkit.NewTestKit(t, store)
	tkAlter := testkit.NewTestKit(t, store)
	tkCancel := testkit.NewTestKit(t, store)
	tkSetup.MustExec("use test")
	tkAlter.MustExec("use test")
	tkCancel.MustExec("use test")
	tkSetup.MustExec("create placement policy ai_p1 primary_region='r1' regions='r1'")
	tkSetup.MustExec("create placement policy ai_p2 primary_region='r2' regions='r2'")
	tkSetup.MustExec("create table ai_placement_t(id int primary key) placement policy ai_p1")
	tbl, err := dom.InfoSchema().TableByName(context.Background(), ast.NewCIStr("test"), ast.NewCIStr("ai_placement_t"))
	require.NoError(t, err)
	return tkSetup, tkAlter, tkCancel, tbl.Meta().ID
}

func TestAINativeAlterTablePlacementCancelExternalDriftRED(t *testing.T) {
	tkSetup, tkAlter, tkCancel, tableID := setupPlacementTable(t)
	beforeMeta, beforePD := placementBundleViews(t, tkSetup, tableID)
	require.JSONEq(t, beforeMeta, beforePD)

	reached := make(chan int64, 1)
	release := make(chan struct{})
	testfailpoint.EnableCall(t, alterTablePlacementExternalHook, func(job *model.Job) {
		if job.Type != model.ActionAlterTablePlacement {
			return
		}
		reached <- job.ID
		<-release
	})

	alterDone := make(chan error, 1)
	go func() {
		_, err := tkAlter.Exec("alter table ai_placement_t placement policy ai_p2")
		alterDone <- err
	}()

	var jobID int64
	select {
	case jobID = <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("ALTER TABLE placement did not reach the post-PD hook")
	}
	tkCancel.MustExec(fmt.Sprintf("admin cancel ddl jobs %d", jobID))
	close(release)
	require.Error(t, <-alterDone)

	showCreate := fmt.Sprint(tkSetup.MustQuery("show create table ai_placement_t").Rows())
	require.Contains(t, showCreate, "PLACEMENT POLICY=`ai_p1`")
	metaJSON, pdJSON := placementBundleViews(t, tkSetup, tableID)
	require.JSONEq(t, beforeMeta, metaJSON)
	require.Contains(t, pdJSON, "r1",
		"a cancelled ALTER must not leave PD on the uncommitted placement policy")
}

func TestAINativeAlterTablePlacementNormalGreen(t *testing.T) {
	tkSetup, tkAlter, _, tableID := setupPlacementTable(t)
	tkAlter.MustExec("alter table ai_placement_t placement policy ai_p2")
	metaJSON, pdJSON := placementBundleViews(t, tkSetup, tableID)
	require.JSONEq(t, metaJSON, pdJSON)
	require.Contains(t, metaJSON, "r2")
}

func TestAINativeAlterTablePlacementCompensationGreen(t *testing.T) {
	tkSetup, tkAlter, tkCancel, tableID := setupPlacementTable(t)
	beforeMeta, _ := placementBundleViews(t, tkSetup, tableID)

	reached := make(chan int64, 1)
	release := make(chan struct{})
	testfailpoint.EnableCall(t, alterTablePlacementExternalHook, func(job *model.Job) {
		if job.Type == model.ActionAlterTablePlacement {
			reached <- job.ID
			<-release
		}
	})
	alterDone := make(chan error, 1)
	go func() {
		_, err := tkAlter.Exec("alter table ai_placement_t placement policy ai_p2")
		alterDone <- err
	}()
	var jobID int64
	select {
	case jobID = <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("ALTER TABLE placement did not reach the post-PD hook")
	}
	tkCancel.MustExec(fmt.Sprintf("admin cancel ddl jobs %d", jobID))
	close(release)
	require.Error(t, <-alterDone)

	metaBundle, ok := domain.GetDomain(tkSetup.Session()).InfoSchema().PlacementBundleByPhysicalTableID(tableID)
	require.True(t, ok)
	require.NoError(t, infosync.PutRuleBundlesWithDefaultRetry(context.Background(), []*placement.Bundle{metaBundle}))
	metaJSON, pdJSON := placementBundleViews(t, tkSetup, tableID)
	require.JSONEq(t, beforeMeta, metaJSON)
	require.JSONEq(t, metaJSON, pdJSON)
}
