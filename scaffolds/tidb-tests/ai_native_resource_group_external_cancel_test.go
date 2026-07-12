package tests

import (
	"fmt"
	"testing"

	"github.com/pingcap/tidb/pkg/meta/model"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/testkit/testfailpoint"
	"github.com/stretchr/testify/require"
)

const alterResourceGroupExternalHook = "github.com/pingcap/tidb/pkg/ddl/afterModifyResourceGroupExternal"

func resourceGroupViews(t *testing.T, tk *testkit.TestKit, name string) (string, string) {
	metaRows := tk.MustQuery(fmt.Sprintf("SHOW CREATE RESOURCE GROUP `%s`", name)).Rows()
	runtimeRows := tk.MustQuery(fmt.Sprintf(
		"SELECT RU_PER_SEC, PRIORITY, BURSTABLE FROM INFORMATION_SCHEMA.RESOURCE_GROUPS WHERE NAME = '%s'",
		name,
	)).Rows()
	require.Len(t, metaRows, 1)
	require.Len(t, runtimeRows, 1)
	return fmt.Sprint(metaRows[0]), fmt.Sprint(runtimeRows[0])
}

func TestAINativeAlterResourceGroupCancelExternalDriftRED(t *testing.T) {
	store := testkit.CreateMockStore(t)
	tkSetup := testkit.NewTestKit(t, store)
	tkAlter := testkit.NewTestKit(t, store)
	tkCancel := testkit.NewTestKit(t, store)
	tkObserve := testkit.NewTestKit(t, store)

	tkSetup.MustExec("SET GLOBAL tidb_enable_resource_control = 'on'")
	tkSetup.MustExec("CREATE RESOURCE GROUP ai_rg_cancel RU_PER_SEC=1000 PRIORITY=LOW")

	reached := make(chan int64, 1)
	release := make(chan struct{})
	testfailpoint.EnableCall(t, alterResourceGroupExternalHook, func(job *model.Job) {
		if job.Type != model.ActionAlterResourceGroup || job.SchemaName != "ai_rg_cancel" {
			return
		}
		reached <- job.ID
		<-release
	})

	alterDone := make(chan error, 1)
	go func() {
		_, err := tkAlter.Exec("ALTER RESOURCE GROUP ai_rg_cancel RU_PER_SEC=1 PRIORITY=HIGH")
		alterDone <- err
	}()

	jobID := <-reached
	tkCancel.MustExec(fmt.Sprintf("ADMIN CANCEL DDL JOBS %d", jobID))
	close(release)
	alterErr := <-alterDone
	require.Error(t, alterErr)

	metadataView, runtimeView := resourceGroupViews(t, tkObserve, "ai_rg_cancel")
	t.Logf("cancelled alter error: %v", alterErr)
	t.Logf("metadata owner: %s", metadataView)
	t.Logf("runtime owner: %s", runtimeView)
	require.Contains(t, metadataView, "RU_PER_SEC=1000")
	require.Contains(t, metadataView, "PRIORITY=LOW")
	require.Equal(t, "[1000 LOW OFF]", runtimeView,
		"a cancelled DDL must not leave the runtime resource manager on the uncommitted configuration")
}

func TestAINativeAlterResourceGroupNormalGreen(t *testing.T) {
	store := testkit.CreateMockStore(t)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("SET GLOBAL tidb_enable_resource_control = 'on'")
	tk.MustExec("CREATE RESOURCE GROUP ai_rg_green RU_PER_SEC=1000 PRIORITY=LOW")
	tk.MustExec("ALTER RESOURCE GROUP ai_rg_green RU_PER_SEC=2000 PRIORITY=HIGH")

	metadataView, runtimeView := resourceGroupViews(t, tk, "ai_rg_green")
	t.Logf("metadata owner: %s", metadataView)
	t.Logf("runtime owner: %s", runtimeView)
	require.Contains(t, metadataView, "RU_PER_SEC=2000")
	require.Contains(t, metadataView, "PRIORITY=HIGH")
	require.Equal(t, "[2000 HIGH OFF]", runtimeView)
}
