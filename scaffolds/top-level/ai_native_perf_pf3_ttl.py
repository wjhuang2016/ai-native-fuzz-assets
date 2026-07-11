#!/usr/bin/env python3
"""PF-3 mining: TTL background job — disable quiescence + accounting conservation.

Audit card (perf sibling loop, nominated by selector PS1):
  P_check:   tidb_ttl_job_enable=0 is checked by the TTL job manager / workers
  Q_claim:   (a) disabling stops a RUNNING job's scan+delete within ~one batch;
             (b) job history accounting (expired_rows/deleted_rows) equals real work
  D_dims:    running-job vs scheduled-job disable; worker/batch/rate config;
             re-enable continuation (rework shape, observational)
  F_effect:  operators trust disable for resource relief (the perf-30001 twin) and
             history rows for observability
  O_oracle:  C1 categorical — after disable + grace (2 batches), COUNT(*) trajectory
             must freeze; trigger evidence: count strictly decreasing on >=2 samples
             AND a running row in mysql.tidb_ttl_task BEFORE the disable.
             C2 categorical — job history expired_rows == deleted_rows == N, errors 0.
  R_redflag: disable signal consumed by the scheduler only, not by running workers
Safety: finally-block restores every TTL global (a leftover rate_limit=1 or
job_enable=0 would cripple cluster TTL).
"""

from __future__ import annotations

import subprocess
import sys
import time

H, P, USER = "127.0.0.1", "14000", "root"
DB = "ai_perf_pf3"

TTL_VARS = ["tidb_ttl_job_enable", "tidb_ttl_delete_batch_size",
            "tidb_ttl_delete_rate_limit", "tidb_ttl_delete_worker_count",
            "tidb_ttl_scan_worker_count", "tidb_ttl_scan_batch_size"]


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


def scalar(sql: str) -> str:
    lines = must(sql).strip().splitlines()
    return lines[1] if len(lines) > 1 else ""


CELLS: list[tuple[str, str, str]] = []


def record(cell: str, cls: str, detail: str) -> None:
    CELLS.append((cell, cls, detail))
    print(f"[{cls}] {cell}: {detail}")


def build(table: str, n_doublings: int) -> int:
    must(f"USE {DB}; CREATE TABLE {table}(id INT PRIMARY KEY, ts TIMESTAMP) "
         f"TTL = `ts` + INTERVAL 1 HOUR;")
    must(f"USE {DB}; INSERT INTO {table} VALUES (1, NOW() - INTERVAL 1 DAY);")
    count = 1
    for _ in range(n_doublings):
        must(f"USE {DB}; INSERT INTO {table} SELECT id + {count}, ts FROM {table};")
        count *= 2
    return count


def tcount(table: str) -> int:
    return int(scalar(f"SELECT COUNT(*) FROM {DB}.{table};"))


def ttl_task_states() -> str:
    out = must("SELECT status, CO