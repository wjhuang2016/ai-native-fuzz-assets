# Concurrency Harness (interleaving dimension)
> 2026-07-06. Reusable harness for the interleaving D_dim — "pinned/widened substate × concurrent
> op". This is the least-exercised lane and where severity lives: 4 of the 5 highest-severity roots
> are state-transforming DDL, and id30038's consequence-3 liveness bug only surfaced here.
> Script: `ai_native_concurrency_harness.sh`. Example case: `ai_native_case_id30038.sh`.

## Why this exists

The battery documents "pinned substate × concurrent operation" but campaigns barely ran it. id30038
proved the cost: the loud oracle (did the DDL error?) saw only a false duplicate and nearly graded
it consequence-1, while the same defect wedged the online DDL in a retry loop — a liveness
consequence-3 that only a state-observing oracle (O28) catches. This harness makes the interleaving
lane a standard, low-friction move so the high-consequence lane actually gets mined.

## What it does (one run = N iterations)

Per iteration: build a fresh table, launch the online DDL under test, drive a concurrent DML load
against a not-yet-backfilled row, and classify the outcome with **both** the loud oracle and the
silent-consequence oracles — always, even when a loud error already fired (oracle library:
"a loud symptom does not close the obligation").

Failure modes it distinguishes:
```text
RED_CORRUPT  ADMIN CHECK fails after the DDL, or the case invariant probe returns a row  (silent C3)
RED_WEDGE    O28: ErrCount climbs across polls while SchemaState is frozen — stuck job    (liveness C3)
RED_ENCKEY   `invalid encoded key` rollback
RED_ERROR    any other loud error (e.g. false duplicate 1062)
GREEN        DDL ok, ADMIN CHECK ok, invariant clean
```

## Reorg-window widening (release build, no failpoints)

The harness sets `tidb_enable_dist_task=OFF`, `tidb_ddl_enable_fast_reorg=OFF`,
`tidb_ddl_reorg_worker_cnt=1`, `tidb_ddl_reorg_batch_size=32`, and the case `SPLIT`s a large table.
That makes the single-threaded txn backfill slow enough to race concurrent DML on a plain nightly
playground — no failpoint build needed. (A failpoint like `mockBackfillSlow` makes it deterministic
in a test build, but it is only a timing aid, never the semantic trigger.)

## O28 wedge detection (the load-bearing new oracle)

While the DML runs, the harness polls `ADMIN SHOW DDL` and reads ErrCount + SchemaState across ≥2
samples. A job whose ErrCount climbs while SchemaState does not advance and which never returns to
the client is a **wedge** (liveness C3), not a slow job. Confirm workload-driven: it reaches a
terminal state (usually clean rollback) once the DML stops. A single ErrCount snapshot is not
evidence — always two polls.

The harness declares `RED_WEDGE` on two signals: (a) ErrCount climbing across polls (the sharp
signal), or (b) no client return within a hard cap (default 90s) — a robust fallback that also
guarantees the harness never hangs. Signal (b) is coarser and can in principle flag a slow-but-
progressing job, so a cap-path RED_WEDGE should be corroborated before grading (check ErrCount is
non-zero/climbing or RowCount is frozen in `ADMIN SHOW DDL`). Validated on id30038: 4 iters gave
2 GREEN + 2 RED_WEDGE, no hang.

## Writing a case file

Source-able shell that defines:
```text
CASE_NAME       label
DB              database (created fresh per iteration)
SETUP_SQL       build table + data, no index yet (may SPLIT)
DDL_SQL         the online DDL under test
dml_for_iter()  echoes the concurrent DML for iteration $1 (target a high, not-yet-backfilled row;
                keep it reversible so the base data stays clean)
SILENT_SQL      (optional) case-specific invariant probe; any returned row = RED_CORRUPT
```
`ADMIN CHECK TABLE t` is the default silent oracle (catches a non-unique unique index, index-record
inconsistency). Add `SILENT_SQL` when a sharper invariant exists (uniqueness `GROUP BY`, row-count,
FK-orphan). Run: `zsh ai_native_concurrency_harness.sh <case_file> [iterations]`.

## Where it plugs into the loop

- **SOURCE_TARGETS / P4 high-consequence lane**: any "online DDL × concurrent DML" target is routed
  here instead of a bespoke script. New targets = new case files, not new harnesses.
- **INTEGRATE silent-oracle gate**: the harness's always-run silent oracles + O28 satisfy the gate —
  a hit's consequence grade is only valid after RED_CORRUPT/RED_WEDGE were checked, not just the
  loud error.
- **Battery**: every online-DDL selector (S1, S5, S9, S17) should carry a "run the concurrency
  harness before grading consequence" note.

## Worked example: id30038

`ai_native_case_id30038.sh`. ADD UNIQUE MVI + sibling unique index under concurrent `UPDATE b`.
Upstream nightly `v9.0.0-beta.2.pre-1774`: across iterations the harness reproduces RED_ERROR
(false dup 1062), RED_ENCKEY (invalid encoded key), and RED_WEDGE (ErrCount 386→467 climbing,
stuck in write-reorg). The silent oracle confirmed the table is healthy after every rollback — so
this is a liveness C3, not silent data corruption. issue #69660.
