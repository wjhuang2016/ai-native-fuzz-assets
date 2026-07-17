package txntest

import (
	"fmt"
	"testing"

	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/tests/realtikvtest"
	"github.com/stretchr/testify/require"
)

type failedStatementPoisonCase struct {
	name       string
	successSQL string
	failureSQL string
}

func resetFailedStatementPoisonTables(tk *testkit.TestKit) {
	tk.MustExec("drop table if exists ai_stmt_control, ai_stmt_probe")
	for _, tableName := range []string{"ai_stmt_control", "ai_stmt_probe"} {
		tk.MustExec(fmt.Sprintf(`create table %s (
			id int primary key clustered,
			u1 int not null,
			u2 int not null,
			base int not null,
			g int as (base + 1) stored,
			unique key u1_idx(u1),
			unique key u2_idx(u2),
			key base_idx(base),
			key g_idx(g)
		)`, tableName))
		tk.MustExec(fmt.Sprintf("insert into %s(id, u1, u2, base) values (1, 10, 100, 100), (2, 20, 200, 200), (3, 30, 300, 300)", tableName))
	}
}

func failedStatementPoisonState(tk *testkit.TestKit, tableName string) string {
	return fmt.Sprint(tk.MustQuery(fmt.Sprintf("select id, u1, u2, base, g from %s order by id", tableName)).Rows())
}

func checkFailedStatementPoisonClosure(t *testing.T, tk *testkit.TestKit, tableName string) {
	tk.MustExec("admin check table " + tableName)
	query := "select id, u1, u2, base, g from %s %s order by id"
	recordRows := fmt.Sprint(tk.MustQuery(fmt.Sprintf(query, tableName, "ignore index(primary, u1_idx, u2_idx, base_idx, g_idx)")).Rows())
	for _, indexName := range []string{"u1_idx", "u2_idx", "base_idx", "g_idx"} {
		indexRows := fmt.Sprint(tk.MustQuery(fmt.Sprintf(query, tableName, "use index("+indexName+")")).Rows())
		require.Equal(t, recordRows, indexRows, "%s disagrees with the record scan", indexName)
	}
}

// Removing a failed statement must not change the transaction's committed state.
func TestAINativeFailedStatementCannotPoisonPriorMutation(t *testing.T) {
	store := realtikvtest.CreateMockStoreAndSetup(t)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("set @@tidb_txn_mode = ''")
	tk.MustQuery("select @@global.tidb_enable_metadata_lock").Check(testkit.Rows("1"))
	tk.MustQuery("select @@tidb_constraint_check_in_place_pessimistic").Check(testkit.Rows("1"))

	cases := []failedStatementPoisonCase{
		{
			name:       "delete_then_failed_multirow_reinsert",
			successSQL: "delete from %s where id = 1",
			failureSQL: "insert into %s(id, u1, u2, base) values (1, 11, 101, 111), (4, 40, 400, null)",
		},
		{
			name:       "update_then_failed_multirow_update",
			successSQL: "update %s set u1 = 11, u2 = 101, base = 101 where id = 1",
			failureSQL: "update %s set u1 = u1 + 100, u2 = u2 + 1000, base = case id when 1 then 102 else null end where id in (1, 2) order by id",
		},
		{
			name:       "insert_then_failed_reverse_multirow_update",
			successSQL: "insert into %s(id, u1, u2, base) values (4, 40, 400, 400)",
			failureSQL: "update %s set u1 = u1 + 100, u2 = u2 + 1000, base = case id when 4 then 401 else null end where id in (2, 4) order by id desc",
		},
		{
			name:       "delete_then_failed_multirow_insert_with_new_owner",
			successSQL: "delete from %s where id = 1",
			failureSQL: "insert into %s(id, u1, u2, base) values (4, 40, 400, 400), (1, 11, 101, null)",
		},
		{
			name:       "update_then_failed_multirow_update_after_index_rewrite",
			successSQL: "update %s set u1 = 11, u2 = 101, base = 101 where id = 1",
			failureSQL: "update %s set u1 = u1 + 200, u2 = u2 + 2000, base = case id when 1 then 103 else null end where id in (1, 3) order by id",
		},
	}

	for _, beginSQL := range []string{"begin", "begin pessimistic"} {
		modeName := "optimistic"
		if beginSQL == "begin pessimistic" {
			modeName = "pessimistic"
		}
		for _, tc := range cases {
			t.Run(modeName+"/"+tc.name, func(t *testing.T) {
				resetFailedStatementPoisonTables(tk)

				tk.MustExec(beginSQL)
				tk.MustExec(fmt.Sprintf(tc.successSQL, "ai_stmt_control"))
				tk.MustExec("commit")

				tk.MustExec(beginSQL)
				tk.MustExec(fmt.Sprintf(tc.successSQL, "ai_stmt_probe"))
				require.Error(t, tk.ExecToErr(fmt.Sprintf(tc.failureSQL, "ai_stmt_probe")))
				tk.MustExec("commit")

				require.Equal(t,
					failedStatementPoisonState(tk, "ai_stmt_control"),
					failedStatementPoisonState(tk, "ai_stmt_probe"),
				)
				checkFailedStatementPoisonClosure(t, tk, "ai_stmt_control")
				checkFailedStatementPoisonClosure(t, tk, "ai_stmt_probe")
			})
		}
	}
}
