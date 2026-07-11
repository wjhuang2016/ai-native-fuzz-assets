package ddl_test

import (
	"fmt"
	"testing"

	"github.com/pingcap/tidb/pkg/sessionctx/vardef"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/testkit/testfailpoint"
	"github.com/stretchr/testify/require"
)

func enableTransientShapeProbe(t *testing.T, shape string) {
	testfailpoint.Enable(t, "github.com/pingcap/tidb/pkg/ddl/mockBackfillRunTransientErr", fmt.Sprintf("1*return(%q)", shape))
}

func runAddIndexTransientShapeProbe(t *testing.T, tableName string) {
	store := testkit.CreateMockStore(t)
	limit := vardef.GetDDLErrorCountLimit()
	vardef.SetDDLErrorCountLimit(5)
	t.Cleanup(func() {
		vardef.SetDDLErrorCountLimit(limit)
	})

	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists " + tableName)
	tk.MustExec("create table " + tableName + " (a int primary key, b int, c int)")
	batchInsert(tk, tableName, 0, 128)
	tk.MustExec("alter table " + tableName + " add index idx_b(b)")
	tk.MustExec("admin check table " + tableName)
}

func runModifyColumnTransientShapeProbe(t *testing.T, tableName, wantErr string) {
	store := testkit.CreateMockStore(t)
	limit := vardef.GetDDLErrorCountLimit()
	vardef.SetDDLErrorCountLimit(5)
	t.Cleanup(func() {
		vardef.SetDDLErrorCountLimit(limit)
	})

	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists " + tableName)
	tk.MustExec("create table " + tableName + " (a int primary key, b int, c int)")
	batchInsert(tk, tableName, 0, 128)

	err := tk.ExecToErr("alter table " + tableName + " change column b b varchar(16)")
	require.Error(t, err)
	require.Contains(t, err.Error(), wantErr)
	tk.MustQuery("show create table " + tableName).Check(testkit.Rows(
		fmt.Sprintf("%s CREATE TABLE `%s` (\n", tableName, tableName) +
			"  `a` int(11) NOT NULL,\n" +
			"  `b` int(11) DEFAULT NULL,\n" +
			"  `c` int(11) DEFAULT NULL,\n" +
			"  PRIMARY KEY (`a`) /*T![clustered_index] CLUSTERED */\n" +
			") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin",
	))
	tk.MustExec("admin check table " + tableName)
}

func runAddIndexTransientOutcomeProbe(t *testing.T, tableName string) {
	store := testkit.CreateMockStore(t)
	limit := vardef.GetDDLErrorCountLimit()
	vardef.SetDDLErrorCountLimit(5)
	t.Cleanup(func() {
		vardef.SetDDLErrorCountLimit(limit)
	})

	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists " + tableName)
	tk.MustExec("create table " + tableName + " (a int primary key, b int, c int)")
	batchInsert(tk, tableName, 0, 128)

	err := tk.ExecToErr("alter table " + tableName + " add index idx_b(b)")
	t.Logf("add-index outcome err=%v", err)
	tk.MustExec("admin check table " + tableName)
	t.Logf("add-index show create: %v", tk.MustQuery("show create table "+tableName).Rows())
}

func runModifyColumnTransientOutcomeProbe(t *testing.T, tableName string) {
	store := testkit.CreateMockStore(t)
	limit := vardef.GetDDLErrorCountLimit()
	vardef.SetDDLErrorCountLimit(5)
	t.Cleanup(func() {
		vardef.SetDDLErrorCountLimit(limit)
	})

	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists " + tableName)
	tk.MustExec("create table " + tableName + " (a int primary key, b int, c int)")
	batchInsert(tk, tableName, 0, 128)

	err := tk.ExecToErr("alter table " + tableName + " change column b b varchar(16)")
	t.Logf("modify-column outcome err=%v", err)
	tk.MustExec("admin check table " + tableName)
	t.Logf("modify-column show create: %v", tk.MustQuery("show create table "+tableName).Rows())
}

func TestAINativeAddIndexRetriesSingleGrpcUnavailableProbe(t *testing.T) {
	store := testkit.CreateMockStore(t)
	limit := vardef.GetDDLErrorCountLimit()
	vardef.SetDDLErrorCountLimit(5)
	t.Cleanup(func() {
		vardef.SetDDLErrorCountLimit(limit)
	})

	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists ai_add_idx_retry")
	tk.MustExec("create table ai_add_idx_retry (a int primary key, b int, c int)")
	batchInsert(tk, "ai_add_idx_retry", 0, 128)

	testfailpoint.Enable(t, "github.com/pingcap/tidb/pkg/ddl/mockBackfillRunGrpcUnavailable", "1*return(true)")

	tk.MustExec("alter table ai_add_idx_retry add index idx_b(b)")
	tk.MustExec("admin check table ai_add_idx_retry")
}

func TestAINativeModifyColumnFailsOnSingleGrpcUnavailableProbe(t *testing.T) {
	store := testkit.CreateMockStore(t)
	limit := vardef.GetDDLErrorCountLimit()
	vardef.SetDDLErrorCountLimit(5)
	t.Cleanup(func() {
		vardef.SetDDLErrorCountLimit(limit)
	})

	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists ai_modify_col_retry")
	tk.MustExec("create table ai_modify_col_retry (a int primary key, b int, c int)")
	batchInsert(tk, "ai_modify_col_retry", 0, 128)

	testfailpoint.Enable(t, "github.com/pingcap/tidb/pkg/ddl/mockBackfillRunGrpcUnavailable", "1*return(true)")

	err := tk.ExecToErr("alter table ai_modify_col_retry change column b b varchar(16)")
	require.Error(t, err)
	require.Contains(t, err.Error(), "mock backfill grpc unavailable")
	tk.MustQuery("show create table ai_modify_col_retry").Check(testkit.Rows(
		"ai_modify_col_retry CREATE TABLE `ai_modify_col_retry` (\n" +
			"  `a` int(11) NOT NULL,\n" +
			"  `b` int(11) DEFAULT NULL,\n" +
			"  `c` int(11) DEFAULT NULL,\n" +
			"  PRIMARY KEY (`a`) /*T![clustered_index] CLUSTERED */\n" +
			") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin",
	))
	tk.MustExec("admin check table ai_modify_col_retry")
}

func TestAINativeAddIndexRetriesSingleGrpcDataLossProbe(t *testing.T) {
	store := testkit.CreateMockStore(t)
	limit := vardef.GetDDLErrorCountLimit()
	vardef.SetDDLErrorCountLimit(5)
	t.Cleanup(func() {
		vardef.SetDDLErrorCountLimit(limit)
	})

	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists ai_add_idx_dataloss")
	tk.MustExec("create table ai_add_idx_dataloss (a int primary key, b int, c int)")
	batchInsert(tk, "ai_add_idx_dataloss", 0, 128)

	testfailpoint.Enable(t, "github.com/pingcap/tidb/pkg/ddl/mockBackfillRunGrpcDataLoss", "1*return(true)")

	tk.MustExec("alter table ai_add_idx_dataloss add index idx_b(b)")
	tk.MustExec("admin check table ai_add_idx_dataloss")
}

func TestAINativeModifyColumnFailsOnSingleGrpcDataLossProbe(t *testing.T) {
	store := testkit.CreateMockStore(t)
	limit := vardef.GetDDLErrorCountLimit()
	vardef.SetDDLErrorCountLimit(5)
	t.Cleanup(func() {
		vardef.SetDDLErrorCountLimit(limit)
	})

	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists ai_modify_col_dataloss")
	tk.MustExec("create table ai_modify_col_dataloss (a int primary key, b int, c int)")
	batchInsert(tk, "ai_modify_col_dataloss", 0, 128)

	testfailpoint.Enable(t, "github.com/pingcap/tidb/pkg/ddl/mockBackfillRunGrpcDataLoss", "1*return(true)")

	err := tk.ExecToErr("alter table ai_modify_col_dataloss change column b b varchar(16)")
	require.Error(t, err)
	require.Contains(t, err.Error(), "mock backfill grpc dataloss")
	tk.MustQuery("show create table ai_modify_col_dataloss").Check(testkit.Rows(
		"ai_modify_col_dataloss CREATE TABLE `ai_modify_col_dataloss` (\n" +
			"  `a` int(11) NOT NULL,\n" +
			"  `b` int(11) DEFAULT NULL,\n" +
			"  `c` int(11) DEFAULT NULL,\n" +
			"  PRIMARY KEY (`a`) /*T![clustered_index] CLUSTERED */\n" +
			") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin",
	))
	tk.MustExec("admin check table ai_modify_col_dataloss")
}

func TestAINativeAddIndexRetriesTransientConnErrorFamilyProbe(t *testing.T) {
	cases := []struct {
		name  string
		shape string
		table string
	}{
		{name: "mysql_invalid_conn", shape: "mysql_invalid_conn", table: "ai_add_idx_invalid_conn"},
		{name: "driver_bad_conn", shape: "driver_bad_conn", table: "ai_add_idx_bad_conn"},
		{name: "net_conn_reset", shape: "net_conn_reset", table: "ai_add_idx_conn_reset"},
		{name: "net_broken_pipe", shape: "net_broken_pipe", table: "ai_add_idx_broken_pipe"},
		{name: "net_conn_refused", shape: "net_conn_refused", table: "ai_add_idx_conn_refused"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enableTransientShapeProbe(t, tc.shape)
			runAddIndexTransientShapeProbe(t, tc.table)
		})
	}
}

func TestAINativeModifyColumnFailsTransientConnErrorFamilyProbe(t *testing.T) {
	cases := []struct {
		name    string
		shape   string
		table   string
		wantErr string
	}{
		{name: "mysql_invalid_conn", shape: "mysql_invalid_conn", table: "ai_modify_col_invalid_conn", wantErr: "invalid connection"},
		{name: "driver_bad_conn", shape: "driver_bad_conn", table: "ai_modify_col_bad_conn", wantErr: "driver: bad connection"},
		{name: "net_conn_reset", shape: "net_conn_reset", table: "ai_modify_col_conn_reset", wantErr: "connection reset by peer"},
		{name: "net_broken_pipe", shape: "net_broken_pipe", table: "ai_modify_col_broken_pipe", wantErr: "broken pipe"},
		{name: "net_conn_refused", shape: "net_conn_refused", table: "ai_modify_col_conn_refused", wantErr: "connection refused"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enableTransientShapeProbe(t, tc.shape)
			runModifyColumnTransientShapeProbe(t, tc.table, tc.wantErr)
		})
	}
}

func TestAINativeIngestRetryableErrorFamilyOutcomeProbe(t *testing.T) {
	cases := []struct {
		name  string
		shape string
	}{
		{name: "ingest_kv_not_leader", shape: "ingest_kv_not_leader"},
		{name: "ingest_kv_region_not_found", shape: "ingest_kv_region_not_found"},
		{name: "ingest_kv_no_leader", shape: "ingest_kv_no_leader"},
	}

	for _, tc := range cases {
		t.Run("add_index/"+tc.name, func(t *testing.T) {
			enableTransientShapeProbe(t, tc.shape)
			runAddIndexTransientOutcomeProbe(t, "ai_add_idx_probe_"+tc.name)
		})
		t.Run("modify_column/"+tc.name, func(t *testing.T) {
			enableTransientShapeProbe(t, tc.shape)
			runModifyColumnTransientOutcomeProbe(t, "ai_modify_col_probe_"+tc.name)
		})
	}
}
