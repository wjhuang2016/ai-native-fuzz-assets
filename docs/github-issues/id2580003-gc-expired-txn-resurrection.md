# [txn] Expired optimistic transaction can resurrect a row deleted before GC

## Bug Report

### 1. Minimal reproduce step (Required)

#### Concrete production trigger

This can happen without a TiDB/TiKV crash, network fault, DDL race, async commit, or 1PC. A concrete
production scenario is an order-reconciliation worker holding an optimistic SQL transaction open
while it waits for an external service:

1. For the original existing-row `UPDATE` reproducer, the affected Classic cluster returns `OFF` for:

   ```sql
   SELECT @@global.tidb_txn_assertion_level;
   ```

   `OFF` is the registered compatibility default. Initial bootstrap of a new Classic cluster
   special-cases this hidden variable to `FAST`, but an older upgraded cluster where the variable was
   not materialized can fall back to `OFF`. An explicit operator setting of `OFF` has the same effect.
   The absent-row INSERT and foreign-key variants described below also reproduce with `FAST` and
   `STRICT`, so `OFF` is not a general prerequisite for this bug.
2. The operator has set `tidb_gc_max_wait_time=600`, the supported minimum, to stop abandoned or
   unusually long transactions from indefinitely blocking GC and retaining MVCC history. The default
   `tikv_gc_life_time` and `tikv_gc_run_interval` remain 10 minutes.
3. At 10:00, worker A starts an optimistic transaction and runs an ordinary update such as
   `UPDATE orders SET state='settled' WHERE id=42`. The update is buffered locally; optimistic 2PC has
   not prewritten a lock. Worker A then stalls while waiting for an external payment API, a paused
   batch task, or an application retry.
4. At 10:02, a cancellation or retention worker deletes order 42 and commits. It is not blocked by
   worker A because the old optimistic transaction has not prewritten.
5. Once worker A's transaction is older than 600 seconds, TiDB omits its start TS from min-start-TS
   reporting. The next normal GC round can advance the safe point beyond that start TS. Routine
   RocksDB compaction on a write-active table then removes both the delete tombstone and the older row
   version as obsolete MVCC history.
6. Around 10:20-10:30, after the GC/compaction phase has completed but well before client-go's fixed
   24-hour maximum transaction age, worker A resumes and commits.

Current code returns COMMIT success and permanently recreates order 42 as `state='settled'`. The
20-30 minute example is deliberately conservative; the exact delay depends on GC phase and normal
compaction activity.

Newly bootstrapped Classic clusters normally use `FAST`, which rejects this particular SQL UPDATE via
an `Assertion=Exist` check. Current-master follow-up testing shows that this mask is operation-shaped,
not an ownership guard:

- An old optimistic INSERT starts while the primary key is absent. Another transaction inserts and
  deletes the same key. Without GC, the old COMMIT returns write conflict; after GC and compaction,
  the old COMMIT returns success and leaves `(1,11)` under both `FAST` and `STRICT`.
- With MDL, foreign keys, and `foreign_key_checks` enabled, an old optimistic transaction validates a
  parent and buffers a child INSERT. Another transaction deletes the parent. Without GC, COMMIT
  returns write conflict and no orphan; after GC, COMMIT returns success under `STRICT` and a fresh
  anti-join returns orphan child `(1,1)`.

The INSERT path uses lazy duplicate checking and `AssertUnknown`; the foreign-key parent is carried
as a lock-only proof. Once GC removes the post-startTS write history, neither FAST nor STRICT can
recover those logical preconditions. Direct client-go consumers remain exposed for the same reason.

The following deterministic real-TiKV test reproduces the same state transition. It advances the GC
safe point directly and requests TiKV's production compaction filter, compressing only the
nondeterministic 20-30 minute wait. It does not mock MVCC, fabricate a write conflict, or inject a
commit error.

The expanded current-master matrix, including no-GC controls, FAST/STRICT insert-delete ABA, and the
STRICT foreign-key orphan oracle, is preserved in
`scaffolds/tidb-tests/ai_native_gc_expired_txn_assertion_fk_probe_test.go`.

Add this file as
`tests/realtikvtest/txntest/ai_native_gc_expired_txn_resurrection_test.go`:

```go
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

type gcControllableStore interface {
	GC(context.Context, uint64, ...tikv.GCOpt) (uint64, error)
	UpdateTxnSafePointCache(uint64, time.Time)
}

func forceTiKVGCCompaction(t *testing.T, store kv.Storage) {
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
		conn, err := grpc.NewClient(
			storeMeta.GetAddress(),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
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

func TestExpiredOptimisticTxnCannotResurrectDeletedRow(t *testing.T) {
	store := realtikvtest.CreateMockStoreAndSetup(t)
	tk1 := testkit.NewTestKit(t, store)
	tk2 := testkit.NewTestKit(t, store)
	tk3 := testkit.NewTestKit(t, store)
	for _, tk := range []*testkit.TestKit{tk1, tk2, tk3} {
		tk.MustExec("use test")
		tk.MustExec("set @@tidb_enable_1pc = off")
		tk.MustExec("set @@tidb_enable_async_commit = off")
	}
	tk1.MustExec("set @@tidb_txn_assertion_level = off")
	tk1.MustQuery("select @@tidb_enable_metadata_lock").Check(testkit.Rows("1"))
	tk1.MustQuery("select @@tidb_enable_1pc, @@tidb_enable_async_commit").Check(testkit.Rows("0 0"))
	tk1.MustQuery("select @@tidb_txn_assertion_level").Check(testkit.Rows("OFF"))

	tk1.MustExec("create table gc_resurrection (id bigint primary key, v bigint)")
	tk1.MustExec("insert into gc_resurrection values (1, 10)")

	tk1.MustExec("begin optimistic")
	tk1.MustExec("update gc_resurrection set v = 11 where id = 1")
	tk2.MustExec("delete from gc_resurrection where id = 1")
	deleteCommitTS := tk2.Session().GetSessionVars().LastCommitTS
	require.NotZero(t, deleteCommitTS)

	gcStore := store.(gcControllableStore)
	newSafePoint, err := gcStore.GC(context.Background(), deleteCommitTS)
	require.NoError(t, err)
	require.GreaterOrEqual(t, newSafePoint, deleteCommitTS)
	time.Sleep(12 * time.Second) // TiKV polls the PD GC safe point every 10 seconds.
	forceTiKVGCCompaction(t, store)
	gcStore.UpdateTxnSafePointCache(newSafePoint, time.Now())

	err = tk1.ExecToErr("commit")
	t.Logf("expired transaction commit result: %v", err)
	result := tk3.MustQuery("select id, v from gc_resurrection")
	t.Logf("fresh row state after commit: %v", result.Rows())
	require.ErrorContains(t, err, "GC life time is shorter than transaction duration")
	result.Check(testkit.Rows())
}
```

Start a one-TiKV playground and run:

```bash
tiup playground nightly --db=0 --kv=1 --tiflash=0
go test -tags=intest -v -count=1 \
  -run '^TestExpiredOptimisticTxnCannotResurrectDeletedRow$' \
  ./tests/realtikvtest/txntest
```

### 2. What did you expect to see? (Required)

Once the GC safe point is newer than T1's start TS, T1 must no longer be allowed to create durable
effects from that retired snapshot. Its COMMIT should fail, and a fresh session should continue to
see the row as deleted.

### 3. What did you see instead (Required)

On current source, the test prints:

```text
expired transaction commit result: <nil>
fresh row state after commit: [[1 11]]
```

COMMIT reports success and a fresh session sees the deleted row permanently recreated with T1's old
buffered value. This is silent durable row resurrection, not a stale read or terminal-error mismatch.

The source-level ownership gap is:

1. `ReportMinStartTS` deliberately excludes active transactions older than configurable
   `tidb_gc_max_wait_time`, allowing GC to reclaim their conflict history.
2. Snapshot `Get`, `BatchGet`, and `Scan` call `CheckVisibility(startTS)` before reading.
3. Effectful `KVTxn.Commit` reaches prewrite without checking the transaction safe point.
4. client-go's independent `MaxTxnTimeUse` is fixed at 24 hours and is checked after prewrite. It
   does not align with the supported 600-second GC-exclusion horizon.
5. Once compaction removes the newer delete record, TiKV prewrite has no surviving write conflict to
   reject, so the stale mutation recreates the row.

Adding `CheckVisibility(startTS)` before prewrite is an exact ownership counterfactual: the same test
returns error 9006 and the row remains absent. This demonstrates where admission is missing, but it
is not necessarily a complete fix because safe-point advancement can race with a client-side check.
A robust fix should close that race, for example by enforcing the relevant safe point at TiKV
prewrite or by keeping the GC exclusion horizon consistent with the maximum transaction age admitted
to prewrite.

### 4. What is your TiDB version? (Required)

Reproduced with:

```text
TiDB:          b8d04e17a2ca61eee1220c5ce2d641a376f75e9b
tikv/client-go: 01bd8f99f4da23c6fc9d671eecc0166c7b6ceb9b
TiKV nightly:  7ecce12e
```

Expanded impact matrix revalidated on:

```text
TiDB:           94b834d94b604b1940ecc2c3064168337863269d
tikv/client-go: 01bd8f99f4da23c6fc9d671eecc0166c7b6ceb9b
TiKV nightly:   c27c66202dcd2ec0113619c613e0dac3d17780b6
```

MDL was enabled (`@@tidb_enable_metadata_lock=1`). 1PC and async commit were disabled, so the
reproducer uses ordinary optimistic 2PC.
