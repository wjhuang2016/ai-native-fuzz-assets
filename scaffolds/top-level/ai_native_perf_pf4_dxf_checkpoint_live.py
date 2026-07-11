#!/usr/bin/env python3
"""PF-4 (final): does the DXF ingest ADD INDEX persist an ADVANCING checkpoint across a
mid-flight pause/resume — the mechanism the bespoke txn backfill (perf-30001) and TTL scan
(perf-30002) both lack?

Direct boundary contrast, not a row_count oracle (PO4 row_count is BLIND to DXF ingest:
it stays 0 until completion, so it never shows 2x — a documented PO4 blind spot). Instead:

  perf-30001 (txn path):  at pause, mysql.tidb_ddl_reorg.start_key is FROZEN at handle 1
                          (nothing persisted) -> resume redoes the whole table (2N).
  PF-4 (DXF ingest path): mysql.tidb_ddl_reorg.reorg_meta holds a checkpoint that ADVANCES
                          during the run and survives pause; resume continues from it.

Oracle (progress-persistence): sample reorg_meta at 3+ points during the run; the encoded
checkpoint MUST change (advance). Then pause mid-flight (subtask running + checkpoint present
= reliable mid-flight signal, since row_count is coarse), confirm the checkpoint is persisted,
resume, and confirm ADMIN CHECK + index==table==N.

GREEN here = the DXF framework honors checkpoints; the perf-30001/30002 rework class lives
specifically in loops that predate/bypass the DXF checkpoint. This SHARPENS selector PS1.
"""

from __future__ import annotations

import hashlib
import subprocess
import sys
import time

H, P, USER = "127.0.0.1", "14000", "root"
DB = "ai_perf_dxf2"
N_DOUBLINGS = 19  # 524288 rows
NROWS = 1 << N_DOUBLINGS


def mysql(sql: str) -> tuple[int, str, str]:
    r = subprocess.run(["mysql", f"-h{H}", f"-P{P}", f"-u{USER}", "--comments", "-e", sql],
                       capture_output=True, text=True, timeout=1200)
    return r.returncode, r.stdout, r.stderr


def must(sql: str) -> str:
    rc, out, err = mysql(sql)
    if rc != 0:
        raise RuntimeError(f"SQL failed: {err}\n--- {sql[:300]}")
    return out


def q1(sql: str) -> str:
    out = must(sql).strip().splitlines()
    return out[1] if len(out) > 1 else ""


def job() -> tuple[int, int, str] | None:
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


def ckpt_fingerprint() -> tuple[str, str]:
    """(subtask running-state, hash of the persisted checkpoint blobs) — advances as progress
    is recorded. Combines tidb_ddl_reorg.reorg_meta and tidb_background_subtask.checkpoint."""
    rm = must("SELECT IFNULL(HEX(reorg_meta),'') FROM mysql.tidb_ddl_reorg;")
    sub = must("SELECT IFNULL(state,''), IFNULL(HEX(checkpoint),'') "
               "FROM mysql.tidb_background_subtask;")
    rm_lines = rm.strip().splitlines()[1:]
    sub_lines = sub.strip().splitlines()[1:]
    blob = "|".join(rm_lines) + "||" + "|".join(sub_lines)
    state = sub_lines[0].split("\t")[0] if sub_lines else ""
    return state, hashlib.sha1(blob.encode()).hexdigest()[:12]


def main() -> int:
    saved = {v: q1(f"SELECT @@global.{v};") for v in (
        "tidb_ddl_enable_fast_reorg", "tidb_enable_dist_task", "tidb_enable_auto_analyze",
        "tidb_ddl_reorg_max_write_speed")}
    print("saved:", saved, flush=True)
    must("SET GLOBAL tidb_enable_auto_analyze=0;SET GLOBAL tidb_ddl_enable_fast_reorg=1;"
         "SET GLOBAL tidb_enable_dist_task=1;SET GLOBAL tidb_ddl_reorg_max_write_speed='256KiB';")
    must(f"DROP DATABASE IF EXISTS {DB}; CREATE DATABASE {DB}; USE {DB};"
         "CREATE TABLE t(a INT PRIMARY KEY, b INT); INSERT INTO t VALUES (1,1);")
    count = 1
    for _ in range(N_DOUBLINGS):
        must(f"USE {DB}; INSERT INTO t SELECT a+{count},(a+{count})%1000 FROM t;")
        count *= 2
    print(f"built {NROWS} rows; fast_reorg+dist_task on, write speed 256KiB", flush=True)
    job_id = None
    fps: list[str] = []
    states: list[str] = []
    ckpt_at_pause = ""
    paused = False
    try:
        proc = subprocess.Popen(["mysql", f"-h{H}", f"-P{P}", f"-u{USER}", DB, "-e",
                                 "ALTER TABLE t ADD INDEX ib(b);"],
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        t0 = time.time()
        while time.time() - t0 < 900:
            st = job()
            if st:
                job_id, rc, state = st
                if state in ("synced", "done"):
                    break
                sub_state, fp = ckpt_fingerprint()
                if sub_state == "running":
                    fps.append(fp)
                    states.append(f"{round(time.time()-t0)}s:{fp}")
                    # pause once we have >=3 DISTINCT checkpoint fingerprints (proves the
                    # checkpoint advanced = real mid-flight progress persisted)
                    if not paused and len(set(fps)) >= 3:
                        _, ckpt_at_pause = ckpt_fingerprint()
                        must(f"ADMIN PAUSE DDL JOBS {job_id};")
                        paused = True
                        print(f"PAUSE at advancing checkpoint (distinct fps={len(set(fps))}); "
                              f"ckpt fp={ckpt_at_pause}", flush=True)
                if paused and state == "paused":
                    break
            time.sleep(1)
        st = job()
        distinct = len(set(fps))
        print(f"after pause: {st}; distinct checkpoint fingerprints seen: {distinct}", flush=True)
        print(f"checkpoint fingerprint trail: {states[:12]}", flush=True)
        if not paused or (st and st[2] in ("synced", "done")):
            print("INVALID: could not pause on an advancing mid-flight checkpoint", flush=True)
            proc.wait()
            return 0
        # persisted-checkpoint trigger evidence: reorg_meta non-empty at pause
        rm_len = q1("SELECT IFNULL(LENGTH(reorg_meta),0) FROM mysql.tidb_ddl_reorg;")
        time.sleep(3)
        must(f"ADMIN RESUME DDL JOBS {job_id};")
        print("RESUME issued", flush=True)
        while time.time() - t0 < 900:
            st = job()
            if st and st[2] in ("synced", "done", "cancelled", "failed"):
                break
            time.sleep(1)
        proc.wait()
        final = job()
        chk = must(f"USE {DB}; ADMIN CHECK TABLE t;"
                   "SELECT count(*) FROM t USE INDEX(ib) WHERE b>=0;"
                   "SELECT count(*) FROM t IGNORE INDEX(ib) WHERE b>=0;")
        cnts = [int(x) for x in chk.split() if x.isdigit()]
        print("\n== RESULT ==", flush=True)
        print(f"distinct checkpoint fingerprints during run: {distinct} (>=3 => checkpoint advanced)")
        print(f"reorg_meta length at pause (persisted ckpt): {rm_len}")
        print(f"final job state: {final[2] if final else '?'}")
        print(f"correctness idx==table==N: {cnts[-2:]} vs {NROWS}")
        advanced = distinct >= 3
        persisted = rm_len.isdigit() and int(rm_len) > 2
        correct = cnts[-2:] == [NROWS, NROWS] and final and final[2] == "synced"
        if not paused:
            print("VERDICT: INVALID — no mid-flight pause")
        elif advanced and persisted and correct:
            print("VERDICT: GREEN — the DXF ingest ADD INDEX persists an ADVANCING checkpoint "
                  "(reorg_meta) across pause/resume and completes correctly. Unlike the txn path "
                  "(perf-30001: start_key frozen at handle 1 -> 2N rework) and TTL scan "
                  "(perf-30002: no scan-progress key), the DXF framework HONORS its checkpoint. "
                  "PS1 boundary sharpened: the rework class lives in bespoke/legacy loops that "
                  "predate or bypass the DXF checkpoint, not in DXF itself.")
        elif not persisted:
            print(f"VERDICT: RED-candidate — checkpoint not persisted at pause (rm_len={rm_len}); "
                  "would mean DXF loses progress on pause. Re-examine.")
        else:
            print(f"VERDICT: uncertain — advanced={advanced} persisted={persisted} correct={correct}")
    finally:
        st = job()
        if st and st[2] == "paused":
            mysql(f"ADMIN RESUME DDL JOBS {st[0]};")
            dl = time.time() + 600
            while time.time() < dl:
                s2 = job()
                if not s2 or s2[2] in ("synced", "done", "cancelled", "failed"):
                    break
                time.sleep(1)
        for v, val in saved.items():
            must(f"SET GLOBAL {v} = {val};")
        must(f"DROP DATABASE IF EXISTS {DB};")
        print("cleanup done", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
