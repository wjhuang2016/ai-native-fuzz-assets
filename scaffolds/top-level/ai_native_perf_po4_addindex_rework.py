#!/usr/bin/env python3
"""PF-2 mining: ADD INDEX rework conservation across ADMIN PAUSE/RESUME (oracle PO4).

Audit card (perf sibling loop):
  P_check:   resume path reads the reorg checkpoint (mysql.tidb_ddl_reorg / job args) and
             claims to continue from where the backfill stopped
  Q_claim:   after pause->resume, (a) backfill progress is monotonic (no restart), and
             (b) total backfilled row accounting ~= table row count (no duplicated work)
  D_dims:    backfill mode (txn-merge vs ingest/fast-reorg), pause point (early/mid),
             checkpoint granularity, owner unchanged (single-node cluster)
  F_effect:  cleanup/progress accounting and user-visible DDL time trust the checkpoint
  O_oracle:  PO4 (two forms): monotonicity — first row_count observed after resume must be
             >= last row_count before pause (minus one batch); conservation — final
             row_count / N < 2
  R_redflag: resume sibling path reconstructs progress state (the perf twin of id30009's
             rollback-path metadata loss)
  Correctness tripwire: ADMIN CHECK TABLE + index vs table count after each cell.

Interruption primitive: ADMIN PAUSE/RESUME DDL JOBS (SQL-level, no failpoints needed).
"""

from __future__ import annotations

import subprocess
import sys
import time

H, P, USER = "127.0.0.1", "14000", "root"
DB = "ai_perf_pf2"
N_DOUBLINGS = 18
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
        raise RuntimeError(f"SQL failed: {err}\n--- sql was:\n{sql[:400]}")
    return out


CELLS: list[tuple[str, str, str]] = []


def record(cell: str, cls: str, detail: str) -> None:
    CELLS.append((cell, cls, detail))
    print(f"[{cls}] {cell}: {detail}")


def build_table(name: str) -> None:
    must(f"USE {DB}; CREATE TABLE {name}(a INT, b INT);")
    must(f"USE {DB}; INSERT INTO {name} VALUES (1, 1);")
    count = 1
    for _ in range(N_DOUBLINGS):
        must(f"USE {DB}; INSERT INTO {name} SELECT a + {count}, (a + {count}) % 1000 FROM {name};")
        count *= 2
    must(f"USE {DB}; ANALYZE TABLE {name};")


def job_status(table: str) -> tuple[int, int, str] | None:
    """(job_id, row_count, state) of the newest add-index job for `table`."""
    out = must("ADMIN SHOW DDL JOBS 15;")
    lines = out.strip().splitlines()
    if len(lines) < 2:
        return None
    hdr = lines[0].split("\t")
    idx = {name: i for i, name in enumerate(hdr)}
    for line in lines[1:]:
        f = line.split("\t")
        if len(f) < len(hdr):
            continue
        if f[idx["TABLE_NAME"]] == table and "add index" in f[idx["JOB_TYPE"]]:
            return int(f[idx["JOB_ID"]]), int(f[idx["ROW_COUNT"]]), f[idx["STATE"]]
    return None


def add_index_cell(cell: str, table: str, fast_reorg: int) -> None:
    must(f"SET GLOBAL tidb_ddl_enable_fast_reorg = {fast_reorg};")
    proc = subprocess.Popen(
        ["mysql", f"-h{H}", f"-P{P}", f"-u{USER}", DB, "-e",
         f"ALTER TABLE {table} ADD INDEX ib(b);"],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
    )
    # wait for mid-flight progress, then pause
    paused_at = -1
    job_id = None
    deadline = time.time() + 300
    while time.time() < deadline:
        st = job_status(table)
        if st:
            job_id, rc_now, state = st
            if state in ("synced", "done"):
                break
            if rc_now > NROWS // 20 and state == "running":
                must(f"ADMIN PAUSE DDL JOBS {job_id};")
                paused_at = rc_now
                break
        time.sleep(0.15)
    if paused_at < 0:
        record(cell, "invalid", "job finished before a mid-flight pause landed "
                                f"(state={st[2] if st else 'none'}) — no resume path exercised")
        proc.wait()
        return
    # confirm paused, snapshot progress
    for _ in range(100):
        st = job_status(table)
        if st and st[2] == "paused":
            break
        time.sleep(0.2)
    st = job_status(table)
    pause_rc = st[1] if st else paused_at
    time.sleep(1.0)
    must(f"ADMIN RESUME DDL JOBS {job_id};")
    # monotonicity watch: row_count right after resume must not fall below pause_rc
    min_after_resume, first_seen = None, None
    deadline = time.time() + 600
    final_rc, final_state = -1, ""
    while time.time() < deadline:
        st = job_status(table)
        if st:
            _, rc_now, state = st
            final_rc, final_state = rc_now, state
            if state == "running" and rc_now > 0:
                if first_seen is None:
                    first_seen = rc_now
                min_after_resume = rc_now if min_after_resume is None else min(min_after_resume, rc_now)
            if state in ("synced", "done"):
                break
            if state in ("cancelled", "rollback done", "failed"):
                record(cell, "invalid", f"job ended in state {state}")
                proc.wait()
                return
        time.sleep(0.15)
    proc.wait()
    # correctness tripwire
    chk = must(f"USE {DB}; ADMIN CHECK TABLE {table}; "
               f"SELECT /*+ USE_INDEX({table} ib) */ count(*) FROM {table} WHERE b >= 0; "
               f"SELECT /*+ IGNORE_INDEX({table} ib) */ count(*) FROM {table} WHERE b >= 0;")
    cnts = [int(x) for x in chk.split() if x.isdigit()]
    corr = "ok" if cnts[-2:] == [NROWS, NROWS] else f"COUNTS {cnts[-2:]} != {NROWS}"
    mono = "n/a(no running sample)" if first_seen is None else (
        f"pause_rc={pause_rc} first_after_resume={first_seen} min_after={min_after_resume}")
    detail = (f"mode={'ingest' if fast_reorg else 'txn'} paused@{pause_rc} "
              f"final_row_count={final_rc} N={NROWS} ratio={final_rc / NROWS:.2f} "
              f"state={final_state} | mono: {mono} | correctness: {corr}")
    if corr != "ok":
        record(cell, "RED(correctness)", detail)
    elif first_seen is not None and first_seen < pause_rc - 4096:
        record(cell, "RED(monotonicity)", detail + " — progress restarted after resume")
    elif final_rc >= 2 * NROWS:
        record(cell, "RED(conservation)", detail + " — accounting shows >=2x work")
    else:
        record(cell, "green(triggered)", detail)


def main() -> int:
    print("== setup ==")
    saved = {}
    for var in ("tidb_ddl_enable_fast_reorg", "tidb_ddl_reorg_worker_cnt",
                "tidb_ddl_reorg_batch_size", "tidb_enable_auto_analyze"):
        saved[var] = must(f"SELECT @@global.{var};").strip().splitlines()[1]
    must("SET GLOBAL tidb_enable_auto_analyze = 0;")
    must("SET GLOBAL tidb_ddl_reorg_worker_cnt = 1;")
    must("SET GLOBAL tidb_ddl_reorg_batch_size = 32;")
    must(f"DROP DATABASE IF EXISTS {DB}; CREATE DATABASE {DB};")
    try:
        build_table("t_txn")
        build_table("t_ing")
        print(f"tables built: {NROWS} rows each; reorg slowed (1 worker, batch 32)")
        add_index_cell("PF2-A txn-merge backfill pause/resume", "t_txn", 0)
        add_index_cell("PF2-B ingest (fast reorg) pause/resume", "t_ing", 1)
    finally:
        must(f"DROP DATABASE IF EXISTS {DB};")
        for var, val in saved.items():
            must(f"SET GLOBAL {var} = {val};")
        print("cleanup done: db dropped, globals restored")
    print("== matrix summary ==")
    for cell, cls, _ in CELLS:
        print(f"  {cell}: {cls}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
