#!/usr/bin/env python3
"""Execution-verify O2' (hardened rowset oracle) against O2's reproduced defects.

Compares the OLD O2 (COUNT USE vs COUNT IGNORE) with O2' on three scenarios, to show
O2' fixes the structural blind spot (CE-1) while keeping sensitivity to a real bug.

O2' form:
  1. TRIGGER EVIDENCE: USE INDEX arm's plan must actually scan the index; if not (hint
     not honored, e.g. multi-valued index), the cell is INVALID, not a pass.
  2. ORDERED ROW HASH under a total ORDER BY in ONE snapshot (not COUNT) so a
     cardinality-preserving divergence cannot hide and concurrency cannot false-fire.

Scenarios:
  A normal index, no bug        -> truth: clean.  O2_old=pass, O2'=GREEN         (both right)
  B multi-valued index, COUNT   -> truth: untestable here (hint ignored, index not scanned).
                                   O2_old=pass (VACUOUS false assurance), O2'=INVALID (honest)
  C partial index id30001       -> truth: real wrong-result. O2_old=fired, O2'=RED (both catch)
"""

from __future__ import annotations
import subprocess

H, P = "127.0.0.1", "14000"


def q(sql, db="test"):
    p = subprocess.run(["mysql", f"-h{H}", f"-P{P}", "-uroot", "-N", "--batch", "--raw",
                        "--connect-timeout=5", db, "-e", sql],
                       text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
    return p.returncode, p.stdout.strip(), p.stderr.strip()


def uses_index(tbl, idx, pred):
    """trigger evidence: does the USE INDEX arm actually scan the index?"""
    _, out, _ = q(f"EXPLAIN SELECT * FROM {tbl} USE INDEX({idx}) WHERE {pred}")
    return any(k in out for k in ("IndexRangeScan", "IndexFullScan", "IndexReader", "IndexLookUp"))


def o2_old(tbl, idx, pred):
    _, cu, _ = q(f"SELECT COUNT(*) FROM {tbl} USE INDEX({idx}) WHERE {pred}")
    _, ci, _ = q(f"SELECT COUNT(*) FROM {tbl} IGNORE INDEX({idx}) WHERE {pred}")
    return "fired" if cu != ci else "pass"


def o2_prime(tbl, idx, pred, order):
    if not uses_index(tbl, idx, pred):
        return "INVALID(no-plan-divergence)"
    # ordered row hash, both arms in ONE snapshot (plain txn; TiDB RR = single read TS).
    rc, out, err = q(
        f"START TRANSACTION;"
        f"SELECT 'U', GROUP_CONCAT(id ORDER BY {order}) FROM {tbl} USE INDEX({idx}) WHERE {pred};"
        f"SELECT 'I', GROUP_CONCAT(id ORDER BY {order}) FROM {tbl} IGNORE INDEX({idx}) WHERE {pred};"
        f"COMMIT;")
    # trigger-evidence at the oracle's own level: a differential that reports "equal"
    # proves nothing if the arms did not actually run. A failed/missing arm => INVALID.
    if rc != 0:
        return "INVALID(query-error)"
    rows = {line.split("\t")[0]: (line.split("\t")[1] if "\t" in line else "")
            for line in out.splitlines() if line and line[0] in "UI"}
    if "U" not in rows or "I" not in rows:
        return "INVALID(missing-arm)"
    return "RED" if rows["U"] != rows["I"] else "GREEN"


def main():
    _, ver, _ = q("SELECT VERSION()")
    print(f"FINGERPRINT {ver}\n")

    # A: normal index, no bug
    q("DROP TABLE IF EXISTS test.o2a")
    q("CREATE TABLE test.o2a(id INT PRIMARY KEY, b INT, KEY kb(b))")
    q("INSERT INTO test.o2a VALUES (1,50),(2,40),(3,30),(4,20),(5,10)")
    a_old = o2_old("test.o2a", "kb", "b>0")
    a_new = o2_prime("test.o2a", "kb", "b>0", "b,id")

    # B: multi-valued index, COUNT form (hint not honored)
    q("DROP TABLE IF EXISTS test.o2b")
    q("CREATE TABLE test.o2b(id INT, j JSON, KEY i((CAST(j->'$.a' AS UNSIGNED ARRAY))))")
    q("INSERT INTO test.o2b VALUES (1,'{\"a\":[1,2]}'),(2,'{\"a\":[3]}'),(3,'{\"a\":[2,4]}')")
    b_old = o2_old("test.o2b", "i", "1=1")
    b_new = o2_prime("test.o2b", "i", "1=1", "id")

    # C: partial index id30001 (real wrong-result)
    q("DROP TABLE IF EXISTS test.o2c")
    q("CREATE TABLE test.o2c(id INT PRIMARY KEY, a INT NULL, b INT, INDEX pi(b) WHERE a<3)")
    q("INSERT INTO test.o2c VALUES (1,1,1),(2,2,2),(3,3,3),(4,10,4),(5,NULL,5)")
    c_old = o2_old("test.o2c", "pi", "a>=0")
    c_new = o2_prime("test.o2c", "pi", "a>=0", "b,id")

    for t in ("o2a", "o2b", "o2c"):
        q(f"DROP TABLE IF EXISTS test.{t}")

    print(f"{'scenario':28s} {'truth':22s} {'O2_old(count)':16s} {'O2prime'}")
    print(f"{'A normal index, no bug':28s} {'clean':22s} {a_old:16s} {a_new}")
    print(f"{'B multi-valued idx COUNT':28s} {'untestable(no scan)':22s} {b_old:16s} {b_new}")
    print(f"{'C partial index id30001':28s} {'real wrong-result':22s} {c_old:16s} {c_new}")
    print()
    fixed = (b_old == "pass" and b_new.startswith("INVALID"))
    keeps = (c_old == "fired" and c_new == "RED")
    clean = (a_old == "pass" and a_new == "GREEN")
    print(f"CE-1 fixed (B: old vacuous-pass -> new honest INVALID): {fixed}")
    print(f"sensitivity kept (C: real bug still RED):               {keeps}")
    print(f"no false alarm (A: clean stays GREEN):                  {clean}")
    print(f"\nO2' verdict: {'CE-1 closed + sensitivity + specificity confirmed on these shapes' if (fixed and keeps and clean) else 'CHECK — unexpected'}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
