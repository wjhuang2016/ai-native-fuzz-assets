#!/usr/bin/env python3
"""PF-1 mining matrix: prepared plan cache x dynamic-prune partitioned table.

Audit card (perf sibling loop, methodology v2):
  P_check:   plan-cache reuse path checks the cached plan is usable for the new parameter
             (cacheability guards at cache time; runtime partition re-pruning at reuse time)
  Q_claim:   a cached plan executed with parameter p2 (a) re-prunes partitions for p2 at
             runtime, (b) returns exactly the rows p2 selects, and (c) does < K x the work of
             a fresh optimization for p2
  D_dims:    parameter drift direction (narrow->wide / wide->narrow / cross-partition point),
             partition boundaries, dynamic prune mode, plan shape baked into cache
             (full scan vs IndexLookup)
  F_effect:  executor trusts the cached physical plan + its pruning state for every later param
  O_oracle:  PO3 cache_drift_counter_differential (TRUSTED 2026-07-03) + free correctness
             tripwire: expected row counts are known by construction; a wrong count is a
             correctness red cell routed to the main loop
  R_redflag: p2 in a partition p1 never touched; p2 wider than the pruned set cached for p1
  S_selector (if hit): cached-plan state x runtime re-derivation sibling path (the perf twin of
             "recorded vs reconstructed metadata")

Matrix (each cell asserts counters AND correctness; normal table nt is the control):
  C0  cacheability trigger: same param twice        -> Plan_from_cache=1 else family INVALID
  C1  wide p1 -> narrow p2 (pt vs nt control)       -> re-prune claim, counter ratio
  C2  point drift across partitions p0 -> p3        -> correctness + pruned-set claim
  C3  narrow p1 -> wide p2                          -> stale-prune wrong-count tripwire

Copr-cache discipline (library lessons L2/L5/L6): unique live-range dummy per execution;
big-plan counters only from cold executions; distinct constants across cells.
"""

from __future__ import annotations

import re
import subprocess
import sys
import time

H, P, USER = "127.0.0.1", "14000", "root"
DB = "ai_perf_pf1"
N_DOUBLINGS = 17
NROWS = 1 << N_DOUBLINGS
K = 10

SESSION_PREAMBLE = (
    f"USE {DB};\n"
    "SET tidb_enable_collect_execution_info = 1;\n"
    "SET tidb_slow_log_threshold = 0;\n"
)

_dummy = [0]


def next_dummy() -> int:
    _dummy[0] += 1
    return _dummy[0]


def mysql(sql: str) -> tuple[int, str, str]:
    r = subprocess.run(
        ["mysql", f"-h{H}", f"-P{P}", f"-u{USER}", "--comments", "-e", sql],
        capture_output=True, text=True, timeout=600,
    )
    return r.returncode, r.stdout, r.stderr


def must(sql: str) -> str:
    rc, out, err = mysql(sql)
    if rc != 0:
        raise RuntimeError(f"SQL failed: {err}\n--- sql was:\n{sql[:500]}")
    return out


def now() -> str:
    return must("SELECT NOW(6);").strip().splitlines()[1]


def slow_rows(table: str, since: str) -> list[dict]:
    out = must(
        "SELECT time, query, process_keys, plan_from_cache, plan_digest "
        "FROM information_schema.slow_query "
        f"WHERE time > '{since}' AND query LIKE '%{table} %' "
        "AND query NOT LIKE '%slow_query%' AND query NOT LIKE 'PREPARE%' "
        "ORDER BY time;"
    )
    rows = []
    lines = out.strip().splitlines()
    for line in lines[1:] if len(lines) > 1 else []:
        f = line.split("\t")
        if len(f) < 5:
            continue
        rows.append(dict(
            time=f[0], query=f[1],
            keys=int(f[2]) if f[2].isdigit() else 0,
            from_cache=f[3] == "1", digest=f[4],
        ))
    return rows


def arg_of(row: dict) -> str:
    m = re.search(r"\[arguments: ([^\]]+)\]", row["query"])
    return m.group(1) if m else ""


CELLS: list[tuple[str, str, str]] = []


def record(cell: str, cls: str, detail: str) -> None:
    CELLS.append((cell, cls, detail))
    print(f"[{cls}] {cell}: {detail}")


def build() -> None:
    must(f"DROP DATABASE IF EXISTS {DB}; CREATE DATABASE {DB};")
    must(
        f"USE {DB};"
        "CREATE TABLE pt(a INT, b INT, KEY ia(a)) "
        "PARTITION BY RANGE (a) ("
        " PARTITION p0 VALUES LESS THAN (32768),"
        " PARTITION p1 VALUES LESS THAN (65536),"
        " PARTITION p2 VALUES LESS THAN (98304),"
        " PARTITION p3 VALUES LESS THAN (MAXVALUE));"
        "CREATE TABLE nt(a INT, b INT, KEY ia(a));"
        "INSERT INTO nt VALUES (1, 1);"
    )
    count = 1
    for _ in range(N_DOUBLINGS):
        must(f"USE {DB}; INSERT INTO nt SELECT a + {count}, b FROM nt;")
        count *= 2
    must(f"USE {DB}; INSERT INTO pt SELECT * FROM nt; ANALYZE TABLE pt; ANALYZE TABLE nt;")


def drift_cell(cell: str, table: str, p1: int, p2: int, expect_cnt2: int) -> dict | None:
    """PO3 form on `table`: cache with p1, execute p2 cached (cold) then fresh (cold).
    Returns dict(ka, kb, ratio, digests) or None if INVALID."""
    t0 = now()
    d = [next_dummy() for _ in range(4)]
    out = must(
        SESSION_PREAMBLE
        + f"PREPARE st FROM 'SELECT count(b) FROM {table} WHERE a <= ? AND a > ?';\n"
        + f"SET @p={p1}, @d=-{d[0]}; EXECUTE st USING @p, @d;\n"
        + f"SET @p={p2}, @d=-{d[1]}; EXECUTE st USING @p, @d;\n"
        + "SET tidb_enable_prepared_plan_cache = 0;\n"
        + f"PREPARE stb FROM 'SELECT count(b) FROM {table} WHERE a <= ? AND a > ?';\n"
        + f"SET @p={p2}, @d=-{d[2]}; EXECUTE stb USING @p, @d;\n"
    )
    counts = [int(x) for x in out.split() if x.isdigit()]
    time.sleep(0.5)
    rows = [r for r in slow_rows(table, t0) if arg_of(r).startswith(f"({p2},")]
    if len(rows) < 2:
        record(cell, "invalid", f"expected 2 executions of p2, saw {len(rows)}")
        return None
    arm_a, arm_b = rows[0], rows[1]
    # correctness tripwire: both arms and the p1 run returned counts
    bad = [c for c in counts[1:] if c != expect_cnt2]
    if bad:
        record(cell, "RED(correctness)",
               f"wrong row count {bad} (expected {expect_cnt2}) — route to correctness loop")
        return None
    if not arm_a["from_cache"]:
        record(cell, "invalid(guard)",
               "plan cache declined reuse — cacheability-guard green for this shape")
        return None
    if arm_a["keys"] == 0 and arm_b["keys"] == 0:
        record(cell, "invalid", "both arms 0 keys — surface dead")
        return None
    ratio = arm_a["keys"] / max(arm_b["keys"], 1)
    return dict(ka=arm_a["keys"], kb=arm_b["keys"], ratio=ratio,
                same_digest=arm_a["digest"] == arm_b["digest"])


def main() -> int:
    print("== build ==")
    auto = must("SELECT @@global.tidb_enable_auto_analyze;").strip().splitlines()[1]
    must("SET GLOBAL tidb_enable_auto_analyze = 0;")
    build()
    print(f"pt (4 range partitions) and nt built, {NROWS} rows each")
    try:
        # C0 cacheability trigger
        t0 = now()
        d1, d2 = next_dummy(), next_dummy()
        must(SESSION_PREAMBLE
             + "PREPARE st FROM 'SELECT count(b) FROM pt WHERE a <= ? AND a > ?';\n"
             + f"SET @p=500, @d=-{d1}; EXECUTE st USING @p, @d;\n"
             + f"SET @p=500, @d=-{d2}; EXECUTE st USING @p, @d;\n")
        time.sleep(0.5)
        rows = slow_rows("pt", t0)
        if len(rows) >= 2 and rows[1]["from_cache"]:
            record("C0 cacheability", "green(triggered)",
                   "partitioned prepared plan IS cached and reused (dynamic prune mode)")
        else:
            record("C0 cacheability", "invalid(guard)",
                   f"plan not reused (rows={len(rows)}, "
                   f"from_cache={[r['from_cache'] for r in rows]}) — family gated")
            return 1

        # C1 wide -> narrow on pt, with nt control
        pt_res = drift_cell("C1a pt wide->narrow (131072 -> 1000)", "pt", NROWS, 1000, 1000)
        nt_res = drift_cell("C1b nt control  (131072 -> 1001)", "nt", NROWS, 1001, 1001)
        if pt_res and nt_res:
            detail = (f"pt: cached={pt_res['ka']} fresh={pt_res['kb']} ratio={pt_res['ratio']:.0f} | "
                      f"nt: cached={nt_res['ka']} fresh={nt_res['kb']} ratio={nt_res['ratio']:.0f}")
            if pt_res["ratio"] >= K and pt_res["ratio"] > 4 * max(nt_res["ratio"], 1):
                record("C1 verdict", "RED(partition-specific)",
                       detail + " — pt amplification exceeds generic drift: re-prune suspect")
            elif pt_res["ratio"] >= K:
                record("C1 verdict", "red(generic-drift)",
                       detail + " — same class as normal-table drift (PO3-S shape); "
                       "info(cost-model-tradeoff) unless a guard claims otherwise")
            else:
                record("C1 verdict", "green(triggered)", detail)

        # C2 point drift across partitions p0 -> p3
        t0 = now()
        d = [next_dummy() for _ in range(4)]
        out = must(
            SESSION_PREAMBLE
            + "PREPARE sp FROM 'SELECT count(b) FROM pt WHERE a = ? AND b IN (1, ?)';\n"
            + f"SET @p=2000, @d=-{d[0]}; EXECUTE sp USING @p, @d;\n"
            + f"SET @p=100001, @d=-{d[1]}; EXECUTE sp USING @p, @d;\n"
            + "SET tidb_enable_prepared_plan_cache = 0;\n"
            + "PREPARE spb FROM 'SELECT count(b) FROM pt WHERE a = ? AND b IN (1, ?)';\n"
            + f"SET @p=100001, @d=-{d[2]}; EXECUTE spb USING @p, @d;\n"
        )
        counts = [int(x) for x in out.split() if x.isdigit()]
        time.sleep(0.5)
        rows = [r for r in slow_rows("pt", t0) if arg_of(r).startswith("(100001,")]
        if counts[1:] != [1, 1]:
            record("C2 point drift p0->p3", "RED(correctness)",
                   f"counts={counts} expected [.,1,1] — cached plan lost the row")
        elif len(rows) >= 2 and not rows[0]["from_cache"]:
            record("C2 point drift p0->p3", "invalid(guard)", "point plan not reused")
        elif len(rows) >= 2:
            record("C2 point drift p0->p3", "green(triggered)",
                   f"correct count via cached plan; cached keys={rows[0]['keys']} "
                   f"fresh keys={rows[1]['keys']}")
        else:
            record("C2 point drift p0->p3", "invalid", f"rows={len(rows)}")

        # C3 narrow -> wide on pt (stale-prune tripwire)
        res = drift_cell("C3 pt narrow->wide (3000 -> 131072)", "pt", 3000, NROWS, NROWS)
        if res:
            cls = "RED" if res["ratio"] >= K else "green(triggered)"
            record("C3 verdict", cls,
                   f"cached={res['ka']} fresh={res['kb']} ratio={res['ratio']:.1f} "
                   f"same_digest={res['same_digest']} (correct count => no stale prune)")
    finally:
        must(f"DROP DATABASE IF EXISTS {DB};")
        must(f"SET GLOBAL tidb_enable_auto_analyze = {auto};")
        print("cleanup done")

    print("== matrix summary ==")
    for cell, cls, _ in CELLS:
        print(f"  {cell}: {cls}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
