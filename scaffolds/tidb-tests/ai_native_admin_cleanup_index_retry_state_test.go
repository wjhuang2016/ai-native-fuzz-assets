// Fixture for pkg/executor/test/admintest. Imports are supplied by admin_test.go when this function
// is pasted there for a focused oracle run.
func TestAdminCleanupIndexRetryStateOracle(t *testing.T) {
	store, domain := testkit.CreateMockStoreAndDomain(t)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("create table admin_cleanup_retry (id bigint primary key, v bigint, key idx_v(v))")

	tbl, err := domain.InfoSchema().TableByName(
		context.Background(), ast.NewCIStr("test"), ast.NewCIStr("admin_cleanup_retry"))
	require.NoError(t, err)
	idxInfo := tbl.Meta().FindIndexByName("idx_v")
	indexOpr, err := tables.NewIndex(tbl.Meta().ID, tbl.Meta(), idxInfo)
	require.NoError(t, err)

	sctx := mock.NewContext()
	sctx.Store = store
	txn, err := store.Begin()
	require.NoError(t, err)
	for i := int64(1); i <= 20001; i++ {
		_, err = indexOpr.Create(sctx.GetTableCtx(), txn, types.MakeDatums(i), kv.IntHandle(i), nil)
		require.NoError(t, err)
	}
	require.NoError(t, txn.Commit(context.Background()))

	testfailpoint.Enable(t, "github.com/pingcap/tidb/pkg/kv/mockCommitErrorInNewTxn", `return("retry_once")`)
	tk.MustQuery("admin cleanup index admin_cleanup_retry idx_v").Check(testkit.Rows("20001"))
	tk.MustExec("admin check index admin_cleanup_retry idx_v")
}
