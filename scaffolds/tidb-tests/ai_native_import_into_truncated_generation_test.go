// Place this test in tests/realtikvtest/importintotest3/cross_ks_test.go.
// Start it with the CSE TiKV worker stopped. After the "truncated target" log,
// start the worker so the persisted task executes after the generation change.
func TestAINativeImportIntoWritesToTruncatedGeneration(t *testing.T) {
	if kerneltype.IsClassic() {
		t.Skip("only runs in nextgen kernel")
	}

	const fakeGCSPort = 4444
	server, err := fakestorage.NewServerWithOptions(fakestorage.Options{
		Scheme:     "http",
		Host:       "127.0.0.1",
		Port:       fakeGCSPort,
		PublicHost: "127.0.0.1",
	})
	require.NoError(t, err)
	t.Cleanup(server.Stop)
	gcsEndpoint := fmt.Sprintf("http://127.0.0.1:%d/storage/v1/", fakeGCSPort)
	server.CreateBucketWithOpts(fakestorage.CreateBucketOpts{Name: "stale-generation-source"})
	server.CreateBucketWithOpts(fakestorage.CreateBucketOpts{Name: "stale-generation-sort"})
	server.CreateObject(fakestorage.Object{
		ObjectAttrs: fakestorage.ObjectAttrs{
			BucketName: "stale-generation-source",
			Name:       "data.csv",
		},
		Content: []byte("1,a\n2,b\n"),
	})

	const (
		keyspaceName = "keyspace1"
		dbName       = "cross_ks_stale_generation"
		tableName    = "t"
	)
	runtimes := realtikvtest.PrepareForCrossKSTest(t, keyspaceName)
	userStore := runtimes[keyspaceName].Store
	userTK := testkit.NewTestKit(t, userStore)
	submitTK := testkit.NewTestKit(t, userStore)
	sysKSTK := testkit.NewTestKit(t, kvstore.GetSystemStorage())
	prepareAndUseDB(dbName, userTK)
	submitTK.MustExec("use " + dbName)
	createImportTable(userTK, tableName)

	getTableID := func() int64 {
		rows := userTK.MustQuery(
			"select tidb_table_id from information_schema.tables where table_schema = ? and table_name = ?",
			dbName,
			tableName,
		).Rows()
		require.Len(t, rows, 1)
		tableID, err := strconv.ParseInt(rows[0][0].(string), 10, 64)
		require.NoError(t, err)
		return tableID
	}
	oldTableID := getTableID()

	sourceURI := fmt.Sprintf("gs://stale-generation-source/data.csv?endpoint=%s", gcsEndpoint)
	sortURI := fmt.Sprintf("gs://stale-generation-sort/data?endpoint=%s", gcsEndpoint)
	jobID := submitDetachedJob(t, submitTK, tableName, sourceURI, sortURI)
	userTK.MustExec("truncate table " + tableName)
	newTableID := getTableID()
	require.NotEqual(t, oldTableID, newTableID)
	userTK.MustExec("insert into t values (100, 'new-generation')")
	t.Logf("truncated target after detached job submission: job ID=%d, retired table ID=%d, current table ID=%d",
		jobID, oldTableID, newTableID)

	waitTerminalState(t, sysKSTK, importinto.TaskKey(jobID), proto.TaskStateSucceed)

	recordPrefix := tablecodec.GenTableRecordPrefix(oldTableID)
	iter, err := userStore.GetSnapshot(kv.MaxVersion).Iter(recordPrefix, recordPrefix.PrefixNext())
	require.NoError(t, err)
	defer iter.Close()
	oldGenerationRows := 0
	for iter.Valid() {
		oldGenerationRows++
		require.NoError(t, iter.Next())
	}

	userTK.MustQuery(
		"select status, json_unquote(json_extract(summary, '$.\"row-count\"')) from mysql.tidb_import_jobs where id = ?",
		jobID,
	).Check(testkit.Rows("finished 2"))
	userTK.MustQuery("select * from " + tableName).Check(testkit.Rows("100 new-generation"))
	require.Equal(t, 2, oldGenerationRows)
	userTK.MustExec("admin check table " + tableName)
}
