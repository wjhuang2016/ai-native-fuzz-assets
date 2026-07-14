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

package txntest

import (
	"context"
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/config"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/tests/realtikvtest"
	"github.com/stretchr/testify/require"
)

func TestAINativeAutoIDRetryDoesNotMixOldIDWithNewPayloadRealTiKV(t *testing.T) {
	if !*realtikvtest.WithRealTiKV {
		t.Skip("requires real TiKV")
	}

	store := realtikvtest.CreateMockStoreAndSetup(t)
	competitor := testkit.NewTestKit(t, store)
	competitor.MustExec("use test")
	competitor.MustExec("drop table if exists ai_source, ai_target")
	competitor.MustExec("create table ai_source(slot bigint primary key, target_id bigint not null, payload varchar(32) not null)")
	competitor.MustExec("create table ai_target(id bigint auto_increment primary key, payload varchar(32) not null)")
	competitor.MustExec("insert into ai_source values (1, 1, 'from-one'), (2, 100, 'old')")
	competitor.MustExec("insert into ai_target values (1, 'base')")
	competitor.MustQuery("select @@tidb_enable_metadata_lock, @@autocommit, @@tidb_retry_limit").
		Check(testkit.Rows("1 1 10"))
	require.False(t, config.GetGlobalConfig().PessimisticTxn.PessimisticAutoCommit.Load())

	worker := testkit.NewTestKit(t, store)
	worker.MustExec("use test")
	done := make(chan error, 1)
	go func() {
		resultSets, err := worker.Session().Execute(context.Background(), `insert into ai_target(id, payload)
			select target_id, if(sleep(if(slot = 1, 2, 0)) = 0, payload, payload)
			from ai_source order by slot
			on duplicate key update payload = values(payload)`)
		for _, resultSet := range resultSets {
			if closeErr := resultSet.Close(); err == nil {
				err = closeErr
			}
		}
		done <- err
	}()

	time.Sleep(500 * time.Millisecond)
	competitor.MustExec("begin")
	competitor.MustExec("update ai_source set target_id = 200, payload = 'new' where slot = 2")
	competitor.MustExec("update ai_target set payload = 'competitor' where id = 1")
	competitor.MustExec("commit")
	require.NoError(t, <-done)
	require.GreaterOrEqual(t, worker.Session().GetSessionVars().StmtCtx.ExecRetryCount, uint64(1))

	fresh := testkit.NewTestKit(t, store)
	fresh.MustExec("use test")
	fresh.MustQuery("select * from ai_source order by slot").Check(testkit.Rows("1 1 from-one", "2 200 new"))
	fresh.MustQuery("select * from ai_target order by id").Check(testkit.Rows("1 from-one", "200 new"))
	fresh.MustQuery(`select s.id, s.payload, t.id, t.payload
		from (select target_id as id, payload from ai_source where slot <> 1) s
		left join ai_target t on t.id = s.id`).
		Check(testkit.Rows("200 new 200 new"))
	fresh.MustExec("admin check table ai_target")
}
