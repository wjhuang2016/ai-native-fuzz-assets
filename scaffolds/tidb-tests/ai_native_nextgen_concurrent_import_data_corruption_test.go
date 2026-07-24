// Drop this file into tests/realtikvtest/importintotest3.
package importintotest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/config/kerneltype"
	"github.com/pingcap/tidb/pkg/dxf/framework/handle"
	"github.com/pingcap/tidb/pkg/dxf/framework/storage"
	"github.com/pingcap/tidb/pkg/executor/importer"
	"github.com/pingcap/tidb/pkg/keyspace"
	"github.com/pingcap/tidb/pkg/objstore"
	"github.com/pingcap/tidb/pkg/session"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/tests/realtikvtest"
	"github.com/stretchr/testify/require"
	tikvutil "github.com/tikv/client-go/v2/util"
)

func TestAINativeConcurrentImportIntoSameTableAdmission(t *testing.T) {
	if kerneltype.IsClassic() {
		t.Skip("only runs in nextgen kernel")
	}
	ctx := context.Background()
	s3Args := "access-key=minioadmin&secret-access-key=minioadmin&endpoint=http%3a%2f%2f0.0.0.0%3a9000"
	sourceStore, err := objstore.NewFromURL(
		ctx,
		fmt.Sprintf("s3://next-gen-test/ai-native-concurrent-import?%s", s3Args),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		sourceStore.Close()
	})
	require.NoError(t, sourceStore.WriteFile(ctx, "a.csv", []byte("a1\na2\n")))
	require.NoError(t, sourceStore.WriteFile(ctx, "b.csv", []byte("b1\nb2\n")))

	const keyspaceName = "keyspace1"
	runtimes := realtikvtest.PrepareForCrossKSTest(t, keyspaceName)
	systemTK := testkit.NewTestKit(t, runtimes[keyspace.System].Store)
	systemTK.MustExec("delete from mysql.tidb_background_subtask")
	systemTK.MustExec("delete from mysql.tidb_background_subtask_history")
	systemTK.MustExec("delete from mysql.tidb_global_task")
	systemTK.MustExec("delete from mysql.tidb_global_task_history")
	systemTK.MustExec("delete from mysql.tidb_import_jobs")

	userStore := runtimes[keyspaceName].Store
	cleanupTK := testkit.NewTestKit(t, userStore)
	cleanupTK.MustExec("delete from mysql.tidb_import_jobs")
	adminTK := testkit.NewTestKit(t, userStore)
	importTK1 := testkit.NewTestKit(t, userStore)
	importTK2 := testkit.NewTestKit(t, userStore)
	prepareAndUseDB("ai_native_concurrent_import", adminTK)
	importTK1.MustExec("use ai_native_concurrent_import")
	importTK2.MustExec("use ai_native_concurrent_import")
	adminTK.MustExec("create table t (v varchar(100), unique key uk(v))")

	taskManager, err := storage.GetDXFSvcTaskMgr()
	require.NoError(t, err)
	taskCtx := tikvutil.WithInternalSourceType(ctx, "taskManager")
	require.NoError(t, taskManager.InitMeta(taskCtx, ":4000", handle.GetTargetScope()))

	type submitResult struct {
		err      error
		closeErr error
	}
	submitCh := make(chan submitResult, 2)
	submit := func(tk *testkit.TestKit, fileName, sortPrefix string) {
		sourceURI := fmt.Sprintf(
			"s3://next-gen-test/ai-native-concurrent-import/%s?%s",
			fileName,
			s3Args,
		)
		sortURI := fmt.Sprintf(
			"s3://next-gen-test/ai-native-concurrent-sort/%s?%s",
			sortPrefix,
			s3Args,
		)
		rs, err := tk.Exec(fmt.Sprintf(
			"IMPORT INTO t FROM '%s' WITH DETACHED, cloud_storage_uri='%s'",
			sourceURI,
			sortURI,
		))
		var closeErr error
		if rs != nil {
			_, drainErr := session.GetRows4Test(context.Background(), tk.Session(), rs)
			if err == nil {
				err = drainErr
			}
			closeErr = rs.Close()
		}
		submitCh <- submitResult{err: err, closeErr: closeErr}
	}

	// This is natural concurrency: there is no product hook or failpoint.
	go submit(importTK1, "a.csv", "job-a")
	go submit(importTK2, "b.csv", "job-b")
	for range 2 {
		result := <-submitCh
		require.NoError(t, result.err)
		require.NoError(t, result.closeErr)
	}

	var jobs [][]any
	require.Eventually(t, func() bool {
		jobs = adminTK.MustQuery(`
			select id, status, ifnull(error_message, '')
			from mysql.tidb_import_jobs
			where table_schema = 'ai_native_concurrent_import' and table_name = 't'
			order by id`).Rows()
		if len(jobs) != 2 {
			return false
		}
		for _, job := range jobs {
			status := job[1].(string)
			if status != importer.JobStatusFinished && status != "failed" && status != "cancelled" {
				return false
			}
		}
		return true
	}, 2*time.Minute, time.Second)

	tableRows := adminTK.MustQuery("select _tidb_rowid, v from t order by _tidb_rowid").Rows()
	indexRows := adminTK.MustQuery("select _tidb_rowid, v from t force index(uk) order by v").Rows()
	checkErr := adminTK.ExecToErr("admin check table t")
	t.Logf("jobs: %#v", jobs)
	t.Logf("table scan: %#v", tableRows)
	t.Logf("unique-index scan: %#v", indexRows)
	t.Logf("admin check: %v", checkErr)

	// Current master fails here with error 8223.
	require.NoError(t, checkErr)
}
