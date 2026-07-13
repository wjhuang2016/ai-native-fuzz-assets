// Copyright 2026 PingCAP, Inc.

package writetest

import (
	"fmt"
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/stretchr/testify/require"
)

// TestAINativePessimisticRetryDoesNotRetainFailedAttemptAdvisoryLock probes
// external capability ownership across transparent statement retry.
func TestAINativePessimisticRetryDoesNotRetainFailedAttemptAdvisoryLock(t *testing.T) {
	store := testkit.CreateMockStore(t)
	owner := testkit.NewTestKit(t, store)
	competitor := testkit.NewTestKit(t, store)
	owner.MustExec("use test")
	competitor.MustExec("use test")
	owner.MustQuery("show global variables like 'tidb_enable_metadata_lock'").
		Check(testkit.Rows("tidb_enable_metadata_lock ON"))
	owner.MustExec("create table ai_retry_lock_t (id int primary key, u int unique, v int)")
	owner.MustExec("create table ai_retry_lock_gate (id int primary key)")
	owner.MustExec("insert into ai_retry_lock_t values (1, 10, 0)")
	owner.MustExec("set transaction_isolation = 'READ-COMMITTED'")
	competitor.MustExec("set transaction_isolation = 'READ-COMMITTED'")

	owner.MustExec("begin pessimistic")
	errCh := make(chan error, 1)
	go func() {
		_, err := owner.Exec(`update ai_retry_lock_t as t
			set u = 1, v = get_lock(concat('ai_retry_lock_', t.id), 0) + sleep(0.8)
			where id = 1 and not exists
				(select 1 from ai_retry_lock_gate as g where g.id = t.id)`)
		errCh <- err
	}()

	time.Sleep(150 * time.Millisecond)
	competitor.MustExec("begin pessimistic")
	competitor.MustExec("insert into ai_retry_lock_t values (2, 1, 0)")
	competitor.MustExec("insert into ai_retry_lock_gate values (1)")
	competitor.MustExec("commit")

	require.NoError(t, <-errCh)
	require.Greater(t, owner.Session().GetSessionVars().StmtCtx.ExecRetryCount, uint64(0))
	owner.MustExec("commit")
	owner.MustQuery("select * from ai_retry_lock_t order by id").
		Check(testkit.Rows("1 10 0", "2 1 0"))

	// The successful retry matched zero rows, so the failed attempt must not leave a lock behind.
	redAcquire := fmt.Sprint(competitor.MustQuery("select get_lock('ai_retry_lock_1', 0)").Rows()[0][0])
	owner.MustQuery("select release_lock('ai_retry_lock_1')").Check(testkit.Rows("1"))

	// Same final database state without a failed attempt must not evaluate the row-dependent lock.
	owner.MustExec("begin pessimistic")
	owner.MustExec(`update ai_retry_lock_t as t
		set u = 2, v = get_lock(concat('ai_retry_lock_control_', t.id), 0)
		where id = 1 and not exists
			(select 1 from ai_retry_lock_gate as g where g.id = t.id)`)
	owner.MustExec("commit")
	controlAcquire := fmt.Sprint(competitor.MustQuery(
		"select get_lock('ai_retry_lock_control_1', 0)").Rows()[0][0])
	competitor.MustQuery("select release_lock('ai_retry_lock_control_1')").Check(testkit.Rows("1"))

	require.Equal(t, "1", controlAcquire)
	require.Equal(t, "1", redAcquire, "failed-attempt advisory lock survived transparent retry")
}
