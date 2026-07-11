# Held-Out Blind Test Framework
> 2026-07-02. Purpose: measure the FALSE-NEGATIVE rate of the method itself — what our selectors and oracles MISS — using only local ground truth. No external sign-off, no GitHub, no owner. We decided to trust the LLM's judgment; this framework is how we audit that judgment's blind spots.

## Why this exists

Self-graded discovery tells us "what we reported looks right." It says nothing about
**what we missed** or **which bug shapes our oracles are blind to**. Once self-judgment is the
final verdict, the recall of that judgment is the one number we cannot get by finding more bugs —
only by testing against cases where the answer is already known and hidden from the solver.

Constraint: the bug library's newest fix predates the 2026-06-22 build, so those bugs are dead on
the current fp-tidb. Live ground truth therefore comes from **local failpoint injection**, which
is fully controllable and needs nothing external. This turns the "no external confirmation" rule
from a limitation into the design: every question has a locally-known answer.

## Two layers

### Layer 1 — Reasoning held-out ("could the method re-derive the oracle?")
Tests whether the method's audit-card / selector reasoning independently reaches the correct
proof obligation and oracle for a scenario whose answer is hidden.

- **Question pool**: `bug_ddl WHERE has_regression_test=1` (234 available; metadata-error 64,
  data-inconsistency 35, ddl-cancel 31, ddl-hang 27, panic 26, wrong-result 17, schema-state 10).
- **Masking**: the solver sees only the scenario/DDL sequence (`scenario_dims`, repro setup).
  Hidden: `violated_invariant`, `bug_type`, `fix_pr`, `oracle`.
- **Task**: produce the audit card — which `D_dim` is fragile here, which proof obligation is at
  risk, which oracle would catch it.
- **Scoring**: does the derived oracle family match the recorded `violated_invariant`? Did it name
  the right `D_dim`? Graded by an independent judge pass against the hidden ground truth.
- **Metric**: reasoning recall = fraction of scenarios where the method independently re-derives a
  correct oracle. Low recall on a `bug_type` = a class the *selectors* are blind to.
- Pure local: uses stored data only; no cluster run.

### Layer 2 — Execution held-out ("does the oracle actually fire on a live bug?")
Tests oracle sensitivity: inject a known bug via failpoint, hide which one, run the full oracle
suite blind, measure what fires.

- **Question pool**: a catalog of injectable failpoints, each with a setup recipe and an expected
  symptom class. Seed (validated in the TRUE-positive demo): `skipReorgWorkForTempIndex` →
  index/temp-index merge skipped → `ADMIN CHECK` 8223 + USE/IGNORE INDEX rowset drift.
- **Masking**: the harness injects one failpoint (or none = control) chosen without telling the
  oracle side which.
- **Task**: run every oracle in the suite; record which fired.
- **Scoring**: confusion matrix over injected-class × detected:
  - TP injected≠none & detected · FN injected≠none & not detected
  - FP control & detected · TN control & not detected
- **Metric**: `FN_rate = FN/(TP+FN)` — the oracle suite's false-negative rate, stratified by
  symptom class to expose which bug shapes the oracles are blind to. `FP_rate` guards against a
  suite that "detects" everything (which would make TP meaningless).
- Pure local: failpoint injection needs only fp-tidb + `/fail/`.

## Blind protocol (anti-leak)

1. Preparer and solver are information-isolated. Layer 1: the masking view is a DB view that
   projects only allowed columns. Layer 2: the injection choice is written to a sealed answer file
   the oracle harness does not read until after it emits its verdicts.
2. The oracle suite is fixed BEFORE seeing the question set. Adding an oracle after seeing a miss
   is allowed only as a NEW round, logged as such (otherwise recall is inflated by hindsight).
3. Controls (no injection) are mixed in at a known rate so FP rate is measurable, not assumed.
4. Every question's answer is locally known and archived; nothing depends on an external verdict.

## What each layer feeds back

- Layer 1 misses → weak/absent selectors or D_dim battery gaps → add battery entries, new selectors.
- Layer 2 misses → oracle blind spots → new oracle patterns, or a documented "this class needs a
  different oracle" (e.g. hang needs a watchdog, not equality).
- Both are logged against the selector ledger and the D_dims battery, so the audit improves the
  same living assets the discovery loop uses.

## Status
- Framework + harness skeleton: this document + `/Users/bba/pc/ai_native_heldout_harness.py`.
- Harness self-test (scoring/confusion-matrix closed loop): validated. The self-test plants a
  ddl-hang blind spot in a mock oracle and the harness correctly surfaces it as an FN — proving
  the framework can *see* misses, which is its whole purpose.
- Layer 2 live run #1 (2026-07-02): `/Users/bba/pc/ai_native_heldout_layer2_live.py`, seed 20260702,
  3 inject + 3 control, N_ROWS=32768, injector `skipReorgWorkForTempIndex`.
  - All 6 questions had `held=True` (injection actually reached write-reorg — valid, not INVALID).
  - CONFUSION tp=3 fn=0 fp=0 tn=3. FN_RATE=0.00, FP_RATE=0.00.
  - Injected cells showed idx=32268 vs tbl=32768 (500 unmerged rows); controls idx==tbl.
- Layer 2 live run #2 (2026-07-02): `/Users/bba/pc/ai_native_heldout_layer2_round2.py`, seed 20260702,
  3 bug + 3 control, injector = predicate-simplification wrong-result (id30002 shape), pure SQL.
  - Round 1 suite {admin_check, index_vs_table_rowset}: tp=0 **fn=3** fp=0 tn=3, **FN_RATE=1.00**.
    The suite is fully blind to this bug class — ADMIN CHECK passes (storage is fine) and there is
    no index-vs-table rowset drift, so neither oracle can see a wrong-result caused by predicate
    simplification. This is the first real blind spot the framework surfaced.
  - Round 2 suite += case_wrapped_equivalence (logged new round): tp=3 fn=0 fp=0 tn=3, **FN_RATE=0.00**.
    Adding the CASE-wrapped oracle closes the blind spot; controls stay TN, so the new oracle is
    sensitive, not trigger-happy.
  - Full loop demonstrated: blind harness -> real miss surfaced -> missing oracle identified ->
    oracle added -> miss eliminated, verified.
- Layer 2 live run #3 (2026-07-02): `/Users/bba/pc/ai_native_heldout_metadata_probe.py`, seed 20260702,
  3 bug + 3 control, injector = stats column-rename staleness (id30003 shape), pure SQL.
  - Purpose: graduate the HYPOTHESIS oracle `metadata_sync_check` to TRUSTED.
  - CONFUSION tp=3 fn=0 fp=0 tn=3 invalid=0. Sensitivity FN_RATE=0.00, specificity FP_RATE=0.00.
  - Bug arms: SHOW STATS_HISTOGRAMS shows `a` while live schema has `aa`. Controls rename AND
    re-analyze, so a firing there would be a false alarm — silence proves the oracle keys on
    staleness, not on the mere presence of a rename.
  - Result at the time: `metadata_sync_check` HYPOTHESIS -> TRUSTED (O9).
  - **OVERTURNED same day by adversarial LLM verification (see below).** The TRUSTED verdict was
    wrong: it rested on a single injection shape plus a timing artifact.
- Adversarial LLM verification of O9 (2026-07-02): a skeptic agent, told to REFUTE the oracle,
  found and reproduced three defects held-out missed:
  - FALSE NEGATIVE (durable): `DROP COLUMN a; ADD COLUMN a <new dist>; ANALYZE` — an orphan
    hist_id for the old `a` (NDV=8) is rendered by SHOW as `a`, masking the new `a` (NDV=1).
    Name-set {a,b}=={a,b} => oracle silent on exactly the staleness it exists to catch.
  - FALSE POSITIVE: `DROP COLUMN b` => fires on b's benign, self-healing GC lag.
  - FLAKY GROUND TRUTH: rename staleness self-heals after re-ANALYZE, so run #3's "0 FP on
    rename+re-analyze controls" was a scheduling artifact, not soundness.
  - Root cause: O9 keys on the column-NAME set; TiDB keys stats identity on column ID.
  - Replacement O9' (stats_value_differential: recompute NDV per live column) was mined from the
    refutation and execution-confirmed on both counterexamples (FN-1 fires 8≠1, FP-1 silent).
  - **Lesson: a single held-out injection shape gives false confidence. held-out execution and
    adversarial LLM review are complementary — one is independent-but-narrow, the other is
    broad-but-same-source. TRUSTED requires BOTH. This is the case that proves it, and it is more
    valuable than any green run.**
- Layer 1 masking view + judge pass: TODO (pool of 234 ready).
- GitHub DDL held-out retrospective (2026-07-09): `/Users/bba/pc/ai-native-ddl-github-heldout-methodology.md`.
  This is a **PR-review/static rediscovery baseline**, not a test-harness recall number.
  - Corpus: 82 selected DDL validation root-cause cases from
    `/Users/bba/pc/ai-bug-dataset/out/20260206_082028_ddl_validation_rootcause_closed_20240101_20250831_full/`.
  - Best current run with DDL docs/battery:
    `/Users/bba/pc/ai-bug-dataset/out/20260208_232200_re2_ddl_validation_selected_csv_gpt52_c20_ddldocs/report.md`.
  - Review result: FOUND=49, NOT_FOUND=29, UNCERTAIN=4. Generic RE2 baseline was FOUND=42/82; larger
    no-DDL-doc run was FOUND=44/82. DDL context improves recall, but does not cover all historical
    DDL bugs in review mode.
  - Interpretation: misses are concentrated in fault-injection, external IO/topology,
    lifecycle/commit-boundary, error-preservation, stress/perf, and compatibility-contract shapes.
    Treat every miss as a selector/oracle/discoverability ticket, not as a prompt failure.
  - Testing follow-up: evaluate `test_discoverability` separately. A true testing recall claim
    requires a generated test that fails on the vulnerable intro revision and passes after the fix,
    or a failpoint/topology harness for classes that cannot be exposed by SQL-only matrices.

## Blind-spot map (what run #2 established)

An oracle suite must be matched to the symptom class, and a suite tuned for one class is blind to
others. Concretely, from runs #1 and #2:

```text
symptom class            oracle that fires            oracles that are BLIND to it
data-inconsistency       admin_check, rowset_diff     case_wrapped (no wrong plan here)
wrong-result (planner/   case_wrapped / no-shortcut   admin_check (storage is fine),
 extractor/predicate)     / safe-path differential     rowset_diff (no index drift)
```

This is exactly why the confirmed wrong-result bugs (id30001/30002/30010/30012) all used
CASE-wrapped or differential oracles and never ADMIN CHECK: ADMIN CHECK proves storage sanity, not
query correctness. Run #2 is the empirical proof of that split — and the rule for the harness: never
run a single suite against a mixed question set and read a low FN as coverage; FN is only meaningful
per symptom class with the matching oracle present.

## Reading of live run #1 (do not over-read)

A clean 0% FN here is a NARROW positive, not a verdict on the oracle suite:
- One injector, one symptom class (data-inconsistency). The other four oracles in the suite
  (liveness_watchdog, no_panic_probe, metadata_sync_check, case_wrapped_equivalence) were never
  exercised — their false-negative rate is still completely unknown.
- The inject-vs-control signal is strong (a single failpoint toggle); this is the easy end of the
  difficulty range, not the adversarial end.
- n=6 is small.

What it does establish: the blind harness runs end to end, injection validity is gated by `held`
evidence (so a failed injection can never masquerade as an oracle miss — the exact bug that made
the first smoke run report a false FN), and the primary data-inconsistency oracle
(`ADMIN CHECK` + USE/IGNORE INDEX rowset) fires reliably on the shape it was designed for.

## An FN is an oracle-mining ticket, not a manual patch

Run #2 closed the blind spot by adding `case_wrapped_equivalence`, and it is tempting to read that
as "a human noticed the miss and patched the suite." That is the wrong framing. Oracle mining is a
first-class part of this method (see methodology-v2 "Oracle Mining and the Oracle Library"): an FN
is a symptom class with no firing oracle, and the missing oracle is *derivable from the missed
bug's `Q_claim`* — here, "predicate simplification must preserve 3-valued semantics" yields the
CASE-wrapped differential directly. Held-out's job is not only to grade oracles but to **generate
the ticket** that mines the next one. Every oracle so mined enters `ai-native-oracle-library.md`
and is only TRUSTED once held-out shows it fires on the injected bug and stays silent on controls —
exactly the sensitivity/specificity that run #2 provided for O3.

The oracle library's HYPOTHESIS rows (liveness_watchdog, no_panic_probe, metadata_sync_check) are
the standing oracle-mining backlog: each is an oracle we believe we have but have never proven
fires. They are the highest-value held-out targets precisely because a blind spot there is
currently invisible.

## Next (find the non-zero)

The point of held-out is to find where FN > 0, not to collect green runs. Priority: add injectors
for classes where the suite has an oracle but no validation —
- reorg global-index missing rows (id30007 shape) → tests index_vs_table_rowset on a different bug,
- a metadata-staleness injector → tests metadata_sync_check,
- a deliberately un-oracled class (e.g. a subtle wrong-result with no rowset drift) → expected to
  produce a real FN, which is the signal we actually want.
Each new class that survives without an FN widens the trust boundary; the first FN tells us which
bug shape our judgment is blind to.
