#!/usr/bin/env python3
"""Held-out blind-test harness (Layer 2: execution / oracle sensitivity).

Measures the FALSE-NEGATIVE rate of the oracle suite using local failpoint injection.
No external ground truth: every question's answer is a locally-known injected failpoint
(or a control with no injection). The oracle side runs blind and emits verdicts; only
afterwards are verdicts scored against the sealed answer key.

Modes:
  --selftest   Validate the scoring / confusion-matrix closed loop WITHOUT touching the
               cluster, using synthetic questions with known answers. Proves the harness
               itself is correct before trusting it on live injections.
  --live       Run the real Layer-2 catalog against fp-tidb + /fail/ (injectors are TODO
               beyond the validated seed; see QUESTION_CATALOG).

Design invariants (from ai-native-heldout-blind-test.md):
  - The oracle suite is fixed before questions are drawn.
  - Controls (no injection) are mixed in so FP rate is measurable.
  - The answer key is not consulted until all verdicts are emitted.
"""

from __future__ import annotations

import argparse
import dataclasses
import subprocess
from collections import defaultdict


# ----- question / answer model -------------------------------------------------

@dataclasses.dataclass
class Question:
    qid: str
    injected_class: str          # "none" (control) or a symptom class
    injector: str | None         # failpoint name or None
    setup_recipe: str | None     # how to build the state (live mode)


@dataclasses.dataclass
class Verdict:
    qid: str
    fired_oracles: list[str]     # which oracles fired, emitted BEFORE scoring


# ----- oracle suite (fixed before questions) -----------------------------------
# Each oracle maps a symptom class it is designed to catch. In live mode each is a
# callable running SQL against the cluster; here we keep the registry explicit so the
# suite is auditable and frozen.
ORACLE_SUITE = {
    "admin_check_consistency": {"catches": {"data-inconsistency"}},
    "index_vs_table_rowset":   {"catches": {"data-inconsistency", "wrong-result"}},
    "case_wrapped_equivalence": {"catches": {"wrong-result"}},
    "liveness_watchdog":       {"catches": {"ddl-hang"}},
    "no_panic_probe":          {"catches": {"panic"}},
    "metadata_sync_check":     {"catches": {"metadata-error"}},
}


def score(questions, verdicts):
    """Confusion matrix + per-class FN rate. Answer key consulted only here."""
    ans = {q.qid: q.injected_class for q in questions}
    fired = {v.qid: set(v.fired_oracles) for v in verdicts}
    tp = fn = fp = tn = 0
    per_class = defaultdict(lambda: {"tp": 0, "fn": 0})
    for qid, cls in ans.items():
        detected = len(fired.get(qid, set())) > 0
        if cls == "none":
            if detected:
                fp += 1
            else:
                tn += 1
        else:
            if detected:
                tp += 1
                per_class[cls]["tp"] += 1
            else:
                fn += 1
                per_class[cls]["fn"] += 1
    fn_rate = fn / (tp + fn) if (tp + fn) else 0.0
    fp_rate = fp / (fp + tn) if (fp + tn) else 0.0
    return {"tp": tp, "fn": fn, "fp": fp, "tn": tn,
            "fn_rate": fn_rate, "fp_rate": fp_rate, "per_class": dict(per_class)}


def report(res):
    print(f"CONFUSION tp={res['tp']} fn={res['fn']} fp={res['fp']} tn={res['tn']}")
    print(f"FN_RATE={res['fn_rate']:.2f}  (oracle blind spots)   "
          f"FP_RATE={res['fp_rate']:.2f}  (over-detection)")
    for cls, c in sorted(res["per_class"].items()):
        tot = c["tp"] + c["fn"]
        print(f"  class={cls:20s} recall={c['tp']}/{tot}  fn={c['fn']}")


# ----- self-test: synthetic closed loop, no cluster ----------------------------

def selftest():
    """Known-answer questions + a deterministic mock oracle side. Verifies scoring."""
    questions = [
        Question("s1", "data-inconsistency", "MOCK_incons", None),
        Question("s2", "none", None, None),                       # control
        Question("s3", "wrong-result", "MOCK_wrong", None),
        Question("s4", "ddl-hang", "MOCK_hang", None),
        Question("s5", "none", None, None),                       # control
        Question("s6", "panic", "MOCK_panic", None),
    ]

    # Mock oracle side runs BLIND (only sees injected marker, not injected_class label).
    # Deliberately imperfect: it detects inconsistency/wrong-result/panic, but is BLIND
    # to ddl-hang (no watchdog wired) and correctly stays silent on controls.
    def mock_oracle_run(q: Question) -> list[str]:
        marker = q.injector or ""
        fired = []
        if marker == "MOCK_incons":
            fired += ["admin_check_consistency", "index_vs_table_rowset"]
        if marker == "MOCK_wrong":
            fired += ["case_wrapped_equivalence"]
        if marker == "MOCK_panic":
            fired += ["no_panic_probe"]
        # MOCK_hang intentionally undetected -> should surface as an FN in ddl-hang class
        return fired

    verdicts = [Verdict(q.qid, mock_oracle_run(q)) for q in questions]
    res = score(questions, verdicts)
    report(res)

    # Assert the harness computed the expected closed-loop result.
    assert res["tp"] == 3 and res["fn"] == 1 and res["fp"] == 0 and res["tn"] == 2, res
    assert res["per_class"]["ddl-hang"]["fn"] == 1, res
    print("SELFTEST OK: scoring/confusion closed loop verified; ddl-hang blind spot surfaced.")
    return 0


# ----- live catalog (Layer 2) --------------------------------------------------
# Seeded with the validated TRUE-positive injector. More are TODO; each needs a setup
# recipe + the failpoint. Kept explicit so the frozen suite is auditable.
QUESTION_CATALOG = [
    Question(
        qid="addindex_temp_merge_skip",
        injected_class="data-inconsistency",
        injector="skipReorgWorkForTempIndex",
        setup_recipe=(
            "fast_reorg=ON; ~131072 rows; pause beforeAddIndexScan at write-reorg; "
            "concurrent DELETE+INSERT on fresh id range; resume; "
            "oracle=ADMIN CHECK TABLE + COUNT(*) USE INDEX vs IGNORE INDEX. "
            "(recipe: fuzz-handoff.md section 6)"),
    ),
    # TODO: add-index/global rollback delete-range (id30009 injector),
    #       reorg global-index missing rows (id30007), a control with no injection.
]


def run_live(args):
    print("LIVE mode: Layer-2 catalog. Injectors beyond the validated seed are TODO.")
    print(f"catalog_size={len(QUESTION_CATALOG)} (seed only; not a full blind run yet)")
    for q in QUESTION_CATALOG:
        print(f"  q={q.qid} class={q.injected_class} injector={q.injector}")
    print("Next: implement inject()/run_oracles() against fp-tidb /fail/, mix in controls, "
          "seal answer key, then score().")
    return 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--selftest", action="store_true")
    ap.add_argument("--live", action="store_true")
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", default="14000")
    args = ap.parse_args()
    if args.selftest:
        return selftest()
    if args.live:
        return run_live(args)
    ap.print_help()
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
