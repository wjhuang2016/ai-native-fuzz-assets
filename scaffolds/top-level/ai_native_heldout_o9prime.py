#!/usr/bin/env python3
"""Multi-shape blind held-out for O9' stats_value_differential (autonomous-loop tick P1).

O9' obligation: cached stats for each LIVE column, present in the visible stats surface by
name, match a fresh recomputation. It is a VALUE oracle — it must NOT fire on pure display
staleness (rename), which is O9's job. This run verifies three things at once:
  sensitivity  S1 drop+re-add same name (value wrong)      -> RED
  specificity  control (analyze, no schema change)         -> GREEN
  scope        S4 rename (display stale, value correct)    -> SILENT (O9' must not overreach)

Form: for each row of SHOW STATS_HISTOGRAMS (Is_index=0) whose Column_name is a LIVE column,
compare its Distinct_count to SELECT COUNT(DISTINCT that_column). Any mismatch => fired.
"""

from __future__ import annotations
import subprocess, random, argparse

H, P = "127.0.0.1", "14000"


def q(sql, db="test"):
    p = subprocess.run(["mysql", f"-h{H}", f"-P{P}", "-uroot", "-N", "--batch", "--raw",
                        "--connect-timeout=5", db, "-e", sql],
                       text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
    return p.returncode, p.stdout.strip(), p.stderr.strip()


def live_cols(t):
    _, out, _ = q(f"SELECT column_name FROM information_schema.columns "
                  f"WHERE table_schema='test' AND table_name='{t}'")
    return set(out.split()) if out else set()


def o9prime(t):
    """value-differential over live, name-present stats columns. returns (fired, detail)."""
    live = live_cols(t)
    _, out, _ = q(f"SHOW STATS_HISTOGRAMS WHERE Table_name='{t}'")
    fired = []
    for line in out.splitlines():
        f = line.split("\t")
        if len(f) < 7 or f[4] != "0":
            continue
        name, stats_ndv = f[3], f[6]
        if name not in live:      # not a live column -> O9's display concern, skip (scope boundary)
            continue
        rc, real, _ = q(f"SELECT COUNT(DISTINCT `{name}`) FROM test.{t}")
        if rc != 0:
            return None, f"query-error on {name}"   # trigger-evidence: never call this 'equal'
        if stats_ndv != real:
            fired.append(f"{name}:stats{stats_ndv}!=real{real}")
    return (len(fired) > 0), (";".join(fired) if fired else "all live cols match")


def build(t, shape):
    q(f"DROP TABLE IF EXISTS test.{t}")
    q(f"CREATE TABLE test.{t}(a INT, b INT)")
    q(f"INSERT INTO test.{t} VALUES (1,1),(2,2),(3,3),(4,4),(5,5),(6,6),(7,7),(8,8)")
    q(f"ANALYZE TABLE test.{t}")
    if shape == "bug":            # drop + re-add same name, new low-NDV distribution
        q(f"ALTER TABLE test.{t} DROP COLUMN a")
        q(f"ALTER TABLE test.{t} ADD COLUMN a INT DEFAULT 99")
        q(f"ANALYZE TABLE test.{t}")
    elif shape == "rename":       # display-only staleness (value stays correct, id unchanged)
        q(f"ALTER TABLE test.{t} RENAME COLUMN a TO aa")
    # control: no schema change


def main():
    ap = argparse.ArgumentParser(); ap.add_argument("--seed", type=int, default=20260702)
    args = ap.parse_args()
    rng = random.Random(args.seed)

    sens = ["bug"] * 3 + ["control"] * 3
    rng.shuffle(sens)
    key = {f"q{i}": s for i, s in enumerate(sens)}
    key["b0"] = "rename"; key["b1"] = "rename"   # scope-boundary probes

    _, ver, _ = q("SELECT VERSION()")
    print(f"FINGERPRINT {ver}  oracle=O9'(value-diff)  sealed={list(key.values())}\n")

    verdict = {}
    for qid, shape in key.items():
        build(f"o9p_{qid}", shape)
        fired, detail = o9prime(f"o9p_{qid}")
        verdict[qid] = fired
        q(f"DROP TABLE IF EXISTS test.o9p_{qid}")
        print(f"  {qid}[{shape:7s}] fired={fired}  {detail}")

    tp = fn = fp = tn = 0
    scope_ok = scope_bad = 0
    print("\nSCORING")
    for qid in sorted(key):
        s = key[qid]; det = verdict[qid]
        if s == "bug":
            if det: tp += 1; r = "TP"
            else: fn += 1; r = "FN"
        elif s == "control":
            if det: fp += 1; r = "FP"
            else: tn += 1; r = "TN"
        else:  # rename scope boundary: O9' must NOT fire (that is O9's display job)
            if det: scope_bad += 1; r = "SCOPE-VIOLATION (fired on display staleness)"
            else: scope_ok += 1; r = "SCOPE-OK (silent, left to O9)"
        print(f"  {qid}[{s:7s}] fired={det} => {r}")

    fn_rate = fn / (tp + fn) if (tp + fn) else 0.0
    fp_rate = fp / (fp + tn) if (fp + tn) else 0.0
    print(f"\nCONFUSION tp={tp} fn={fn} fp={fp} tn={tn}  FN_RATE={fn_rate:.2f} FP_RATE={fp_rate:.2f}")
    print(f"SCOPE boundary: ok={scope_ok} violations={scope_bad}")
    trusted = (tp > 0 and fn == 0 and fp == 0 and scope_bad == 0)
    print(f"O9' verdict: {'TRUSTED on these shapes (sensitivity+specificity+scope)' if trusted else 'CHECK'}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
