#!/usr/bin/env python3
"""Held-out validation of the metadata_sync_check oracle (HYPOTHESIS -> TRUSTED).

Injects a metadata-staleness bug that is alive on the current build (id30003 shape):
ANALYZE, then RENAME COLUMN a->aa leaves SHOW STATS_HISTOGRAMS showing the old column
name 'a', which no longer exists in the live schema.

Oracle metadata_sync_check(t):
  stats_cols = {Column_name in SHOW STATS_HISTOGRAMS where Is_index=0}
  live_cols  = {column_name in information_schema.columns}
  fired iff stats_cols has a name absent from live_cols (a visible API references a
  column the live schema no longer has).

Blind test: bug (rename, leave stale) vs control (rename, then re-ANALYZE = fixed).
Both perform the rename, so a firing on control would be a false alarm. Injection validity
is gated: if the rename did not take effect (live schema still has 'a'), the cell is INVALID
and cannot masquerade as a result.
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


def live_cols(name):
    rc, out, _ = sql(f"SELECT column_name FROM information_schema.columns "
                     f"WHERE table_schema='test' AND table_name='{name}'")
    return set(out.split()) if rc == 0 and out else set()


def stats_cols(name):
    rc, out, _ = sql(f"SHOW STATS_HISTOGRAMS WHERE Table_name='{name}'")
    cols = set()
    if rc == 0 and out:
        for line in out.splitlines():
            f = line.split("\t")
            if len(f) >= 5 and f[4] == "0":   # Is_index == 0 -> a real column
                cols.add(f[3])                # Column_name
    return cols


def setup(name):
    sql(f"DROP TABLE IF EXISTS test.{name}")
    sql(f"CREATE TABLE test.{name}(a INT, b INT)")
    sql(f"INSERT INTO test.{name} VALUES (1,1),(2,2),(3,3),(4,4),(5,5)")
    sql(f"ANALYZE TABLE test.{name}")


def oracle_metadata_sync(name):
    live = live_cols(name)
    stats = stats_cols(name)
    stale = stats - live
    return len(stale) > 0, stale, live, stats


def run_question(name, inject):
    setup(name)
    sql(f"ALTER TABLE test.{name} RENAME COLUMN a TO aa")   # both arms rename
    if not inject:
        sql(f"ANALYZE TABLE test.{name}")                    # control: re-analyze = fixed
    # injection validity: rename must have taken effect
    live = live_cols(name)
    valid = ("aa" in live and "a" not in live)
    fired, stale, live2, stats = oracle_metadata_sync(name)
    sql(f"DROP TABLE IF EXISTS test.{name}")
    return fired, valid, {"stale": sorted(stale), "live": sorted(live2), "stats": sorted(stats)}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--seed", type=int, default=20260702)
    args = ap.parse_args()

    rng = random.Random(args.seed)
    answers = ["bug"] * 3 + ["control"] * 3
    rng.shuffle(answers)
    key = {f"q{i}": a for i, a in enumerate(answers)}

    rc, ver, _ = sql("SELECT VERSION()")
    print(f"FINGERPRINT {ver}  oracle=metadata_sync_check  "
          f"injector=stats column-rename staleness (id30003 shape)  questions={len(answers)} (sealed)")

    verdicts, valids, diag = {}, {}, {}
    for i in range(len(answers)):
        qid = f"q{i}"
        fired, valid, d = run_question(f"md_{qid}", key[qid] == "bug")
        verdicts[qid], valids[qid], diag[qid] = fired, valid, d
        print(f"  {qid}: fired={fired} valid={valid}  stats_cols={d['stats']} live={d['live']} stale={d['stale']}")

    tp = fn = fp = tn = inv = 0
    print("\nANSWER KEY + SCORING")
    for qid in sorted(key, key=lambda x: int(x[1:])):
        if not valids[qid]:
            inv += 1; print(f"  {qid}: INVALID (rename did not take effect) — excluded"); continue
        det = verdicts[qid]; a = key[qid]
        if a == "bug":
            if det: tp += 1; r = "TP"
            else: fn += 1; r = "FN <-- MISS"
        else:
            if det: fp += 1; r = "FP <-- false alarm"
            else: tn += 1; r = "TN"
        print(f"  {qid}: answer={a:7s} fired={det} => {r}")

    fn_rate = fn / (tp + fn) if (tp + fn) else 0.0
    fp_rate = fp / (fp + tn) if (fp + tn) else 0.0
    print(f"\nCONFUSION tp={tp} fn={fn} fp={fp} tn={tn} invalid={inv}")
    print(f"FN_RATE={fn_rate:.2f} (sensitivity)  FP_RATE={fp_rate:.2f} (specificity)")
    verdict = ("TRUSTED" if (tp > 0 and fn == 0 and fp == 0) else "NOT-YET-TRUSTED")
    print(f"ORACLE metadata_sync_check: {verdict} "
          f"(needs sensitivity tp>0,fn=0 AND specificity fp=0)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
