## Bug Report

### 1. Minimal reproduce step

On current master, add a test-only callback immediately after the table placement bundle has been
successfully published to PD in `onAlterTablePlacement`:

```go
if err != nil {
	job.State = model.JobStateCancelled
	return ver, errors.Trace(err)
}
failpoint.InjectCall("afterAlterTablePlacementExternal", job)

job.FinishTableJob(model.JobStateDone, model.StatePublic, ver, tblInfo)
```

Then add and run the following test with failpoints enabled:

```go
package ddl_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/pingcap/tidb/pkg/ddl/placement"
	"github.com/pingcap/tidb/pkg/domain"
	"github.com/pingcap/tidb/pkg/domain/infosync"
	"github.com/pingcap/tidb/pkg/meta/model"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/testkit"
	"github.com/pingcap/tidb/pkg/testkit/testfailpoint"
	"github.com/stretchr/testify/require"
)

func TestAlterTablePlacementCancelMustRestorePDBundle(t *testing.T) {
	store, dom := testkit.CreateMockStoreAndDomain(t)
	setup := testkit.NewTestKit(t, store)
	alter := testkit.NewTestKit(t, store)
	cancel := testkit.NewTestKit(t, store)
	for _, tk := range []*testkit.TestKit{setup, alter, cancel} {
		tk.MustExec("use test")
	}
	setup.MustExec("create placement policy p1 primary_region='r1' regions='r1'")
	setup.MustExec("create placement policy p2 primary_region='r2' regions='r2'")
	setup.MustExec("create table t(id int primary key) placement policy p1")
	tbl, err := dom.InfoSchema().TableByName(
		context.Background(), ast.NewCIStr("test"), ast.NewCIStr("t"))
	require.NoError(t, err)
	tableID := tbl.Meta().ID

	reached := make(chan int64, 1)
	release := make(chan struct{})
	testfailpoint.EnableCall(t,
		"github.com/pingcap/tidb/pkg/ddl/afterAlterTablePlacementExternal",
		func(job *model.Job) {
			if job.Type == model.ActionAlterTablePlacement {
				reached <- job.ID
				<-release
			}
		})

	done := make(chan error, 1)
	go func() {
		_, err := alter.Exec("alter table t placement policy p2")
		done <- err
	}()
	jobID := <-reached
	cancel.MustExec(fmt.Sprintf("admin cancel ddl jobs %d", jobID))
	close(release)
	require.Error(t, <-done)

	showCreate := fmt.Sprint(setup.MustQuery("show create table t").Rows())
	require.Contains(t, showCreate, "PLACEMENT POLICY=`p1`")

	metaBundle, ok := dom.InfoSchema().PlacementBundleByPhysicalTableID(tableID)
	require.True(t, ok)
	pdBundle, err := infosync.GetRuleBundle(context.Background(), placement.GroupID(tableID))
	require.NoError(t, err)
	metaJSON, err := json.Marshal(metaBundle)
	require.NoError(t, err)
	pdJSON, err := json.Marshal(pdBundle)
	require.NoError(t, err)

	require.Contains(t, string(metaJSON), "r1")
	require.Contains(t, string(pdJSON), "r1",
		"cancelled ALTER must not leave PD on the uncommitted policy")
}
```

```bash
make failpoint-enable
go test -tags=intest ./pkg/ddl -run '^TestAlterTablePlacementCancelMustRestorePDBundle$' -count=1 -v
make failpoint-disable
```

The final assertion fails. The observed state is:

```text
ALTER TABLE: ERROR 8214 Cancelled DDL job
DDL history: cancelled
SHOW CREATE / InfoSchema bundle: p1 / region r1
PD placement bundle: p2 / region r2
```

The same schedule was also confirmed with real PD by using valid policies with different replica
counts: p1 had `FOLLOWERS=2` (three voters) and p2 had `FOLLOWERS=1` (two voters). After successful
cancellation, TiDB still declared p1 while PD retained voter count 2.

### 2. What did you expect to see?

After `ADMIN CANCEL DDL JOBS` succeeds and the ALTER returns 8214, both committed table metadata
and PD should remain on the old placement policy. A cancelled DDL must not change the effective
replica rule.

### 3. What did you see instead?

`onAlterTablePlacement` publishes the new bundle through
`PutRuleBundlesWithDefaultRetry(context.TODO(), ...)` before the DDL worker transaction and job
state are durable. `ADMIN CANCEL` then aborts the local transaction, but generic cancellation does
not restore the old PD bundle.

This can silently weaken replica redundancy even though the operator was told the DDL was
cancelled. A normal ALTER control keeps metadata and PD coherent, and republishing the bundle
derived from committed InfoSchema after cancellation restores coherence.

### 4. What is your TiDB version?

Current master at `13282a8bd06bd33324a4dbfd3c1c03685f3cd9aa`.
