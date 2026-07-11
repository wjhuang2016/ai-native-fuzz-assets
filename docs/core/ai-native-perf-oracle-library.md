# Performance Oracle Library (sibling loop, separate ledger)
> Started 2026-07-03 per methodology v2 §"Performance obligations are a sibling loop". Same evidence
> tiers as the correctness oracle library (HYPOTHESIS < LLM-VERIFIED < TRUSTED, REFUTED on reproduced
> FP/FN). Perf oracles add TWO extra axes the skeptic must attack beyond form soundness:
> (a) the THRESHOLD K (too low => benign-variance FP; too high => real-regression FN), and
> (b) the OBSERVATION SURFACE (counters can be polluted: coprocessor cache zeroes process_keys,
>     cop retries inflate total_keys, statements_summary aggregates across arms).
> Claim-shape taxonomy: work / choice / progress / isolation. Every oracle names its shape.

## Red-cell criteria shared by all perf oracles
- Never wall-clock. Verdicts come from deterministic work counters (process_keys, total_keys,
  plan shape, partitions accessed, backfill row_count).
- Categorical claims (pruning happened / pushdown happened / cache hit) need no threshold.
- Quantitative claims fire on counter ratio >= K (default K=10, deliberately high) or on a wrong
  growth exponent between N and cN — never on a single slow run.
- `info(cost-model-tradeoff)`: a differential that is real but within defensible cost-model
  judgment (ratio < K, or a documented tradeoff). Recorded, not counted as red.
- Trigger evidence is mandatory and recursive (O2' lesson): an arm must PROVE it did the work —
  plan digests of the two arms must differ where the claim requires different paths; a failed,
  empty, or counter-less arm is INVALID, never "equal"; counters must be repeatable (2 runs, take
  min) to exclude cache-admission noise.

## PO1 forced_plan_counter_differential (choice claim)
```text
obligation:  the plan the system chose does not do >= K x the work of a force-able alternative
             plan on identical data, identical snapshot, frozen stats
form:        run the query as chosen (no hints) and under each forced alternative
             (USE INDEX / IGNORE INDEX / join hints); counter = per-execution process_keys from
             slow_query (threshold=0 session); fire if keys_chosen / keys_best_alt >= K
trigger ev:  (1) plan_digest of chosen != plan_digest of the best alternative (else the "alt" is
             the same plan — that arm is INVALID, hint not honored: the O2/MVI lesson transfers
             verbatim); (2) each arm run twice, counters within 2x of each other (repeatability),
             else INVALID (copr-cache / retry pollution); (3) stats frozen during the cell
             (auto-analyze off), else INVALID.
catches:     optimizer mis-choice under stale/pseudo stats, out-of-range estimation, skew;
             binding-induced bad plans (bindings are part of the choice surface)
blind to:    cost differences not expressed in key counts — e.g. N random index lookups vs N
             sequential scan keys have equal process_keys but very different real cost (skeptic
             FP/FN axis: counter is a work proxy, not a cost proxy). Also blind to plans whose
             alternative cannot be forced by hints.
skeptic (llm-review, 2026-07-03, pre-registered attacks):
  FP-ax1: cop retry / region miss re-reads inflate one arm — mitigated by repeat-min clause.
  FP-ax2: chosen plan reads more keys but is genuinely better (covering-index full scan vs
          many random lookups) — mitigated only partially by high K; record as noise axis.
  FN-ax1: K=10 misses real 3-5x regressions — accepted scope, not a defect.
  INV-ax: hint silently ignored (MVI precedent) — plan-digest clause makes this INVALID.
  OBS-ax: coprocessor cache serves an arm with process_keys=0 — repeat-min + nonzero-counter
          guard: an arm with 0 keys on a table known to have N rows is INVALID unless the plan
          is a point/dual plan.
held-out:    EXECUTED 2026-07-03 (ai_native_perf_po_heldout.py, TiDB v8.4.0 local):
             sensitivity FIRE on binding-injected bad index (chosen 262144 keys vs best alt 2,
             ratio 131072 >= K); specificity SILENT on native optimizer choice (ratio ~0);
             same-plan alternatives correctly excluded by the digest clause both times.
status:      TRUSTED on the validated shape (bad-index-choice, single injection shape). Blind
             multi-shape run (stale-stats / out-of-range injections) still owed before full trust
             — the O9 single-shape lesson applies.
```

## PO2 growth_shape_counter (work claim)
```text
obligation:  a path claiming O(k) work (k independent of table size N) keeps its work counter
             ~constant as N grows: LIMIT-with-matching-index, point get, TopN pushdown,
             partition pruning to a fixed partition set
form:        same query on the same schema at N and cN rows (c>=4, same stats freshness);
             fire if keys(cN)/keys(N) >= c/2 for a claimed-O(k) plan
trigger ev:  plan at BOTH sizes must show the claimed bounded operator (Limit under
             IndexReader, point get, pruned partition list); if the plan CHANGES shape between
             N and cN, the cell is routed to PO1 (choice claim), not judged here.
catches:     lost LIMIT/TopN pushdown, pruning that silently degrades to scan-all,
             "bounded" iterators that actually restart per batch
blind to:    constants (a 10x-slower O(k) is invisible); anything not visible in key counters
skeptic:     FP-ax: stats/data distribution differ between the two sizes => different but
             legitimate plans — the shape-identity trigger clause converts this to routed/INVALID.
             OBS-ax: at small N the whole answer may be served from one region / copr cache —
             use N large enough that keys(N) >= 100.
held-out:    pending: sensitivity = a deliberately unbounded arm (LIMIT without usable index
             order) must fire; specificity = index-ordered LIMIT 1 must stay silent at both sizes.
status:      HYPOTHESIS
```

## PO3 cache_drift_counter_differential (reuse/choice claim)
```text
obligation:  a plan reused from the plan cache for parameter p2 does < K x the work of the plan
             a fresh optimization would choose for p2 (the cache's implicit "still good enough"
             claim under parameter drift)
form:        arm A: PREPARE once, EXECUTE p1 (populates cache), EXECUTE p2; per-execution
             process_keys and Plan_from_cache/Plan_digest from slow_query.
             arm B: same session, tidb_enable_prepared_plan_cache=0, re-PREPARE, EXECUTE p2.
             fire if keys_A(p2) / max(keys_B(p2),1) >= K
trigger ev:  (1) arm A's p2 row must show Plan_from_cache=1 (the reuse actually happened; if the
             system DECLINED to cache — Plan_from_cache=0 — the cell is INVALID for the drift
             claim and GREEN-calibrating for the cacheability guard, a separate obligation);
             (2) plan_digest_A(p2) != plan_digest_B(p2) (the fresh optimizer really chose a
             different plan; equal digests => reuse is harmless for p2 => green(triggered));
             (3) stats frozen across both arms (auto-analyze off) — a mid-cell stats refresh
             both invalidates the cache and changes arm B, double pollution;
             (4) failed/empty arm => INVALID (O2' clause 5).
catches:     catastrophic parameter drift (cached wide-scan plan reused for selective params or
             vice versa), cache reuse across partition-boundary drift in dynamic prune mode
blind to:    drift whose damage is access-pattern shaped, not key-count shaped (cached
             IndexLookup on skewed param: ~2N lookup keys vs N scan keys => ratio 2 < K even
             when real cost is 50x — PRE-REGISTERED FN: counter is blind to random-vs-sequential);
             non-prepared plan cache (separate surface, off on this cluster)
skeptic:     FP-ax: p2's first execution warms nothing relevant (keys deterministic) — no FP
             expected from warmth; concurrency FP excluded by single-session serial arms.
             FN-ax: TiDB's cacheability guards (Cacheable checks) may refuse exactly the risky
             shapes => everything INVALID => the oracle measures nothing while looking busy —
             the harness must REPORT invalid-rate; a high invalid rate is a green calibration
             of the guard, not coverage.
             OBS-ax: slow_query is time-windowed and shared — match rows by unique table name +
             [arguments: ...] tag + session order, never by digest alone (both arms share the
             statement digest).
held-out:    EXECUTED 2026-07-03 (ai_native_perf_po_heldout.py, TiDB v8.4.0 local):
             sensitivity FIRE (cached full-scan reused for p2=1: 131072 keys vs fresh
             IndexLookup 2 keys, ratio 65536); specificity green(triggered) on adjacent-param
             control (digest-equal clause correctly classified reuse as harmless).
status:      TRUSTED on the validated shape (wide->selective range drift, single shape). Blind
             multi-shape run still owed.
```

## PO4 addindex_rework_conservation (progress claim) — registered, mining deferred to L2
```text
obligation:  ADD INDEX backfill scans each row O(1) times: total backfill row_count ~= table
             row count (x small constant), preserved across pause/resume, owner switch, retry
form:        ADMIN SHOW DDL JOBS row_count (and mysql.tidb_ddl_reorg checkpoint progression)
             vs SELECT COUNT(*) baseline; fire if scanned/count >= 2 after an interruption that
             the system claims to resume from checkpoint
trigger ev:  the interruption must actually have hit mid-backfill (checkpoint row exists with
             0 < done < total at interrupt time), else INVALID — interrupting before the first
             batch or after the last proves nothing about resume.
catches:     checkpoint ignored on resume, overlapping range re-dispatch after owner failover,
             ingest->txn fallback restarting from zero silently
blind to:    rework hidden below the row counter (re-reading KV inside one recorded batch);
             wall-clock regressions with correct row accounting
skeptic:     sibling-path selector transfers from DDL correctness loop (id30009 family): the
             success path will be green; resume / owner-switch / fallback siblings are where
             rework lives. FP-ax: retry-on-error legitimately re-scans the failed batch — a
             small constant, below the 2x threshold by design.
held-out:    superseded by a live hit — PO4 fired on its FIRST mining matrix (PF2-A,
             2026-07-03): txn-mode pause/resume gives final row_count exactly 2N with real
             full-table rework (perf-30001, see
             ai-native-perf-addindex-pause-rework-draft.md). Conservation form validated by
             two clean controls (no pause -> exactly N) and two red runs (2N at two different
             pause points and two table sizes). Specificity on the DEFAULT ingest path:
             pause(early)/resume -> ratio 1.000 exactly (green).
status:      USED (caught a real bug in discovery; injected held-out still owed for the
             monotonicity form — the observed hit fired on the conservation form).
```

## PO5 ttl_scan_rework_reseek (progress claim) — TTL analog of PO4, EXECUTION-CONFIRMED hit
```text
obligation:  a re-locked TTL scan task (HB timeout / owner failover / restart) continues from
             where it stopped — it does NOT re-scan its range from ScanRangeStart
form:        interrupt a running scan task mid-range (inject fake dead owner + stale
             owner_hb_time -> real checkInvalidTask -> HB-timeout re-lock). After re-lock,
             inspect internal scan SELECTs (information_schema.slow_query, is_internal=1):
             fire if a scan appears with NO `_tidb_rowid >` lower bound (range-start form)
             AND total_keys >= 10x a normal continuation batch (it re-seeks the deleted prefix)
trigger ev:  (1) re-lock actually happened: owner_id changed away from the injected fake value
             (else the interruption did not interrupt -> INVALID, the L7 lesson);
             (2) baseline = median total_keys of pre-interrupt continuation batches;
             (3) the RED query must be the no-lower-bound (range-start) form, not merely a
             high-total_keys continuation — a continuation with a `> N` bound is NOT a restart.
catches:     missing intra-range progress checkpoint in background range-scanners on re-entry
blind to:    rework hidden below the scan-query surface; correctness (there is none — idempotent)
llm-review:  the cursor-min detector (first attempt) is a KNOWN FN — continuation resumes just
             past the deleted prefix so `min(_tidb_rowid > N)` stays high; must key on the
             no-lower-bound query instead (detector fix, 2026-07-03).
held-out:    EXECUTION-CONFIRMED as a discovery hit (perf-30002): baseline batch total_keys=481,
             post-relock range-start scan total_keys=21145 skip=16384 (44x), owner-change
             trigger evidence TRUE. Built-in specificity: the same range-start shape at job
             start has skip~0 (nothing deleted) — the 44x is caused solely by the interruption.
status:      USED (caught perf-30002 in discovery with trigger evidence + within-run control);
             a blind held-out with an un-interrupted control run still owed for full TRUSTED.
```

## Coverage view (claim shape -> oracle)
```text
choice   (optimizer/binding picks best)      PO1  TRUSTED (single shape) / held-out done
work     (bounded-work claims)               PO2  HYPOTHESIS
reuse    (plan cache drift)                  PO3  TRUSTED (single shape) / held-out done
progress (background no-rework)              PO4  USED (perf-30001 txn backfill) — BLIND to DXF
                                                  ingest row_count (L8); DXF path GREEN (PF-4)
progress (TTL scan re-lock)                  PO5  USED (perf-30002 TTL re-lock re-seek)
progress/lifecycle (ANALYZE sub-jobs)        PO6  USED (perf-30003 / id30014 stale running job)
isolation(backpressure/stall correctness)    —    unregistered; needs metrics-diff harness (L3)
```

## Where the checkpoint discipline exists vs is missing (PS1 map, 2026-07-03)
```text
background progress loop            persisted checkpoint?   interrupted-rework?
txn backfill (add index, legacy)   NO (start_key frozen)   YES -> perf-30001 RED
TTL scan task                      NO (no scan-key field)  YES -> perf-30002 RED
ANALYZE partition sub-jobs         N/A for rework;          NO rework proven, but lifecycle RED:
                                   visible job lifecycle    started sub-job can remain RUNNING
                                   split across workers     with dead process_id -> perf-30003
DXF ingest backfill (fast-reorg)   YES (reorg_meta +       NO  -> PF-4 GREEN (boundary)
                                   adjustStartKey skip)
DXF file IMPORT INTO               YES (shared DXF ckpt)   untested (needs file infra)
IMPORT INTO FROM SELECT            N/A (inline, no task)   N/A
GC / auto-analyze                  unaudited               PS1 candidates for the next campaign
```
The pattern: modern DXF-framework tasks carry an engineered persisted checkpoint; bespoke/legacy
loops do not. PS1's next nominations should target the remaining bespoke loops, not DXF tasks.
PO1/PO3 passed held-out (sensitivity + specificity) on 2026-07-03; mining is unblocked.

## Observation-surface lessons (executed, 2026-07-03) — perf-loop analog of "trigger evidence goes all the way down"
Every one of these produced a real vacuous or polluted verdict before being closed with a clause:
```text
L1  tidb_enable_collect_execution_info was OFF cluster-wide -> every counter read 0; the first
    harness run certified SILENT on dead instruments. Clause: per-session enable + a SURFACE
    PROBE (full scan must report >= N keys) gates all verdicts.
L2  coprocessor cache serves a repeated identical cop request with proc_keys=0 -> the
    "repeat, take min" anti-retry clause picked exactly the polluted run. Clause: every
    execution carries a unique constant that reaches the cop request; zero-vs-nonzero spread
    across repeats => INVALID.
L3  TiDB bindings OVERRIDE in-statement hints -> with a binding active, every "alternative"
    arm silently ran the bound plan (all digests equal). Clause: alternatives run in a
    binding-free session. (Also makes bindings a legitimate injection surface for PO1.)
L4  hint-text fragment matching collided ("ignore_index(t ia)" contains "(t ia)") -> arm rows
    mixed. Clause: unambiguous fragments.
L5  constant propagation FOLDS a dummy predicate on an equality-constrained column
    ("a=12345 AND a>-d" loses the dummy) -> cop requests identical again -> L2 returns.
    Clause: the unique constant must ride inside a live range (b IN (1,-d)), not a foldable
    conjunct.
L6  IndexLookup table-side requests are keyed by handles and CANNOT be uniquified by query
    text -> any second execution of a big lookup plan is partially cached (observed 262144 ->
    20448). Clause: big-plan counters are trusted only on a first-ever (cold) execution;
    per-case fresh tables.
L7  (PF-3 TTL) the intended interruption primitive did not fire: tidb_ttl_scan_worker_count
    has MinValue=1 (setting 0 clamps to 1) AND resizeWorkers cancels workers[count:] while the
    running task always sits on workers[0] — so shrinking NEVER cancels the busy worker and no
    resign happens. Two runs certified INVALID (prev_owner never set) — the trigger-evidence
    guard caught it both times instead of emitting a false RED. Working primitive: inject a
    fake dead owner + stale owner_hb_time into mysql.tidb_ttl_task, which drives the real
    checkInvalidTask -> HB-timeout re-lock path. Lesson: an interruption oracle needs
    trigger evidence that the interruption ACTUALLY interrupted (owner changed / prev_owner
    set), exactly like a differential oracle needs proof its two arms diverged.
L8  (PF-4 DXF ingest) PO4's row_count conservation oracle is STRUCTURALLY BLIND to the DXF
    fast-reorg/ingest path: ADMIN SHOW DDL JOBS row_count stays 0 for the whole run and is set
    to N once at completion (never 2N), so the "final/N ratio" signal that caught perf-30001
    cannot fire here. Three runs certified INVALID (pause landed at row_count=0; no advancing
    fingerprint) — the guard refused to read a vacuous ratio=1.000 as GREEN. Compounding it:
    the ingest checkpoint flush ticker is 10s (checkpoint.go:596) and a single whole-table
    subtask advances its persisted watermark only near completion, so a short throttled run
    shows one constant checkpoint fingerprint — present but not visibly advancing. Lesson: the
    conservation oracle must be matched to the path's actual counter surface; for DXF ingest the
    right surface is the persisted checkpoint key advancement / ingested bytes, NOT job row_count.
L9  (PF-6 ANALYZE) row_count and stats visibility were the WRONG first oracle: ANALYZE explicitly
    allows partially persisted stats, so "some stats exist after interruption" is not a clean
    bug. The strong oracle is lifecycle coverage plus process liveness: after the parent ANALYZE
    returns, every started visible sub-job must be terminal, and no `running` row may reference a
    dead process_id. This caught perf-30003/id30014. Lesson: for multi-task background pipelines,
    the proof obligation may be Start/Finish ownership, not only checkpoint/resume work.
```
Meta: the counter surface fought back nine times in one afternoon. A perf oracle without an
instrument-health gate is not an oracle; it is a random-verdict generator with good intentions.

## Perf selector ledger (PS prefix; same rules as the correctness selector ledger)
```text
PS1: background-job interruption sibling paths (pause/resume, owner switch, restart, kill) x a
     progress/lifecycle claim whose state is reconstructed or finalized across components
     (checkpoint, row accounting, visible sub-job state).
     born from:   perf-30001 (first hit of the perf loop)
     predictions: PF2-A txn ADD INDEX pause/resume -> RED (the birth hit);
                  PF2-B ingest early-pause/resume -> green(triggered);
                  PF-3  TTL scan task HB-timeout re-lock -> RED (perf-30002) — DIFFERENT MODULE;
                  PF-6  ANALYZE partition sub-job kill-before-save -> RED (perf-30003/id30014)
                         — OBSERVABILITY/LIFECYCLE, not rework;
                  open: mid-ingest pause, owner-switch, restart variants
     status:      active, PROMOTED + REFINED. THREE hits (perf-30001 DDL txn backfill,
                  perf-30002 TTL scan, perf-30003 ANALYZE lifecycle) in three subsystems, and
                  ONE green boundary (PF-4 DXF ingest add-index). The boundary is the payoff:
                  the two rework RED targets are bespoke/legacy background loops that persist NO
                  usable progress checkpoint; the GREEN target runs on the DXF framework, which
                  HAS an engineered persisted checkpoint (mysql.tidb_ddl_reorg.reorg_meta +
                  adjustStartKey range-skip). PF-6 further refines PS1: even without a rework
                  symptom, a multi-task pipeline can fail the visible lifecycle obligation when
                  StartAnalyzeJob and FinishAnalyzeJob are owned by different components and a
                  kill happens before the handoff. REFINED selector: PS1's bug class is
                  "background progress/lifecycle loops that predate or BYPASS the DXF checkpoint
                  or lifecycle discipline", not "any interrupted background job." That tells the
                  next campaign WHERE to look (bespoke loops: GC, auto-analyze, legacy backfills)
                  and where not to (DXF-framework tasks).
                  Cross-loop origin: the correctness selector "sibling rollback/cancel path
                  reconstructs state" (id30009), now with 3 perf confirmations + 1 boundary.
```

## Campaign log
```text
2026-07-03  PF-1 plan-cache x partitioned (dynamic prune): gated at C0 — v8.4 build does not
            cache prepared plans on partitioned tables at all (Plan_from_cache=0 on identical
            params). Family closed green(guard-calibration). REOPEN trigger: any version/build
            where partitioned prepared plans become cacheable.
2026-07-03  PF-2 ADD INDEX pause/resume rework: RED on txn path (perf-30001, CLOSED-FIXABLE);
            ingest path green on early-pause shape; mid-ingest pause probe still open.
2026-07-03  PF-3 TTL scan re-lock rework (module switch, PS1 nomination): RED (perf-30002,
            CLOSED-FIXABLE). Re-locked scan task restarts from ScanRangeStart, re-seeks the
            deleted prefix (total_keys 44x a normal batch). Along the way, the reference
            differential (O6-style) KILLED a false lead: tidb_ttl_delete_rate_limit "only"
            22s for 131072 rows looked like a broken throttle, but official docs define its
            unit as delete-OPERATIONS/s (1 token per DELETE statement), so impl matches
            contract -> info(contract-ambiguous), NOT filed. Harness fought back once more
            (L7: worker-count interruption primitive is inert; cursor-min detector had an FN).
2026-07-03  PF-5 query-optimizer module, NON-checkpoint claim (work + choice), user-directed
            away from the PS1/progress class. Target-sourced diff-directed (recent planner
            commits) + D_dims battery (out-of-range, correlation). ALL GREEN — a justified
            downweighting of the optimizer partition/choice family on this build (v8.4/v9.0-dev):
              - partition pruning selection: dynamic==static across eq / IN / range / OR /
                function(a+0) / subquery-IN / null-safe(<=>) forms; func & subquery go
                partition:all in BOTH modes (by-design, not a gap).
              - null-safe equality pruning (diff-directed, fix #68425 neighborhood): const-left
                `25000<=>a`, `a<=>NULL`->p0, OR-of-<=> all prune to the right single partition.
                Fix is complete for these forms.
              - TopN `ORDER BY b LIMIT 10` on 8-partition table: Limit build reads 80 (=10x8),
                cop fan-out bounded at 8 (matches #67676). Correct.
              - dynamic vs static WORK (processed_keys) identical (80000 for a range query).
              - correlated columns (k==v): optimizer picks the MORE selective single index
                (iv 2622 keys < ik 5254). Correct.
              - out-of-range estimate (est=1 vs real 20000, 20000x under): (a) join case ->
                optimizer picks HashJoin, does NOT fall into a degenerate index join (the
                #67646 class hazard); (b) aggregate case -> IndexLookup chosen and it is
                genuinely CHEAPER than full scan (RU 41 vs 152 cold), so the underestimate still
                landed on the right plan.
            Discipline note: did NOT manufacture a marginal finding. The one hypothesis that
            looked live (out-of-range -> index-vs-scan mis-choice) was REFUTED by an actual
            cost measurement (RU/time), the same way O6 refuted the ttl rate-limit false lead.
            Reopen: cop-request in-flight CONCURRENCY / burst (the #67676 territory) is NOT
            observable in slow_query/EXPLAIN — it needs the metrics-diff harness (the L3
            isolation-oracle gap). That, not the choice/work paths, is where the remaining
            query-layer perf density likely sits. Cost-oracle-only mis-choices (random-vs-
            sequential) remain a PO1 blind spot by design.
2026-07-03  PF-4 third-module attempt via IMPORT INTO / DXF (PS1 nomination): GREEN boundary
            (no bug). Findings: (a) IMPORT INTO ... FROM SELECT is an INLINE pipeline
            (importFromSelect, errgroup in-session) — no DXF task, no checkpoint, not a PS1
            target; file-based IMPORT INTO uses DXF but needs infra unavailable here. (b) The
            shared DXF backfill framework (reached via fast-reorg add index) PERSISTS a
            checkpoint (reorg_meta + adjustStartKey range-skip, LoadCheckpoint on resume) —
            source-confirmed + partial execution (checkpoint blob present & non-empty across the
            run; contrast perf-30001's txn start_key FROZEN at handle 1). Not fully
            execution-verified GREEN: PO4 row_count oracle is blind to DXF ingest (L8) and the
            10s flush + single-subtask watermark hid checkpoint advancement in a short run.
            Reopen: multi-region table + longer throttle to observe watermark advance, or real
            multi-node failover, would fully verify. Net value = the PS1 boundary refinement.
2026-07-03  PF-6 ANALYZE/auto-analyze candidate (PS1 refined nomination): RED, but a different
            symptom class from perf-30001/30002. Partitioned ANALYZE has visible per-partition
            `mysql.analyze_jobs` rows. With `analyzeBeforeSendToSaveResults=2*off->pause`,
            first partition results finish, a later result is paused before `saveResultsCh <-
            results`; `KILL QUERY` then returns `ERROR 1317`. Two clean runs left one partition
            row in `running` with a dead `process_id` (processlist count 0), and `SHOW ANALYZE
            STATUS` kept showing the old row as running with huge remaining time. A clean rerun
            appends finished rows but does not immediately clear the stale running row. Source
            chain: `StartAnalyzeJob` in `analyzeWorker`; `FinishAnalyzeJob` for successful
            results is owned by the save worker; the kill check before handoff closes
            `saveResultsCh` and returns, so an already-started but unsent result bypasses
            `finishJobWithLog`. Draft: `/Users/bba/pc/ai-native-perf-analyze-interrupt-running-job-draft.md`.
            Probe: `/Users/bba/pc/ai_native_perf_pf6_analyze_interrupt.py`. Method upgrade:
            PS1 now includes visible sub-job lifecycle coverage, not only checkpoint/rework.
```
