#!/usr/bin/env python3
"""PF-3: TTL scan task has no intra-range progress checkpoint -> a resigned/re-locked task
re-scans its whole range from ScanRangeStart, re-seeking over the already-deleted prefix.

Audit card (perf sibling loop, PS1 selector):
  P_check:   on re-lock, lockScanTask reads tidb_ttl_task row incl. State (TotalRows/
             SuccessRows/PreviousOwner) and claims to continue the task
  Q_claim:   a re-taken TTL scan task continues from where it stopped (bounded rework)
  D_dims:    interruption cause (worker-stop resign / HB timeout), how much of the range was
             already deleted at interruption, scan/delete rate, range size (regions)
  F_effect:  the scan generator is rebuilt from t.ScanRangeStart every run; the in-memory
             progress key (lastResult) is never persisted to tidb_ttl_task.state
  O_oracle:  PO4-style work conservation on the SCAN side: sum(process_keys of scan SELECTs)
             should ~= N (each live row scanned once). A resign forces a re-seek over the
             deleted prefix, so total scan work jumps. Smoking gun = one post-resign scan
             SELECT whose delete_skipped_count ~= rows deleted before the resign (>> batch).
  R_redflag: sibling re-entry path reconstructs progress state it never persisted
             (perf twin of id30009 / perf-30001)

Interruption primitive (single node, no failover needed): SET GLOBAL
tidb_ttl_scan_worker_count=0 cancels the running scan worker -> ReasonWorkerStop -> resign;
setting it back to >0 re-locks and rescans.

Observation surface: information_schema.slow_query internal scan SELECTs, columns
process_keys / total_keys / rocksdb_delete_skipped_count. Trigger evidence: the resigned
task row shows state.prev_owner set.
"""

from __future__ import annotations

import json
import subprocess
import sys
import time

H, P, USER, STATUS = "127.0.0.1", "14000", "root", "18080"
DB = "ai_perf_ttl_rescan"
N_DOUBLINGS = 15
NROWS = 1 << N_DOUBLINGS


def mysql(sql: str) -> tuple[int, str, str]:
    r = subprocess.run(
        ["mysql", f"-h{H}", f"-P{P}", f"-u{USER}", "--comments", "-e", sql],
        capture_output=True, text=True, timeout=600,
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


def trigger() -> str:
    r = subprocess.run(["curl", "-s", "-XPOST",
                        f"http://{H}:{STATUS}/test/ttl/trigger/{DB}/t"],
                       capture_output=True, text=True, timeout=30)
    return r.stdout


def remaining() -> int:
    v = q1(f"SELECT COUNT(*) FROM {DB}.t;")
    return int(v) if v.isdigit() else -1


def task_row() -> dict:
    out = must("SELECT scan_id, status, IFNULL(state,'') , IFNULL(owner_id,'') "
               f"FROM mysql.tidb_ttl_task WHERE table_id = "
               f"(SELECT tidb_table_id FROM information_schema.tables "
               f"WHERE table_schema='{DB}' AND table_name='t');")
    lines = out.strip().splitlines()
    if len(lines) < 2:
        return {}
    f = lines[1].split("\t")
    return dict(scan_id=f[0], status=f[1], state=f[2] if len(f) > 2 else "",
                owner=f[3] if len(f) > 3 else "")


def max_scan_skip(since: str) -> tuple[int, int, int]:
    """(max delete_skipped, max total_keys, count) of TTL scan SELECTs since `since`."""
    out = must(
        "SELECT IFNULL(MAX(rocksdb_delete_skipped_count),0), IFNULL(MAX(total_keys),0), COUNT(*) "
        "FROM information_schema.slow_query "
        f"WHERE time > '{since}' AND is_internal=1 AND query LIKE 'SELECT%{DB}%' "
        "AND query LIKE '%_tidb_rowid%';"
    )
    f = out.strip().splitlines()[1].split("\t")
    return int(f[0]), int(f[1]), int(f[2])


def _scan_rowids(since: str) -> list[int]:
    """The `_tidb_rowid > N` cursor value of every TTL scan SELECT since `since`."""
    out = must(
        "SELECT REGEXP_SUBSTR(query, '_tidb_rowid` > [0-9]+') "
        "FROM information_schema.slow_query "
        f"WHERE time > '{since}' AND is_internal=1 AND query LIKE 'SELECT%{DB}%' "
        "AND query LIKE '%_tidb_rowid%';"
    )
    vals = []
    for line in out.strip().splitlines()[1:]:
        line = line.strip()
        if ">" in line:
            try:
                vals.append(int(line.split(">")[1]))
            except (ValueError, IndexError):
                pass
    return vals


def max_scan_rowid(since: str) -> int:
    vals = _scan_rowids(since)
    return max(vals) if vals else 0


def median_scan_total_keys(t0: str, t1: str) -> int:
    """Median total_keys of TTL scan SELECTs in [t0, t1) — a normal continuation batch."""
    out = must(
        "SELECT total_keys FROM information_schema.slow_query "
        f"WHERE time > '{t0}' AND time <= '{t1}' AND is_internal=1 "
        f"AND query LIKE 'SELECT%{DB}%' AND query LIKE '%_tidb_rowid%' "
        "AND total_keys > 0 ORDER BY total_keys;"
    )
    vals = [int(x) for x in out.strip().splitlines()[1:] if x.strip().isdigit()]
    return vals[len(vals) // 2] if vals else 0


def min_scan_rowid(since: str) -> int | None:
    vals = _scan_rowids(since)
    return min(vals) if vals else None


def top_scan_queries(since: str, n: int = 4) -> list[tuple[int, int, str]]:
    """Top-n scan SELECTs by delete_skipped since `since`: (skip, total_keys, rowid_predicate)."""
    out = must(
        "SELECT rocksdb_delete_skipped_count, total_keys, "
        "REGEXP_SUBSTR(query, '_tidb_rowid` > [0-9]+') "
        "FROM information_schema.slow_query "
        f"WHERE time > '{since}' AND is_internal=1 AND query LIKE 'SELECT%{DB}%' "
        "AND query LIKE '%_tidb_rowid%' "
        f"ORDER BY rocksdb_delete_skipped_count DESC LIMIT {n};"
    )
    rows = []
    for line in out.strip().splitlines()[1:]:
        f = line.split("\t")
        if len(f) >= 3:
            rows.append((int(f[0]), int(f[1]), f[2]))
    return rows


def now() -> str:
    return q1("SELECT NOW(6);")


def build() -> None:
    must("SET GLOBAL tidb_ttl_job_enable=OFF;")
    must(f"DROP DATABASE IF EXISTS {DB}; CREATE DATABASE {DB}; USE {DB};"
         "CREATE TABLE t(id INT PRIMARY KEY, ts DATETIME) "
         "TTL = ts + INTERVAL 1 HOUR TTL_ENABLE='OFF';"
         "INSERT INTO t VALUES (1, '2020-01-01 00:00:00');")
    count = 1
    for _ in range(N_DOUBLINGS):
        must(f"USE {DB}; INSERT INTO t SELECT id + {count}, ts FROM t;")
        count *= 2
    must(f"ALTER TABLE {DB}.t TTL_ENABLE='ON';")


def main() -> int:
    saved = {v: q1(f"SELECT @@global.{v};") for v in (
        "tidb_ttl_job_enable", "tidb_ttl_scan_worker_count", "tidb_ttl_delete_worker_count",
        "tidb_ttl_scan_batch_size", "tidb_ttl_delete_batch_size", "tidb_ttl_delete_rate_limit",
        "tidb_enable_auto_analyze")}
    print("saved globals:", saved)
    try:
        must("SET GLOBAL tidb_enable_auto_analyze=0;")
        build()
        print(f"table built: {NROWS} all-expired rows")
        # slow the deletes so the scan blocks mid-range (backpressure), 1 scan worker
        must("SET GLOBAL tidb_ttl_scan_worker_count=1;"
             "SET GLOBAL tidb_ttl_delete_worker_count=1;"
             "SET GLOBAL tidb_ttl_scan_batch_size=256;"
             "SET GLOBAL tidb_ttl_delete_batch_size=20;"
             "SET GLOBAL tidb_ttl_delete_rate_limit=5;")  # 5 batches/s * 20 = 100 rows/s
        must("SET GLOBAL tidb_ttl_job_enable=ON;")
        # wait for infoschema, then trigger
        for _ in range(30):
            if "not exists" not in trigger():
                break
            time.sleep(1)
        print("trigger response ok; watching deletion until ~30% deleted", flush=True)
        t_job = now()
        deleted_at_resign = -1
        deadline = time.time() + 300
        while time.time() < deadline:
            rem = remaining()
            done = NROWS - rem
            if done >= int(NROWS * 0.3):
                deleted_at_resign = done
                break
            time.sleep(0.5)
        if deleted_at_resign < 0:
            print(f"INVALID: never reached 40% deleted (remaining={remaining()})")
            return 1
        print(f"reached {deleted_at_resign} deleted (~{deleted_at_resign*100//NROWS}%)", flush=True)
        # cursor BEFORE interruption: the max _tidb_rowid the scan has advanced to
        cur_before = max_scan_rowid(t_job)
        print(f"scan cursor (max _tidb_rowid predicate) BEFORE interrupt: {cur_before}", flush=True)
        # INTERRUPT via heartbeat-timeout injection (simulates owner crash / HB timeout —
        # the real re-lock path). scan_worker_count has MinValue=1 so it can't cancel the
        # busy worker (lesson L7). Instead: set a fake dead owner + stale heartbeat.
        # checkInvalidTask (5s) sees owner!=me -> cancels the running scan; rescheduleTasks
        # then peeks (status=running AND owner_hb_time<now-2min) -> lockScanTask HB-timeout
        # branch -> NEW ScanQueryGenerator from ScanRangeStart -> rescan.
        t_interrupt = now()
        tid = q1(f"SELECT tidb_table_id FROM information_schema.tables "
                 f"WHERE table_schema='{DB}' AND table_name='t';")
        must("UPDATE mysql.tidb_ttl_task SET owner_id='fake-dead-owner-pf3', "
             "owner_hb_time = NOW() - INTERVAL 200 SECOND "
             f"WHERE table_id = {tid} AND status='running';")
        print("injected fake dead owner + stale heartbeat; waiting for re-lock + rescan",
              flush=True)
        # baseline: a normal continuation scan batch's total_keys (~ scan_batch_size)
        base_tk = median_scan_total_keys(t_job, t_interrupt)
        relocked = False
        restart_tk = 0  # max total_keys of a post-interrupt scan query
        deadline = time.time() + 120
        while time.time() < deadline:
            time.sleep(2)
            tr = task_row()
            owner = tr.get("owner", "")
            if owner and owner != "fake-dead-owner-pf3":
                relocked = True
            for skip, tk, pred in top_scan_queries(t_interrupt, 6):
                restart_tk = max(restart_tk, tk)
            if remaining() <= 0:
                break
            if relocked and restart_tk >= 10 * max(base_tk, 1):
                break
        final_rem = remaining()
        print("\n== RESULT ==", flush=True)
        print(f"deleted before interrupt:              {deleted_at_resign}")
        print(f"scan cursor BEFORE interrupt (max rowid): {cur_before}")
        print(f"normal continuation batch total_keys (baseline): {base_tk}")
        print(f"max post-interrupt scan total_keys:    {restart_tk}")
        print(f"re-lock trigger evidence (owner changed from fake): {relocked}")
        print(f"final remaining rows:                  {final_rem}")
        print("post-interrupt scan queries (skip, total_keys, rowid predicate):")
        top = top_scan_queries(t_interrupt, 6)
        for skip, tk, pred in top:
            print(f"    skip={skip:8d} total_keys={tk:8d}  {pred}")
        # RED signal: after the interrupt, the re-locked task issues a RANGE-START scan
        # (no `_tidb_rowid >` lower bound, shown as NULL) that re-seeks the whole deleted
        # prefix — its total_keys/skip is orders of magnitude above a normal batch.
        restart_query = next(((skip, tk, pred) for skip, tk, pred in top
                              if ("NULL" in pred or ">" not in pred) and tk >= 10 * max(base_tk, 1)),
                             None)
        if not relocked:
            print("VERDICT: INVALID — task was not re-locked (no owner change); "
                  "rescan path not exercised")
        elif restart_query is not None:
            print(f"VERDICT: RED — after re-lock the task issues a RANGE-START scan "
                  f"(no _tidb_rowid lower bound) that re-seeks the deleted prefix: "
                  f"total_keys={restart_query[1]} skip={restart_query[0]} vs normal batch "
                  f"{base_tk}. The re-locked TTL scan task restarts from ScanRangeStart — no "
                  f"intra-range progress checkpoint; rework ~= O(rows scanned before interrupt).")
        else:
            print(f"VERDICT: green/uncertain — no range-start re-seek query "
                  f"(baseline={base_tk}, max_after={restart_tk}); progress may be preserved")
    finally:
        must("SET GLOBAL tidb_ttl_job_enable=OFF;")
        time.sleep(2)
        # remove any injected/leftover task rows for our table, then restore + drop
        tid = q1(f"SELECT tidb_table_id FROM information_schema.tables "
                 f"WHERE table_schema='{DB}' AND table_name='t';")
        if tid.isdigit():
            must(f"DELETE FROM mysql.tidb_ttl_task WHERE table_id={tid};")
        for v, val in saved.items():
            must(f"SET GLOBAL {v} = {val};")
        must(f"DROP DATABASE IF EXISTS {DB};")
        print("cleanup done: globals restored, task rows cleared, db dropped")
    return 0


def max_scan_skip_window(t0: str, t1: str) -> tuple[int, int, int]:
    out = must(
        "SELECT IFNULL(MAX(rocksdb_delete_skipped_count),0), IFNULL(MAX(total_keys),0), COUNT(*) "
        "FROM information_schema.slow_query "
        f"WHERE time > '{t0}' AND time <= '{t1}' AND is_internal=1 "
        f"AND query LIKE 'SELECT%{DB}%' AND query LIKE '%_tidb_rowid%';"
    )
    f = out.strip().splitlines()[1].split("\t")
    return int(f[0]), int(f[1]), int(f[2])


if __name__ == "__main__":
    sys.exit(main())
