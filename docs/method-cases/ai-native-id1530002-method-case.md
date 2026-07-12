# Method Case id1530002: Runtime asset loss rediscovered an existing crash root

## Accounting

- Experiment asset: `id1530002`
- Final accounting: `known-duplicate`, not a new root
- Existing upstream root: [TiDB #65958](https://github.com/pingcap/tidb/issues/65958)
- Current fix candidate: [TiDB #66187](https://github.com/pingcap/tidb/pull/66187), still OPEN
- Testbed evidence: `assets/store/logs/add-index-local-engine-db-loss-red-20260712.log`

## Why the probe looked new

The initial source obligation was reasonable:

```text
after DXF progress is durable and a local Pebble engine is open,
loss of a temporary engine asset must not kill the serving TiDB front
```

The live probe used a precise phase boundary, deleted the internal DB directory
containing `000004.sst`, and observed `ERROR 2013`, process disappearance, task
failover, and eventual DDL completion. A neighboring raw-input-SST deletion was
GREEN, so the result was not an undifferentiated "local file loss" result.

## The deduplication step

Before promoting a runtime RED to a new bug, search three layers using the exact
asset, log signature, and lifecycle action:

1. Upstream issue search: `tmp_ddl`, `000004.sst`, `MustExist`, `ADD INDEX`, `cancel`.
2. History/PR search: cleanup ownership, active-engine tracking, and regression tests.
3. Source root comparison: same failure boundary, same user action, and same intended
   fix locus.

The match was exact enough to merge accounting: #65958 already reports cancel-induced
`tmp_ddl` cleanup deleting SSTs still used by a Pebble local engine, with the same
fatal log shape and the same TiDB process exit. PR #66187 protects active job IDs in
the cleanup loop. Our manual deletion is therefore a deterministic replay amplifier,
not a second product root.

## What the AI loop did prove

```text
source proof obligation
-> exact pause after SetTSBeforeImportEngine
-> smallest owned asset mutation
-> process/owner/task/end-state oracle
-> adjacent asset-type GREEN control
-> upstream issue/PR dedup
-> rediscovery asset, not duplicate issue
```

The strongest result is not the raw crash alone. It is the separation of:

- internal engine DB loss: process-exit RED;
- raw ingest input loss: retry/rebuild GREEN;
- final table state: green after survivor failover, without erasing the availability RED;
- upstream root: already known, so no new severe count.

## Method lesson

The loop needs an explicit **upstream history/issue dedup gate** after a strong RED:

```text
RED observed
-> normalize exact log / asset / lifecycle tuple
-> search issues, PRs, commits, and existing bug roots
-> same trigger + same failure boundary + same fix locus?
   yes -> known-duplicate rediscovery asset
   no  -> continue product-contract review and new-root accounting
```

This gate must run before filing an issue or claiming a new root. It does not discard
the experiment: a rediscovery can still improve the harness, shrink the trigger,
validate a fix, or contribute a reusable oracle.

## Reuse

When #66187 or a later fix is available, rerun the same matrix:

| Asset mutation | Expected result |
| --- | --- |
| Delete internal engine DB SST during the active-consumer window | no serving-process exit; bounded subtask failure/retry or clean cancel |
| Delete raw input SST before `db.Ingest` | retry/rebuild without process exit |
| Cancel DDL while local engine is active, then let cleanup tick | cleanup must skip active job directory; no Pebble fatal |

The third row is the natural production-shaped regression. The first row is the
deterministic boundary probe. The two must not be conflated.
