// Copyright 2026 PingCAP, Inc.

package writetest

import (
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/stretchr/testify/require"
)

func TestAINativePessimisticRetryPreservesConstantSeedRand(t *testing.T) {
	store := testkit.CreateMockStore(t)
	owner := testkit.NewTestKit(t, store)
	competitor := testkit.NewTestKit(t, store)
	owner.MustExec("use test")
	competitor.MustExec("use test")
	owner.MustQuery("show global variables like 'tidb_enable_metadata_lock'").
		Check(testkit.Rows("tidb_enable_metadata_lock ON"))

	owner.MustExec("create table ai_rand_src (id int primary key, next_u int)")
	owner.MustExec("create table ai_rand_retry (id int primary key, u int unique, v bigint unsigned)")
	owner.MustExec("create table ai_rand_control like ai_rand_retry")
	owner.MustExec("insert into ai_rand_src values (1, 1)")
	owner.MustExec("insert into ai_rand_retry values (1, 10, 0)")
	owner.MustExec("insert into ai_rand_control values (1, 10, 0)")
	owner.MustExec("set transaction_isolation = 'READ-COMMITTED'")
	competitor.MustExec("set transaction_isolation = 'READ-COMMITTED'")

	owner.MustExec("begin pessimistic")
	errCh := make(chan error, 1)
	go func() {
		_, err := owner.Exec(`update ai_rand_retry as d join ai_rand_src as s on s.id = d.id
			set d.u = s.next_u,
				d.v = cast(rand(12345) * 1000000000 as unsigned) + sleep(0.8)
			where d.id = 1`)
		errCh <- err
	}()

	time.Sleep(150 * time.Millisecond)
	competitor.MustExec("begin pessimistic")
	competitor.MustExec("insert into ai_rand_retry values (2, 1, 0)")
	competitor.MustExec("update ai_rand_src set next_u = 2 where id = 1")
	competitor.MustExec("commit")

	require.NoError(t, <-errCh)
	require.Greater(t, owner.Session().GetSessionVars().StmtCtx.ExecRetryCount, uint64(0))
	owner.MustExec("commit")
	retryRow := owner.MustQuery("select * from ai_rand_retry where id = 1").Rows()

	owner.MustExec(`update ai_rand_control as d join ai_rand_src as s on s.id = d.id
		set d.u = s.next_u,
			d.v = cast(rand(12345) * 1000000000 as unsigned)
		where d.id = 1`)
	controlRow := owner.MustQuery("select * from ai_rand_control where id = 1").Rows()

	require.Equal(t, controlRow, retryRow,
		"hidden retry must preserve the constant-seed RAND sequence position")
}

func TestAINativePessimisticRetryDoesNotChangeSeededRandUniqueKey(t *testing.T) {
	store := testkit.CreateMockStore(t)
	owner := testkit.NewTestKit(t, store)
	competitor := testkit.NewTestKit(t, store)
	owner.MustExec("use test")
	competitor.MustExec("use test")
	owner.MustQuery("show global variables like 'tidb_enable_metadata_lock'").
		Check(testkit.Rows("tidb_enable_metadata_lock ON"))

	owner.MustExec("create table ai_rand_key_retry (id int primary key, u int unique)")
	owner.MustExec("create table ai_rand_key_control like ai_rand_key_retry")
	owner.MustExec("insert into ai_rand_key_retry values (1, 10)")
	owner.MustExec("insert into ai_rand_key_control values (1, 10)")
	owner.MustExec("set transaction_isolation = 'READ-COMMITTED'")
	competitor.MustExec("set transaction_isolation = 'READ-COMMITTED'")

	owner.MustExec("begin pessimistic")
	errCh := make(chan error, 1)
	go func() {
		_, err := owner.Exec(`update ai_rand_key_retry
			set u = if(rand(12345) < 0.8, 1, 2) + sleep(0.8) * 0
			where id = 1`)
		errCh <- err
	}()

	time.Sleep(150 * time.Millisecond)
	competitor.MustExec("begin pessimistic")
	competitor.MustExec("insert into ai_rand_key_retry values (2, 1)")
	competitor.MustExec("insert into ai_rand_key_control values (2, 1)")
	competitor.MustExec("commit")

	ownerErr := <-errCh
	if ownerErr == nil {
		owner.MustExec("commit")
	} else {
		owner.MustExec("rollback")
	}
	retryRows := owner.MustQuery("select * from ai_rand_key_retry order by id").Rows()

	_, controlErr := owner.Exec(`update ai_rand_key_control
		set u = if(rand(12345) < 0.8, 1, 2)
		where id = 1`)
	require.Error(t, controlErr)
	controlRows := owner.MustQuery("select * from ai_rand_key_control order by id").Rows()

	require.Error(t, ownerErr,
		"hidden retry must not advance constant-seed RAND past the conflicting first value")
	require.Equal(t, controlRows, retryRows)
}
