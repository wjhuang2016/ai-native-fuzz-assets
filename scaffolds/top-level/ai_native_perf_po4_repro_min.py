#!/usr/bin/env python3
"""Minimized repro for PF2-A: ADD INDEX row_count doubling across ADMIN PAUSE/RESUME.

Varies the pause point (early ~6%) and samples the row_count trajectory densely, to
distinguish 'always exactly 2N' (pause-point-independent double accounting) from
'restart from scratch' (final = pause_rc + N) or 'checkpoint rework' (final in between).
"""

from __future__ import annotations

import subprocess
import sys
import time

H, P, USER = "127.0.0.1", "14000", "root"
DB = "ai_perf_m2"
N_DOUBLINGS = 17
NROWS = 1 << N_DOUBLINGS


def mysql(sql: str) -> tuple[int, str, str]:
    r = subprocess.run(
        ["mysql", f"-h{H}", f"-P{P}", f"-u{USER}", "--comments", "-e", sql],
        capture_output=True, text=True, timeout=900,
    )
    return r.returncode, r.stdout, r.stderr


def must(sql: str) -> str:
    rc, out, err = mysql(sql)
    if rc != 0:
        raise RuntimeError(f"SQL failed: {err}\n--- {sql[:300]}")
    return out


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


def checkpoint() -> str:
    out = must("SELECT job_id, curr_ele_id, start_key, end_key, physical_id "
               "FROM mysql.tidb_ddl_reorg;")
    return " | ".join(out.strip().splitlines()[1:]) or "(empty)"


def main() -> int:
    saved = {}
    for var in ("tidb_ddl_enable_fast_reorg", "tidb_ddl_reorg_worker_cnt",
                "tidb_ddl_reorg_batch_size", "tidb_enable_auto_analyze"):
        saved[var] = must(f"SELECT @@global.{var};").strip().splitlines()[1]
    must("SET GLOBAL tidb_enable_auto_analyze=0; SET GLOBAL tidb_ddl_enable_fast_reorg=0;"
         "SET GLOBAL tidb_ddl_reorg_worker_cnt=1; SET GLOBAL tidb_ddl_reorg_batch_size=32;")
    must(f"DROP DATABASE IF EXISTS {DB}; CREATE DATABASE {DB}; USE {DB};"
         "CREATE TABLE t(a INT, b INT); INSERT INTO t VALUES (1,1);")
    count = 1
    for _ in range(N_DOUBLINGS):
        must(f"USE {DB}; INSERT INTO t SELECT a + {count}, (a + {count}) % 1000 FROM t;")
        count *= 2
    print(f"table built: {NROWS} rows")
    try:
        proc = subprocess.Popen(
            ["mysql", f"-h{H}", f"-P{P}", f"-u{USER}", DB, "-e",
             "ALTER TABLE t ADD INDEX ib(b);"],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        traj: list[tuple[float, int, str]] = []
        job_id, paused_at = None, -1
        t_start = time.time()
        while time.time() - t_start < 600:
            st = job_status()
            if not st:
                time.sleep(0.1)
                continue
            job_id, rc_now, state = st
            traj.append((round(time.time() - t_start, 1), rc_now, state))
            if paused_at < 0 and state == "running" and rc_now > NROWS // 16:
                must(f"ADMIN PAUSE DDL JOBS {job_id};")
                paused_at = rc_now
                print(f"PAUSE issued at row_count={rc_now}")
            if paused_at > 0 and state == "paused":
                break
            if state in ("synced", "done"):
                break
            time.sleep(0.1)
        st = job_status()
        print(f"state after pause wait: {st}")
        print(f"checkpoint at pause: {checkpoint()}")
        time.sleep(1)
        must(f"ADMIN RESUME DDL JOBS {job_id};")
        print("RESUME issued")
        while time.time() - t_start < 600:
            st = job_status()
            if st:
                traj.append((round(time.time() - t_start, 1), st[1], st[2]))
                if st[2] in ("synced", "done"):
                    break
            time.sleep(0.1)
        proc.wait()
        final = job_status()
        print(f"final: row_count={final[1]} N={NROWS} ratio={final[1] / NROWS:.3f}")
        print("trajectory (t, row_count, state) — transitions only:")
        last = None
        for pt in traj:
            if last is None or pt[1] != last[1] or pt[2] != last[2]:
                print(f"  {pt[0]:7.1f}s  {pt[1]:8d}  {pt[2]}")
            last = pt
    finally:
        must(f"DROP DATABASE IF EXISTS {DB};")
        for var, val in saved.items():
            must(f"SET GLOBAL {var} = {val};")
        print("cleanup done")
    return 0


if __name__ == "__main__":
    sys.exit(main())
