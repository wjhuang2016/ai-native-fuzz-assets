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

package addindextest_test

import (
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/testkit/testfailpoint"
	"github.com/pingcap/tidb/tests/realtikvtest"
	"github.com/stretchr/testify/require"
)

const (
	issue62531PoolProbeWorkers      = 16
	issue62531PoolProbeRowsPerOp    = 200
	issue62531PoolProbePrefillRows  = 120000
	issue62531PoolProbeMaxVal0      = 1000
	issue62531PoolProbeHold         = 15 * time.Second
	issue62531PoolProbeSleep        = 500 * time.Millisecond
	issue62531PoolProbeSessionPool  = 4
	issue62531PoolProbePadding      = 256
	issue62531PoolProbeBatchSize    = 32
	issue62531PoolProbePrefillBatch = 1000
)

// TestAINativeIssue62531PoolLikeProbe is a gated local reproducer workbench.
// Enable it explicitly when bug-hunting:
//   AI_NATIVE_ENABLE_FAILING_PROBES=1 go test -tags=intest ./tests/realtikvtest/addindextest4 -run TestAINativeIssue62531PoolLikeProbe -count=1
func TestAINativeIssue62531PoolLikeProbe(t *testing.T) {
	if os.Getenv("AI_NATIVE_ENABLE_FAILING_PROBES") != "1" {
		t.Skip("gated local bug-hunting probe")
	}

	store := realtikvtest.CreateMockStoreAndSetup(t)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t_issue62531_pool_probe")
	tk.MustExec("set @@global.tidb_enable_dist_task = off")
	tk.MustExec("set @@global.tidb_ddl_enable_fast_reorg = off")
	tk.MustExec("set @@global.tidb_ddl_reorg_worker_cnt = 1")
	tk.MustExec(fmt.Sprintf("set @@global.tidb_ddl_reorg_batch_size = %d", issue62531PoolProbeBatchSize))
	tk.MustExec("set @@global.tidb_ddl_reorg_max_write_speed = 0")
	tk.MustExec(`
		create table t_issue62531_pool_probe (
			id int not null auto_increment,
			val0 int not null,
			val1 int not null,
			padding varchar(256) not null default '',
			primary key (id),
			key val0_idx(val0)
		)
	`)
	prefillIssue62531PoolProbe(t, tk)

	testfailpoint.Enable(t, "github.com/pingcap/tidb/pkg/ddl/disableLossyDDLOptimization", "return(true)")

	var (
		started  atomic.Bool
		startOnce sync.Once
		stopOnce  sync.Once
		wg        sync.WaitGroup
	)
	stopCh := make(chan struct{})
	errCh := make(chan error, 1)
	stopWorkers := func() {
		stopOnce.Do(func() {
			close(stopCh)
		})
	}

	testfailpoint.EnableCall(t, "github.com/pingcap/tidb/pkg/ddl/beforeUpdateColumnBackfillApply", func() {
		startOnce.Do(func() {
			started.Store(true)
			for workerID := 0; workerID < issue62531PoolProbeWorkers; workerID++ {
				insertTKs := make([]*testkit.TestKit, 0, issue62531PoolProbeSessionPool)
				deleteTKs := make([]*testkit.TestKit, 0, issue62531PoolProbeSessionPool)
				for range issue62531PoolProbeSessionPool {
					insertTK := testkit.NewTestKit(t, store)
					insertTK.MustExec("use test")
					deleteTK := testkit.NewTestKit(t, store)
					deleteTK.MustExec("use test")
					insertTKs = append(insertTKs, insertTK)
					deleteTKs = append(deleteTKs, deleteTK)
				}
				wg.Add(1)
				go func(id int, inserts, deletes []*testkit.TestKit) {
					defer wg.Done()
					runIssue62531PoolProbeWorker(t, id, inserts, deletes, stopCh, errCh)
				}(workerID, insertTKs, deleteTKs)
			}
			select {
			case <-stopCh:
			case <-time.After(issue62531PoolProbeHold):
			}
		})
	})

	ddlErrCh := make(chan error, 1)
	go func() {
		ddlErrCh <- tk.ExecToErr("alter table t_issue62531_pool_probe modify column val0 varchar(16) not null")
	}()

	var (
		ddlErr  error
		hitErr  error
		ddlDone bool
	)

	select {
	case hitErr = <-errCh:
		stopWorkers()
	case ddlErr = <-ddlErrCh:
		ddlDone = true
		stopWorkers()
	case <-time.After(2 * issue62531PoolProbeHold):
		stopWorkers()
		t.Fatalf("probe timed out before observing hit or ddl completion")
	}

	if !ddlDone {
		select {
		case ddlErr = <-ddlErrCh:
			ddlDone = true
		case <-time.After(time.Minute):
			stopWorkers()
			t.Fatalf("ddl did not finish after probe stop")
		}
	}
	stopWorkers()
	wg.Wait()

	require.True(t, started.Load(), "beforeUpdateColumnBackfillApply probe did not fire")
	require.NoError(t, ddlErr)
	if hitErr != nil {
		t.Fatalf("issue62531 pool-like probe hit: %v", hitErr)
	}

	tk.MustExec("admin check table t_issue62531_pool_probe")
}

func prefillIssue62531PoolProbe(t *testing.T, tk *testkit.TestKit) {
	padding := strings.Repeat("a", issue62531PoolProbePadding)
	for start := 0; start < issue62531PoolProbePrefillRows; start += issue62531PoolProbePrefillBatch {
		end := start + issue62531PoolProbePrefillBatch
		if end > issue62531PoolProbePrefillRows {
			end = issue62531PoolProbePrefillRows
		}
		var sb strings.Builder
		sb.WriteString("insert into t_issue62531_pool_probe (val0, val1, padding) values ")
		for i := start; i < end; i++ {
			if i > start {
				sb.WriteByte(',')
			}
			val0 := i % issue62531PoolProbeMaxVal0
			fmt.Fprintf(&sb, "(%d,%d,'%s')", val0, val0*10, padding)
		}
		tk.MustExec(sb.String())
	}
}

func runIssue62531PoolProbeWorker(
	t *testing.T,
	workerID int,
	insertTKs []*testkit.TestKit,
	deleteTKs []*testkit.TestKit,
	stopCh <-chan struct{},
	errCh chan<- error,
) {
	rng := rand.New(rand.NewSource(1 + int64(workerID)*131))
	padding := strings.Repeat("b", issue62531PoolProbePadding)
	loop := 0
	for {
		select {
		case <-stopCh:
			return
		default:
		}

		start := rng.Intn(issue62531PoolProbeMaxVal0)
		vals := buildIssue62531PoolProbeWindow(start)

		insertTK := insertTKs[loop%len(insertTKs)]
		if err := execIssue62531PoolProbeInsert(insertTK, vals, padding); err != nil {
			signalIssue62531PoolProbeErr(errCh, fmt.Errorf("worker %d insert failed on [%d..%d]: %w", workerID, vals[0], vals[len(vals)-1], err))
			return
		}
		if stopIssue62531PoolProbeSleep(stopCh, issue62531PoolProbeSleep) {
			return
		}

		deleteTK := deleteTKs[(loop+1)%len(deleteTKs)]
		if err := execIssue62531PoolProbeDelete(deleteTK, vals); err != nil {
			if strings.Contains(err.Error(), "missing data for NOT NULL column") {
				signalIssue62531PoolProbeErr(errCh, fmt.Errorf("worker %d delete hit issue62531 signature on [%d..%d]: %w", workerID, vals[0], vals[len(vals)-1], err))
				return
			}
			signalIssue62531PoolProbeErr(errCh, fmt.Errorf("worker %d delete failed on [%d..%d]: %w", workerID, vals[0], vals[len(vals)-1], err))
			return
		}
		if stopIssue62531PoolProbeSleep(stopCh, issue62531PoolProbeSleep) {
			return
		}
		loop++
	}
}

func buildIssue62531PoolProbeWindow(start int) []int {
	vals := make([]int, issue62531PoolProbeRowsPerOp)
	for i := range vals {
		vals[i] = (start + i) % issue62531PoolProbeMaxVal0
	}
	return vals
}

func execIssue62531PoolProbeInsert(tk *testkit.TestKit, vals []int, padding string) error {
	var sb strings.Builder
	sb.WriteString("insert into t_issue62531_pool_probe (val0, val1, padding) values ")
	for i, val0 := range vals {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, "(%d,%d,'%s')", val0, val0*10, padding)
	}
	return tk.ExecToErr(sb.String())
}

func execIssue62531PoolProbeDelete(tk *testkit.TestKit, vals []int) error {
	var sb strings.Builder
	sb.WriteString("delete from t_issue62531_pool_probe where val0 in (")
	for i, val0 := range vals {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, "%d", val0)
	}
	sb.WriteByte(')')
	return tk.ExecToErr(sb.String())
}

func stopIssue62531PoolProbeSleep(stopCh <-chan struct{}, d time.Duration) bool {
	select {
	case <-stopCh:
		return true
	case <-time.After(d):
		return false
	}
}

func signalIssue62531PoolProbeErr(errCh chan<- error, err error) {
	select {
	case errCh <- err:
	default:
	}
}
