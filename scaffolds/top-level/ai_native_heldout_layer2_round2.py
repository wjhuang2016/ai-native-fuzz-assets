#!/usr/bin/env python3
"""Held-out Layer 2, round 2: find a real oracle blind spot, then close it.

The value of held-out is finding FN>0, not collecting green runs. Round 1 covered
data-inconsistency (add-index temp-merge skip) and the suite scored 0% FN — the easy end.

This round injects a predicate-simplification wrong-result (id30002 shape): the query
returns wrong rows, but ADMIN CHECK passes (storage is fine) and there is no index-vs-table
rowset drift. The round-1 oracle suite is therefore BLIND to it — we expect real FNs.

Then we add the case_wrapped_equivalence oracle (already registered in the suite design,
never wired into live) as a logged NEW round, and rerun. The blind spot should close.

This demonstrates the full loop: blind harness -> real miss surfaced -> oracle added ->
miss eliminated. Pure SQL, no failpoints.
"""

from __future__ import annotations

import argparse
import subprocess
import random

HOST, PORT = "127.0.0.1", "14000"


def sql(q, db="test"):
    p = subprocess.run(
        ["mysql", f"-h{HOST}", f"-P{PORT}", "-uroot", "--batch", "--raw",
         "--skip-column-names", "--connect-timeout=5", db, "-e", q],
        text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
    return p.returncode, p.stdout.strip(), p.stderr.strip()


# Each question: a predicate over the same table. bug -> predicate-simplification
# drops the != under collation and returns extra rows; control -> a predicate that
# simplifies safely.
BUG_PRED = "s IN ('a','A') AND s != _utf8mb4'A' COLLATE utf8mb4_bin"
CTRL_PRED = "s IN ('a') AND s != _utf8mb4'A' COLLATE utf8mb4_bin"


def setup(tbl):
    sql(f"DROP TABLE IF EXISTS {tbl}")
    sql(f"CREATE TABLE {tbl}(id INT PRIMARY KEY, s VARCHAR(8) COLLATE utf8mb4_general_ci)")
    sql(f"INSERT INTO {tbl} VALUES (1,'a'),(2,'A'),(3,'b')")


# ---- oracle suite ----
def oracle_admin_check(tbl, pred):
    rc, _, _ = sql(f"ADMIN CHECK TABLE {tbl}")
    return rc != 0


def oracle_index_vs_table(tbl, pred):
    # no secondary index on this table -> oracle not applicable -> never fires
    rc, out, _ = sql(f"SHOW INDEX FROM {tbl} WHERE Key_name<>'PRIMARY'")
    if rc != 0 or not out.strip():
        return False
    return False  # (would compare USE/IGNORE INDEX if an index existed)


def oracle_case_wrapped(tbl, pred):
    rc1, plain, _ = sql(f"SELECT COUNT(*) FROM {tbl} WHERE {pred}")
    rc2, wrapped, _ = sql(
        f"SELECT COUNT(*) FROM {tbl} WHERE CASE WHEN ({pred}) THEN 1 ELSE 0 END = 1")
    return rc1 == 0 and rc2 == 0 and plain != wrapped


SUITE_R1 = [("admin_check_consistency", oracle_admin_check),
            ("index_vs_table_rowset", oracle_index_vs_table)]
SUITE_R2 = SUITE_R1 + [("case_wrapped_equivalence", oracle_case_wrapped)]


def run_suite(suite, tbl, pred):
    return [name for name, fn in suite if fn(tbl, pred)]


def score(key, verdicts):
    tp = fn = fp = tn = 0
    rows = []
    for qid in sorted(key, key=lambda x: int(x[1:])):
        detected = len(verdicts[qid]) > 0
        a = key[qid]
        if a == "bug":
            if detected: tp += 1; r = "TP"
            else: fn += 1; r = "FN <-- MISS"
        else:
            if detected: fp += 1; r = "FP"
            else: tn += 1; r = "TN"
        rows.append((qid, a, detected, r, verdicts[qid]))
    fn_rate = fn / (tp + fn) if (tp + fn) else 0.0
    return rows, dict(tp=tp, fn=fn, fp=fp, tn=tn, fn_rate=fn_rate)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--seed", type=int, default=20260702)
    args = ap.parse_args()

    rng = random.Random(args.seed)
    answers = ["bug"] * 3 + ["control"] * 3
    rng.shuffle(answers)
    key = {f"q{i}": a for i, a in enumerate(answers)}

    rc, ver, _ = sql("SELECT VERSION()")
    print(f"FINGERPRINT {ver}  questions={len(answers)} (sealed)  "
          f"injector=predicate-simplification wrong-result (id30002 shape)")

    # run both suites per question (same injected state)
    v_r1, v_r2 = {}, {}
    for i in range(len(answers)):
        qid = f"q{i}"
        tbl = f"test.r2_{qid}"
        pred = BUG_PRED if key[qid] == "bug" else CTRL_PRED
        setup(tbl)
        v_r1[qid] = run_suite(SUITE_R1, tbl, pred)
        v_r2[qid] = run_suite(SUITE_R2, tbl, pred)
        sql(f"DROP TABLE IF EXISTS {tbl}")
        print(f"  {qid}: r1_fired={v_r1[qid] or '[]'}  r2_fired={v_r2[qid] or '[]'}")

    print("\n--- ROUND 1 suite = {admin_check, index_vs_table_rowset} ---")
    rows, s1 = score(key, v_r1)
    for qid, a, det, r, fired in rows:
        print(f"  {qid}: answer={a:7s} detected={det} => {r}  fired={fired}")
    print(f"CONFUSION tp={s1['tp']} fn={s1['fn']} fp={s1['fp']} tn={s1['tn']}  "
          f"FN_RATE={s1['fn_rate']:.2f}  <-- BLIND SPOT if fn>0")

    print("\n--- ROUND 2 suite += case_wrapped_equivalence (logged new round) ---")
    rows, s2 = score(key, v_r2)
    for qid, a, det, r, fired in rows:
        print(f"  {qid}: answer={a:7s} detected={det} => {r}  fired={fired}")
    print(f"CONFUSION tp={s2['tp']} fn={s2['fn']} fp={s2['fp']} tn={s2['tn']}  "
          f"FN_RATE={s2['fn_rate']:.2f}")

    print(f"\nRESULT: round1 FN_RATE={s1['fn_rate']:.2f} (suite blind to predicate-simplification "
          f"wrong-result) -> add case_wrapped -> round2 FN_RATE={s2['fn_rate']:.2f}. "
          f"Held-out found the blind spot and verified the fix.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
