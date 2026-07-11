# perf-30002: a re-locked TTL scan task restarts its range from ScanRangeStart — no intra-range progress checkpoint, so owner-failover / heartbeat-timeout re-seeks the whole deleted prefix

> Second hit of the performance sibling loop, in a DIFFERENT module from perf-30001 (TTL vs
> ADD INDEX), nominated by the SAME selector PS1 (background-job interruption × progress-state
> reconstruction). Found 2026-07-03. Oracle PO5 (ttl_scan_rework). Harness:
> ai_native_perf_pf3_ttl_rescan.py. Loop: ai-native-perf-oracle-library.md.

## One-line

When a running TTL scan task is re-locked by another owner (heartbeat timeout / owner
failover / TiDB restart mid-job), it rebuilds its scan query from the task's original
`ScanRangeStart` and re-scans the entire range from the beginning. The rows deleted before the
interruption are MVCC tombstones (until GC), so the restarted scan must re-seek over the whole
deleted prefix. Correctness is unaffected (idempotent, no duplicate deletes); the cost is
wasted re-seek IO of O(rows already processed before the interruption), per region-sized task.

## Severity (stated honestly)

Lower than perf-30001. Bounded per task (a scan task covers one region-sized range), and the
trigger is an interruption event (failover / HB timeout / restart), not a routine operation.
No correctness impact, no duplicate deletes. But it is a genuine, execution-confirmed
"missing checkpoint → rework on interruption" gap: every re-lock throws away all intra-range
scan progress. On a large table (one task per region) with periodic owner changes, the wasted
re-seek accumulates across all in-flight tasks.

## Execution evidence (2026-07-03, TiDB v8.4.0 fp-tidb, ai_native_perf_pf3_ttl_rescan.py)

Setup: 32768 all-expired rows, single region → single scan task (scan_id=0, whole-table
range), deletes throttled (5 batches/s × 20) so the scan blocks mid-range under backpressure.

Interruption primitive (single node, no real failover available): inject a fake dead owner +
stale heartbeat into `mysql.tidb_ttl_task` —
`UPDATE mysql.tidb_ttl_task SET owner_id='fake-dead-owner', owner_hb_time = NOW() - INTERVAL
200 SECOND WHERE table_id=? AND status='running'`. This drives the REAL re-lock path:
`checkInvalidTask` (5s ticker) sees owner≠self → cancels the running scan; `rescheduleTasks` →
`peekWaitingScanTasks` matches `(status='running' AND owner_hb_time < now-2·hbInterval)` →
`lockScanTask` heartbeat-timeout branch → new `ScanQueryGenerator(tbl, expire, ScanRangeStart,
ScanRangeEnd)` → rescan.

```text
deleted before interrupt:                  9840   (~30%)
scan cursor BEFORE interrupt (max rowid):  9984   (scan had advanced to ~10000)
re-lock trigger evidence (owner changed off 'fake'): TRUE
normal continuation batch total_keys:      481    (baseline)
post-interrupt scan queries (skip, total_keys, rowid predicate):
     skip=16384 total_keys=  481   _tidb_rowid > 10240
     skip=16384 total_keys=  481   _tidb_rowid > 10496
     skip=16384 total_keys=21145   NULL          <-- RANGE-START re-seek (no lower bound)
     skip=16384 total_keys=  481   _tidb_rowid > 10588
VERDICT: RED
```

The smoking gun is the `NULL`-predicate query: a TTL scan SELECT with **no `_tidb_rowid >`
lower bound** appearing AFTER the scan had already advanced to rowid ~10000. Its total_keys
(21145) is 44× a normal batch (481) because it seeks from the range start and skips the entire
deleted prefix. A no-lower-bound scan can only be the FIRST query of a fresh generator → the
task restarted from `ScanRangeStart`.

Built-in specificity control (within the same run): the identical range-start query shape also
runs at job start, but then `delete_skipped ≈ 0` (nothing deleted yet). The only variable that
turns a cheap range-start scan into a 44× re-seek is the interruption having deleted a prefix
first. The differential isolates the interruption as the cause.

## Trigger-evidence discipline paid for itself (harness lessons L7 + detector fix)

Two earlier runs certified INVALID, correctly: the intended primitive
`SET GLOBAL tidb_ttl_scan_worker_count=0` is clamped to MinValue=1, and `resizeWorkers` cancels
`workers[count:]` while the running task always sits on `workers[0]` — so shrinking never
cancels the busy worker and no re-lock happened (`owner unchanged`). The re-lock trigger-
evidence guard blocked a false RED both times. A third run with the HB-timeout injection fired
the re-lock but the FIRST verdict logic keyed on the `_tidb_rowid > N` cursor min (which stays
~10240, since continuation resumes just past the deleted prefix) and wrongly read green — until
the range-start (NULL-predicate) query with 44× total_keys was noticed. The detector was fixed
to key on "a no-lower-bound scan with total_keys ≫ a normal batch," and the RED reproduced
cleanly. Reasoning proposed the gap from source; execution disposed the WRONG detector twice
before the right signal was pinned.

## P/Q/D/F mapping

```text
P_check:   lockScanTask reads the tidb_ttl_task row (incl. state: TotalRows/SuccessRows/
           PreviousOwner) and claims to continue the task
Q_claim:   a re-taken TTL scan task continues from where it stopped — bounded rework
D_dims:    interruption cause (HB timeout / owner failover / restart), fraction of range
           deleted at interrupt, region/range size, scan vs delete rate
F_effect:  the scan generator is rebuilt from ScanRangeStart every run; the in-memory progress
           key (ScanQueryGenerator.stack / continueFromResult) is never persisted, and the
           running statistics are reset to &ttlStatistics{} on re-lock
R_redflag: sibling re-entry path reconstructs progress state it never persisted — the perf twin
           of id30009 (rollback rebuilds metadata, drops a bit) and perf-30001 (resume rebuilds
           progress, drops the checkpoint)
```

## Source anchors (/Users/bba/pc/tidb master v9.0.0-beta.2.pre, behavior matches v8.4)

```text
pkg/ttl/cache/task.go:112    TTLTaskState = {TotalRows, SuccessRows, ErrorRows, ScanTaskErr,
                             PreviousOwner} — NO last-scanned-key / progress field.
pkg/ttl/ttlworker/scan.go:212  NewScanQueryGenerator(t.tbl, t.ExpireTime, t.ScanRangeStart,
                             t.ScanRangeEnd) — generator always seeded from ScanRangeStart.
pkg/ttl/sqlbuilder/sql.go:339  NextSQL continues from `continueFromResult` held ONLY in the
                             in-memory scan loop (scan.go:218 lastResult) — not persisted.
pkg/ttl/ttlworker/task_manager.go:441  lockScanTask HB-timeout / resigned branch re-locks.
pkg/ttl/ttlworker/task_manager.go:476  runningScanTask built with statistics: &ttlStatistics{}
                             — fresh zero; prevSuccessRows from state only feeds a log line
                             (task_manager.go:360-371).
```

The `state` even carries `PreviousOwner` (proof the design KNOWS a task can change hands) yet
persists no scan position for the new owner to resume from.

## Fix direction + fix-validation contract (CLOSED-FIXABLE)

Fix: persist the last-successfully-scanned key into `tidb_ttl_task.state` (e.g. a
`ContinueFromKey` field) on heartbeat/resign, and on re-lock seed `NewScanQueryGenerator` from
`max(ScanRangeStart, ContinueFromKey)` instead of always `ScanRangeStart`. Seed the running
statistics from the persisted counts so accounting is continuous too.

A correct patch must satisfy, across D_dims {HB-timeout | resign | restart} ×
{interrupt early | mid | late} × {single-region | multi-region range} × {clustered | non-clustered PK}:
```text
V1 after re-lock, the first scan SELECT carries a `>= ContinueFromKey` lower bound near the
   interrupt cursor — NOT a range-start (no-lower-bound) scan; its total_keys ≈ a normal batch,
   not O(deleted prefix).
V2 no row is skipped: rows in (ScanRangeStart, ContinueFromKey] that were expired-but-not-yet-
   deleted at interrupt time must still be handled (the checkpoint must advance only past
   rows whose deletes were durably enqueued/acked, else a fix trades rework for data left
   un-deleted — worse than the bug).
V3 accounting (job history scanned/deleted) is continuous across the re-lock, not reset.
V4 idempotence preserved: re-deletion of already-deleted rows stays a no-op (correctness).
```
V2 is the load-bearing constraint: the reason a naive "resume from last scanned key" is unsafe
is that the scan is AHEAD of the deletes (unbuffered delCh, but still a window). The checkpoint
must be the min over in-flight delete tasks' progress, not the scan cursor.

## Terminal state: CLOSED-FIXABLE (recorded 2026-07-03)

Report-ready with the V2 caveat called out (the fix is subtly more than "save the scan key").
Blast-radius variants under GUARD (do not expand without a new D_dim):
- multi-region table (many scan tasks): expected same per-task, larger aggregate — not re-run.
- real owner failover / TiDB restart (vs injected HB timeout): same code path; injection
  faithfully drives lockScanTask's HB-timeout branch, so the finding does not depend on the
  injection being "real".

## Selector ledger update (PS1 — second hit, cross-module)

```text
PS1: background-job interruption sibling paths × progress-state reconstructed on re-entry.
     predictions now: perf-30001 (ADD INDEX pause/resume) RED; perf-30002 (TTL re-lock) RED.
     TWO hits in two different modules (DDL backfill, TTL) from one selector — the selector
     predicts across subsystems, not just within one. Promote confidence: this is the perf
     loop's strongest selector, and it is the cross-loop transfer of the correctness selector
     "sibling rollback/cancel path reconstructs state" (id30009).
```
