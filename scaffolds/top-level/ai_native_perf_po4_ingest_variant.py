#!/usr/bin/env python3
"""PF2-B retry: ADD INDEX pause/resume rework probe on the DEFAULT ingest (fast reorg) path.

Same obligation as the txn-mode red cell (PO4): after pause->resume, no restart and
row accounting ~= N. Bigger table + fast polling because ingest completes quickly.
Safety: finally-block always resumes or cancels any paused job so the DDL queue can
never be left blocked (lesson from the crashed txn repro).
"""

from __future__ import annotations

import subprocess
import sys
import time

H, P, USER = "127.0.0.1", "14000", "root"
DB = "ai_perf_pf2b"
N_DOUBLINGS = 19  # 1,048,576 rows
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


def main() -> int:
    saved = {}
    for var in ("tidb_ddl_enable_fast_reorg", "tidb_enable_auto_analyze"):
        saved[var] = must(f"SELECT @@global.{var};").strip().splitlines()[1]
    must("SET GLOBAL tidb_enable_auto_analyze=0; SET GLOBAL tidb_ddl_enable_fast_reorg=1;")
    must("SET GLOBAL tidb_ddl_reorg_max_write_speed='2MiB';")
    must(f"DROP DATABASE IF EXISTS {DB}; CREATE DATABASE {DB}; USE {DB};"
         "CREATE TABLE t(a INT, b INT); INSERT INTO t VALUES (1,1);")
    count = 1
    for _ in range(N_DOUBLINGS):
        must(f"USE {DB}; INSERT INTO t SELECT a + {count}, (a + {count}) % 1000 FROM t;")
        count *= 2
    print(f"table built: {NROWS} rows, fast_reorg=1 (ingest)")
    job_id = None
    try:
        proc = subprocess.Popen(
            ["mysql", f"-h{H}", f"-P{P}", f"-u{USER}", DB, "-e",
             "ALTER TABLE t ADD INDEX ib(b);"],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        paused_at = -1
        t_start = time.time()
        traj: list[tuple[float, int, str]] = []
        while time.time() - t_start < 900:
            st = job_status()
            if st:
                job_id, rc_now, state = st
                traj.append((round(time.time() - t_start, 1), rc_now, state))
                if paused_at < 0 and state == "running":
                    must(f"ADMIN PAUSE DDL JOBS {job_id};")
                    paused_at = rc_now
                    print(f"PAUSE issued at row_count={rc_now}")
                if paused_at >= 0 and state == "paused":
                    break
                if state in ("synced", "done"):
                    break
            time.sleep(0.05)
        st = job_status()
        print(f"after pause wait: {st}")
        if paused_at < 0 or (st and st[2] in ("synced", "done")):
            print("INVALID: no mid-flight pause landed (ingest too fast)")
            proc.wait()
            return 0
        ckpt = must("SELECT job_id, HEX(start_key) FROM mysql.tidb_ddl_reorg;")
        print(f"checkpoint rows at pause:\n{ckpt.strip()}")
        time.sleep(3)
        must(f"ADMIN RESUME DDL JOBS {job_id};")
        print("RESUME issued")
        while time.time() - t_start < 900:
            st = job_status()
            if st:
                traj.append((round(time.time() - t_start, 1), st[1], st[2]))
                if st[2] in ("synced", "done", "cancelled", "failed"):
                    break
            time.sleep(0.05)
        proc.wait()
        final = job_status()
        print(f"final: row_count={final[1]} N={NROWS} ratio={final[1] / NROWS:.3f} state={final[2]}")
        chk = must(f"USE {DB}; ADMIN CHECK TABLE t;"
                   "SELECT count(*) FROM t USE INDEX(ib) WHERE b >= 0;"
                   "SELECT count(*) FROM t IGNORE INDEX(ib) WHERE b >= 0;")
        print(f"correctness: {[x for x in chk.split() if x.isdigit()]} (expect [{NROWS}, {NROWS}])")
        print("trajectory transitions:")
        last = None
        for pt in traj:
            if last is None or pt[1] != last[1] or pt[2] != last[2]:
                print(f"  {pt[0]:7.1f}s  {pt[1]:8d}  {pt[2]}")
            last = pt
    finally:
        # never leave a paused job blocking the queue
        st = job_status()
        if st and st[2] == "paused":
            rc, out, err = mysql(f"ADMIN RESUME DDL JOBS {st[0]};")
            print(f"safety-resume of job {st[0]}: rc={rc}")
            deadline = time.time() + 600
            while time.time() < deadline:
                st2 = job_status()
                if not st2 or st2[2] in ("synced", "done", "cancelled", "failed"):
                    break
                time.sleep(1)
        must(f"DROP DATABASE IF EXISTS {DB};")
        must("SET GLOBAL tidb_ddl_reorg_max_write_speed=0;")
        for var, val in saved.items():
            must(f"SET GLOBAL {var} = {val};")
        print("cleanup done")
    return 0


if __name__ == "__main__":
    sys.exit(main())
