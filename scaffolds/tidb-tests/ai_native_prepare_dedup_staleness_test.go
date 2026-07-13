package common

import (
	"context"
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/stretchr/testify/require"
)

func executePreparedInt(t *testing.T, tk *testkit.TestKit, stmtID uint32) int64 {
	t.Helper()
	ctx := context.Background()
	rs, err := tk.Session().ExecutePreparedStmt(ctx, stmtID, nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, rs.Close()) }()

	req := rs.NewChunk(nil)
	require.NoError(t, rs.Next(ctx, req))
	require.Equal(t, 1, req.NumRows())
	got := req.Column(0).GetInt64(0)
	req.Reset()
	require.NoError(t, rs.Next(ctx, req))
	require.Equal(t, 0, req.NumRows())
	return got
}

func TestAINativePrepareDedupMustNotReuseReadStaleness(t *testing.T) {
	store := testkit.CreateMockStore(t)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("set tidb_enable_cache_prepare_stmt = 1")
	tk.MustExec("create table ai_prepare_stale (id int primary key, v int)")
	tk.MustExec("insert into ai_prepare_stale values (1, 1)")

	// Make the initial version old enough to remain visible at now-1s.
	time.Sleep(1500 * time.Millisecond)

	const sql = "select v from ai_prepare_stale where id = 1"
	tk.MustExec("set @@tidb_read_staleness = -1")
	staleStmtID, _, _, err := tk.Session().PrepareStmt(sql)
	require.NoError(t, err)

	tk.MustExec("set @@tidb_read_staleness = ''")
	tk.MustExec("update ai_prepare_stale set v = 2 where id = 1")

	// The same SQL takes the prepare-dedup fast path after read staleness is
	// cleared. It must not inherit the old statement's snapshot evaluator.
	freshContextStmtID, _, _, err := tk.Session().PrepareStmt(sql)
	require.NoError(t, err)
	require.NotEqual(t, staleStmtID, freshContextStmtID)
	contaminatedValue := executePreparedInt(t, tk, freshContextStmtID)

	// The same SQL with only the dedup fast path disabled proves a full Prepare
	// under the current non-stale context sees v=2.
	tk.MustExec("set tidb_enable_cache_prepare_stmt = 0")
	controlStmtID, _, _, err := tk.Session().PrepareStmt(sql)
	require.NoError(t, err)
	require.Equal(t, int64(2), executePreparedInt(t, tk, controlStmtID))

	require.Equal(t, int64(2), contaminatedValue)
}
