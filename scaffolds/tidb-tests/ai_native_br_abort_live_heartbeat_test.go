package brregistrytest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pingcap/tidb/br/pkg/gluetidb"
	"github.com/pingcap/tidb/br/pkg/registry"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/testkit/testfailpoint"
	"github.com/pingcap/tidb/tests/realtikvtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAINativeAbortDeletesLiveRestoreRED(t *testing.T) {
	store, dom := realtikvtest.CreateMockStoreAndDomainAndSetup(t)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("DELETE FROM mysql.tidb_restore_registry")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	reg, err := registry.NewRestoreRegistry(ctx, gluetidb.New(), dom)
	require.NoError(t, err)

	info := registry.RegistrationInfo{
		FilterStrings:     []string{"test.*"},
		StartTS:           100,
		RestoredTS:        200,
		UpstreamClusterID: 300,
		WithSysTable:      true,
		Cmd:               "point restore",
	}
	restoreID, _, err := reg.ResumeOrCreateRegistration(ctx, info, true)
	require.NoError(t, err)

	stopBackgroundHeartbeat := make(chan struct{})
	backgroundHeartbeatDone := make(chan struct{})
	go func() {
		defer close(backgroundHeartbeatDone)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = reg.UpdateHeartbeat(ctx, restoreID)
			case <-stopBackgroundHeartbeat:
				return
			}
		}
	}()

	testfailpoint.Enable(t,
		"github.com/pingcap/tidb/br/pkg/registry/is-task-stale-ticker-duration", "return(1)")
	testfailpoint.Enable(t,
		"github.com/pingcap/tidb/br/pkg/registry/is-task-stale-check-count", "return(2)")
	resolved := make(chan struct{})
	continueAfterResolved := make(chan struct{})
	var resolveOnce sync.Once
	testfailpoint.EnableCall(t,
		"github.com/pingcap/tidb/br/pkg/registry/afterAbortResolvedRestoreTS", func() {
			resolveOnce.Do(func() { close(resolved) })
			<-continueAfterResolved
		})
	locked := make(chan struct{})
	var once sync.Once
	testfailpoint.EnableCall(t,
		"github.com/pingcap/tidb/br/pkg/registry/afterAbortLockedRestoreTask", func(id uint64) {
			require.Equal(t, restoreID, id)
			once.Do(func() { close(locked) })
		})

	resultCh := make(chan struct {
		id  uint64
		err error
	}, 1)
	go func() {
		id, err := reg.FindAndDeleteMatchingTask(ctx, info, true)
		resultCh <- struct {
			id  uint64
			err error
		}{id: id, err: err}
	}()

	select {
	case <-resolved:
	case <-time.After(5 * time.Second):
		t.Fatal("abort did not finish the unlocked active-heartbeat check")
	}
	close(stopBackgroundHeartbeat)
	<-backgroundHeartbeatDone
	require.NoError(t, reg.UpdateHeartbeat(ctx, restoreID))

	stopLockedHeartbeat := make(chan struct{})
	lockedHeartbeatDone := make(chan struct{})
	go func() {
		defer close(lockedHeartbeatDone)
		<-locked
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = reg.UpdateHeartbeat(ctx, restoreID)
			case <-stopLockedHeartbeat:
				return
			}
		}
	}()
	close(continueAfterResolved)

	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("abort did not lock the live registry row")
	}

	var result struct {
		id  uint64
		err error
	}
	select {
	case result = <-resultCh:
	case <-time.After(10 * time.Second):
		t.Fatal("abort did not finish")
	}
	require.NoError(t, result.err)
	rows := tk.MustQuery("SELECT COUNT(*) FROM mysql.tidb_restore_registry WHERE id = ?", restoreID).Rows()
	assert.Zero(t, result.id, "an active restore with advancing heartbeats must not be aborted")
	assert.Equal(t, "1", rows[0][0], "the active restore registry row must remain")

	close(stopLockedHeartbeat)
	<-lockedHeartbeatDone
	reg.Close()
}

func TestAINativeAbortDeletesActuallyStaleRestoreGREEN(t *testing.T) {
	store, dom := realtikvtest.CreateMockStoreAndDomainAndSetup(t)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("DELETE FROM mysql.tidb_restore_registry")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	reg, err := registry.NewRestoreRegistry(ctx, gluetidb.New(), dom)
	require.NoError(t, err)
	defer reg.Close()

	info := registry.RegistrationInfo{
		FilterStrings:     []string{"test.*"},
		StartTS:           100,
		RestoredTS:        200,
		UpstreamClusterID: 300,
		WithSysTable:      true,
		Cmd:               "point restore",
	}
	restoreID, _, err := reg.ResumeOrCreateRegistration(ctx, info, true)
	require.NoError(t, err)

	testfailpoint.Enable(t,
		"github.com/pingcap/tidb/br/pkg/registry/is-task-stale-ticker-duration", "return(1)")
	testfailpoint.Enable(t,
		"github.com/pingcap/tidb/br/pkg/registry/is-task-stale-check-count", "return(2)")

	deletedID, err := reg.FindAndDeleteMatchingTask(ctx, info, true)
	require.NoError(t, err)
	require.Equal(t, restoreID, deletedID)
	tk.MustQuery("SELECT COUNT(*) FROM mysql.tidb_restore_registry WHERE id = ?", restoreID).Check(testkit.Rows("0"))
}
