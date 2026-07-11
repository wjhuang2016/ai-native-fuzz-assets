#!/usr/bin/env python3
"""Held-out blind test — Layer 2 live run against fp-tidb.

Injects a REAL bug via failpoint (skipReorgWorkForTempIndex: temp-index merge skipped
during add-index write-reorg, so concurrent DML never merges into the index) or runs a
control with no injection. The oracle suite runs BLIND per question; the answer key is
revealed and scored only after all verdicts are emitted.

This is a true blind test: injection vs control is the only difference, decided by a
fixed-seed shuffle recorded as the sealed answer key.
"""

from __future__ import annotations

import argparse
import subprocess
import threading
import time
import random

HOST, PORT = "127.0.0.1", "14000"
STATUS = "http://127.0.0.1:18080"
FP_SKIP = "github.com/pingcap/tidb/pkg/ddl/skipReorgWorkForTempIndex"
FP_PAUSE = "github.com/pingcap/tidb/pkg/ddl/beforeAddIndexScan"
N_ROWS = 32768


def sql(q, db="test", timeout=60):
    p = subprocess.run(
        ["mysql", f"-h{HOST}", f"-P{PORT}", "-uroot", "--batch", "--raw",
         "--skip-column-names", f"--connect-timeout=5", db, "-e", q],
        text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
    return p.returncode, p.stdout.strip(), p.stderr.strip()


def fp_put(name, action):
    subprocess.run(["curl", "-s", "-X", "PUT", "-d", action, f"{STATUS}/fail/{name}"],
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def fp_del(name):
    subprocess.run(["curl", "-s", "-X", "DELETE", f"{STATUS}/fail/{name}"],
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def build_table(tbl):
    sql(f"DROP TABLE IF EXISTS {tbl}")
    sql(f"CREATE TABLE {tbl}(id INT PRIMARY KEY, v INT)")
    sql(f"INSERT INTO {tbl} VALUES (1,1)")
    # doubling insert to N_ROWS
    n = 1
    while n < N_ROWS:
        rc, _, err = sql(
            f"INSERT INTO {tbl}(id,v) SELECT id+{n}, v+{n} FROM {tbl}")
        if rc != 0:
            raise RuntimeError(f"seed insert failed: {err}")
        n *= 2
    sql(f"UPDATE {tbl} SET v=id")   # v unique => differential-friendly


def run_question(qid, inject):
    """Returns the list of oracle names that fired. Blind to the answer."""
    tbl = f"test.hld_{qid}"
    build_table(tbl)

    fp_put(FP_PAUSE, "pause")
    if inject:
        fp_put(FP_SKIP, "return(true)")

    alter_rc = {}

    def do_alter():
        alter_rc["rc"], _, alter_rc["err"] = sql(
            f"ALTER TABLE {tbl} ADD INDEX vidx(v)", timeout=120)

    t = threading.Thread(target=do_alter)
    t.start()

    # wait until the add-index job is held at write reorganization
    held = False
    short = f"hld_{qid}"   # table name appears in ADMIN SHOW DDL JOBS; index name does not
    for _ in range(60):
        rc, out, _ = sql("ADMIN SHOW DDL JOBS 3")
        low = out.lower()
        if "write reorganization" in low and short in low:
            held = True
            break
        time.sleep(0.5)

    if held:
        # concurrent DML on a fresh id range -> writes into temp index
        sql(f"DELETE FROM {tbl} WHERE id BETWEEN 1000 AND 1499")
        sql(f"INSERT INTO {tbl}(id,v) SELECT id+500000, id+500000 FROM {tbl} "
            f"WHERE id BETWEEN 2000 AND 2499")

    fp_del(FP_PAUSE)     # resume
    t.join(timeout=120)
    if inject:
        fp_del(FP_SKIP)

    # ---- oracle suite (fixed, blind) ----
    fired = []
    rc, out, err = sql(f"ADMIN CHECK TABLE {tbl}")
    if rc != 0:
        fired.append("admin_check_consistency")
    rc1, via_idx, _ = sql(f"SELECT COUNT(*) FROM {tbl} USE INDEX(vidx)")
    rc2, via_tbl, _ = sql(f"SELECT COUNT(*) FROM {tbl} IGNORE INDEX(vidx)")
    if rc1 == 0 and rc2 == 0 and via_idx != via_tbl:
        fired.append("index_vs_table_rowset")

    sql(f"DROP TABLE IF EXISTS {tbl}")
    return fired, {"held": held, "alter_rc": alter_rc.get("rc"),
                   "via_idx": locals().get("via_idx"), "via_tbl": locals().get("via_tbl")}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--seed", type=int, default=20260702)
    ap.add_argument("--n-inject", type=int, default=3)
    ap.add_argument("--n-control", type=int, default=3)
    args = ap.parse_args()

    # sealed answer key: shuffle inject/control, recorded but NOT shown during the run
    rng = random.Random(args.seed)
    answers = ["inject"] * args.n_inject + ["control"] * args.n_control
    rng.shuffle(answers)
    key = {f"q{i}": a for i, a in enumerate(answers)}

    # health
    rc, ver, _ = sql("SELECT VERSION()")
    if rc != 0:
        print("cannot connect to fp-tidb")
        return 2
    print(f"FINGERPRINT {ver}  N_ROWS={N_ROWS}  questions={len(answers)} "
          f"(sealed; oracle side runs blind)")

    verdicts = {}
    diag = {}
    for i in range(len(answers)):
        qid = f"q{i}"
        inject = key[qid] == "inject"     # harness knows; oracle output does not label it
        fired, d = run_question(qid, inject)
        verdicts[qid] = fired
        diag[qid] = d
        print(f"  {qid}: fired={fired or '[]'}  (held={d['held']} "
              f"idx={d['via_idx']} tbl={d['via_tbl']})")

    # ---- reveal + score ----
    tp = fn = fp = tn = 0
    print("\nANSWER KEY + SCORING")
    for qid in sorted(key, key=lambda x: int(x[1:])):
        detected = len(verdicts[qid]) > 0
        a = key[qid]
        if a == "inject":
            if detected: tp += 1; res = "TP"
            else: fn += 1; res = "FN <-- MISS"
        else:
            if detected: fp += 1; res = "FP <-- false alarm"
            else: tn += 1; res = "TN"
        print(f"  {qid}: answer={a:7s} detected={detected}  => {res}  fired={verdicts[qid]}")

    fn_rate = fn / (tp + fn) if (tp + fn) else 0.0
    fp_rate = fp / (fp + tn) if (fp + tn) else 0.0
    print(f"\nCONFUSION tp={tp} fn={fn} fp={fp} tn={tn}")
    print(f"FN_RATE={fn_rate:.2f} (data-inconsistency oracle blind spots)  FP_RATE={fp_rate:.2f}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
