package ddl_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/ddl/placement"
	"github.com/pingcap/tidb/pkg/domain"
	"github.com/pingcap/tidb/pkg/domain/infosync"
	"github.com/pingcap/tidb/pkg/meta/model"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/testkit/external"
	"github.com/pingcap/tidb/pkg/testkit/testfailpoint"
	"github.com/stretchr/testify/require"
)

const setTiFlashReplicaExternalHook = "github.com/pingcap/tidb/pkg/ddl/afterSetTiFlashReplicaExternal"

func setupAvailableTiFlashReplica(t *testing.T) (*testkit.TestKit, *testkit.TestKit, *testkit.TestKit, int64) {
	t.Helper()
	testfailpoint.Enable(t, "github.com/pingcap/tidb/pkg/infoschema/mockTiFlashStoreCount", "return(true)")
	store := testkit.CreateMockStoreWithSchemaLease(t, 50*time.Millisecond)
	setup := testkit.NewTestKit(t, store)
	alter := testkit.NewTestKit(t, store)
	cancel := testkit.NewTestKit(t, store)
	setUpMockTiFlash(t)
	for _, tk := range []*testkit.TestKit{setup, alter, cancel} {
		tk.MustExec("use test")
	}
	setup.MustExec("create table ai_tiflash_cancel_t(id int primary key)")
	setup.MustExec("alter table ai_tiflash_cancel_t set tiflash replica 1")
	tbl := external.GetTableByName(t, setup, "test", "ai_tiflash_cancel_t")
	require.NoError(t, domain.GetDomain(setup.Session()).DDLExecutor().UpdateTableReplicaInfo(
		setup.Session(), tbl.Meta().ID, true))
	return setup, alter, cancel, tbl.Meta().ID
}

func tiFlashRuleCount(t *testing.T, tableID int64) (int, bool) {
	t.Helper()
	rules, err := infosync.GetTiFlashGroupRules(context.Background(), placement.TiFlashRuleGroupID)
	require.NoError(t, err)
	wantID := infosync.MakeRuleID(tableID)
	for _, rule := range rules {
		if rule.ID == wantID {
			return rule.Count, true
		}
	}
	return 0, false
}

func TestAINativeSetTiFlashReplicaCancelExternalDriftRED(t *testing.T) {
	setup, alter, cancel, tableID := setupAvailableTiFlashReplica(t)
	count, ok := tiFlashRuleCount(t, tableID)
	require.True(t, ok)
	require.Equal(t, 1, count)

	reached := make(chan int64, 1)
	release := make(chan struct{})
	testfailpoint.EnableCall(t, setTiFlashReplicaExternalHook, func(job *model.Job) {
		if job.Type == model.ActionSetTiFlashReplica {
			reached <- job.ID
			<-release
		}
	})
	done := make(chan error, 1)
	go func() {
		_, err := alter.Exec("alter table ai_tiflash_cancel_t set tiflash replica 0")
		done <- err
	}()
	jobID := <-reached
	_, ok = tiFlashRuleCount(t, tableID)
	require.False(t, ok)
	cancel.MustExec(fmt.Sprintf("admin cancel ddl jobs %d", jobID))
	close(release)
	require.Error(t, <-done)

	tbl := external.GetTableByName(t, setup, "test", "ai_tiflash_cancel_t")
	require.NotNil(t, tbl.Meta().TiFlashReplica)
	require.Equal(t, uint64(1), tbl.Meta().TiFlashReplica.Count)
	require.True(t, tbl.Meta().TiFlashReplica.Available)
	count, ok = tiFlashRuleCount(t, tableID)
	require.True(t, ok, "cancelled DDL must restore the PD rule required by committed metadata")
	require.Equal(t, 1, count)
}

func TestAINativeSetTiFlashReplicaNormalGreen(t *testing.T) {
	setup, alter, _, tableID := setupAvailableTiFlashReplica(t)
	alter.MustExec("alter table ai_tiflash_cancel_t set tiflash replica 0")
	tbl := external.GetTableByName(t, setup, "test", "ai_tiflash_cancel_t")
	require.Nil(t, tbl.Meta().TiFlashReplica)
	_, ok := tiFlashRuleCount(t, tableID)
	require.False(t, ok)
}

func TestAINativeSetTiFlashReplicaCompensationGreen(t *testing.T) {
	setup, alter, cancel, tableID := setupAvailableTiFlashReplica(t)
	reached := make(chan int64, 1)
	release := make(chan struct{})
	testfailpoint.EnableCall(t, setTiFlashReplicaExternalHook, func(job *model.Job) {
		if job.Type == model.ActionSetTiFlashReplica {
			reached <- job.ID
			<-release
		}
	})
	done := make(chan error, 1)
	go func() {
		_, err := alter.Exec("alter table ai_tiflash_cancel_t set tiflash replica 0")
		done <- err
	}()
	jobID := <-reached
	cancel.MustExec(fmt.Sprintf("admin cancel ddl jobs %d", jobID))
	close(release)
	require.Error(t, <-done)

	tbl := external.GetTableByName(t, setup, "test", "ai_tiflash_cancel_t")
	require.NotNil(t, tbl.Meta().TiFlashReplica)
	require.NoError(t, infosync.ConfigureTiFlashPDForTable(
		tableID, tbl.Meta().TiFlashReplica.Count, &tbl.Meta().TiFlashReplica.LocationLabels))
	count, ok := tiFlashRuleCount(t, tableID)
	require.True(t, ok)
	require.Equal(t, 1, count)
}
