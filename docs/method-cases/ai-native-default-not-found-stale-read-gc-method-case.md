# DefaultNotFound From Unregistered Autocommit Stale Read

> Reproduced: 2026-07-17. Classification: historical replay and method calibration, not a new
> cold-source finding. Old stack is RED; current master is GREEN at the missing owner.

## Conclusion

Current TiDB master fixes the reachable TiDB root that allowed an active autocommit stale read to
fall behind GC. It does not remove TiKV's `DefaultNotFound` error class or make reads below a
forcibly advanced GC safe point valid.

The old stack reproduced the exact user-visible error with one TiDB and three TiKV nodes:

```text
ERROR 1105 (HY000): tikv aborts txn:
Error(Txn(Error(Mvcc(Error(DefaultNotFound { key: ... })))))
```

The master counterfactual registered the stale-read timestamp in both processlist and etcd, so the
normal TiDB GC owner cannot advance past the active read.

## Production Trigger

The user-facing path does not require TiDB failover, partitions, MDL changes, or multiple TiDB
servers. One TiDB is enough.

1. A session uses `tidb_enable_external_ts_read=ON` and executes an autocommit read at an external
   timestamp. External timestamp reads are used when an application coordinates TiDB reads with an
   external system's known-consistent timestamp.
2. The statement remains active longer than `tidb_gc_life_time`, which is 10 minutes by default.
   A long analytical query, throttled scan, overloaded TiKV, or client that slowly consumes a result
   can provide this duration.
3. The same rows receive newer committed versions while the old read remains active.
4. TiDB's GC worker calculates a safe point. In the old version, the autocommit stale-read session
   exposes `TxnCtx.StartTS=0` and does not publish `TxnCtx.StaleReadTs`, so GC does not see the live
   reader and may advance past it.
5. Normal Write CF flush and RocksDB compaction remove versions older than the safe point. During
   Write CF compaction, TiKV deletes a long value from Default CF before the corresponding old Write
   CF record is removed from the installed view.
6. A point read in that window sees the old Write record but cannot load its Default value and
   returns `DefaultNotFound`. After compaction completes, the same invalid old snapshot may instead
   observe missing historical rows.

The lab used two timing accelerators only: it advanced PD's GC safe point directly and reduced the
Write CF buffer from 128 MB to 1 MB. Neither creates the omitted registration. Under default
settings, the same state is reached after the ten-minute lifetime and ordinary flush/compaction.

## Exact Historical Stack

```text
TiDB      0dc34e5203c6a183c72984937dfe28147969d9df  (2024-12-26)
client-go fd950fcf9fcc4b67df874290c63211724d92daa6
TiKV      6ff4b9d4bf63dab5ffefb5be76524e6d23b26f71
PD        4d8009db1b6b5ed9f32255a226dd5608eab36999
topology  1 TiDB, 3 TiKV, 1 PD
MDL       ON
```

The pinned build configuration is
`tools/txnlab/examples/local-defaultnotfound-20241226.toml`.

## Compressed Matrix

| Read form | Old processlist `TxnStart` | GC protection | Result |
| --- | ---: | --- | --- |
| Ordinary explicit transaction | nonzero transaction TS | protected | GREEN control |
| Explicit stale-read transaction | stale TS | protected | GREEN control |
| Autocommit external stale read | empty | not protected | RED owner |
| Same autocommit read on master | exact stale TS | protected | GREEN counterfactual |

The version schedule for the consequence cell is:

```text
write A (long value)
  -> choose external snapshot S
  -> write B
  -> keep autocommit stale reads at S active
  -> GC safe point > B commitTS
  -> Write CF compaction filters A
  -> read at S during Default-delete / Write-install window
```

Choosing `S` between B and a later C is not a valid red cell. GC retains the newest version before
the safe point as a fence, so that schedule can continue to return B. The snapshot must select a
version older than the retained fence.

## Old Runtime Evidence

The executed matrix used 20,000 rows with 4,096-byte values:

```text
snapshot_ts:       467723891750731777
stale baseline:    (20000, A, A, A, A)
current baseline:  (20000, B, B, B, B)
processlist state: autocommit
processlist TxnStart: empty
forced safe point: 467723902044864512
successful reads before hit: 117855
DefaultNotFound observations: 13
```

At `2026-07-17 02:08:38 +08:00`, TiKV logged both the precise load failure and its public error
identity:

```text
[ERROR] [mod.rs:452] ["default value not found"] [hint=load_data_from_default_cf]
[ERROR] [errors.rs:487] ["txn aborts"] [err_code=KV:Storage:DefaultNotFound]
```

TiDB logged the real SQL and read timestamp:

```text
sql="select length(v),note from t where id=82"
status="inTxn:0, autocommit:1"
timestamp=467723891750731777
err="...DefaultNotFound..."
```

The decoded keys belong to table ID 9990 and real integer handles including 3, 7, 17, 82, 100,
108, 143, 144, 145, 153, 170, 219, and 229. This closes the gap left by a bare error string: the
request mode, read TS, table, row, safe point, and compaction phase are all known.

## Master Counterfactual

The testbed ran:

```text
TiDB version: v9.0.0-beta.2.pre-1995-gd573e284da
TiDB commit:  d573e284da773c820c1c313105b73d587378381b
MDL:          ON
topology:     1 TiDB, 3 TiKV, 1 PD
```

For a 61.424-second autocommit external stale read:

```text
external TS:             467723585512275969
processlist TxnStart:    07-17 01:44:42.722(467723585512275969)
etcd reported minStartTS:467723585512275969
query result:            3000 rows, value set [4096]
```

The exact source counterfactual is in `pkg/session/session.go`. Old TiDB populates process info only
from `TxnCtx.StartTS`. Master adds the same fallback in `SetProcessInfo` and `UpdateProcessInfo`:

```go
if curTxnStartTS == 0 {
    curTxnStartTS = s.sessionVars.TxnCtx.StaleReadTs
}
```

`ReportMinStartTS` consumes `CurTxnStartTS`, so this is the missing owner, not merely a logging fix.

Post-RED historical lookup maps the root to TiDB issue
[#61325](https://github.com/pingcap/tidb/issues/61325) and merged PR
[#61329](https://github.com/pingcap/tidb/pull/61329). The current master source contains the fix.
This run did not prove that every supported release branch has received a backport.

## Reusable Probe

The scaffold is
`scaffolds/top-level/ai_native_default_not_found_stale_gc_probe.py`.

Owner-only mode prepares A/snapshot/B and classifies processlist registration. Accelerated window
mode additionally requires the test-only PD utility in
`scaffolds/pd-tools/force-gc-safepoint/main.go`, matching `tikv-ctl`, and every TiKV debug endpoint.
It refuses to bypass a GREEN master owner: forcing PD on master would evade the very protection the
counterfactual is meant to verify.

The scaffold itself was smoke-tested against both binaries. The old stack returned
`OWNER_RED ... processlist_txnstart=''`; master returned `OWNER_GREEN` with the exact numeric
snapshot embedded in `TxnStart`.

The accelerated mode advances global GC state and must run only on an isolated disposable cluster.
It restores the temporary Write CF buffer size, but neither PD's GC safe point nor
`tidb_external_ts` can be moved backward.

## Method Improvement

This replay adds a `live-resource registration` selector:

```text
resource: an active snapshot, lock, lease, cursor, or reservation
registry: processlist, minStartTS, service safe point, owner table, or heartbeat
collector: GC, cleanup, timeout, failover, or compaction

P: collector checks the registry
Q: every live resource that must survive is represented there
F: collector reclaims state not protected by the registry
R: an alternate mode stores identity outside the field the registry reads
```

The efficient oracle ladder is:

1. **Registration witness:** live read TS versus processlist `TxnStart`.
2. **Aggregation witness:** processlist value versus etcd minStartTS.
3. **Collection witness:** GC safe point crosses the live timestamp.
4. **Process consequence:** Default CF deletion while old Write is still visible.
5. **Highest consumer:** exact SQL error or stale rowset loss.
6. **Counterfactual:** change only registration ownership and require safe point protection.

The first two levels take seconds and should gate the expensive GC/compaction run. The failed B/C
schedule and the initially unflushed Write CF are stored as negative screens: an apparently GREEN
read is invalid unless the selected historical version is collectible and the compaction filter
actually ran.
