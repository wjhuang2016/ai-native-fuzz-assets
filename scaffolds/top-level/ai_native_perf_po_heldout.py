#!/usr/bin/env python3
"""Held-out validation for performance oracles PO1 / PO3 (ai-native-perf-oracle-library.md).

PO1 forced_plan_counter_differential (choice claim)
  sensitivity: a binding-injected bad index choice must FIRE (ratio >= K)
  specificity: binding dropped, fresh stats -> SILENT

PO3 cache_drift_counter_differential (reuse claim)
  sensitivity: cached wide-scan plan reused for selective param must FIRE
  specificity: adjacent param (no real drift) -> SILENT or green(triggered)

Counters: per-execution process_keys / plan_from_cache / plan_from_binding / plan_digest
from information_schema.slow_query (session tidb_slow_log_threshold=0).

Harness lessons already embedded (each was a real vacuous-pass found by execution):
  L1: tidb_enable_collect_execution_info was OFF cluster-wide -> every counter read 0 and the
      first harness version certified SILENT on dead instruments. Fix: set it per session +
      a surface probe that must see full-scan keys before any verdict counts.
  L2: coprocessor cache serves repeated identical cop requests with proc_keys=0 -> the
      "repeat and take min" clause picked exactly the polluted run. Fix: every execution
      carries a unique no-op predicate (AND a > -k, all rows satisfy) so no two cop requests
      are ever identical; plus zero-vs-nonzero repeat spread => INVALID, not min().
  L3: TiDB bindings OVERRIDE in-statement hints -> with a session binding active, every
      "alternative" arm silently ran the bound plan (all digests equal). Fix: alternatives
      run in a separate binding-free session.
  L4: hint-text fragment matching collided ("ignore_index(t ia)" contains "(t ia)").
      Fix: unambiguous fragments.
Cleanup: drops the database and restores tidb_enable_auto_analyze at the end.
"""

from __future__ import annotations

import re
import subprocess
import sys
import time

H, P, USER = "127.0.0.1", "14000", "root"
DB = "ai_perf_po_heldout"
N_DOUBLINGS = 17  # 2^17 = 131072 rows
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


def mysql(sql: str, db: str | None = None) -> tuple[int, str, str]:
    cmd = ["mysql", f"-h{H}", f"-P{P}", f"-u{USER}", "--comments", "-e", sql]
    if db:
        cmd.insert(-2, db)
    r = subprocess.run(cmd, capture_output=True, text=True, timeout=600)
    return r.returncode, r.stdout, r.stderr


def must(sql: str, db: str | None = None) -> str:
    rc, out, err = mysql(sql, db)
    if rc != 0:
        raise RuntimeError(f"SQL failed: {err}\n--- sql was:\n{sql[:500]}")
    return out


def build_table(name: str) -> None:
    must(f"CREATE TABLE {name}(a INT, b INT, KEY ia(a), KEY ib(b));", DB)
    must(f"INSERT INTO {name} VALUES (1, 1);", DB)
    count = 1
    for _ in range(N_DOUBLINGS):
        must(f"INSERT INTO {name} SELECT a + {count}, b FROM {name};", DB)
        count *= 2
    must(f"ANALYZE TABLE {name};", DB)


def slow_rows(table: str, since: str) -> list[dict]:
    out = must(
        "SELECT time, query, process_keys, plan_from_cache, plan_from_binding, plan_digest "
        "FROM information_schema.slow_query "
        f"WHERE time > '{since}' AND query LIKE '%{table}%' "
        "AND query NOT LIKE '%slow_query%' AND query NOT LIKE 'PREPARE%' "
        "AND query NOT LIKE 'ANALYZE%' AND query NOT LIKE 'INSERT%' "
        "AND query NOT LIKE '%BINDING%' AND query NOT LIKE 'EXPLAIN%' "
        "ORDER BY time;"
    )
    rows = []
    lines = out.strip().splitlines()
    for line in lines[1:] if len(lines) > 1 else []:
        f = line.split("\t")
        if len(f) < 6:
            continue
        rows.append(
            dict(
                time=f[0],
                query=f[1],
                keys=int(f[2]) if f[2].isdigit() else 0,
                from_cache=f[3] == "1",
                from_binding=f[4] == "1",
                digest=f[5],
            )
        )
    return rows


def now() -> str:
    return must("SELECT NOW(6);").strip().splitlines()[1]


def arg_of(row: dict) -> str:
    m = re.search(r"\[arguments: ([^\]]+)\]", row["query"])
    return m.group(1) if m else ""


def arm_keys(arm: list[dict]) -> tuple[int, str]:
    """Repeatability-guarded counter for one arm: (keys, "") or (-1, reason)."""
    ks = [r["keys"] for r in arm]
    hi, lo = max(ks), min(ks)
    if hi > 0 and lo == 0:
        return -1, f"counter spread {lo}..{hi}: one run polluted (copr cache?)"
    if lo > 0 and hi / lo > 2:
        return -1, f"counters unstable: {lo} vs {hi}"
    return lo, ""


VERDICTS: list[tuple[str, str, str]] = []


def record(case: str, verdict: str, detail: str) -> None:
    VERDICTS.append((case, verdict, detail))
    print(f"[{verdict}] {case}: {detail}")


def surface_probe(table: str) -> bool:
    """Instrument health: the counter surface must prove itself before any verdict counts."""
    t0 = now()
    must(SESSION_PREAMBLE + f"SELECT count(b) FROM {table} WHERE a <= {NROWS} AND a > -{next_dummy()};")
    time.sleep(0.5)
    keys = max((r["keys"] for r in slow_rows(table, t0)), default=0)
    if keys < NROWS:
        record(f"surface-probe {table}", "INVALID",
               f"counter surface dead: full scan reported {keys} keys (need >= {NROWS})")
        return False
    print(f"surface probe {table}: alive ({keys} keys on full scan)")
    return True


# ---------------------------------------------------------------- PO3 cases
def po3_case(case: str, table: str, p1: int, p2: int, expect: str) -> None:
    t0 = now()
    d = [next_dummy() for _ in range(5)]
    must(
        SESSION_PREAMBLE
        + f"PREPARE st FROM 'SELECT count(b) FROM {table} WHERE a <= ? AND a > ?';\n"
        + f"SET @p={p1}, @d=-{d[0]}; EXECUTE st USING @p, @d;\n"
        + f"SET @p={p2}, @d=-{d[1]}; EXECUTE st USING @p, @d;\n"
        + f"SET @p={p2}, @d=-{d[2]}; EXECUTE st USING @p, @d;\n"
        + "SET tidb_enable_prepared_plan_cache = 0;\n"
        + f"PREPARE stb FROM 'SELECT count(b) FROM {table} WHERE a <= ? AND a > ?';\n"
        + f"SET @p={p2}, @d=-{d[3]}; EXECUTE stb USING @p, @d;\n"
        + f"SET @p={p2}, @d=-{d[4]}; EXECUTE stb USING @p, @d;\n"
    )
    time.sleep(0.5)
    rows = [r for r in slow_rows(table, t0) if arg_of(r).startswith(f"({p2},")]
    if len(rows) < 4:
        record(case, "INVALID", f"expected 4 executions of p2, saw {len(rows)}")
        return
    arm_a, arm_b = rows[:2], rows[2:4]
    if not arm_a[1]["from_cache"]:
        record(case, "INVALID",
               "plan cache declined reuse (Plan_from_cache=0) — cacheability-guard "
               "green-calibration, drift claim untested")
        return
    if any(r["from_cache"] for r in arm_b):
        record(case, "INVALID", "arm B unexpectedly served from cache")
        return
    ka, why_a = arm_keys(arm_a)
    kb, why_b = arm_keys(arm_b)
    if ka < 0 or kb < 0:
        record(case, "INVALID", f"armA: {why_a or 'ok'}; armB: {why_b or 'ok'}")
        return
    if arm_a[1]["digest"] == arm_b[1]["digest"]:
        record(case,
               "green(triggered)" if expect == "SILENT" else "SILENT-but-same-plan",
               f"fresh plan == cached plan (digest equal); keysA={ka} keysB={kb}")
        return
    ratio = ka / max(kb, 1)
    record(case, "FIRE" if ratio >= K else "SILENT",
           f"keys cached={ka} fresh={kb} ratio={ratio:.1f} (K={K}); digests differ; expected {expect}")


# ---------------------------------------------------------------- PO1 cases
# The unique constant -{d} rides inside the IN list so it reaches the cop request as an
# index range / selection constant. A trailing "AND a > -d" gets FOLDED AWAY by constant
# propagation under "a = 12345" (lesson L5, found by execution: identical cop requests kept
# hitting the coprocessor cache). Small 1-2 key requests sit below copr-cache admission
# (admission-min-process-ms) and need no defeat.
PO1_QUERY = "SELECT count(*) FROM {t} WHERE a = 12345 AND b IN (1, -{d})"
PO1_ALTS = {
    "use_ia": "SELECT /*+ USE_INDEX({t} ia) */ count(*) FROM {t} "
              "WHERE a = 12345 AND b IN (1, -{d})",
    "use_ib": "SELECT /*+ USE_INDEX({t} ib) */ count(*) FROM {t} "
              "WHERE a = 12345 AND b IN (1, -{d})",
    "fullscan": "SELECT /*+ IGNORE_INDEX({t} ia), IGNORE_INDEX({t} ib) */ count(*) "
                "FROM {t} WHERE a = 12345 AND b IN (1, -{d})",
}
ALT_FRAGS = {"use_ia": "use_index({t} ia)", "use_ib": "use_index({t} ib)",
             "fullscan": "ignore_index"}


def po1_case(case: str, table: str, binding: bool, expect: str) -> None:
    t0 = now()
    stmts = [SESSION_PREAMBLE]
    if binding:
        stmts.append(
            f"CREATE SESSION BINDING FOR {PO1_QUERY.format(t=table, d=1)} "
            f"USING {PO1_ALTS['use_ib'].format(t=table, d=1)};"
        )
    # single COLD run per arm: IndexLookup table-side cop requests cannot be uniquified by
    # query text (same handles => same requests), so only a first-ever execution of a plan on
    # a table is guaranteed copr-cache-clean (lesson L6). Each case gets its own fresh table;
    # the arm duplicating the chosen plan is dropped by the same-plan exclusion clause anyway.
    stmts += [PO1_QUERY.format(t=table, d=next_dummy()) + ";"]
    must("\n".join(stmts))
    # alternatives: fresh session, no binding (bindings override hints — lesson L3)
    alt_stmts = [SESSION_PREAMBLE]
    for alt in PO1_ALTS.values():
        alt_stmts += [alt.format(t=table, d=next_dummy()) + ";"]
    must("\n".join(alt_stmts))
    time.sleep(0.5)
    rows = slow_rows(table, t0)
    plain = [r for r in rows if "+" not in r["query"]]
    if len(plain) < 1:
        record(case, "INVALID", f"chosen arm rows missing: {len(plain)}")
        return
    if binding and not plain[-1]["from_binding"]:
        record(case, "INVALID", "binding injection did not attach (Plan_from_binding=0)")
        return
    k_chosen, why = arm_keys(plain)
    if k_chosen < 0:
        record(case, "INVALID", f"chosen arm: {why}")
        return
    chosen_digest = plain[-1]["digest"]
    best_name, best_keys = None, None
    excluded = []
    for name in PO1_ALTS:
        frag = ALT_FRAGS[name].format(t=table)
        arm = [r for r in rows if frag in r["query"].lower()]
        if len(arm) < 1:
            excluded.append(f"{name}(missing)")
            continue
        if arm[-1]["digest"] == chosen_digest:
            excluded.append(f"{name}(same-plan)")
            continue
        k, why = arm_keys(arm)
        if k < 0:
            excluded.append(f"{name}({why})")
            continue
        if best_keys is None or k < best_keys:
            best_name, best_keys = name, k
    if best_keys is None:
        record(case, "INVALID", f"no valid alternative arm; excluded={excluded}")
        return
    ratio = k_chosen / max(best_keys, 1)
    record(case, "FIRE" if ratio >= K else "SILENT",
           f"chosen keys={k_chosen} best_alt={best_name}({best_keys}) ratio={ratio:.1f} "
           f"(K={K}); excluded={excluded}; expected {expect}")


def main() -> int:
    print("== setup ==")
    auto = must("SELECT @@global.tidb_enable_auto_analyze;").strip().splitlines()[1]
    must("SET GLOBAL tidb_enable_auto_analyze = 0;")
    must(f"DROP DATABASE IF EXISTS {DB}; CREATE DATABASE {DB};")
    try:
        for t in ("t3", "t1s", "t1c"):
            build_table(t)
        print(f"tables built: {NROWS} rows each, stats analyzed, auto-analyze off")

        if not (surface_probe("t3") and surface_probe("t1s")):
            print("ABORT: counter surface unusable; no verdict below would be evidence")
            return 1

        print("== PO3 held-out ==")
        po3_case(f"PO3-S sensitivity (wide p1={NROWS} -> selective p2=1)", "t3", NROWS, 1, "FIRE")
        po3_case("PO3-C specificity (adjacent wide p2)", "t3", NROWS, NROWS - 72, "SILENT")

        print("== PO1 held-out ==")
        po1_case("PO1-S sensitivity (binding-injected bad index ib)", "t1s", True, "FIRE")
        po1_case("PO1-C specificity (native optimizer choice)", "t1c", False, "SILENT")
    finally:
        must(f"DROP DATABASE IF EXISTS {DB};")
        must(f"SET GLOBAL tidb_enable_auto_analyze = {auto};")
        print("cleanup done: database dropped, auto_analyze restored to", auto)

    print("== summary ==")
    ok = True
    expects = {"PO3-S": ("FIRE",), "PO3-C": ("SILENT", "green(triggered)"),
               "PO1-S": ("FIRE",), "PO1-C": ("SILENT",)}
    for case, verdict, _ in VERDICTS:
        want = expects.get(case.split()[0], ())
        hit = verdict in want
        print(f"  {case}: {verdict} {'OK' if hit else '** UNEXPECTED **'}")
        ok = ok and hit
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
