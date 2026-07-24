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

package logclient_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/pingcap/failpoint"
	logclient "github.com/pingcap/tidb/br/pkg/restore/log_client"
	"github.com/pingcap/tidb/br/pkg/stream"
	"github.com/pingcap/tidb/br/pkg/utiltest"
	"github.com/pingcap/tidb/pkg/kv"
	"github.com/pingcap/tidb/pkg/meta"
	"github.com/pingcap/tidb/pkg/meta/model"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/types"
	"github.com/stretchr/testify/require"
)

func TestAINativePITRRebaseFailureCanOverwriteRestoredRow(t *testing.T) {
	ctx := context.Background()
	s := utiltest.CreateRestoreSchemaSuite(t)
	tk := testkit.NewTestKit(t, s.Mock.Storage)
	tk.MustExec("use test")
	tk.MustExec("create table t (id bigint primary key auto_increment, data varchar(32)) auto_id_cache=1")
	tk.MustExec("insert into t (data) values ('live-one')")

	tbl, err := s.Mock.Domain.InfoSchema().TableByName(
		ctx,
		ast.NewCIStr("test"),
		ast.NewCIStr("t"),
	)
	require.NoError(t, err)
	dbID := tbl.Meta().DBID
	tableID := tbl.Meta().ID

	// Model a row and high-water counter restored through raw KV replay. This
	// intentionally does not notify the separated autoid service.
	txn, err := s.Mock.Storage.Begin()
	require.NoError(t, err)
	_, err = tbl.AddRecord(
		tk.Session().GetTableCtx(),
		txn,
		types.MakeDatums(2, "restored-two"),
	)
	require.NoError(t, err)
	require.NoError(t, txn.Commit(ctx))

	var restoredBase int64
	require.NoError(t, kv.RunInNewTxn(ctx, s.Mock.Storage, false, func(
		_ context.Context,
		txn kv.Transaction,
	) error {
		acc := meta.NewMutator(txn).
			GetAutoIDAccessors(dbID, tableID).
			IncrementID(model.TableInfoVersion5)
		cur, err := acc.Get()
		if err != nil {
			return err
		}
		restoredBase = cur + 1000000
		_, err = acc.Inc(restoredBase - cur)
		return err
	}))
	tk.MustQuery("select * from t order by id").Check(testkit.Rows(
		"1 live-one",
		"2 restored-two",
	))

	client := logclient.TEST_NewLogClient(123, 1, 2, 3, s.Mock.Domain, nil)
	schemasReplace := &stream.SchemasReplace{
		DbReplaceMap: map[stream.UpstreamID]*stream.DBReplace{
			dbID: {
				Name: "test",
				DbID: dbID,
				TableMap: map[stream.UpstreamID]*stream.TableReplace{
					tableID: {Name: "t", TableID: tableID},
				},
			},
		},
	}

	fp := "github.com/pingcap/tidb/pkg/kv/mockCommitErrorInNewTxn"
	require.NoError(t, failpoint.Enable(fp, `return("no_retry")`))
	t.Cleanup(func() {
		_ = failpoint.Disable(fp)
	})

	// RED: the only table's required repair fails, but the helper reports
	// success. REPLACE reuses restored id=2 and removes its old payload.
	require.NoError(t, client.RebaseAutoIncrementIDForSepAutoIncTables(ctx, schemasReplace))
	tk.MustExec("replace into t (data) values ('replacement')")
	tk.MustQuery("select row_count(), last_insert_id()").Check(testkit.Rows("2 2"))
	tk.MustQuery("select * from t order by id").Check(testkit.Rows(
		"1 live-one",
		"2 replacement",
	))

	// Matched GREEN: restore the overwritten preimage, remove only the fault,
	// then repeat the same rebase and SQL write.
	tk.MustExec("replace into t (id, data) values (2, 'restored-two')")
	require.NoError(t, failpoint.Disable(fp))
	require.NoError(t, client.RebaseAutoIncrementIDForSepAutoIncTables(ctx, schemasReplace))
	tk.MustExec("replace into t (data) values ('green-replacement')")
	tk.MustQuery("select row_count(), last_insert_id()").Check(
		testkit.Rows(fmt.Sprintf("1 %d", restoredBase+1)),
	)
	tk.MustQuery("select * from t order by id").Check(testkit.Rows(
		"1 live-one",
		"2 restored-two",
		fmt.Sprintf("%d green-replacement", restoredBase+1),
	))
}
