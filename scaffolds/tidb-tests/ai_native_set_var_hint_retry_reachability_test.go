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

package txn

import (
	"testing"

	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/stretchr/testify/require"
)

func TestAISetVarHintDoesNotLeakAcrossOptimisticTxnRetry(t *testing.T) {
	store := testkit.CreateMockStore(t)
	tkA := testkit.NewTestKit(t, store)
	tkB := testkit.NewTestKit(t, store)
	for _, tk := range []*testkit.TestKit{tkA, tkB} {
		tk.MustExec("use test")
		tk.MustExec("set session tidb_txn_mode='optimistic'")
		tk.MustExec("set session sql_mode=''")
	}
	tkA.MustExec("set session tidb_disable_txn_auto_retry=off")
	tkA.MustExec("set session tidb_retry_limit=10")
	tkA.MustExec("create table marker (id int primary key, v int)")
	tkA.MustExec("create table conflict_row (id int primary key, v int)")
	tkA.MustExec("create table auto_control (id bigint primary key auto_increment, v int)")
	tkA.MustExec("create table auto_retry (id bigint primary key auto_increment, v int)")
	tkA.MustExec("insert into marker values (1,0),(2,0)")
	tkA.MustExec("insert into conflict_row values (1,0),(2,0)")

	// No retry: the next statement entry restores SQL mode before id=0 is evaluated.
	tkA.MustExec("begin")
	tkA.MustExec("update /*+ SET_VAR(sql_mode='NO_AUTO_VALUE_ON_ZERO') */ marker set v=v+1 where id=1")
	tkA.MustExec("insert into auto_control(id,v) values (0,10)")
	tkA.MustExec("update conflict_row set v=v+1 where id=1")
	tkA.MustExec("commit")
	tkA.MustQuery("select id,v from auto_control").Check(testkit.Rows("1 10"))
	tkA.MustQuery("select @@sql_mode").Check(testkit.Rows(""))

	// Natural optimistic conflict: transaction retry replays all three write statements.
	tkA.MustExec("begin")
	tkA.MustExec("update /*+ SET_VAR(sql_mode='NO_AUTO_VALUE_ON_ZERO') */ marker set v=v+1 where id=2")
	tkA.MustExec("insert into auto_retry(id,v) values (0,20)")
	tkA.MustExec("update conflict_row set v=v+1 where id=2")
	tkB.MustExec("update conflict_row set v=v+1 where id=2")
	tkA.MustExec("commit")
	require.Greater(t, tkA.Session().GetSessionVars().StmtCtx.ExecRetryCount, uint64(0))

	tkA.MustQuery("select id,v from auto_retry").Check(testkit.Rows("1 20"))
	tkA.MustQuery("select @@sql_mode").Check(testkit.Rows(""))
	tkA.MustQuery("select id,v from marker order by id").Check(testkit.Rows("1 1", "2 1"))
	tkA.MustQuery("select id,v from conflict_row order by id").Check(testkit.Rows("1 1", "2 2"))
}
