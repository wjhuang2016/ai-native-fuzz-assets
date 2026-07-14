// Copyright 2026 PingCAP, Inc.

package writetest

import (
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/stretchr/testify/require"
)

func TestAINativePessimisticRetryDoesNotPublishFailedAttemptInsertID(t *testing.T) {
	store := testkit.CreateMockStore(t)
	owner := testkit.NewTestKit(t, store)
	competitor := testkit.NewTestKit(t, store)
	owner.MustExec("use test")
	competitor.MustExec("use test")
	owner.MustQuery("show global variables like 'tidb_enable_metadata_lock'").
		Check(testkit.Rows("tidb_enable_metadata_lock ON"))

	owner.MustExec("create table ai_insert_id_src (id int primary key, explicit_id bigint, u int)")
	owner.MustExec("create table ai_insert_id_gate (id int primary key)")
	owner.MustExec("create table ai_insert_id_dst (id bigint auto_increment primary key, u int unique)")
	owner.MustExec("create table ai_insert_id_sink (arm varchar(16) primary key, reported_id bigint)")
	owner.MustExec("insert into ai_insert_id_src values (1, 42, 1)")
	owner.MustExec("set transaction_isolation = 'READ-COMMITTED'")
	competitor.MustExec("set transaction_isolation = 'READ-COMMITTED'")

	insertSQL := `insert into ai_insert_id_dst(id, u)
		select explicit_id, u + sleep(0.8) * 0
		from ai_insert_id_src s
		where not exists (select 1 from ai_insert_id_gate g where g.id = s.id)`

	owner.MustExec("begin pessimistic")
	errCh := make(chan error, 1)
	go func() {
		_, err := owner.Exec(insertSQL)
		errCh <- err
	}()

	time.Sleep(150 * time.Millisecond)
	competitor.MustExec("begin pessimistic")
	competitor.MustExec("insert into ai_insert_id_dst values (2, 1)")
	competitor.MustExec("insert into ai_insert_id_gate values (1)")
	competitor.MustExec("commit")

	require.NoError(t, <-errCh)
	require.Greater(t, owner.Session().GetSessionVars().StmtCtx.ExecRetryCount, uint64(0))
	retryAffected := owner.Session().AffectedRows()
	retryInsertID := owner.Session().LastInsertID()
	owner.MustExec("commit")
	owner.MustExec("insert into ai_insert_id_sink values ('retry', ?)", retryInsertID)

	control := testkit.NewTestKit(t, store)
	control.MustExec("use test")
	control.MustExec("set transaction_isolation = 'READ-COMMITTED'")
	_, err := control.Exec(insertSQL)
	require.NoError(t, err)
	controlAffected := control.Session().AffectedRows()
	controlInsertID := control.Session().LastInsertID()
	control.MustExec("insert into ai_insert_id_sink values ('control', ?)", controlInsertID)

	t.Logf("retry: affected=%d insertID=%d; control: affected=%d insertID=%d",
		retryAffected, retryInsertID, controlAffected, controlInsertID)
	owner.MustQuery("select * from ai_insert_id_dst order by id").Check(testkit.Rows("2 1"))
	owner.MustQuery("select * from ai_insert_id_sink order by arm").
		Check(testkit.Rows("control 0", "retry 0"))
	require.Equal(t, uint64(0), retryAffected)
	require.Equal(t, uint64(0), controlAffected)
	require.Equal(t, controlInsertID, retryInsertID,
		"a successful zero-row retry must not publish the explicit ID from its failed attempt")
}
