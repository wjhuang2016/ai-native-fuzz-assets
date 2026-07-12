## Bug Report

### 1. Minimal reproduce step

Add a test-only callback after the TiFlash placement rule is successfully updated, but before table
metadata is updated in `onSetTableFlashReplica`:

```go
if pi := tblInfo.GetPartitionInfo(); pi != nil {
	// Existing partitioned-table branch.
} else {
	if e := infosync.ConfigureTiFlashPDForTable(
		tblInfo.ID, replicaInfo.Count, &replicaInfo.Labels); e != nil {
		job.State = model.JobStateCancelled
		return ver, errors.Trace(e)
	}
}
failpoint.InjectCall("afterSetTiFlashReplicaExternal", job)

if replicaInfo.Count > 0 {
	// Existing metadata update.
```

Then add the following test in package `ddl_test`:

```go
func TestSetTiFlashReplicaCancelMustRestorePDRule(t *testing.T) {
	testfailpoint.Enable(t,
		"github.com/pingcap/tidb/pkg/infoschema/mockTiFlashStoreCount", "return(true)")
	store := testkit.CreateMockStoreWithSchemaLease(t, 50*time.Millisecond)
	setup := testkit.NewTestKit(t, store)
	alter := testkit.NewTestKit(t, store)
	cancel := testkit.NewTestKit(t, store)
	tiflash := infosync.NewMockTiFlash()
	infosync.SetMockTiFlash(tiflash)
	t.Cleanup(func() {
		tiflash.Lock()
		tiflash.StatusServer.Close()
		tiflash.Unlock()
	})
	for _, tk := range []*testkit.TestKit{setup, alter, cancel} {
		tk.MustExec("use test")
	}

	setup.MustExec("create table t(id int primary key)")
	setup.MustExec("alter table t set tiflash replica 1")
	tbl := external.GetTableByName(t, setup, "test", "t")
	require.NoError(t, domain.GetDomain(setup.Session()).DDLExecutor().UpdateTableReplicaInfo(
		setup.Session(), tbl.Meta().ID, true))
	tableID := tbl.Meta().ID

	reached := make(chan int64, 1)
	release := make(chan struct{})
	testfailpoint.EnableCall(t,
		"github.com/pingcap/tidb/pkg/ddl/afterSetTiFlashReplicaExternal",
		func(job *model.Job) {
			if job.Type == model.ActionSetTiFlashReplica {
				reached <- job.ID
				<-release
			}
		})

	done := make(chan error, 1)
	go func() {
		_, err := alter.Exec("alter table t set tiflash replica 0")
		done <- err
	}()
	jobID := <-reached
	cancel.MustExec(fmt.Sprintf("admin cancel ddl jobs %d", jobID))
	close(release)
	require.Error(t, <-done)

	tbl = external.GetTableByName(t, setup, "test", "t")
	require.NotNil(t, tbl.Meta().TiFlashReplica)
	require.Equal(t, uint64(1), tbl.Meta().TiFlashReplica.Count)
	require.True(t, tbl.Meta().TiFlashReplica.Available)

	rules, err := infosync.GetTiFlashGroupRules(
		context.Background(), placement.TiFlashRuleGroupID)
	require.NoError(t, err)
	wantID := infosync.MakeRuleID(tableID)
	found := false
	for _, rule := range rules {
		if rule.ID == wantID && rule.Count == 1 {
			found = true
		}
	}
	require.True(t, found,
		"cancelled DDL must restore the PD rule required by committed metadata")
}
```

Required imports are `context`, `fmt`, `testing`, `time`, `pkg/ddl/placement`, `pkg/domain`,
`pkg/domain/infosync`, `pkg/meta/model`, `pkg/testkit`, `pkg/testkit/external`,
`pkg/testkit/testfailpoint`, and `testify/require`.

```bash
make failpoint-enable
go test -tags=intest ./pkg/ddl \
  -run '^TestSetTiFlashReplicaCancelMustRestorePDRule$' -count=1 -v
make failpoint-disable
```

The final assertion fails. DDL history is `cancelled`; metadata remains
`REPLICA_COUNT=1, AVAILABLE=1`, but the table's PD rule is absent.

The same schedule was confirmed with real PD, TiKV, and TiFlash:

```text
Before ALTER:
  metadata: REPLICA_COUNT=1, AVAILABLE=1, PROGRESS=1
  PD rule:  tiflash/table-5378-r, count=1
  TiFlash-only query: count=5, sum=150

After PD deletes the rule, ADMIN CANCEL succeeds, and ALTER returns 8214:
  DDL history: cancelled
  metadata: REPLICA_COUNT=1, AVAILABLE=1
  PD rule: absent
  SET tidb_isolation_read_engines='tiflash'; SELECT ...:
    ERROR 9012 TiFlash server timeout
```

Restoring only the PD rule derived from committed metadata changed `PROGRESS` back to 1 and made
the same TiFlash-only query return `5,150`. A normal, non-cancelled replica removal made metadata
and PD both empty; TiDB then immediately returned the expected 1815 "No access path" diagnostic
instead of attempting a stale TiFlash plan and timing out.

### 2. What did you expect to see?

If `ADMIN CANCEL DDL JOBS` succeeds and `SET TIFLASH REPLICA 0` returns 8214, the PD rule required
by the still-committed `REPLICA_COUNT=1, AVAILABLE=1` metadata should remain active.

### 3. What did you see instead?

`onSetTableFlashReplica` calls `ConfigureTiFlashPDForTable` before updating table metadata and
finishing the DDL job. For replica count 0, that external call deletes the TiFlash placement rule.
A later cancellation rolls back the metadata transaction but has no compensation edge for PD.

The optimizer can therefore continue to accept TiFlash-only plans from stale available metadata,
while PD has removed the corresponding replica rule, leading to query timeout.

### 4. What is your TiDB version?

Current master at `13282a8bd06bd33324a4dbfd3c1c03685f3cd9aa`.
