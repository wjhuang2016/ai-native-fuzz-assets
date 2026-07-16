// Copyright 2026 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sessiontxn_test

import (
	"context"
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/stretchr/testify/require"
)

// TestReadCommittedScalarSubqueryAllowedOneAttemptWitness establishes the
// allowed one-attempt result when the publisher commits after the RC statement
// timestamp has been allocated. Both the scalar and DML source must stay old.
func TestReadCommittedScalarSubqueryAllowedOneAttemptWitness(t *testing.T) {
	store, _ := setupTxnContextTest(t)
	competitor := newTxnContextTestKit(t, store)
	competitor.MustExec("use test")
	competitor.MustExec("drop table if exists ai_witness_target, ai_witness_src")
	competitor.MustExec("create table ai_witness_target(id int primary key, u int not null unique, v int not null)")
	competitor.MustExec("create table ai_witness_src(id int primary key, next_u int not null)")
	competitor.MustExec("insert into ai_witness_src values(1, 200)")
	competitor.MustExec("insert into ai_witness_target values(1, 10, 10), (2, 20, 20)")

	async := testkit.NewAsyncTestKit(t, store)
	ctx := async.OpenSession(context.Background(), "test")
	defer async.CloseSession(ctx)
	async.MustExec(ctx, "set transaction_isolation = 'READ-COMMITTED'")
	async.MustExec(ctx, "set tidb_txn_mode = 'pessimistic'")
	async.MustExec(ctx, "begin pessimistic")
	se := testkit.TryRetrieveSession(ctx)

	done := make(chan error, 1)
	go func() {
		done <- async.ExecToErr(ctx, `update ai_witness_target d
		join ai_witness_src src on src.id = 1
		set d.u = if(d.id = 1, 100, src.next_u),
			d.v = (select sum(s.v + sleep(3) * 0) from ai_witness_target s) + d.id
		where d.id in (1, 2)`)
	}()
	time.Sleep(time.Second)
	select {
	case err := <-done:
		require.FailNowf(t, "scalar subquery left the scheduling window", "err=%v", err)
	default:
	}

	competitor.MustExec("update ai_witness_src set next_u = 300 where id = 1")
	require.NoError(t, <-done)
	require.Equal(t, uint64(0), se.GetSessionVars().StmtCtx.ExecRetryCount)
	async.MustExec(ctx, "commit")
	competitor.MustQuery("select * from ai_witness_target order by id").Check(testkit.Rows(
		"1 100 31",
		"2 200 32",
	))
}

// TestPessimisticRetryReevaluatesScalarSubquery is RED on the affected build.
// The hidden retry refreshes the RC statement timestamp but reuses the scalar
// subquery constant from the failed attempt, producing an impossible old/new mix.
func TestPessimisticRetryReevaluatesScalarSubquery(t *testing.T) {
	store, _ := setupTxnContextTest(t)
	setup := newTxnContextTestKit(t, store)
	setup.MustExec("use test")
	setup.MustExec("drop table if exists ai_retry_target, ai_retry_control, ai_retry_src")
	setup.MustExec("create table ai_retry_target(id int primary key, u int not null unique, v int not null)")
	setup.MustExec("create table ai_retry_control like ai_retry_target")
	setup.MustExec("create table ai_retry_src(id int primary key, next_u int not null)")
	setup.MustExec("insert into ai_retry_src values(1, 200)")
	setup.MustExec("insert into ai_retry_target values(1, 10, 10), (2, 20, 20)")
	setup.MustExec("insert into ai_retry_control values(1, 10, 10), (2, 20, 20), (3, 200, 999)")

	async := testkit.NewAsyncTestKit(t, store)
	ctxA := async.OpenSession(context.Background(), "test")
	defer async.CloseSession(ctxA)
	async.MustExec(ctxA, "set transaction_isolation = 'READ-COMMITTED'")
	async.MustExec(ctxA, "set tidb_txn_mode = 'pessimistic'")
	async.MustExec(ctxA, "begin pessimistic")
	seA := testkit.TryRetrieveSession(ctxA)

	done := make(chan error, 1)
	go func() {
		done <- async.ExecToErr(ctxA, `update ai_retry_target d
		join ai_retry_src src on src.id = 1
		set d.u = if(d.id = 1, 100, src.next_u + sleep(3) * 0),
			d.v = (select sum(s.v) from ai_retry_target s) + d.id
		where d.id in (1, 2)`)
	}()

	time.Sleep(time.Second)
	select {
	case err := <-done:
		require.FailNowf(t, "target statement left the scheduling window", "err=%v", err)
	default:
	}

	setup.MustExec("update ai_retry_src set next_u = 300 where id = 1")
	setup.MustExec("insert into ai_retry_target values(3, 200, 999)")
	require.NoError(t, <-done)
	require.Equal(t, uint64(1), seA.GetSessionVars().StmtCtx.ExecRetryCount)
	async.MustExec(ctxA, "commit")

	setup.MustExec(`update ai_retry_control d
		join ai_retry_src src on src.id = 1
		set d.u = if(d.id = 1, 100, src.next_u),
			d.v = (select sum(s.v) from ai_retry_control s) + d.id
		where d.id in (1, 2)`)
	setup.MustQuery("select * from ai_retry_target order by id").Check(testkit.Rows(
		"1 100 1030",
		"2 300 1031",
		"3 200 999",
	))
	setup.MustQuery("select * from ai_retry_target order by id").Check(
		setup.MustQuery("select * from ai_retry_control order by id").Rows(),
	)
}
