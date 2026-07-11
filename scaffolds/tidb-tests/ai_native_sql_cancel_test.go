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

package ingest_test

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pingcap/tidb/pkg/config/kerneltype"
	"github.com/pingcap/tidb/pkg/ddl/ingest"
	"github.com/pingcap/tidb/pkg/errno"
	"github.com/pingcap/tidb/pkg/ingestor/ingestctrl"
	tidbkv "github.com/pingcap/tidb/pkg/kv"
	"github.com/pingcap/tidb/pkg/lightning/backend"
	"github.com/pingcap/tidb/pkg/meta/model"
	"github.com/pingcap/tidb/pkg/table"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/testkit/testfailpoint"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type observedCancelBackendCtx struct {
	inner ingest.BackendCtx

	mu      sync.Mutex
	engines []*observedCancelEngine

	activeWindowHit atomic.Bool
	registered      atomic.Int64
	createdWriters  atomic.Int64
	liveEngines     atomic.Int64
	liveWriters     atomic.Int64
	closedEngines   atomic.Int64
	duplicateCloses atomic.Int64
	finishCalls     atomic.Int64
}

func (o *observedCancelBackendCtx) Register(indexIDs []int64, uniques []bool, tbl table.Table) ([]ingest.Engine, error) {
	engines, err := o.inner.Register(indexIDs, uniques, tbl)
	if err != nil {
		return nil, err
	}
	if len(engines) > 0 {
		o.activeWindowHit.Store(true)
	}
	o.registered.Add(int64(len(engines)))
	o.liveEngines.Add(int64(len(engines)))

	wrapped := make([]ingest.Engine, 0, len(engines))
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, engine := range engines {
		observed := &observedCancelEngine{inner: engine, owner: o}
		o.engines = append(o.engines, observed)
		wrapped = append(wrapped, observed)
	}
	return wrapped, nil
}

func (o *observedCancelBackendCtx) FinishAndUnregisterEngines(opt ingest.UnregisterOpt) error {
	o.finishCalls.Add(1)

	o.mu.Lock()
	engines := append([]*observedCancelEngine(nil), o.engines...)
	o.engines = nil
	o.mu.Unlock()

	for _, engine := range engines {
		engine.Close(opt&ingest.OptCleanData != 0)
	}
	return o.inner.FinishAndUnregisterEngines(opt)
}

func (o *observedCancelBackendCtx) CollectRemoteDuplicateRows(indexID int64, tbl table.Table) error {
	return o.inner.CollectRemoteDuplicateRows(indexID, tbl)
}

func (o *observedCancelBackendCtx) IngestIfQuotaExceeded(ctx context.Context, taskID int, count int) error {
	return o.inner.IngestIfQuotaExceeded(ctx, taskID, count)
}

func (o *observedCancelBackendCtx) Ingest(ctx context.Context) error {
	return o.inner.Ingest(ctx)
}

func (o *observedCancelBackendCtx) NextStartKey() tidbkv.Key {
	return o.inner.NextStartKey()
}

func (o *observedCancelBackendCtx) TotalKeyCount() int {
	return o.inner.TotalKeyCount()
}

func (o *observedCancelBackendCtx) AddChunk(id int, endKey tidbkv.Key) {
	o.inner.AddChunk(id, endKey)
}

func (o *observedCancelBackendCtx) UpdateChunk(id int, count int, done bool) {
	o.inner.UpdateChunk(id, count, done)
}

func (o *observedCancelBackendCtx) FinishChunk(id int, count int) {
	o.inner.FinishChunk(id, count)
}

func (o *observedCancelBackendCtx) AdvanceWatermark(imported bool) error {
	return o.inner.AdvanceWatermark(imported)
}

func (o *observedCancelBackendCtx) GetImportTS() uint64 {
	return o.inner.GetImportTS()
}

func (o *observedCancelBackendCtx) GetLocalBackend() *ingestctrl.Backend {
	return o.inner.GetLocalBackend()
}

func (o *observedCancelBackendCtx) GetDiskUsage() uint64 {
	return o.inner.GetDiskUsage()
}

func (o *observedCancelBackendCtx) Close() {
	o.inner.Close()
}

type observedCancelEngine struct {
	inner ingest.Engine
	owner *observedCancelBackendCtx

	closed      atomic.Bool
	writerCount atomic.Int64
}

func (e *observedCancelEngine) Flush() error {
	return e.inner.Flush()
}

func (e *observedCancelEngine) Close(cleanup bool) {
	if !e.closed.CompareAndSwap(false, true) {
		e.owner.duplicateCloses.Add(1)
		return
	}
	e.owner.closedEngines.Add(1)
	e.owner.liveEngines.Add(-1)
	writers := e.writerCount.Swap(0)
	if writers > 0 {
		e.owner.liveWriters.Add(-writers)
	}
	e.inner.Close(cleanup)
}

func (e *observedCancelEngine) CreateWriter(id int, cfg *backend.LocalWriterConfig) (ingest.Writer, error) {
	writer, err := e.inner.CreateWriter(id, cfg)
	if err != nil {
		return nil, err
	}
	e.writerCount.Add(1)
	e.owner.createdWriters.Add(1)
	e.owner.liveWriters.Add(1)
	return writer, nil
}

func TestAINativeAddIndexCancelLeavesNoLiveMockIngestResource(t *testing.T) {
	if kerneltype.IsNextGen() {
		t.Skip("add-index always runs on DXF with ingest mode in nextgen")
	}

	store, _ := testkit.CreateMockStoreAndDomain(t)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test;")
	tk.MustExec("set global tidb_enable_dist_task = 0;")
	tk.MustExec("set @@tidb_ddl_reorg_worker_cnt = 1;")

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

	var observed atomic.Pointer[observedCancelBackendCtx]
	tkForBackend := testkit.NewTestKit(t, store)
	testfailpoint.EnableCall(t, "github.com/pingcap/tidb/pkg/ddl/ingest/mockNewBackendContext",
		func(job *model.Job, cpOp ingest.CheckpointOperator, mockBackendCtx *ingest.BackendCtx) {
			inner := ingest.NewMockBackendCtx(job, tkForBackend.Session(), cpOp)
			ctx := &observedCancelBackendCtx{inner: inner}
			observed.Store(ctx)
			*mockBackendCtx = ctx
		})
	t.Cleanup(func() {
		testfailpoint.Disable(t, "github.com/pingcap/tidb/pkg/ddl/ingest/mockNewBackendContext")
	})

	tk.MustExec("create table t (a int primary key, b int);")
	for i := range 64 {
		tk.MustExec(fmt.Sprintf("insert into t values (%d, %d);", i, i))
	}

	var jobID atomic.Int64
	testfailpoint.EnableCall(t, "github.com/pingcap/tidb/pkg/ddl/beforeRunOneJobStep", func(job *model.Job) {
		if job.Type == model.ActionAddIndex {
			jobID.Store(job.ID)
		}
	})
	t.Cleanup(func() {
		testfailpoint.Disable(t, "github.com/pingcap/tidb/pkg/ddl/beforeRunOneJobStep")
	})

	var activeWrites atomic.Int64
	var cancelled atomic.Bool
	testfailpoint.EnableCall(t, "github.com/pingcap/tidb/pkg/ddl/ingest/onMockWriterWriteRow", func() {
		activeWrites.Add(1)
		id := jobID.Load()
		if id == 0 {
			return
		}
		if !cancelled.CompareAndSwap(false, true) {
			return
		}
		tk2 := testkit.NewTestKit(t, store)
		rs, err := tk2.Exec(fmt.Sprintf("admin cancel ddl jobs %d", id))
		assert.NoError(t, err)
		if rs != nil {
			assert.NoError(t, rs.Close())
		}
	})
	t.Cleanup(func() {
		testfailpoint.Disable(t, "github.com/pingcap/tidb/pkg/ddl/ingest/onMockWriterWriteRow")
	})

	tk.MustGetErrCode("alter table t add index idx(b);", errno.ErrCancelledDDLJob)
	require.True(t, cancelled.Load())
	require.Greater(t, activeWrites.Load(), int64(0))
	tk.MustExec("admin check table t;")

	ctx := observed.Load()
	require.NotNil(t, ctx)
	t.Logf(
		"ai_native_observed active_writes=%d registered=%d created_writers=%d finish_calls=%d live_engines=%d live_writers=%d closed_engines=%d duplicate_closes=%d disk_root_count=%d",
		activeWrites.Load(),
		ctx.registered.Load(),
		ctx.createdWriters.Load(),
		ctx.finishCalls.Load(),
		ctx.liveEngines.Load(),
		ctx.liveWriters.Load(),
		ctx.closedEngines.Load(),
		ctx.duplicateCloses.Load(),
		ingest.LitDiskRoot.Count(),
	)
	require.True(t, ctx.activeWindowHit.Load())
	require.Greater(t, ctx.registered.Load(), int64(0))
	require.Greater(t, ctx.createdWriters.Load(), int64(0))
	require.Greater(t, ctx.finishCalls.Load(), int64(0))
	require.Equal(t, int64(0), ctx.liveEngines.Load())
	require.Equal(t, int64(0), ctx.liveWriters.Load())
	require.Equal(t, int64(0), ctx.duplicateCloses.Load())
	require.Equal(t, ctx.registered.Load(), ctx.closedEngines.Load())
	require.Equal(t, 0, ingest.LitDiskRoot.Count())
}
