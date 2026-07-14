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
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pingcap/failpoint"
	"github.com/pingcap/tidb/pkg/config"
	"github.com/pingcap/tidb/pkg/tablecodec"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/tests/realtikvtest"
	"github.com/stretchr/testify/require"
	"github.com/tikv/client-go/v2/oracle"
)

type aiNativeSQLProofFailureOracle struct {
	oracle.Oracle
	expireLocks atomic.Bool
}

func (o *aiNativeSQLProofFailureOracle) IsExpired(lockTS, ttl uint64, opt *oracle.Option) bool {
	if o.expireLocks.Load() {
		return true
	}
	return o.Oracle.IsExpired(lockTS, ttl, opt)
}

func TestAINativeAsyncCheckNotExistsSQLRecovery(t *testing.T) {
	defer config.RestoreFunc()()
	config.UpdateGlobal(func(conf *config.Config) {
		conf.TiKVClient.AsyncCommit.SafeWindow = 10 * time.Second
		conf.TiKVClient.AsyncCommit.AllowedClockDrift = 500 * time.Millisecond
	})

	store, domain := realtikvtest.CreateMockStoreAndDomainAndSetup(t)
	tk := testkit.NewTestKit(t, store)
	tk.Session().SetConnectionID(1)
	tk.MustExec("use test")
	tk.MustQuery("select @@tidb_enable_metadata_lock").Check(testkit.Rows("1"))
	tk.MustQuery("select @@tidb_constraint_check_in_place").Check(testkit.Rows("0"))
	tk.MustExec("set @@tidb_enable_async_commit = on")
	tk.MustExec("set @@tidb_enable_1pc = off")
	tk.MustExec("drop table if exists ai_accounts, ai_candidates")
	tk.MustExec("create table ai_accounts (id bigint primary key, balance bigint not null)")
	tk.MustExec("create table ai_candidates (id bigint primary key, email varchar(128) not null, unique key uk_email(email))")
	tk.MustExec("insert into ai_accounts values (1, 0)")
	tk.MustExec("insert into ai_candidates values (100, 'used@example.com')")

	tableID, err := strconv.ParseInt(tk.MustQuery(
		"select tidb_table_id from information_schema.tables where table_schema='test' and table_name='ai_candidates'",
	).Rows()[0][0].(string), 10, 64)
	require.NoError(t, err)
	_, err = domain.GetPDClient().SplitRegions(context.Background(), [][]byte{tablecodec.EncodeTablePrefix(tableID)})
	require.NoError(t, err)

	type oracleStore interface {
		GetOracle() oracle.Oracle
		SetOracle(oracle.Oracle)
	}
	tikvStore, ok := store.(oracleStore)
	require.True(t, ok)
	baseOracle := tikvStore.GetOracle()
	expiringOracle := &aiNativeSQLProofFailureOracle{Oracle: baseOracle}
	tikvStore.SetOracle(expiringOracle)
	defer tikvStore.SetOracle(baseOracle)

	require.NoError(t, failpoint.Enable("tikvclient/commitFailedSkipCleanup", "return"))
	defer func() {
		require.NoError(t, failpoint.Disable("tikvclient/commitFailedSkipCleanup"))
	}()

	tk.MustExec("begin optimistic")
	tk.MustExec("insert into ai_candidates values (200, 'used@example.com')")
	tk.MustExec("delete from ai_candidates where id = 200")
	tk.MustExec("update ai_accounts set balance = balance - 100 where id = 1")
	commitErr := tk.ExecToErr("commit")
	require.ErrorContains(t, commitErr, "Duplicate entry 'used@example.com'")

	expiringOracle.expireLocks.Store(true)
	tk2 := testkit.NewTestKit(t, store)
	tk2.Session().SetConnectionID(2)
	tk2.MustExec("use test")
	accountRows := tk2.MustQuery("select id, balance from ai_accounts").Rows()
	proofRows := tk2.MustQuery("select id, email from ai_candidates order by id").Rows()
	require.Equal(t, testkit.Rows("100 used@example.com"), proofRows)
	require.Equal(t, testkit.Rows("1 0"), accountRows)
}
