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

package test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pingcap/failpoint"
	"github.com/pingcap/tidb/pkg/config"
	"github.com/pingcap/tidb/pkg/config/kerneltype"
	"github.com/pingcap/tidb/pkg/domain"
	"github.com/pingcap/tidb/pkg/kv"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/tablecodec"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/stretchr/testify/require"
)

func TestAINative1PCSchemaCheckMustCoverPrewrite(t *testing.T) {
	if kerneltype.IsNextGen() {
		t.Skip("MDL cannot be disabled in nextgen")
	}
	defer config.RestoreFunc()()
	config.UpdateGlobal(func(conf *config.Config) {
		conf.TiKVClient.AsyncCommit.SafeWindow = time.Second
	})

	store := testkit.CreateMockStoreWithSchemaLease(t, time.Second)
	setup := testkit.NewTestKit(t, store)
	setup.MustExec("set global tidb_enable_metadata_lock=0")
	setup.MustExec("set global tidb_txn_mode=''")

	for _, tc := range []struct {
		name        string
		table       string
		enableOnePC bool
	}{
		{name: "1pc", table: "ai_onepc_schema", enableOnePC: true},
		{name: "2pc-control", table: "ai_twopc_schema", enableOnePC: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writer := testkit.NewTestKit(t, store)
			ddl := testkit.NewTestKit(t, store)
			observer := testkit.NewTestKit(t, store)
			writer.MustExec("use test")
			ddl.MustExec("use test")
			observer.MustExec("use test")
			writer.MustExec(fmt.Sprintf("drop table if exists %s", tc.table))
			writer.MustExec(fmt.Sprintf("create table %s (id int primary key, v int)", tc.table))
			writer.MustExec("set @@tidb_enable_async_commit=0")
			writer.MustExec(fmt.Sprintf("set @@tidb_enable_1pc=%d", map[bool]int{false: 0, true: 1}[tc.enableOnePC]))

			tbl, err := domain.GetDomain(writer.Session()).InfoSchema().TableByName(
				context.Background(), ast.NewCIStr("test"), ast.NewCIStr(tc.table),
			)
			require.NoError(t, err)
			oldTableID := tbl.Meta().ID

			require.NoError(t, failpoint.Enable("tikvclient/beforePrewrite", "1*pause"))
			commitDone := make(chan error, 1)
			go func() {
				commitDone <- writer.ExecToErr(fmt.Sprintf("insert into %s values (1, 10)", tc.table))
			}()

			select {
			case err = <-commitDone:
				require.Failf(t, "commit reached terminal result before pause", "err=%v", err)
			case <-time.After(300 * time.Millisecond):
			}

			ddl.MustExec(fmt.Sprintf("truncate table %s", tc.table))
			require.NoError(t, failpoint.Disable("tikvclient/beforePrewrite"))
			select {
			case err = <-commitDone:
			case <-time.After(10 * time.Second):
				t.Fatal("timed out waiting for commit after releasing beforePrewrite")
			}

			mode := writer.MustQuery("select json_extract(@@tidb_last_txn_info, '$.txn_commit_mode')").Rows()[0][0]
			rows := observer.MustQuery(fmt.Sprintf("select * from %s", tc.table)).Rows()
			oldRowKey := tablecodec.EncodeRowKeyWithHandle(oldTableID, kv.IntHandle(1))
			rawTxn, beginErr := store.Begin()
			require.NoError(t, beginErr)
			oldValue, oldValueErr := rawTxn.Get(context.Background(), oldRowKey)
			require.NoError(t, rawTxn.Rollback())
			t.Logf("mode=%v commitErr=%v currentRows=%v oldTableID=%d oldValueLen=%d oldValueErr=%v",
				mode, err, rows, oldTableID, len(oldValue.Value), oldValueErr)

			if err == nil {
				require.Equal(t, testkit.Rows("1 10"), rows, "a successful INSERT must be visible in the current table")
			} else {
				require.Empty(t, rows, "a failed INSERT must not be visible")
			}
			require.True(t, kv.ErrNotExist.Equal(oldValueErr),
				"the terminal result must not leave a committed row under the truncated table ID: %v", oldValueErr)
		})
	}
}
