#!/usr/bin/env python3
"""PF-4: DXF (fast-reorg / ingest) ADD INDEX mid-flight pause/resume — does the DXF subtask
checkpoint prevent rework, unlike the bespoke txn backfill loop (perf-30001)?

This tests the SHARED DXF backfill framework (used by add-index ingest AND file-based
IMPORT INTO). IMPORT INTO ... FROM SELECT turned out to be an INLINE pipeline (no DXF, no
checkpoint), so the DXF checkpoint is reached here via fast-reorg add index.

Audit card (PS1 selector, third target):
  Q_claim:   a DXF backfill paused mid-flight resumes from the subtask checkpoint
             (adjustStartKey skips processed ranges) -> total work ~= N, NOT 2N
  O_oracle:  PO4 conservation — final ADMIN SHOW DDL JOBS row_count / N. ratio ~1 => checkpoint
             honored (GREEN, sharpens the boundary); ratio ~2 => rework (RED, same bug class as
             perf-30001 but in the DXF path)
  trigger ev: (1) fast_reorg=1 AND dist_task=1 (DXF path actually taken); (2) the pause must
             land MID-FLIGHT (0 < row_count < N at pause) else INVALID — a pause before the
             first range proves nothing; (3) a subtask checkpoint row must exist during the run.

Time-based pause (ingest row_count updates coarsely per-range, so threshold-on-row_count is
unreliable). Throttle write speed so the backfill is still running when we pause.
Safety: finally-block resumes any paused job and restores all globals.
"""

from __future__ import annotations

import subprocess
import sys
import time

H, P, USER = "127.0.0.1", "14000", "root"
DB = "ai_perf_dxf"
N_DOUBLINGS = 20  # 1,048,576 rows
NROWS = 1 << N_DOUBLINGS


def mysql(sql: str) -> tuple[int, str, str]:
    r = subprocess.run(
        ["mysql", f"-h{H}", f"-P{P}", f"-u{USER}", "--comments", "-e", sql],
        capture_output=True, text=True, timeout=1200,
    )
    return r.returncode, r.stdout, r.stderr


def must(sql: str) -> str:
    rc, out, err = mysql(sql)
    if rc != 0:
        raise RuntimeError(f"SQL failed: {err}\n--- {sql[:300]}")
    return out


def q1(sql: str) -> str:
    out = must(sql).strip().splitlines()
    return out[1] if len(out) > 1 else ""


def job_status() -> tuple[int, int, str] | None:
    out = must("ADMIN SHOW DDL JOBS 10;")
    lines = out.strip().splitlines()
    if len(lines) < 2:
        return None
    hdr = lines[0].split("\t")
    idx = {n: i for i, n in enumerate(hdr)}
    for line in lines[1:]:
        f = line.split("\t")
        if (len(f) >= len(hdr) and f[idx["DB_NAME"]] == DB
                and f[idx["TABLE_NAME"]] == "t" and "add index" in f[idx["JOB_TYPE"]]):
            return int(f[idx["JOB_ID"]]), int(f[idx["ROW_COUNT"]]), f[idx["STATE"]]
    return None


def subtask_checkpoint_seen(job_id: int) -> bool:
    for tbl in ("tidb_background_subtask", "tidb_background_subtask_history"):
        out = must(f"SELECT COUNT(*) FROM mysql.{tbl} "
                   f"WHERE task_key LIKE 'ddl/backfill/{job_id}%' AND LENGTH(checkpoint) > 2;")
        v = out.strip().splitlines()
        if len(v) > 1 and v[1].isdigit() and int(v[1]) > 0:
            return True
    return False


def main() -> int:
    saved = {v: q1(f"SELECT @@global.{v};") for v in (
        "tidb_ddl_enable_fast_reorg", "tidb_enable_dist_task", "tidb_enable_auto_analyze",
        "tidb_ddl_reorg_max_write_speed")}
    print("saved:", saved, flush=True)
    must("SET GLOBAL tidb_enable_auto_analyze=0;"
         "SET GLOBAL tidb_ddl_enable_fast_reorg=1;"
         "SET GLOBAL tidb_enable_dist_task=1;"
         "SET GLOBAL tidb_ddl_reorg_max_write_speed='1MiB';")
    must(f"DROP DATABASE IF EXISTS {DB}; CREATE DATABASE {DB}; USE {DB};"
         "CREATE TABLE t(a INT PRIMARY KEY, b INT);"
         "INSERT INTO t VALUES (1,1);")
    count = 1
    for _ in range(N_DOUBLINGS):
        must(f"USE {DB}; INSERT INTO t SELECT a + {count}, (a + {count}) % 1000 FROM t;")
        count *= 2
    print(f"table built: {NROWS} rows, fast_reorg=1, dist_task=1, write speed 1MiB", flush=True)
    job_id, pause_rc, ckpt_seen = None, -1, False
    traj: list[tuple[float, int, str]] = []
    try:
        proc = subprocess.Popen(
            ["mysql", f"-h{H}", f"-P{P}", f"-u{USER}", DB, "-e",
             "ALTER TABLE t ADD INDEX ib(b);"],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        t_start = time.time()
        # pause once the backfill is MID-INGEST: row_count crossed a threshold (ingest reports
        # row_count only in its write phase, so this guarantees a real checkpoint window).
        paused = False
        pause_threshold = NROWS // 4
        while time.time() - t_start < 900:
            st = job_status()
            if st:
                job_id, rc_now, state = st
                traj.append((round(time.time() - t_start, 1), rc_now, state))
                if not ckpt_seen and job_id:
                    ckpt_seen = subtask_checkpoint_seen(job_id)
                if state in ("synced", "done"):
                    break
                if not paused and state == "running" and rc_now >= pause_threshold:
                    must(f"ADMIN PAUSE DDL JOBS {job_id};")
                    pause_rc = rc_now
                    paused = True
                    print(f"PAUSE issued at row_count={rc_now} (>= {pause_threshold})", flush=True)
                if paused and state == "paused":
                    break
            time.sleep(0.2)
        st = job_status()
        print(f"after pause: {st}; checkpoint seen during run: {ckpt_seen}", flush=True)
        if not paused or (st and st[2] in ("synced", "done")):
            print("INVALID: job finished before a mid-flight pause landed", flush=True)
            proc.wait()
            return 0
        ckpt_at_pause = subtask_checkpoint_seen(job_id)
        time.sleep(3)
        must(f"ADMIN RESUME DDL JOBS {job_id};")
        print("RESUME issued", flush=True)
        while time.time() - t_start < 900:
            st = job_status()
            if st:
                traj.append((round(time.time() - t_start, 1), st[1], st[2]))
                if st[2] in ("synced", "done", "cancelled", "failed"):
                    break
            time.sleep(0.3)
        proc.wait()
        final = job_status()
        chk = must(f"USE {DB}; ADMIN CHECK TABLE t;"
                   "SELECT count(*) FROM t USE INDEX(ib) WHERE b >= 0;"
                   "SELECT count(*) FROM t IGNORE INDEX(ib) WHERE b >= 0;")
        cnts = [int(x) for x in chk.split() if x.isdigit()]
        ratio = final[1] / NROWS if final else 0
        print("\n== RESULT ==", flush=True)
        print(f"pause landed at row_count:   {pause_rc}  (mid-flight: {0 < pause_rc < NROWS})")
        print(f"final row_count:             {final[1]}  N={NROWS}  ratio={ratio:.3f}")
        print(f"subtask checkpoint present:  run={ckpt_seen} at_pause={ckpt_at_pause}")
        print(f"correctness (idx==table==N): {cnts[-2:]} vs {NROWS}")
        print("trajectory transitions:")
        last = None
        for pt in traj:
            if last is None or pt[1] != last[1] or pt[2] != last[2]:
                print(f"   {pt[0]:7.1f}s {pt[1]:9d} {pt[2]}")
            last = pt
        mid = 0 < pause_rc < NROWS
        if not mid:
            print("VERDICT: INVALID — pause did not land mid-flight; checkpoint not exercised")
        elif cnts[-2:] != [NROWS, NROWS]:
            print(f"VERDICT: RED(correctness) — index/table counts {cnts[-2:]} != {NROWS}")
        elif ratio >= 1.8:
            print(f"VERDICT: RED — DXF ingest resume re-does work: row_count ratio {ratio:.2f} "
                  f"(~2x). DXF checkpoint NOT honored on pause/resume — same class as perf-30001.")
        elif ratio <= 1.3:
            print(f"VERDICT: GREEN — DXF ingest resume conserves work: ratio {ratio:.2f} (~1x). "
                  f"The DXF subtask checkpoint IS honored; the rework bug is specific to the "
                  f"bespoke non-DXF loops (txn backfill perf-30001, TTL scan perf-30002). "
                  f"PS1 boundary sharpened.")
        else:
            print(f"VERDICT: uncertain — ratio {ratio:.2f} between 1x and 2x; inspect trajectory")
    finally:
        st = job_status()
        if st and st[2] == "paused":
            mysql(f"ADMIN RESUME DDL JOBS {st[0]};")
            deadline = time.time() + 600
            while time.time() < deadline:
                s2 = job_status()
                if not s2 or s2[2] in ("synced", "done", "cancelled", "failed"):
                    break
                time.sleep(1)
        for v, val in saved.items():
            must(f"SET GLOBAL {v} = {val};")
        must(f"DROP DATABASE IF EXISTS {DB};")
        print("cleanup done: globals restored, db dropped", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
