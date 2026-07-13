// Copyright 2026 PingCAP, Inc.

package writetest

import (
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/stretchr/testify/require"
)

func TestAINativePessimisticRetryDoesNotRetainFailedAttemptSetval(t *testing.T) {
	store := testkit.CreateMockStore(t)
	owner := testkit.NewTestKit(t, store)
	competitor := testkit.NewTestKit(t, store)
	owner.MustExec("use test")
	competitor.MustExec("use test")
	owner.MustQuery("show global variables like 'tidb_enable_metadata_lock'").
		Check(testkit.Rows("tidb_enable_metadata_lock ON"))

	owner.MustExec("create sequence ai_retry_seq start with 1 increment by 1")
	owner.MustExec("create sequence ai_control_seq start with 1 increment by 1")
	owner.MustExec("create table ai_retry_src (id int primary key, next_u int)")
	owner.MustExec("create table ai_retry_dst (id int primary key, u int unique, v bigint null)")
	owner.MustExec("create table ai_control_dst like ai_retry_dst")
	owner.MustExec("insert into ai_retry_src values (1, 1)")
	owner.MustExec("insert into ai_retry_dst values (1, 10, 0)")
	owner.MustExec("insert into ai_control_dst values (1, 10, 0)")
	owner.MustExec("set transaction_isolation = 'READ-COMMITTED'")
	competitor.MustExec("set transaction_isolation = 'READ-COMMITTED'")

	owner.MustExec("begin pessimistic")
	errCh := make(chan error, 1)
	go func() {
		_, err := owner.Exec(`update ai_retry_dst as d join ai_retry_src as s on s.id = d.id
			set d.u = s.next_u, d.v = setval(ai_retry_seq, 100) + sleep(0.8)
			where d.id = 1`)
		errCh <- err
	}()

	time.Sleep(150 * time.Millisecond)
	competitor.MustExec("begin pessimistic")
	competitor.MustExec("insert into ai_retry_dst values (2, 1, 0)")
	competitor.MustExec("update ai_retry_src set next_u = 2 where id = 1")
	competitor.MustExec("commit")

	require.NoError(t, <-errCh)
	require.Greater(t, owner.Session().GetSessionVars().StmtCtx.ExecRetryCount, uint64(0))
	owner.MustExec("commit")
	redRow := owner.MustQuery("select * from ai_retry_dst where id = 1").Rows()
	redNextVal := owner.MustQuery("select nextval(ai_retry_seq)").Rows()

	owner.MustExec(`update ai_control_dst as d join ai_retry_src as s on s.id = d.id
		set d.u = s.next_u, d.v = setval(ai_control_seq, 100)
		where d.id = 1`)
	controlRow := owner.MustQuery("select * from ai_control_dst where id = 1").Rows()
	controlNextVal := owner.MustQuery("select nextval(ai_control_seq)").Rows()

	require.Equal(t, controlNextVal, redNextVal, "the hidden retry must not change sequence state")
	require.Equal(t, controlRow, redRow,
		"transparent retry must be equivalent to one execution from the successful attempt's state")
}
