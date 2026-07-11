package ingest_test

import (
	"context"
	"fmt"
	"math"
	"sync/atomic"
	"testing"

	"github.com/pingcap/tidb/pkg/config/kerneltype"
	"github.com/pingcap/tidb/pkg/ddl/ingest"
	"github.com/pingcap/tidb/pkg/ingestor/errdef"
	"github.com/pingcap/tidb/pkg/ingestor/ingestcli"
	"github.com/pingcap/tidb/pkg/meta/model"
	"github.com/pingcap/tidb/pkg/sessionctx/vardef"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/testkit/testfailpoint"
	"github.com/stretchr/testify/require"
	pderrors "github.com/tikv/pd/client/errs"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type injectedRetryBackendCtx struct {
	ingest.BackendCtx
	injectErr error
	injected  *atomic.Bool

	ingestCalls atomic.Int64
}

func (b *injectedRetryBackendCtx) Ingest(ctx context.Context) error {
	b.ingestCalls.Add(1)
	if b.injected != nil && b.injected.CompareAndSwap(false, true) {
		return b.injectErr
	}
	return b.BackendCtx.Ingest(ctx)
}

func TestAINativeAddIndexIngestRetryClassifierProbe(t *testing.T) {
	if kerneltype.IsNextGen() {
		t.Skip("add-index always runs on DXF with ingest mode in nextgen")
	}

	ops := []struct {
		name           string
		createTableSQL string
		alterSQL       string
		successMarker  string
	}{
		{
			name:           "add_index",
			createTableSQL: "create table t (a int primary key, b int);",
			alterSQL:       "alter table t add index idx(b);",
			successMarker:  "KEY `idx` (`b`)",
		},
		{
			name:           "add_primary_key",
			createTableSQL: "create table t (a int, b int);",
			alterSQL:       "alter table t add primary key idx(a);",
			successMarker:  "PRIMARY KEY (`a`)",
		},
	}

	cases := []struct {
		name string
		err  error
	}{
		{
			name: "grpc_unavailable",
			err:  status.Error(codes.Unavailable, "mock ingest grpc unavailable"),
		},
		{
			name: "pd_tso_retry_timeout",
			err:  pderrors.ErrClientCreateTSOStream.FastGenByArgs(pderrors.RetryTimeoutErr),
		},
		{
			name: "ingest_kv_not_leader",
			err:  &ingestcli.IngestAPIError{Err: errdef.ErrKVNotLeader.GenWithStack("mock ingest not leader")},
		},
		{
			name: "ingest_kv_region_not_found",
			err:  &ingestcli.IngestAPIError{Err: errdef.ErrKVRegionNotFound.GenWithStack("mock ingest region not found")},
		},
	}

	for _, op := range ops {
		op := op
		for _, tc := range cases {
			tc := tc
			t.Run(op.name+"/"+tc.name, func(t *testing.T) {
				store, _ := testkit.CreateMockStoreAndDomain(t)
				tk := testkit.NewTestKit(t, store)
				tk.MustExec("use test;")
				tk.MustExec("set global tidb_enable_dist_task = 0;")
				tk.MustExec("set @@tidb_ddl_reorg_worker_cnt = 1;")
				limit := vardef.GetDDLErrorCountLimit()
				vardef.SetDDLErrorCountLimit(5)
				t.Cleanup(func() {
					vardef.SetDDLErrorCountLimit(limit)
				})

				oldLitDiskRoot := ingest.LitDiskRoot
				oldLitMemRoot := ingest.LitMemRoot
				t.Cleanup(func() {
					ingest.LitInitialized = false
					ingest.LitDiskRoot = oldLitDiskRoot
					ingest.LitMemRoot = oldLitMemRoot
				})
				ingest.LitInitialized = true
				ingest.LitDiskRoot = ingest.NewDiskRootImpl(t.TempDir())
				ingest.LitMemRoot = ingest.NewMemRootImpl(math.MaxInt64)

				var observed atomic.Pointer[injectedRetryBackendCtx]
				var injectedOnce atomic.Bool
				tkForBackend := testkit.NewTestKit(t, store)
				testfailpoint.EnableCall(t, "github.com/pingcap/tidb/pkg/ddl/ingest/mockNewBackendContext",
					func(job *model.Job, cpOp ingest.CheckpointOperator, mockBackendCtx *ingest.BackendCtx) {
						inner := ingest.NewMockBackendCtx(job, tkForBackend.Session(), cpOp)
						ctx := &injectedRetryBackendCtx{
							BackendCtx: inner,
							injectErr:  tc.err,
							injected:   &injectedOnce,
						}
						observed.Store(ctx)
						*mockBackendCtx = ctx
					})
				t.Cleanup(func() {
					testfailpoint.Disable(t, "github.com/pingcap/tidb/pkg/ddl/ingest/mockNewBackendContext")
				})

				var writeRows atomic.Int64
				testfailpoint.EnableCall(t, "github.com/pingcap/tidb/pkg/ddl/ingest/onMockWriterWriteRow", func() {
					writeRows.Add(1)
				})
				t.Cleanup(func() {
					testfailpoint.Disable(t, "github.com/pingcap/tidb/pkg/ddl/ingest/onMockWriterWriteRow")
				})

				tk.MustExec(op.createTableSQL)
				for i := range 32 {
					tk.MustExec(fmt.Sprintf("insert into t values (%d, %d);", i, i))
				}

				err := tk.ExecToErr(op.alterSQL)
				ctx := observed.Load()
				require.NotNil(t, ctx)
				require.True(t, injectedOnce.Load())
				require.Greater(t, writeRows.Load(), int64(0))
				require.GreaterOrEqual(t, ctx.ingestCalls.Load(), int64(1))

				showCreate := fmt.Sprint(tk.MustQuery("show create table t").Rows())
				adminCheckErr := tk.ExecToErr("admin check table t")

				t.Logf("alter err: %v", err)
				t.Logf("write rows: %d", writeRows.Load())
				t.Logf("ingest calls: %d injected: %v", ctx.ingestCalls.Load(), ctx.injected.Load())
				t.Logf("show create: %s", showCreate)
				t.Logf("admin check err: %v", adminCheckErr)

				switch tc.name {
				case "grpc_unavailable":
					require.NoError(t, err)
					require.Contains(t, showCreate, op.successMarker)
					require.NoError(t, adminCheckErr)
				case "pd_tso_retry_timeout", "ingest_kv_not_leader", "ingest_kv_region_not_found":
					require.Error(t, err)
					if tc.name == "pd_tso_retry_timeout" {
						require.ErrorContains(t, err, "create TSO stream failed")
						require.ErrorContains(t, err, "retry timeout")
					} else if tc.name == "ingest_kv_not_leader" {
						require.ErrorContains(t, err, "not leader")
					} else {
						require.ErrorContains(t, err, "region not found")
					}
					require.NotContains(t, showCreate, op.successMarker)
					require.NoError(t, adminCheckErr)
				default:
					t.Fatalf("unknown case %s", tc.name)
				}
			})
		}
	}
}

var _ ingest.BackendCtx = (*injectedRetryBackendCtx)(nil)
