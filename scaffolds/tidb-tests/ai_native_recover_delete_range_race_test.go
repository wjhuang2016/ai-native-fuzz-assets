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
// See the License for the specific language governing permissions and
// limitations under the License.

package flashbacktest

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/config/kerneltype"
	ddlutil "github.com/pingcap/tidb/pkg/ddl/util"
	"github.com/pingcap/tidb/pkg/store/gcworker"
	"github.com/pingcap/tidb/pkg/tablecodec"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/testkit/testfailpoint"
	"github.com/pingcap/tidb/tests/realtikvtest"
	"github.com/stretchr/testify/require"
	"github.com/tikv/client-go/v2/tikv"
	pd "github.com/tikv/pd/client"
)

func TestRecoverTableAfterDeleteRangeTaskLoaded(t *testing.T) {
	if !*realtikvtest.WithRealTiKV {
		t.Skip("requires real TiKV delete-range execution")
	}

	store := realtikvtest.CreateMockStoreAndSetup(t)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	timeBeforeDrop, _, safePointSQL, resetGC := MockGC(tk)
	defer resetGC()
	tk.MustExec(fmt.Sprintf(safePointSQL, timeBeforeDrop))
	tk.MustExec("set global tidb_gc_enable = on")

	worker, err := gcworker.NewMockGCWorker(store)
	require.NoError(t, err)

	type droppedTable struct {
		jobID   int64
		tableID int64
	}
	createAndDrop := func(name string) droppedTable {
		tk.MustExec(fmt.Sprintf("create table %s (id bigint primary key, k bigint, payload longblob, key idx_k(k))", name))
		values := make([]string, 0, 32)
		for i := 1; i <= 32; i++ {
			values = append(values, fmt.Sprintf("(%d,%d,repeat('x',8192))", i, i*10))
		}
		tk.MustExec(fmt.Sprintf("insert into %s values %s", name, strings.Join(values, ",")))
		tk.MustQuery(fmt.Sprintf("select count(*), sum(id), sum(k), sum(length(payload)) from %s", name)).
			Check(testkit.Rows("32 528 5280 262144"))
		tableIDRows := tk.MustQuery(fmt.Sprintf(
			"select tidb_table_id from information_schema.tables where table_schema = 'test' and table_name = '%s'", name,
		)).Rows()
		require.Len(t, tableIDRows, 1)
		tableID, err := strconv.ParseInt(tableIDRows[0][0].(string), 10, 64)
		require.NoError(t, err)
		tk.MustExec("drop table " + name)

		rows := tk.MustQuery(fmt.Sprintf(
			"admin show ddl jobs where db_name = 'test' and table_name = '%s' and job_type = 'drop table'", name,
		)).Rows()
		require.NotEmpty(t, rows)
		jobID, err := strconv.ParseInt(rows[0][0].(string), 10, 64)
		require.NoError(t, err)
		tk.EventuallyMustQueryAndCheck(
			fmt.Sprintf("select count(*) from mysql.gc_delete_range where job_id = %d", jobID),
			nil, testkit.Rows("1"), 10*time.Second, 50*time.Millisecond)
		t.Logf("dropped %s: job_id=%d table_id=%d", name, jobID, tableID)
		return droppedTable{jobID: jobID, tableID: tableID}
	}

	assertPreimage := func(name string) {
		fresh := testkit.NewTestKit(t, store)
		fresh.MustExec("use test")
		fresh.MustQuery(fmt.Sprintf("select count(*), sum(id), sum(k), sum(length(payload)) from %s", name)).
			Check(testkit.Rows("32 528 5280 262144"))
		fresh.MustQuery(fmt.Sprintf("select count(*) from %s use index(idx_k)", name)).Check(testkit.Rows("32"))
		fresh.MustExec("admin check table " + name)
	}

	// Control: once RECOVER removes the persistent task, a later GC load cannot
	// acquire ownership of the old physical range.
	control := createAndDrop("t_recover_gc_control")
	tk.MustExec(fmt.Sprintf("recover table by job %d", control.jobID))
	require.NoError(t, worker.DeleteRanges(context.Background(), math.MaxInt64))
	assertPreimage("t_recover_gc_control")

	// Target: the GC worker owns an in-memory copy before RECOVER removes the
	// persistent task. RECOVER publishes the old table ID, then the stale owner
	// resumes and physically deletes that live range.
	target := createAndDrop("t_recover_gc_target")
	loaded := make(chan []ddlutil.DelRangeTask, 1)
	resume := make(chan struct{})
	var loadedOnce sync.Once
	var resumeOnce sync.Once
	resumeGC := func() { resumeOnce.Do(func() { close(resume) }) }
	t.Cleanup(resumeGC)
	testfailpoint.EnableCall(t, "github.com/pingcap/tidb/pkg/store/gcworker/afterLoadDeleteRanges",
		func(ranges []ddlutil.DelRangeTask) {
			if len(ranges) == 0 {
				return
			}
			t.Logf("deleteRanges loaded: %+v", ranges)
			loadedOnce.Do(func() { loaded <- ranges })
			<-resume
		})

	deleteErrCh := make(chan error, 1)
	go func() {
		deleteErrCh <- worker.DeleteRanges(context.Background(), math.MaxInt64)
	}()
	select {
	case ranges := <-loaded:
		require.Len(t, ranges, 1)
		require.Equal(t, target.jobID, ranges[0].JobID)
		require.Equal(t, target.tableID, tablecodec.DecodeTableID(ranges[0].StartKey))
	case <-time.After(10 * time.Second):
		require.FailNow(t, "delete-range worker did not load the target task")
	}

	tk.MustExec(fmt.Sprintf("recover table by job %d", target.jobID))
	assertPreimage("t_recover_gc_target")
	resumeGC()
	require.NoError(t, <-deleteErrCh)

	fresh := testkit.NewTestKit(t, store)
	fresh.MustExec("use test")
	fresh.MustQuery("select count(*) from t_recover_gc_target").Check(testkit.Rows("0"))
	fresh.MustQuery("select count(*) from t_recover_gc_target use index(idx_k)").Check(testkit.Rows("0"))
	// ADMIN CHECK cannot detect complete table+index range loss, so the preserved
	// pre-drop aggregate above is the decisive oracle.
	fresh.MustExec("admin check table t_recover_gc_target")
}

func TestRecoverTableUnifiedGCAfterDeleteRangeTaskLoaded(t *testing.T) {
	if !*realtikvtest.WithRealTiKV {
		t.Skip("requires real TiKV delete-range execution")
	}
	if !kerneltype.IsNextGen() {
		t.Skip("requires a unified-GC keyspace")
	}

	const keyspaceName = "aifuzzrecover"
	runtimes := realtikvtest.PrepareForCrossKSTest(t, keyspaceName)
	store := runtimes[keyspaceName].Store
	meta := store.GetCodec().GetKeyspaceMeta()
	require.NotNil(t, meta)
	require.False(t, pd.IsKeyspaceUsingKeyspaceLevelGC(meta))

	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	timeBeforeDrop, _, safePointSQL, resetGC := MockGC(tk)
	defer resetGC()
	tk.MustExec(fmt.Sprintf(safePointSQL, timeBeforeDrop))
	tk.MustExec("set global tidb_gc_enable = on")

	tk.MustExec("create table t_recover_unified (id bigint primary key, k bigint, payload longblob, key idx_k(k))")
	values := make([]string, 0, 32)
	for i := 1; i <= 32; i++ {
		values = append(values, fmt.Sprintf("(%d,%d,repeat('x',8192))", i, i*10))
	}
	tk.MustExec("insert into t_recover_unified values " + strings.Join(values, ","))
	tableIDRows := tk.MustQuery(
		"select tidb_table_id from information_schema.tables where table_schema = 'test' and table_name = 't_recover_unified'",
	).Rows()
	require.Len(t, tableIDRows, 1)
	tableID, err := strconv.ParseInt(tableIDRows[0][0].(string), 10, 64)
	require.NoError(t, err)
	tk.MustExec("drop table t_recover_unified")

	jobRows := tk.MustQuery(
		"admin show ddl jobs where db_name = 'test' and table_name = 't_recover_unified' and job_type = 'drop table'",
	).Rows()
	require.NotEmpty(t, jobRows)
	jobID, err := strconv.ParseInt(jobRows[0][0].(string), 10, 64)
	require.NoError(t, err)
	tk.EventuallyMustQueryAndCheck(
		fmt.Sprintf("select count(*) from mysql.gc_delete_range where job_id = %d", jobID),
		nil, testkit.Rows("1"), 10*time.Second, 50*time.Millisecond)
	taskRows := tk.MustQuery(fmt.Sprintf("select ts from mysql.gc_delete_range where job_id = %d", jobID)).Rows()
	require.Len(t, taskRows, 1)
	taskTS, err := strconv.ParseUint(taskRows[0][0].(string), 10, 64)
	require.NoError(t, err)

	tikvStore := store.(tikv.Storage)
	pdSafePoint, err := tikvStore.GetRegionCache().PDClient().UpdateGCSafePoint(context.Background(), taskTS+1)
	require.NoError(t, err)
	require.GreaterOrEqual(t, pdSafePoint, taskTS+1)
	t.Logf("unified GC setup: keyspace=%s job_id=%d table_id=%d task_ts=%d pd_safe_point=%d",
		keyspaceName, jobID, tableID, taskTS, pdSafePoint)

	loaded := make(chan ddlutil.DelRangeTask, 1)
	resume := make(chan struct{})
	var loadedOnce sync.Once
	var resumeOnce sync.Once
	resumeGC := func() { resumeOnce.Do(func() { close(resume) }) }
	t.Cleanup(resumeGC)
	testfailpoint.EnableCall(t, "github.com/pingcap/tidb/pkg/store/gcworker/afterLoadDeleteRanges",
		func(ranges []ddlutil.DelRangeTask) {
			for _, task := range ranges {
				if task.JobID != jobID {
					continue
				}
				loadedOnce.Do(func() { loaded <- task })
				<-resume
				return
			}
		})

	worker, err := gcworker.NewMockGCWorker(store)
	require.NoError(t, err)
	deleteErrCh := make(chan error, 1)
	go func() {
		deleteErrCh <- worker.RunKeyspaceDeleteRange(context.Background())
	}()
	select {
	case task := <-loaded:
		require.Equal(t, tableID, tablecodec.DecodeTableID(task.StartKey))
	case <-time.After(10 * time.Second):
		require.FailNow(t, "unified GC did not load the target task")
	}

	// RECOVER validates the stale mysql.tidb safe point, removes the persistent
	// task, and publishes the old physical table while unified GC still owns it.
	tk.MustExec(fmt.Sprintf("recover table by job %d", jobID))
	tk.MustQuery("select count(*), sum(id), sum(k), sum(length(payload)) from t_recover_unified").
		Check(testkit.Rows("32 528 5280 262144"))
	tk.MustQuery(fmt.Sprintf("select count(*) from mysql.gc_delete_range where job_id = %d", jobID)).
		Check(testkit.Rows("0"))
	resumeGC()
	require.NoError(t, <-deleteErrCh)

	fresh := testkit.NewTestKit(t, store)
	fresh.MustExec("use test")
	fresh.MustQuery("select count(*) from t_recover_unified").Check(testkit.Rows("0"))
	fresh.MustQuery("select count(*) from t_recover_unified use index(idx_k)").Check(testkit.Rows("0"))
	fresh.MustExec("admin check table t_recover_unified")
}
