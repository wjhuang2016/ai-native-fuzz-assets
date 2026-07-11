# perf-30001: ADMIN PAUSE on txn-mode ADD INDEX neither pauses nor checkpoints — RESUME redoes the whole backfill (exactly 2x work)

> First red cell of the performance sibling loop. Found 2026-07-03 by oracle PO4
> (addindex_rework_conservation) on the first mining matrix it ever ran (PF2-A).
> Loop: ai-native-perf-oracle-library.md; harness: ai_native_perf_po4_addindex_rework.py,
> minimized: ai_native_perf_po4_repro_min.py.

## Symptom (user-visible)

On TiDB v8.4.0-dev (fp-tidb pod, Store=tikv) with `tidb_ddl_enable_fast_reorg=0` (classic
txn-merge backfill):

1. `ALTER TABLE t ADD INDEX` on a table with N rows; `ADMIN PAUSE DDL JOBS <id>` mid-backfill
   (state becomes `paused`).
2. **The backfill does not stop.** The scan/write continues in the background to completion
   while the job shows `paused`. Evidence: after RESUME, row_count jumps to exactly N within
   2s (physically impossible via re-scan at the observed ~8k rows/s) — an orphaned in-memory
   counter had already reached N during the "pause".
3. **The mid-flight checkpoint is never persisted.** `mysql.tidb_ddl_reorg.start_key` stays at
   the first row handle (`...5F72 8000000000000001`) for the entire run — sampled twice 6s
   apart during pause, identical, despite row_count=10240 at pause time.
4. **RESUME restarts the backfill from scratch** (start_key = handle 1): the entire table is
   re-scanned and every index KV re-written (idempotent, so data stays correct).
5. Final accounting: `ADMIN SHOW DDL JOBS` row_count = **exactly 2N** regardless of pause
   point (observed 524288 for N=262144, and 262144 for N=131072; trajectory: N at resume+2s
   climbing to 2N). Controls without pause: exactly N (65536 and 131072 runs, ratio 1.000).

Cost model of the defect: pausing a T-hour backfill at any point and resuming costs ~T more
hours and doubles the total read+write IO; "pause to relieve pressure" actually keeps the
pressure on. Correctness is unaffected (ADMIN CHECK TABLE clean; USE INDEX == IGNORE INDEX
counts).

## Facts ledger (all executed on 2026-07-03)

```text
F1 control no-pause, txn mode:       final row_count = N exactly (x2 independent runs)
F2 pause/resume, txn mode:           final row_count = 2N exactly (x2: N=2^18 paused@45216,
                                     N=2^17 paused@10240 -> pause-point-INDEPENDENT)
F3 checkpoint during pause:          start_key frozen at handle 1 (never advanced mid-flight)
F4 resume trajectory:                row_count == N at +2s, then climbs N -> 2N at ~8k rows/s
F5 correctness:                      ADMIN CHECK ok; index vs table rowcounts equal
F6 dedupe:                           #56942 is DXF/ingest display-only (row_count=0);
                                     #25968 is 2021-era accounting; docs describe checkpoints
                                     for the INGEST path (v7.1+). No issue found for the txn
                                     path's pause-ignores-backfill + checkpoint-never-saved.
```

## P/Q/D/F mapping

```text
P_check:   ADMIN PAUSE flips the job to paused; resume path calls getReorgInfo which reads
           mysql.tidb_ddl_reorg start_key as the claimed continuation point
Q_claim:   (a) paused means the backfill stops consuming resources;
           (b) start_key reflects completed progress;
           (c) resume continues, total work ~= N rows
D_dims:    backfill mode (txn vs ingest), pause timing, worker/batch config,
           who owns pause propagation (job worker vs reorg goroutine)
F_effect:  user trusts pause for resource relief and resume for continuation; both trusts
           are violated silently (job state lies about the backfill, row_count lies about work)
R_redflag: sibling-path reconstruction — the pause/resume path reconstructs progress state
           that the success path never needed; exactly the id30009 shape, perf edition
```

## Mechanism anchors (source: /Users/bba/pc/tidb master v9.0.0-beta.2.pre, matching behavior)

- `pkg/ddl/reorg.go:395` — on (re)entry `newReorgCtx(job.ID, job.GetRowCount())`: accounting
  seeds from the persisted row count.
- `pkg/ddl/reorg.go:428/451` — `job.SetRowCount(rc.getRowCount())` on done/tick: the only
  write-backs; once runReorgJob returns (5s tick -> ErrWaitReorgTimeout), the still-running
  backfill goroutine's counts are orphaned.
- `pkg/ddl/job_worker.go:809` — a pausing job makes the job worker return ErrPausedDDLJob
  BEFORE running the reorg step: nothing signals the already-running txn backfill goroutine
  (pause propagation exists for the DXF path via handle.PauseTask, `pkg/ddl/index.go:3171`;
  the txn path has no equivalent).
- `pkg/ddl/backfilling.go:1012ff` — result-collector goroutine calls
  `reorgInfo.UpdateReorgMeta(keeper.nextKey, ...)` every workerSize*4 results; empirically the
  persisted start_key never advanced (silent failure path logs only a Warn) — needs pinning
  in source, but the frozen checkpoint is execution-proven regardless.

Two independent defects compound:
```text
D-1 pause does not propagate to the txn backfill executor  -> "paused" job keeps working
D-2 mid-flight checkpoint never persists (txn path)        -> resume restarts from key 1
      => resume re-does all N rows (real rework, 2x IO)
      => accounting = orphaned-N + rescan-N = exactly 2N (breaks progress observability)
```

## Fix direction + fix-validation contract (CLOSED-FIXABLE)

Fix direction: (1) propagate pause to the txn backfill execution (set/check a pause signal
readable by the backfill workers, not only by the job worker step); (2) make UpdateReorgMeta
actually persist nextKey mid-flight on this path (find/fix the silent failure); (3) on resume,
reconcile accounting with the persisted checkpoint instead of orphaned in-memory counters.

A correct patch must satisfy, across D_dims {txn|ingest} x {pause early|mid|late} x
{worker 1|4} x {batch 32|256} x {partitioned|plain}:
```text
V1 while state=paused: mysql.tidb_ddl_reorg.start_key stable AND no index-KV write activity
V2 resume continuation: total row accounting in [N, N + one batch]
V3 checkpoint at pause: start_key corresponds to >= (row_count - one batch) rows of progress
V4 ADMIN CHECK TABLE + index-vs-table rowset equality stay green (guard against overshoot:
   a fix that skips rows is worse than one that repeats them)
```

## Terminal state: CLOSED-FIXABLE (recorded 2026-07-03)

Report-ready; no owner dependency. Blast-radius variants:
- ingest/DXF path (DEFAULT config): EXECUTED (ai_native_perf_po4_ingest_variant.py,
  524288 rows, write speed throttled to 2MiB): pause propagates correctly
  (running -> pausing -> paused), resume completes with row_count ratio EXACTLY 1.000 and
  clean correctness — the defect is txn-path-specific. Caveat: the pause landed at
  row_count=0 (before backfill work started); a MID-ingest pause shape is still an open
  probe.
- owner-switch / restart interruption (same obligation, different trigger): open probe,
  same harness pattern, needs multi-node or owner bounce.

## Selector extracted (perf ledger entry PS1)

```text
PS1: background-job interruption sibling paths (pause/resume, owner switch, restart) x
     a progress claim whose state is reconstructed on re-entry (checkpoint, row accounting).
     Born from: perf-30001. Prediction record: 1 nomination -> 1 red (this hit).
     The correctness twin (id30009 rollback-metadata) predicted this shape across loops —
     first cross-loop selector transfer with a confirmed hit.
```
