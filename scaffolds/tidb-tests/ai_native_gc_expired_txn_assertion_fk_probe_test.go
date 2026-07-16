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

	"github.com/pingcap/kvproto/pkg/debugpb"
	"github.com/pingcap/tidb/pkg/kv"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/tests/realtikvtest"
	"github.com/stretchr/testify/require"
	"github.com/tikv/client-go/v2/tikv"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type aiNativeGCFastControllableStore interface {
	GC(context.Context, uint64, ...tikv.GCOpt) (uint64, error)
	UpdateTxnSafePointCache(uint64, time.Time)
}

func aiNativeGCFastCompact(t *testing.T, store kv.Storage) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stores, err := store.(kv.StorageWithPD).GetPDClient().GetAllStores(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, stores)
	for _, storeMeta := range stores {
		if storeMeta.GetAddress() == "" {
			continue
		}
		conn, err := grpc.NewClient(storeMeta.GetAddress(), grpc.WithTransportCredentials(insecure.NewCredentials()))
		require.NoError(t, err)
		client := debugpb.NewDebugClient(conn)
		for _, cf := range []string{"write", "default"} {
			_, err = client.Compact(ctx, &debugpb.CompactRequest{
				Db:                        debugpb.DB_KV,
				Cf:                        cf,
				Threads:                   1,
				BottommostLevelCompaction: debugpb.BottommostLevelCompaction_Force,
			})
			require.NoError(t, err)
		}
		require.NoError(t, conn.Close())
	}
}

func TestAINativeExpiredTxnFastAssertionMatrix(t *testing.T) {
	store := realtikvtest.CreateMockStoreAndSetup(t)
	deleter := testkit.NewTestKit(t, store)
	observer := testkit.NewTestKit(t, store)
	deleter.MustExec("use test")
	observer.MustExec("use test")
	defaults := observer.MustQuery("select @@global.tidb_enable_metadata_lock, @@global.tidb_enable_foreign_key, @@foreign_key_checks, @@tidb_enable_1pc, @@tidb_enable_async_commit, @@global.tidb_txn_assertion_level")
	defaultRows := defaults.Rows()
	t.Logf("runtime global/session values before target overrides: %v", defaultRows)
	require.Len(t, defaultRows, 1)
	require.Equal(t, "1", defaultRows[0][0])
	require.Equal(t, "1", defaultRows[0][1])
	require.Equal(t, "1", defaultRows[0][2])

	type matrixCase struct {
		name         string
		dml          string
		startsAbsent bool
		assertion    string
		tk           *testkit.TestKit
	}
	cases := []matrixCase{
		{name: "update", dml: "update ai_gc_fast_update set v=11 where id=1", tk: testkit.NewTestKit(t, store)},
		{name: "update_ignore", dml: "update ignore ai_gc_fast_update_ignore set v=11 where id=1", tk: testkit.NewTestKit(t, store)},
		{name: "on_duplicate", dml: "insert into ai_gc_fast_on_duplicate values(1,11) on duplicate key update v=values(v)", tk: testkit.NewTestKit(t, store)},
		{name: "replace", dml: "replace into ai_gc_fast_replace values(1,11)", tk: testkit.NewTestKit(t, store)},
		{name: "insert_aba", dml: "insert into ai_gc_fast_insert_aba values(1,11)", startsAbsent: true, assertion: "fast", tk: testkit.NewTestKit(t, store)},
		{name: "insert_aba_strict", dml: "insert into ai_gc_fast_insert_aba_strict values(1,11)", startsAbsent: true, assertion: "strict", tk: testkit.NewTestKit(t, store)},
	}

	for _, tc := range cases {
		tableName := "ai_gc_fast_" + tc.name
		deleter.MustExec("create table " + tableName + " (id bigint primary key, v bigint)")
		if !tc.startsAbsent {
			deleter.MustExec("insert into " + tableName + " values(1,10)")
		}
		tc.tk.MustExec("use test")
		tc.tk.MustExec("set @@tidb_enable_1pc=off")
		tc.tk.MustExec("set @@tidb_enable_async_commit=off")
		assertion := tc.assertion
		if assertion == "" {
			assertion = "fast"
		}
		tc.tk.MustExec("set @@tidb_txn_assertion_level=" + assertion)
		tc.tk.MustExec("begin optimistic")
		tc.tk.MustExec(tc.dml)
	}

	deleter.MustExec("create table ai_gc_fk_parent (id bigint primary key)")
	deleter.MustExec("create table ai_gc_fk_child (id bigint primary key, pid bigint, foreign key (pid) references ai_gc_fk_parent(id))")
	deleter.MustExec("insert into ai_gc_fk_parent values(1)")
	fkTxn := testkit.NewTestKit(t, store)
	fkTxn.MustExec("use test")
	fkTxn.MustExec("set @@tidb_enable_1pc=off")
	fkTxn.MustExec("set @@tidb_enable_async_commit=off")
	fkTxn.MustExec("set @@tidb_txn_assertion_level=strict")
	fkTxn.MustExec("begin optimistic")
	fkTxn.MustExec("insert into ai_gc_fk_child values(1,1)")

	control := testkit.NewTestKit(t, store)
	control.MustExec("use test")
	control.MustExec("set @@tidb_enable_1pc=off")
	control.MustExec("set @@tidb_enable_async_commit=off")
	control.MustExec("set @@tidb_txn_assertion_level=fast")
	deleter.MustExec("create table ai_gc_fast_insert_aba_control (id bigint primary key, v bigint)")
	control.MustExec("begin optimistic")
	control.MustExec("insert into ai_gc_fast_insert_aba_control values(1,11)")
	deleter.MustExec("insert into ai_gc_fast_insert_aba_control values(1,99)")
	deleter.MustExec("delete from ai_gc_fast_insert_aba_control where id=1")
	controlErr := control.ExecToErr("commit")
	controlRows := observer.MustQuery("select id,v from ai_gc_fast_insert_aba_control").Rows()
	t.Logf("case=insert_aba_without_gc commit_err=%v rows=%v", controlErr, controlRows)
	require.Error(t, controlErr)
	require.Empty(t, controlRows)

	deleter.MustExec("create table ai_gc_fk_parent_control (id bigint primary key)")
	deleter.MustExec("create table ai_gc_fk_child_control (id bigint primary key, pid bigint, foreign key (pid) references ai_gc_fk_parent_control(id))")
	deleter.MustExec("insert into ai_gc_fk_parent_control values(1)")
	fkControl := testkit.NewTestKit(t, store)
	fkControl.MustExec("use test")
	fkControl.MustExec("set @@tidb_enable_1pc=off")
	fkControl.MustExec("set @@tidb_enable_async_commit=off")
	fkControl.MustExec("set @@tidb_txn_assertion_level=strict")
	fkControl.MustExec("begin optimistic")
	fkControl.MustExec("insert into ai_gc_fk_child_control values(1,1)")
	deleter.MustExec("delete from ai_gc_fk_parent_control where id=1")
	fkControlErr := fkControl.ExecToErr("commit")
	fkControlOrphans := observer.MustQuery("select c.id,c.pid from ai_gc_fk_child_control c left join ai_gc_fk_parent_control p on p.id=c.pid where p.id is null").Rows()
	t.Logf("case=fk_orphan_strict_without_gc commit_err=%v orphans=%v", fkControlErr, fkControlOrphans)
	require.Error(t, fkControlErr)
	require.Empty(t, fkControlOrphans)

	deleter.MustExec("insert into ai_gc_fast_insert_aba values(1,99)")
	deleter.MustExec("insert into ai_gc_fast_insert_aba_strict values(1,99)")
	deleter.MustExec("begin")
	for _, tc := range cases {
		deleter.MustExec("delete from ai_gc_fast_" + tc.name + " where id=1")
	}
	deleter.MustExec("delete from ai_gc_fk_parent where id=1")
	deleter.MustExec("commit")
	deleteCommitTS := deleter.Session().GetSessionVars().LastCommitTS
	require.NotZero(t, deleteCommitTS)

	gcStore := store.(aiNativeGCFastControllableStore)
	newSafePoint, err := gcStore.GC(context.Background(), deleteCommitTS)
	require.NoError(t, err)
	require.GreaterOrEqual(t, newSafePoint, deleteCommitTS)
	time.Sleep(12 * time.Second)
	aiNativeGCFastCompact(t, store)
	gcStore.UpdateTxnSafePointCache(newSafePoint, time.Now())

	var violations []string
	for _, tc := range cases {
		err := tc.tk.ExecToErr("commit")
		rows := observer.MustQuery("select id,v from ai_gc_fast_" + tc.name).Rows()
		t.Logf("case=%s commit_err=%v rows=%v", tc.name, err, rows)
		if err == nil || len(rows) != 0 {
			violations = append(violations, tc.name)
		}
	}
	fkErr := fkTxn.ExecToErr("commit")
	orphans := observer.MustQuery("select c.id,c.pid from ai_gc_fk_child c left join ai_gc_fk_parent p on p.id=c.pid where p.id is null").Rows()
	t.Logf("case=fk_orphan_strict commit_err=%v orphans=%v", fkErr, orphans)
	if fkErr == nil || len(orphans) != 0 {
		violations = append(violations, "fk_orphan_strict")
	}
	require.Empty(t, violations, "expired transactions committed or left rows")
}
