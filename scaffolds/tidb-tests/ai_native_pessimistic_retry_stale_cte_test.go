// Copyright 2026 PingCAP, Inc.

package writetest

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/stretchr/testify/require"
)

func TestAINativePessimisticRetryRefreshesMaterializedCTE(t *testing.T) {
	store := testkit.CreateMockStore(t)
	owner := testkit.NewTestKit(t, store)
	competitor := testkit.NewTestKit(t, store)
	owner.MustExec("use test")
	competitor.MustExec("use test")
	owner.MustQuery("show global variables like 'tidb_enable_metadata_lock'").
		Check(testkit.Rows("tidb_enable_metadata_lock ON"))

	owner.MustExec("create table ai_cte_src (id int primary key, next_u int, payload int)")
	owner.MustExec("create table ai_cte_retry (id int primary key, u int unique, v int)")
	owner.MustExec("create table ai_cte_control like ai_cte_retry")
	owner.MustExec("insert into ai_cte_src values (1, 1, 10)")
	owner.MustExec("insert into ai_cte_retry values (1, 10, 0)")
	owner.MustExec("insert into ai_cte_control values (1, 10, 0)")
	owner.MustExec("set transaction_isolation = 'READ-COMMITTED'")
	competitor.MustExec("set transaction_isolation = 'READ-COMMITTED'")

	updateSQL := `with c as (
		select id, payload + sleep(0.8) * 0 as v from ai_cte_src
	)
	update ai_cte_retry d
	join ai_cte_src s on s.id = d.id
	join c c1 on c1.id = d.id
	join c c2 on c2.id = d.id
	set d.u = s.next_u, d.v = c1.v
	where d.id = 1`
	planRows := owner.MustQuery("explain " + updateSQL).Rows()
	var plan strings.Builder
	for _, row := range planRows {
		fmt.Fprintln(&plan, row)
	}
	require.Contains(t, plan.String(), "CTEFullScan")

	owner.MustExec("begin pessimistic")
	errCh := make(chan error, 1)
	go func() {
		_, err := owner.Exec(updateSQL)
		errCh <- err
	}()

	time.Sleep(150 * time.Millisecond)
	competitor.MustExec("begin pessimistic")
	competitor.MustExec("insert into ai_cte_retry values (2, 1, 0)")
	competitor.MustExec("insert into ai_cte_control values (2, 1, 0)")
	competitor.MustExec("update ai_cte_src set next_u = 2, payload = 20 where id = 1")
	competitor.MustExec("commit")

	require.NoError(t, <-errCh)
	require.Greater(t, owner.Session().GetSessionVars().StmtCtx.ExecRetryCount, uint64(0))
	owner.MustExec("commit")
	retryRows := owner.MustQuery("select * from ai_cte_retry order by id").Rows()

	owner.MustExec(`with c as (select id, payload as v from ai_cte_src)
		update ai_cte_control d
		join ai_cte_src s on s.id = d.id
		join c c1 on c1.id = d.id
		join c c2 on c2.id = d.id
		set d.u = s.next_u, d.v = c1.v
		where d.id = 1`)
	controlRows := owner.MustQuery("select * from ai_cte_control order by id").Rows()

	require.Equal(t, controlRows, retryRows,
		"hidden retry must rebuild materialized CTE from the successful-attempt snapshot")
}
