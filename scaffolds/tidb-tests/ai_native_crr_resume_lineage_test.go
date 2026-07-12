package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/pingcap/tidb/br/pkg/stream/crr/internal/checkpoint"
	"github.com/pingcap/tidb/br/pkg/streamhelper"
	"github.com/pingcap/tidb/pkg/objstore"
	"github.com/stretchr/testify/require"
)

type aiNativeLineagePD struct{ checkpoint uint64 }

func (p aiNativeLineagePD) GetGlobalCheckpointForTask(context.Context, string) (uint64, error) {
	return p.checkpoint, nil
}

func (p aiNativeLineagePD) Stores(context.Context) ([]streamhelper.Store, error) {
	return []streamhelper.Store{{ID: 1}}, nil
}

type aiNativeRejectSyncCheck struct{ calls int }

func (c *aiNativeRejectSyncCheck) FileSynced(context.Context, string) (bool, error) {
	c.calls++
	return false, fmt.Errorf("unexpected object validation")
}

func TestAINativeCRRResumeStateMustNotCrossLineageRED(t *testing.T) {
	ctx := context.Background()
	upstream, err := objstore.NewLocalStorage(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(upstream.Close)
	checker := &aiNativeRejectSyncCheck{}
	calculator, err := checkpoint.NewCalculator(
		checkpoint.CalculatorDeps{
			PD:       aiNativeLineagePD{checkpoint: 10},
			Upstream: upstream,
			Sync:     checker,
		},
		checkpoint.CheckpointCalculatorConfig{TaskName: "reused-task-name"},
		nil,
	)
	require.NoError(t, err)
	require.NoError(t, calculator.RestorePersistentState(checkpoint.PersistentState{
		LastCheckpoint: 100,
		SyncedTS:       100,
		SyncedByStore:  map[uint64]uint64{1: 100},
	}))

	got, err := calculator.ComputeNextCheckpoint(ctx)
	require.NoError(t, err)
	require.Zero(t, checker.calls)
	require.LessOrEqual(t, got, uint64(10))
}
