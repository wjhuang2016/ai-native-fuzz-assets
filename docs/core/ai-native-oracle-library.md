# Oracle Library
> Started 2026-07-02 per methodology v2. Symmetric to the selector ledger: oracles are first-class mining products. Each is derived from some Q_claim, records which symptom classes it catches and is blind to, and is TRUSTED only after held-out shows both sensitivity (fires on the injected bug) and specificity (silent on controls).

## Status legend (evidence tiers, weakest to strongest)
- REFUTED: adversarial verification found a reproduced FP or FN; the form is unsound. Replace it.
- HYPOTHESIS: registered in a suite, never validated on a live injection.
- USED: has caught a real bug in discovery, but not yet run through a held-out injection.
- LLM-VERIFIED: survived adversarial LLM self-verification (a skeptic tried to refute the form
  against its obligation and its counterexamples were addressed) — but those counterexamples were
  not all executed. Stronger than untested, weaker than TRUSTED because of same-source bias.
- TRUSTED: adversarial review AND held-out execution verified sensitivity + specificity.

Two evidence axes are tracked per oracle: `llm-review` (adversarial skeptic pass) and `held-out`
(executed injection). TRUSTED requires both. LLM review is automatic on every newly mined oracle
and generates held-out tickets from the counterexamples it finds; it does not replace execution.

## Oracle discipline: a loud symptom does not close the obligation

id30038 taught this the hard way. The loud oracle ("did the operation error?") fired on a false
duplicate, and the family was about to be graded a consequence-1 wrong-error — but the same defect
also wedged the online DDL in a retry loop (`invalid encoded key`, ErrCount climbing, stuck in
write-reorg), a liveness consequence-3 that only a state-observing oracle could see. Standing rule
for every invariant-bypass / state-transforming target:

- Run the SILENT-consequence oracle regardless of whether a loud error fired. The invariant check
  — ADMIN CHECK, uniqueness `GROUP BY ... HAVING COUNT(*)>1`, row-multiset/`COUNT(*)`, FK-orphan
  scan, ADMIN CHECKSUM differential, and DDL-job liveness (O28) — must run even when the operation
  already returned an error or "succeeded".
- A loud wrong-error must not close the family or set the consequence grade until the silent
  oracle has run and come back clean. This is trigger-evidence-all-the-way-down applied to
  severity: the campaign proves the loud symptom AND rules out (or finds) the hidden silent one.
  A consequence-1 grade is only valid after the silent oracle was clean.

## O1 admin_check_consistency
```text
obligation:  the index encodes exactly the rows of the base table (storage self-consistency)
form:        ADMIN CHECK TABLE t ; nonzero / 8223 => fired
catches:     data-inconsistency (index-vs-record encoding) for NON-columnar indexes
blind to:    planner/extractor wrong-result, metadata staleness, hang, AND (from the skeptic):
             record-vs-partition-definition, columnar/vector indexes
sensitivity: held-out run #1, fired 3/3 on skipReorgWorkForTempIndex (SINGLE shape)
specificity: run #1 silent 3/3; adversarial review CONFIRMED soundness of the narrow claim —
             120 concurrent ADMIN CHECK runs under live DML gave 0 spurious 8223 (FastCheckTable
             reads both sides in one snapshot), and tricky types (float/decimal/bit/enum/json/
             trailing-space char/embedded-NUL varbinary) gave 0 spurious mismatch.
status:      TRUSTED as a NARROW index==record checksum (IFF confirmed by adversarial review).
             REFUTED as a "table is healthy / query is correct" certificate — this is SCOPE
             overreach, not unsoundness. Two reproduced/proven scope blind spots below.
scope blind spots (skeptic, 2026-07-02):
  - SB-1 (reproduced): EXCHANGE PARTITION ... WITHOUT VALIDATION injects a range-violating row;
    ADMIN CHECK passes (index==record holds within the partition) but `WHERE partcol=<misplaced>`
    prunes to dual and silently loses the row while a full scan returns it. O1 never checks
    record-vs-partition-definition. (Note: WITHOUT VALIDATION is a user opt-out, so this is an
    ORACLE scope gap, not a product bug — do not file it.)
  - SB-2 (source-proven, not run — no TiFlash): check_table_index.go:150
    `if idx.MVIndex || idx.IsColumnarIndex() { continue }` skips columnar/vector indexes entirely;
    a divergent columnar index is never examined by ADMIN CHECK.
complement tickets: mine a partition-placement oracle (every row satisfies its partition's range)
             and a columnar-index consistency oracle; O1 does not cover these obligations.
noise:       none observed. Release-build caveat: verifyIndexSideQuery guard is intest-only, so
             the "index side actually scanned the index" assumption is unguarded in production.
```

## O2 index_vs_table_rowset
```text
obligation:  USE INDEX and IGNORE INDEX return the same row set
form:        COUNT(*)/rowset USE INDEX(i) vs IGNORE INDEX(i) ; differ => fired
catches:     data-inconsistency, index-caused wrong-result (id30001 partial index)
blind to:    wrong-result with no index involved (id30002), non-user-table surfaces
sensitivity: run #1 fired 3/3 (SINGLE shape); also caught id30001 in discovery
status:      REFUTED as a COUNT(*) form — obligation is sound and singular (both paths, same rows),
             but the form is too weak on three axes (all reproduced by execution 2026-07-02):
  CE-1 FN structural: multi-valued (JSON array) index — TiDB does not honor USE INDEX(i) for a
     bare COUNT; both arms collapse to byte-identical TableFullScan, so the index is never
     exercised and the oracle can never fire. (Normal / clustered / expression / hash-partition
     indexes DO honor the hint — verified plans diverge — so the blind class is specifically MVI.)
  CE-2 FN weak projection: under LIMIT, USE returns {4,5} and IGNORE returns {1,2} — disjoint rows,
     equal COUNT=2. COUNT(*) is not injective over row sets; any cardinality-preserving divergence
     is invisible. Also makes Q ill-posed for LIMIT / non-deterministic queries.
  CE-3 FP concurrency: the two arms are separate statements => separate snapshots under autocommit;
     an interleaved INSERT makes counts differ with no bug.
honest negative: for a plain filtered COUNT(*), same snapshot, no LIMIT, no concurrency, the two
     hints agreed in every case tried (NULLs indexed, expression index, hash partition) — so the
     form is sound only in that narrow window it was validated on.
```

## O2' rowset_hash_same_snapshot (hardened O2 — same obligation, stronger form)
```text
obligation:  same as O2 (USE INDEX(i) and IGNORE INDEX(i) yield the same rows) — SINGULAR, unchanged
form:        (1) TRIGGER EVIDENCE: EXPLAIN must show the two arms take DIFFERENT access paths;
                 if plans are identical (e.g. MVI, hint not honored) the cell is INVALID, not pass.
             (2) compare an ORDERED ROW HASH, not COUNT — e.g. sorted GROUP_CONCAT / per-row hash
                 under a total ORDER BY, so cardinality-preserving divergence cannot hide.
             (3) run both arms in ONE snapshot (single read-only txn) to remove concurrency FP.
             (4) require a deterministic query: total ORDER BY, no bare LIMIT.
             (5) if either arm errors or returns no result row, the cell is INVALID, not "equal"
                 — an added clause found by executing O2' (see harness lesson below).
llm-review:  each clause addresses one reproduced CE (1->CE-1, 2->CE-2, 3->CE-3, 4->CE-2 ill-posed).
held-out:    EXECUTION-VERIFIED (ai_native_oracle_o2prime_verify.py) on three shapes:
             A normal index no-bug -> GREEN; B multi-valued-index COUNT -> INVALID (old O2 gave a
             vacuous pass here — CE-1 closed); C partial-index id30001 real bug -> RED (sensitivity
             kept). CE-2/CE-3 excluded by construction (clauses 3-4); a full blind harness with
             those shapes is still pending.
status:      TRUSTED on the verified shapes (CE-1 closed + sensitivity + specificity by execution);
             promote to fully TRUSTED after a blind run that also exercises CE-2/CE-3.
harness lesson: verifying O2' EXPOSED a vacuous pass in the verifier ITSELF — `START TRANSACTION
             READ ONLY` is a noop-error in TiDB, so both arms failed and the empty results compared
             "equal" => false GREEN on a real bug. Execution caught what the LLM derivation missed.
             Fix = clause (5). Trigger-evidence is recursive: even the harness that checks an oracle
             must prove its arms actually ran.
noise:       ordered hash over large results is costly; bound the probe query.
extension:   2026-07-09 IndexMerge planner calibration used the same rowset obligation with
             `USE_INDEX_MERGE(...)` as the fast arm and `NO_INDEX_MERGE()` as the safe arm. Four
             triggered cases (top-level AND composite ranges, residual expression, binary collation
             residual, branch-local CNF) all matched. For this subtype, EXPLAIN must prove
             `IndexMerge` fired; `ADMIN CHECK` is only a storage sanity control, not proof that the
             planner predicate was preserved.
             2026-07-09 MVI extension: for multi-valued-index IndexMerge, the fast arm may be
             `USE_INDEX_MERGE(...)` while the safe arm can be `IGNORE_INDEX(...)` over every MVI and
             sibling normal index used by the proof. Trigger evidence must show the MVI-specific
             `IndexMerge` shape (intersection/union plus index names) because old COUNT-style MVI
             oracles were vacuous when the hint was not honored. Parameterized cache probes add a
             separate requirement: `@@last_plan_from_cache=1` plus mismatch against current direct
             reference is required for RED; no cache hit is INVALID_NO_CACHE_HIT, not GREEN.
             2026-07-11 DDL amend-path extension: when probing "txn commits while new indexes are
             being created", COUNT parity is too weak even if `ADMIN CHECK TABLE` stays green.
             The hardened form for success-path amend probes is:
             (1) compare exact ordered rowsets from each fresh index path against the table path,
             not only counts; (2) run the comparison for every newly created index, not only one
             representative index; (3) for generated/stored-derived indexes, project the derived
             column itself in the reference rowset so a stale base->derived mapping cannot hide
             behind base-column equality. This extension was exercised on live
             `multi-add-index-rich` and `add-generated-index-rich` probes and stayed GREEN on the
             commit-success cells, making the later REDs credible availability splits rather than
             weak-oracle artifacts.
```

## O3 case_wrapped_equivalence
```text
obligation:  WHERE P == WHERE (CASE WHEN P THEN 1 ELSE 0 END = 1) — predicate self-consistency
form:        COUNT plain vs COUNT case-wrapped ; differ => fired
catches:     predicate-simplification / planner wrong-result (id30002), extractor prefilter drop
blind to:    data-inconsistency (storage), hang, panic
sensitivity: TRUSTED — held-out run #2, fired 3/3 on predicate-simplification injection
specificity: TRUSTED — run #2 silent 3/3 on controls
noise:       low; requires deterministic query
origin note: mined from Q_claim (simplification must preserve 3-valued semantics). Run #2
             surfaced the FN that this oracle closes; the derivation belongs to the proof
             obligation, not to a human spotting the miss.
```

## O4' no_shortcut_reference / scalar_recheck_differential (hardened O4)
```text
obligation:  when a shortcut/extractor consumes a SQL predicate and removes or replaces scalar
             evaluation, the shortcut rowset must equal the SQL-visible predicate rowset.
form:        fast arm: query shape with trigger evidence that the shortcut consumed P.
             reference arm: equivalent scalar re-check that the shortcut cannot consume, such as
             CASE WHEN P THEN 1 ELSE 0 END = 1, explicit re-check, cache-disabled/direct path, or
             sibling safe path.
             self-predicate evidence: when the fast arm returns rows, project P itself (or its
             scalar components) and require every returned row to evaluate P as true.
             compare an ordered rowset/multiset, not only COUNT.
catches:     extractor/shortcut wrong-result with settled SQL contract (id30010 InfoSchema LIKE,
             id30018 InfoSchema LOWER/UPPER scalar pushdown, id30019 metrics_summary
             METRICS_NAME normalization, id30021 statements_summary coarse interval skip,
             id30022 tikv_region_peers backend not-found error-domain shortcut,
             id30023 tidb_hot_regions_history timezone render/request split,
             id30026 tikv_region_peers negative ID type-domain conversion,
             id30027 inspection_result cluster_config cache key granularity,
             id30030 cluster_log LIKE custom ESCAPE loss,
             id30031 information_schema TABLE_NAME custom ESCAPE loss).
             Also detects contract splits such as id30013, but those classify as INFO until O6 or
             an owner contract settles the product-specific exemption.
blind to:    storage inconsistency, hang
sensitivity: EXECUTION-HARDENED, not fully TRUSTED — caught id30010 in discovery and the 2026-07-03
             O4' tick reproduced the RED with trigger evidence: fast plan had
             `table_name_pattern:[a_%]`, reference plan kept scalar `Selection`, and `Acase` had
             scalar `table_name LIKE 'a_%' = 0` while the fast arm returned it.
             Later id30018 reused the same oracle shape with stronger self-predicate evidence:
             `LOWER(table_name)='ACASE'` and `UPPER(table_name)='acase'` returned `Acase`, while
             the projected predicate was `0` and the CASE-wrapped reference returned no rows.
             id30019 extended the same oracle to a second owner: `METRICS_NAME='TIDB_QPS'`
             returned `tidb_qps` from `information_schema.metrics_summary`, while projected
             `metrics_name='TIDB_QPS'` was `0`, the CASE-wrapped reference returned no rows,
             and the matching-case control `metrics_name='tidb_qps'` returned `1`.
             id30021 extended O4' to a skip shortcut: `summary_begin_time <= A AND
             summary_end_time >= B` triggered `skip_request:true` and returned 0 rows, while the
             CASE-wrapped reference returned rows whose projected predicates were all true.
             id30022 extended O4' to backend error-domain shortcuts: fast `region_ids:[0]`
             errored with a PD 400 response while the CASE reference returned 0 rows; mixed
             `region_id IN (0,2)` errored while the reference returned the valid region-2 rows.
             id30023 extended O4' to request/render context splits: under `time_zone='+14:00'`,
             the fast `update_time` range returned rows displayed as UTC `2026-07-02 23:40:41`
             with projected predicate sum 0, while the CASE self-recheck returned 0 rows.
             id30026 extended O4' to type-domain conversion shortcuts: `region_id=-1` and
             `store_id=-1` on `information_schema.tikv_region_peers` returned the full 269-row
             peer table after the extractor dropped Selection, while CASE-wrapped references
             returned 0 and returned rows projected the predicate as false.
             id30027 extended O4' to shortcut caches: a direct
             `cluster_config WHERE type='tikv' AND key='foo-test'` reference returned only
             `tikv-a,tikv-b`, but `inspection_result` generated a `type='tikv'` config detail
             containing `tidb-a`. Trigger evidence showed the detail query consumed
             `type='tikv'` into `node_types:["tikv"]`, leaving only `key` as scalar Selection,
             while the inspection cache was keyed only by table name.
             id30030 extended O4' to pattern syntax shortcuts: fast
             `cluster_log WHERE message LIKE '%#_%' ESCAPE '#'` consumed the predicate and
             returned 0 rows, while the CASE scalar reference returned 130683 rows whose
             predicate evaluated true. The default-escape control stayed green.
             id30031 confirmed the same omitted `ESCAPE` input across a second extractor owner:
             fast `information_schema.tables WHERE table_name LIKE '%#_%' ESCAPE '#'`
             returned `abc#x` with projected predicate `0`, while the CASE reference returned
             `abc_def` with projected predicate `1`; the default-escape control stayed green.
specificity: EXECUTION-HARDENED — the same tick ran a triggered lowercase-control GREEN:
             fast arm still used `table_name_pattern:[a_%]`, reference arm kept scalar Selection,
             and both rowsets were `a_b,a_c`.
invalid guard: the tick also ran a non-equivalent `LOWER(table_name) LIKE 'a_%'` reference; it
             returned `Acase` by design and is classified INVALID, not GREEN. This closed the
             common false-positive/false-green trap "looks like a bypass, but changed semantics."
info guard:  `cluster_log.type='PD'` vs `type LIKE 'PD'` produced a trigger-evidenced split
             (`node_types:["pd"]` vs scalar LIKE; 63844 `pd` rows vs 0). O4' classifies this as
             INFO(contract-ambiguous) and routes it to O6/reference differential; O4' alone must
             not promote it to confirmed.
classification:
  RED: fast triggered, reference bypassed, both arms succeeded, contract settled, rowsets differ
       or the fast arm returns any row whose projected predicate is false. A triggered empty fast
       arm such as `skip_request:true` is RED when the CASE reference proves satisfiable rows.
       A triggered fast arm that errors on backend object-not-found is RED when the equivalent SQL
       predicate reference succeeds with an empty or partial rowset.
       A triggered fast arm that drops scalar recheck after request-domain conversion is RED when
       converted values are impossible/invalid and the CASE or self-predicate reference differs.
       A triggered cache fast arm is RED when the cache key omits extractor-consumed dimensions
       and a direct reference query proves the cached diagnostic/detail row includes rows outside
       the requested semantic slice.
       A triggered pattern fast arm is RED when it compiles or normalizes the pattern with fewer
       semantic inputs than SQL scalar evaluation uses, such as ignoring a custom LIKE ESCAPE.
  GREEN(triggered): fast triggered, reference bypassed, both arms succeeded, rowsets equal.
  INVALID: no trigger evidence, reference also consumed, non-equivalent reference, unstable data,
           arm failed unexpectedly, or only COUNT equality.
  INFO: rowsets differ but SQL/product contract is ambiguous; route to O6/reference differential.
status:      USED + EXECUTION-HARDENED. Keep below TRUSTED until an independent blind/held-out run
             includes RED injection/known positive, triggered GREEN, INVALID(untriggered or
             non-equivalent), and INFO contract-split controls.
noise:       must confirm the reference form truly bypasses the shortcut and preserves the same
             SQL-visible predicate semantics.
```

## O5 absolute_instant_equivalence
```text
obligation:  the same literal window under two session time zones selects different absolute
             instants (a time filter must honor session time_zone)
form:        same literal window at +00:00 vs +14:00 ; identical row set => fired
catches:     time-range extractor wrong-result (id30012 cluster_log time.Local)
blind to:    non-temporal semantics
sensitivity: USED — caught id30012; not yet held-out injected
specificity: USED — reverse test (tz-respecting literal returns 0) confirmed
noise:       needs populated windows on both sides
```

## O6 reference_implementation_differential (MySQL)
```text
obligation:  a general SQL contract (collation, NULL, coercion) matches the reference engine
form:        same minimal semantics on MySQL / ordinary table ; differ => contract violated
catches:     contract-ambiguous semantics (id30013 bin-collation =)
blind to:    product-specific surfaces the reference lacks (cannot rule on exemptions)
sensitivity: USED — split id30013's settled contract from its product exemption
specificity: n/a (a splitter, not a binary detector)
noise:       compare the underlying rule on an ordinary table, not the product-specific surface
```

## O7 reference_liveness
```text
obligation:  a maintained reference resolves, or DDL rewrote/blocked/cleaned it
form:        SHOW CREATE / catalog lookup vs live object existence ; dangling => fired;
             plus consequence (CREATE TABLE error family)
catches:     metadata-error, dangling references (id30011 flashback placement, id30005 sequence)
blind to:    wrong-result, hang
sensitivity: USED — caught id30011, id30005
specificity: USED
noise:       must distinguish name-bound policy from maintained object-identity refs
```

## O21 side_state_owner_remap
```text
obligation:  after move/rekey/ID-swap DDL, side metadata keyed by object ID still belongs to the
             logical owner exposed by its public management surface, or the DDL is blocked.
form:        before DDL, prove the side row is operable through the logical owner; after DDL,
             map side-row owner IDs to current information_schema object IDs and run a management
             round trip such as DISABLE/ENABLE/DROP/recreate. A row whose ID now belongs to another
             object, or to no manageable logical owner, fires only when the behavior round trip also
             fails or leaves an orphan.
catches:     stale owner/container side state after ID swaps or moves (id30017 stats lock,
             id630014 masking policy, id30039 persisted analyze options)
blind to:    purely historical/advisory rows, intentionally name-bound policy text, storage rowset
             corruption
sensitivity: USED — caught id630014. After EXCHANGE PARTITION, the masking-policy row kept
             table_name=nt but table_id equal to pt.p0's tidb_partition_id; DISABLE/DROP by nt and
             pt failed, and recreating the policy created a second row while the old row stayed
             ENABLED. Also caught id30039: old `pt.p0` analyze_options became current `nt`
             policy and future `ANALYZE TABLE nt` consumed the inherited column list.
specificity: USED — basic masking-policy rename/drop/truncate matrix stayed green, including the
             truncate helper that remaps table_id and preserves operability.
classification:
  RED: side-row ID no longer resolves to the logical owner, and the management round trip cannot
       reach the old row or updates only a newly-created row.
  GREEN: IDs/names are remapped or blocked, and the management round trip affects exactly one live
       owner with no stale row left behind.
  INVALID: side state is documented historical/advisory text, no pre-DDL operability is proven, or
       the post-DDL management surface is unavailable by product design.
status:      USED. Needs held-out/adversarial verification before TRUSTED.
noise:       ID-only survival is insufficient. Always pair mapping evidence with a user-visible
             management or behavior oracle. The behavior oracle can be a future consumer
             (`ANALYZE TABLE`), not only a cleanup command.
```

## O8 delete_range_keyprefix
```text
obligation:  cleanup enqueues delete-range records under the correct table/partition key prefix
form:        decode mysql.gc_delete_range key prefixes vs expected owner ; mismatch => fired
catches:     metadata-cleanup (id30009 add-index global rollback)
blind to:    everything else
sensitivity: USED — caught id30009
specificity: USED
noise:       needs GC disabled to read the enqueued rows deterministically
```

## O9 metadata_sync_check — MISCATEGORIZED (it is a DISPLAY oracle, was used as a correctness oracle)
> Deeper diagnosis (2026-07-02, after execution): the skeptic's counterexamples are real, but the
> root problem is not "the form is unsound" — it is that O9 was asked to test the wrong obligation.
> Verified: after `RENAME COLUMN a TO aa`, hist_id (column ID) is unchanged and the recomputed NDV
> matches — the stats VALUE is correct; only the SHOW-rendered NAME is stale. So id30003 (rename) is
> a pure DISPLAY bug, while FN-1 (drop+re-add) is a VALUE/correctness bug. These are two different
> obligations and need two oracles. O9's name-set form is a valid DISPLAY-consistency oracle; it is
> simply not a stats-correctness oracle. The "FN-1 miss" and "FP-1 false alarm" only appear when O9
> is (mis)used to judge correctness. Lesson upgrade: a refutation often reveals a conflated
> obligation, not a broken form — the fix is to SPLIT the obligation and assign an oracle to each,
> not to swap the form.
```text
obligation:  DISPLAY-consistency — the SHOW surface renders only live column names
form:        {names in SHOW STATS_HISTOGRAMS, Is_index=0} - {live information_schema.columns}
             nonempty => fired
valid for:   rename display staleness (id30003). Correct as a DISPLAY oracle: after rename, SHOW
             shows the old name while the value is fine, which is exactly a display defect.
NOT valid for: stats-value correctness. When (mis)used there it shows a durable FN (drop+re-add
             same name: name-set matches, value wrong) and a benign FP (drop-only: SHOW shows a
             dropped column briefly = real display lag, but self-heals via gcTableStats).
noise:       drop-only column produces a transient display mismatch that GC reaps; distinguish
             persistent display staleness (rename) from self-healing lag (drop) by timing.
held-out:    run #3's "3/3, 0 FP" was single-shape + a re-analyze timing artifact; re-run with the
             obligation stated as DISPLAY-consistency and controls that separate rename from drop.
```

## O9' stats_value_differential (COMPLEMENT to O9, not a replacement — different obligation)
```text
obligation:  cached stats for each LIVE column match a fresh recomputation (value, not name)
form:        for each live column c: stats NDV (SHOW STATS_HISTOGRAMS) vs SELECT COUNT(DISTINCT c)
             differ => fired. Iterate LIVE columns only (so dropped-column GC lag is out of scope).
catches:     metadata/stats staleness including name-collision cases O9 missed (id30003 family)
blind to:    non-stats surfaces; bucket-bound staleness when NDV coincidentally matches (needs a
             bucket-value check on a cluster with tidb_stats_cache_mem_quota>0 — held-out ticket)
obligation:  VALUE correctness — cached stats for each live column match a fresh recomputation.
             This is a SEPARATE obligation from O9's display-consistency, not a better form of it.
llm-review:  derived FROM the O9 refutation; catches the value/correctness class O9 structurally
             cannot (name collision). Does NOT catch pure display staleness (rename) — that is O9's
             job. The two together cover the split obligation.
held-out:    TRUSTED — multi-shape blind run ai_native_heldout_o9prime.py (seed 20260702):
             tp=3 fn=0 fp=0 tn=3 (FN_RATE=0, FP_RATE=0) across drop+re-add bugs and controls,
             PLUS a scope-boundary check: 2 rename probes stayed SILENT (0 violations), proving
             O9' does not overreach into O9's display obligation. The O9/O9' split is clean and
             non-overlapping.
status:      TRUSTED on value-staleness shapes with verified scope boundary.
known gap:   bucket-level staleness after a type change where NDV coincidentally matches — needs a
             bucket-bound oracle on a cluster with tidb_stats_cache_mem_quota>0 (open held-out ticket).
noise:       COUNT(DISTINCT) on large tables is costly; sample or bound for big tables.
```

## O10 cache_disabled_volatile_reexecution
```text
obligation:  enabling a result/cache shortcut must not change volatile expression evaluation
             semantics. Reused payload must be a pure function of the cache key.
form:        fast arm: query with trigger evidence that the cache is ON.
             reference arm: same query with the cache disabled.
             red payload: volatile expression with effectively collision-free output, such as
             UUID(), over duplicate cache keys.
             green control: deterministic expression over the same duplicate keys must remain
             stable in both arms.
catches:     cache/reuse wrong-result where the key covers correlated inputs but the cached value
             contains non-deterministic computed results (id30020 Apply cache + UUID()).
blind to:    deterministic cache-key incompleteness, storage inconsistency, pure performance bugs.
sensitivity: USED — caught id30020. With Apply cache ON, groups with n=24/16 outer rows had
             COUNT(DISTINCT UUID())=1/1; with cache OFF the same groups had 24/16. Trigger
             evidence showed `cache:ON` and `cache:OFF`.
specificity: USED — deterministic `CONCAT('v', inner_t.a)` control stayed distinct=1 per key in
             both cache modes, proving the oracle is about volatility rather than ordinary
             duplicate-key reuse.
classification:
  RED: cache triggered, cache-disabled reference triggered, volatile distinct count collapses
       under cache ON and restores under cache OFF, deterministic control stays green.
  GREEN(triggered): cache triggered but volatile output distribution matches cache-disabled
       reference and deterministic control stays green.
  INVALID: cache did not trigger, reference did not disable it, result expression is not volatile,
       or deterministic control fails.
status:      USED. Promote only after a held-out run with an injected cache-purity bug and at least
             one triggered no-bug cache control.
noise:       avoid RAND() as the primary proof because collisions/statistical thresholds invite
             ambiguity; UUID() gives a cleaner oracle.
```

## O11 cache_hit_flush_reference
```text
obligation:  a cached plan/result must reflect session/config switches that affect expression
             semantics during plan construction, and must not reuse folded/evaluated values when
             a coarse cache key omits semantic details needed by that value.
form:        fill cache under switch value A, prove hit with @@last_plan_from_cache=1, change the
             switch to B, execute the same statement, then compare against the same statement after
             ADMIN FLUSH SESSION PLAN_CACHE or with plan cache disabled.
red:         cached execution returns A semantics while the flush/off-cache reference returns B
             semantics.
catches:     prepared plan cache freezing scalar-function semantics across session switch changes
             (id30024: tidb_sysdate_is_now OFF->ON and ON->OFF), and prepared plan cache reusing
             a timezone-folded `UNIX_TIMESTAMP` literal across named zones with the same current
             offset but different historical rules (id30025).
blind to:    switches included in the cache key, statements made uncacheable by observability
             columns, and pure optimizer knobs where different plans are both semantically valid.
sensitivity: USED — caught id30024. OFF->ON: cached hit kept `sysdate(6)=now(6)` at 0; flush
             reference returned 1. ON->OFF: cached hit kept 1; flush reference returned 0. Caught
             id30025: Johannesburg->Amsterdam kept `1736935200` on hit while flush returned
             `1736938800`; reverse direction symmetrically kept the old value.
specificity: GOOD — timezone-offset TIMESTAMP range candidate was GREEN because the cache hit
             rebuilt ranges under the current session timezone. id30025's summer-date control
             also stayed green when the two zones had the same historical offset.
classification:
  RED: cache hit is proven, switch value changed, cached semantic result differs from the
       same-statement flush/off-cache reference.
  GREEN(triggered): cache hit is proven and cached result matches reference under the changed
       switch.
  INVALID: no cache hit, observation variables/functions made the query uncacheable, or no
       switch-sensitive oracle exists.
status:      USED. Needs held-out injection for TRUSTED, but has one RED and one GREEN calibration.
noise:       do not put `@@var` in the cached SELECT list; check it in a separate statement.
```

## O12 direct_vs_prepared_semantic_switch
```text
obligation:  a prepared statement must not silently bypass current-session semantic validation
             when direct execution of the same SQL under the current switch is rejected or warned.
form:        reference arm: direct SQL under the current session switch.
             reuse arm: PREPARE under switch value A, change to value B, EXECUTE the existing
             prepared statement.
             freshness controls: run `ADMIN FLUSH SESSION PLAN_CACHE` or disable prepared plan
             cache to prove the result is not merely physical plan cache reuse.
             internal control: a sibling switch/test where prepared EXECUTE is known to honor
             changed session semantics, when available.
red:         direct current-session execution errors or warns, while existing prepared EXECUTE
             succeeds silently or produces the old validation behavior.
catches:     prepared/preprocessor semantic freeze (id30028: `tidb_enable_noop_functions=OFF`
             rejects direct `SQL_CALC_FOUND_ROWS` and `GROUP BY expr DESC`, but statements
             prepared under ON continue to execute under OFF even after plan-cache flush).
             It also detects contract-ambiguous prepared DDL AST mutation splits such as id30029:
             direct strict CREATE TABLE rejects an overlong VARCHAR, while PREPARE under
             non-strict mode rewrites it to TEXT and later EXECUTE under strict mode succeeds.
blind to:    cases where the product contract intentionally freezes semantics at PREPARE time,
             switches whose validation is deliberately compile-time only, and statements whose
             direct/reference form is not semantically equivalent.
sensitivity: USED — caught id30028. Direct OFF returned error 1235 for both
             `SQL_CALC_FOUND_ROWS` and `GROUP BY expr DESC`; prepared ON->OFF returned rows with
             warning_count=0. After `ADMIN FLUSH SESSION PLAN_CACHE`, the same prepared statement
             still returned rows with `@@last_plan_from_cache=0`. Also flagged id30029 as
             candidate: direct strict returned error 1074, prepared non-strict -> strict created
             `mediumtext`; this remains INFO/CANDIDATE until the prepared DDL sql_mode contract
             is settled.
specificity: GOOD — the sql_mode/ONLY_FULL_GROUP_BY session-state control shows changed session
             semantics can be expected to affect prepared execution in at least one nearby class.
classification:
  RED: direct current-session reference rejects or warns, prepared reuse arm does not, and
       cache-flush/off-cache controls prove the stale behavior survives physical plan rebuild.
  INFO/CANDIDATE: direct and prepared differ, but PREPARE itself emitted the relevant warning or
       product semantics may intentionally freeze DDL normalization at PREPARE time.
  GREEN(triggered): prepared reuse arm matches the direct current-session reference after the
       switch changes.
  INVALID: direct and prepared SQL are not equivalent, the reference did not run under the changed
       switch, the prepared statement was implicitly re-prepared by schema change, or no freshness
       control separated AST/preprocess freeze from plan-cache reuse.
status:      USED. Needs held-out injection and more sibling-switch controls before TRUSTED.
noise:       the main risk is product-contract ambiguity. Use direct current-session behavior plus
             existing prepared/session-state tests to argue the intended contract before filing.
```

## O13 rowset_cardinality_invariant
```text
obligation:  a DDL that reorganizes storage without deleting logical rows must preserve the visible
             table row multiset/cardinality.
form:        create a small pre-DDL row multiset, record `COUNT(*)` and ordered rows when stable,
             run the DDL, then compare `COUNT(*)` and rows after the DDL.
             Use partition-specific or internal-key queries only to prove the trigger state.
red:         the DDL succeeds but the visible full-table row count or row multiset changes.
catches:     data-loss bugs in DDL reorganization paths where the storage rewrite treats an
             ambiguous row as already copied. Used by id600001: `REORGANIZE PARTITION` changed
             `COUNT(*)` from 2 to 1 after two old partitions contained identical
             `(a,b,_tidb_rowid)` rows.
blind to:    intentional DDLs that delete/filter rows, duplicate SQL rows that are not observable
             as distinct by the chosen projection unless count is included, and cases where a
             later index/query bug hides rows only under a specific access path.
sensitivity: USED — caught id600001. The red cell was same target rowid + same raw row bytes
             across different old partitions. Guard cells stayed green for ordinary reorg,
             same rowid with different raw bytes, and same raw bytes with different rowid.
specificity: GOOD for row-preserving DDL. The green controls show the oracle is not just detecting
             `EXCHANGE WITHOUT VALIDATION` or duplicate `_tidb_rowid`; it fires only when the DDL
             loses a visible row.
classification:
  RED: row-preserving DDL succeeds and visible count/multiset differs after the DDL.
  GREEN(triggered): the trigger state exists, DDL succeeds, and visible count/multiset is preserved.
  INVALID: the DDL contract permits row deletion/filtering, the reference includes nondeterministic
       rows, or the before/after projections do not distinguish duplicates and omit count.
status:      USED. Needs held-out injected row-drop or row-dup bug before TRUSTED.
noise:       always include `COUNT(*)` so duplicate SQL rows are counted even if their displayed
             values are identical.
```

## O14 target_type_acceptance_reference
```text
obligation:  a DDL precheck that claims existing values fit a target type must agree with the
             target type's own acceptance semantics.
form:        reference arm: create the target schema directly and insert the same representative
             value, or create the same target metadata relation directly (for example an FK pair).
             DDL arm: create a valid source schema, then run the metadata/no-reorg/target-state
             DDL to reach the same target schema. Compare success/error and, when it succeeds,
             compare stored value/length metadata and target metadata.
red:         the target schema directly accepts the value, but the DDL precheck rejects it; or the
             target schema rejects the value but the DDL succeeds without rewriting/validating it.
catches:     wrong-error or wrong-acceptance in DDL fit checks. Used by id630001: direct
             `varchar(3)` and `char(3)` accepted `_utf8mb4'中中中'` with `CHAR_LENGTH=3`, but
             `ALTER TABLE` from length 4 to length 3 failed with ERROR 1265 because the precheck
             used byte `LENGTH=9`.
             Used again by id630002: direct FK target schemas parent `varchar(10)` / child
             `varchar(10 or 15)` and parent `varchar(15)` / child `varchar(20)` were accepted,
             but `MODIFY COLUMN` rejected equivalent child/parent transitions with ERROR 1832/1833
             because the FK modify validator required new length to be >= original or related
             length.
             Used by id630003: direct partitioned target schemas with `varchar(5)` partition
             columns and fitting literals/data were accepted, and non-partitioned
             `varchar(6)->varchar(5)` succeeded, but partition-column `MODIFY COLUMN` rejected
             the same safe shrink with ERROR 8200 because its allowlist only permits string
             length extension.
             Used by id630004: direct generated-column target schemas with base-column
             COMMENT/DEFAULT metadata changes were accepted and evaluated correctly, but
             `MODIFY COLUMN` rejected the same target because the base column was referenced by a
             generated column.
             Used by id630007: direct expression-index target schemas with base-column
             COMMENT/DEFAULT metadata changes were accepted and passed `ADMIN CHECK TABLE`, but
             `MODIFY COLUMN` rejected the same target because the base column was referenced by a
             hidden generated column backing an expression index.
             Used by id630009: direct partial-index target schemas with condition-column
             COMMENT/DEFAULT metadata changes were accepted and passed `ADMIN CHECK TABLE`, but
             `MODIFY COLUMN` rejected the same target because the column was referenced by
             `idx.AffectColumn` for the partial-index condition.
blind to:    target-type contract bugs themselves, intentional DDL restrictions stricter than
             direct insert semantics, and cases where the DDL must rewrite normalized storage
             even though direct insertion accepts the value.
sensitivity: USED — caught id630001, id630002, id630003, id630004, id630007, and id630009. For id630001, ASCII `abc` was a triggered GREEN
             control because byte length and character length coincide; utf8mb4 multibyte strings
             were RED. For id630002, direct target FK schemas were GREEN, child/parent ALTER
             transitions were RED, and checker-aligned widening controls were GREEN. For id630003,
             direct partition target schemas and non-partition shrink were GREEN, safe partition
             shrink was RED, and partition-column widen was GREEN. For id630004, direct target
             generated-column schemas were GREEN, metadata-only ALTERs were RED, and true type
             changes remained GREEN rejects. For id630007, direct target expression-index schemas
             were GREEN, metadata-only ALTERs were RED, and non-dependent/drop-index/type-change
             controls separated metadata-only safety from true dependency risk. For id630009,
             direct target partial-index schemas were GREEN, metadata-only ALTERs were RED, and
             non-condition/drop-index controls separated metadata-only safety from condition
             dependency risk.
specificity: GOOD for fit-check DDLs when the product contract says the DDL is validating target
             type compatibility. Keep binary and non-binary controls separate because binary
             string lengths are byte-based.
classification:
  RED: direct target acceptance / target metadata creation and DDL fit-check result differ under
       the same SQL mode and target column definition.
  GREEN(triggered): the precheck runs and matches direct target acceptance/rejection.
  INVALID: the target schema is not equivalent, sql_mode differs, the DDL intentionally has extra
       restrictions, or direct insert itself exposes a separate target-type bug.
status:      USED. Needs held-out injection before TRUSTED.
noise:       the reference must use exactly the same charset/collation/type attributes and metadata
             relation as the DDL target, and should include controls for binary vs character-string
             units and for checker-aligned transitions. For expression indexes, include
             `ADMIN CHECK TABLE` or an indexed expression query so the reference proves the index is
             still coherent, not merely that parsing succeeded.
```

## O16 source_target_metadata_isolation_oracle
```text
obligation:  a DDL that constructs a target object from a source object must not mutate any
             SQL-visible metadata of the source object.
form:        capture source metadata before target reconstruction; execute the target-creating DDL;
             capture source metadata again from the same session and a new session; compare with an
             independent direct-create sibling control. Use target metadata only as trigger
             evidence, not as the oracle.
red:         source `SHOW CREATE`, source runtime error names, source information_schema rows, or
             source behavior change after target reconstruction without an explicit source DDL.
catches:     metadata corruption or stale display caused by shallow-copy target mutation. Used by
             id630005: `CREATE TABLE dst_auto LIKE src_auto` changed source `SHOW CREATE TABLE
             src_auto` from `src_auto_chk_1` to `dst_auto_chk_1`, and source CHECK violations
             reported the target constraint name.
blind to:    intentional source-side DDL, display-only formatting changes with no stable name
             contract, and metadata that is explicitly documented as shared by source and target.
sensitivity: USED — caught id630005. Direct sibling table creation was the green control:
             `d1_chk_1` and `d2_chk_1` stayed independent.
specificity: GOOD when the source object is unchanged by SQL and the target construction path is
             the only operation between before/after source snapshots.
classification:
  RED: source metadata or source-visible error/behavior changes after target reconstruction.
  GREEN(triggered): target reconstruction runs and source metadata remains byte/semantic equivalent.
  INVALID: the operation intentionally modifies the source, the before/after source snapshots are
       taken across unrelated DDL, or the source and target are documented to share that metadata.
status:      USED. Needs held-out injection before TRUSTED.
noise:       include a new-connection read to distinguish session-local display cache from shared
             InfoSchema/meta state, and include direct sibling controls to avoid mistaking normal
             target renaming for source mutation.
```

## O26 runtime_state_clone_oracle
```text
obligation:  a DDL that constructs a new object from an existing object must not copy runtime or
             management state that is not part of the object's schema definition.
form:        create a source object with runtime state; reconstruct a target object from it; show
             the target inherits behavior it should not have; clear only the target state and prove
             the target behavior changes while the source behavior remains.
red:         the target object rejects or changes user operations because it inherited source
             runtime state, and target-only cleanup/round-trip repairs the target without repairing
             the source.
catches:     id1200001: `ALTER TABLE src READ ONLY; CREATE TABLE dst LIKE src` created a new
             `dst` that rejected `INSERT` with `ERROR 8020` until `ALTER TABLE dst READ WRITE`,
             while `src` remained read-only.
blind to:    schema options that `CREATE TABLE LIKE` is documented to copy, and intentional
             operations that explicitly set the target state.
sensitivity: USED — caught id1200001.
specificity: GOOD when the oracle includes a target-only cleanup/control and a source-still-locked
             check. Without that control, a global/session condition could be mistaken for target
             state cloning.
classification:
  RED: target behavior inherits source runtime state and can be changed independently of source.
  GREEN(triggered): target reconstruction runs and target behavior matches a fresh unlocked object.
  INVALID: source state is documented as a schema option, or the statement explicitly applies the
       state to the target.
status:      USED. Needs held-out injection before TRUSTED.
noise:       table-lock features may be disabled in local configs; use a testbed with
             `enable-table-lock=true` or classify the probe as BLOCKED rather than GREEN.
```

## O17 schema_check_constraint_namespace_oracle
```text
obligation:  any DDL that publishes table metadata must preserve the schema-level CHECK constraint
             namespace: no two public CHECK constraints in the same schema may share a name.
form:        establish the normal create/add control by proving duplicate CHECK names are rejected;
             run the candidate DDL; query `information_schema.check_constraints` for duplicate
             `constraint_name` values in the schema; use `SHOW CREATE TABLE` and runtime violation
             errors as supporting SQL-visible evidence.
red:         the DDL succeeds and the schema contains duplicate CHECK constraint names, or runtime
             diagnostics cannot distinguish different CHECK expressions because they share the same
             name.
catches:     recovery/flashback/import metadata publication that bypasses schema-level namespace
             validators. Used by id630006: `FLASHBACK TABLE f TO f_old` succeeded after `f` had
             been recreated with a new `f_chk_1`, leaving two public `f_chk_1` rows with different
             CHECK clauses.
blind to:    compatibility questions about how anonymous names should be generated, intentional
             rejection of recovery on duplicate restored names, and metadata tables that do not
             expose the relevant namespace.
sensitivity: USED — caught id630006. Normal duplicate `CREATE TABLE` was the green rejection
             control, and `CREATE TABLE LIKE` was the target-reconstruction green control.
specificity: GOOD for CHECK constraints because the namespace contract is schema-level and normal
             DDL already enforces it.
classification:
  RED: candidate DDL publishes duplicate CHECK names in the same schema.
  GREEN(triggered): candidate DDL runs and either rejects the conflicting recovery or publishes
       unique CHECK names.
  INVALID: CHECK constraints are disabled, the duplicate is across schemas or temporary/non-temporary
       namespace boundaries, or the observed duplicate comes from a pre-existing invalid schema.
status:      USED. Needs held-out injection before TRUSTED.
noise:       do not rely on `information_schema.table_constraints` on this build; use
             `information_schema.check_constraints`, `SHOW CREATE TABLE`, and direct runtime
             violation errors.
```

## O18 idempotent_ddl_flag_oracle
```text
obligation:  a DDL syntax that accepts an idempotence flag such as IF NOT EXISTS must route the
             existing-object case through idempotent success/note semantics rather than the hard
             duplicate-error path used when the flag is absent.
form:        run the DDL once to create the object; run the same flagged DDL again; compare with
             the unflagged duplicate hard-error control and with a sibling DDL kind that already
             implements the same flag. Query the schema surface afterward to ensure the flagged
             path did not create a duplicate object.
red:         flagged duplicate DDL returns the same hard duplicate error as the unflagged path, or
             creates duplicate metadata instead of no-oping.
catches:     parser/AST flag propagation gaps, spec-splitting flag loss, and owner-specific
             idempotence omissions. Used by id630008: `ADD FOREIGN KEY IF NOT EXISTS` failed with
             `ERROR 1826` on the second run, while sibling `ADD INDEX IF NOT EXISTS` returned
             `Note 1061` and kept one index. Used by id630010: outer
             `ADD IF NOT EXISTS (KEY idx_a(a))` failed with `ERROR 1061`, while the same grammar
             shape for a column returned `Note 1060` and an inner `KEY IF NOT EXISTS` control
             returned `Note 1061`.
blind to:    syntax that the parser does not accept, product decisions that explicitly document a
             flag as ignored for a specific object type, and invalid new-object definitions where
             the object does not already exist.
sensitivity: USED — caught id630008 and id630010. Plain duplicate ADD FOREIGN KEY was the green
             rejection control for id630008, and `information_schema.referential_constraints`
             proved the failed flagged duplicate did not write a second FK. For id630010, outer
             column IFNE and inner KEY IFNE controls separated parent-flag loss from ordinary
             index idempotence failure, while index/CHECK counts proved no duplicate write.
specificity: GOOD when the parser accepts the flag and a sibling DDL owner demonstrates the
             intended idempotent behavior.
classification:
  RED: flagged duplicate takes the hard-error path or writes duplicate metadata.
  GREEN(triggered): flagged duplicate succeeds with note/no-op and schema count remains one.
  INVALID: the flag is rejected by the parser, the object did not already exist, or the DDL fails
       for a different validation reason such as missing parent/index/column.
status:      USED. Needs held-out injection before TRUSTED.
noise:       always pair with the unflagged duplicate control; otherwise a genuine validation
             failure can be mistaken for an idempotence bug.
```

## O19 target_state_rejection_reference
```text
obligation:  a transition DDL must not reach a final schema state that the direct target-state
             validator for the same feature rejects, unless the transition has an explicit stricter
             or migration-only contract.
form:        direct arm: create or add the final target metadata directly and record the expected
             rejection. Transition arm: create a valid source schema, run the DDL that reaches the
             same target metadata, then inspect the final schema and exercise the missing dimension
             with a behavior oracle.
red:         direct target-state path rejects the final schema, but the transition path succeeds
             and publishes it; the follow-up behavior either fails, violates the contract, or shows
             the rejected dimension is now observable.
catches:     wrong-acceptance caused by validator ordering gaps. Used by id630011: direct child
             `pid INT NOT NULL` with `ON DELETE/UPDATE SET NULL` rejected with ERROR 1830, but
             nullable child FK followed by `MODIFY pid INT NOT NULL` succeeded; parent
             DELETE/UPDATE then failed with ERROR 1048 when SET NULL tried to write NULL. Used by
             id630012: direct parent `INT` / child `INT UNSIGNED` FK rejected with ERROR 3780, but
             signed/signed FK followed by child `MODIFY a INT UNSIGNED` succeeded; parent
             `ON UPDATE CASCADE` to `-1` then failed with ERROR 1264, and drop/re-add of the FK
             rejected with ERROR 3780.
blind to:    intentionally migration-only states, product decisions where direct CREATE is stricter
             than ALTER for documented compatibility reasons, and cases where the behavior oracle
             never exercises the rejected dimension.
sensitivity: USED — caught id630011 and id630012. The `ON DELETE RESTRICT` control stayed green,
             separating the SET NULL nullability dimension from ordinary FK modify behavior. The
             signed/signed cascade control stayed green, and the collation sibling stayed green
             because a later indexed-column validator blocked the ALTER.
specificity: GOOD when the direct and transition arms produce the same final metadata and the
             follow-up behavior exercises the missing dimension.
classification:
  RED: direct target-state rejection and transition acceptance reach equivalent final schema, with
       a behavior consequence or SQL-visible invalid state.
  GREEN(triggered): transition rejects with the same target-state error, or both direct and
       transition paths accept a valid final state.
  INVALID: final schemas differ, session variables differ, direct rejection is unrelated, or the
       transition has an explicit documented exception.
status:      USED. Needs held-out injection before TRUSTED.
noise:       always add one green control where the transition is valid under the same owner, plus
             one sibling omitted dimension that is covered by a later validator when possible. For
             id630011 this is `ON DELETE RESTRICT`, which can safely become NOT NULL because no SET
             NULL write is required. For id630012 this is the signed/signed cascade control and the
             collation sibling that is blocked by indexed-column collation validation.
```

## O20 post_conversion_check_oracle
```text
obligation:  a DDL data-rewrite path that changes stored row values must preserve CHECK
             constraints on the post-conversion row, not merely on the pre-conversion row.
form:        build a table where old rows satisfy an enforced CHECK; choose a successful type or
             row-layout conversion that can change the CHECK predicate truth value; run the DDL;
             query the predicate on final rows; compare with ADD CHECK and ordinary DML as safe
             paths on the same final value.
red:         the transition DDL succeeds and final rows exist where the CHECK predicate evaluates
             false/non-null false, while ADD CHECK or DML rejects the same final value.
catches:     raw DDL reorg/backfill writers that bypass DML row-invariant checks. Used by id630013:
             `DECIMAL(10,2)` value `0.40`, `DOUBLE` value `0.4`, and `VARCHAR` value `'0.4'` all
             satisfied `CHECK(a > 0)` before `MODIFY a INT`, but converted to `0`; the final table
             still published `CHECK(a > 0)` and contained a row where `a > 0 = 0`.
blind to:    product decisions that intentionally allow CHECK to be temporarily unenforced during
             a documented online transition, conversions that fail before writing, and nullable
             CHECK semantics where NULL is allowed and the final predicate is NULL rather than
             false.
sensitivity: USED — caught id630013. ADD CHECK on an INT table containing 0 rejected with ERROR
             3819, and ordinary INSERT 0 into the altered table also rejected with ERROR 3819.
specificity: GOOD when the final value is shown explicitly and the safe-path oracle rejects the
             same final value.
classification:
  RED: old rows pass; DDL conversion succeeds; final rows fail the CHECK; safe path rejects.
  GREEN(triggered): DDL rejects before publishing, conversion preserves the CHECK truth value, or
       the DDL revalidates and rolls back on CHECK violation.
  INVALID: old rows did not satisfy the CHECK, CHECK constraints are disabled, the final predicate
       evaluates NULL under allowed CHECK semantics, or the failure is just a conversion error.
status:      USED. Needs held-out injection before TRUSTED.
noise:       do not rely on ADMIN CHECK TABLE for this class; it can return success because it is
             primarily a record/index consistency checker. Always query the CHECK predicate itself
             and include ADD CHECK/DML safe-path references.
```

## O22 backfill_concurrent_dml_differential
```text
obligation:  an online DDL backfill path must classify rows or keys already written by concurrent
             DML as the same logical row/object, not as a duplicate or corrupt key.
form:        widen the online-DDL window with a deterministic failpoint or small backfill batches
             (on a release build without failpoints, race a large split table under one reorg
             worker); run the DDL in one session; run a concurrent DML that writes the new
             DDL-maintained artifact before backfill reaches that row; compare DDL outcome with
             targeted controls that remove one proof dimension at a time. While the DML runs, also
             observe `ADMIN SHOW DDL` for a wedged job (State running, ErrCount climbing,
             SchemaState not advancing) — the failure may be a STUCK job, not a returned error
             (pair with O28). Finish with ADMIN CHECK / rowset or metadata checks after success or
             rollback.
red:         the DDL fails, hangs/retries, or rolls back on a false duplicate/corrupt-key condition
             while controls show the schema/data are valid and the same operation succeeds when the
             missing proof dimension is absent.
catches:     online backfill sibling paths that reconstruct owner/type/identity incorrectly after
             concurrent DML has already written the artifact. id30038 (ADD UNIQUE MVI plus sibling
             multi-column UNIQUE) reproduces THREE outcomes from one owner-misclassification,
             upstream-verified on nightly v9.0.0-beta.2.pre-1774: (1) false duplicate `ERROR 1062`,
             (2) `invalid encoded key` rollback `ERROR 1105`, (3) the subjob WEDGES under sustained
             concurrent DML — loops on `invalid encoded key` (ErrCount climbing), stuck in
             write-reorg, rolls back only when the DML pauses. The liveness outcome (3) is the
             severe one and only O28 sees it; the loud oracle alone would have graded this a
             consequence-1 wrong-error.
blind to:    storage corruptions that happen after DDL success unless paired with O1/O2;
             timing-only failures without a deterministic trigger window. NOTE (resolved for
             id30038): the "concurrent DML creates a real duplicate" case was actively hunted and
             did NOT yield silent acceptance — a wrong-metadata handle decode fails toward an error
             or a stuck job, not a coincidental match, so the risk is stuck/failed DDL, not a
             silently non-unique index.
sensitivity: USED + EXECUTION-CONFIRMED on upstream nightly — caught id30038 on testbed 8192975 and
             reproduced by natural race on a release build; 3 outcomes observed, table healthy
             after every rollback.
specificity: GOOD when the matrix includes:
             - single-owner green,
             - sibling-owner green,
             - red only when the missing dimension is present,
             - post-rollback or post-success table consistency.
classification:
  RED: valid concurrent DML plus valid target schema causes false duplicate, corrupt-key error,
       or non-progress that disappears in the dimension controls.
  GREEN(triggered): DDL succeeds and post-DDL consistency/rowset checks pass.
  INVALID: no proof the DDL was in the online backfill window, DML made a real duplicate, controls
           are missing, or the failure comes from environment cancellation.
status:      USED + EXECUTION-CONFIRMED (upstream release-build repro). Still needs a held-out
             injection with a scripted trigger window before TRUSTED, but the false-toward-error
             direction is now execution-settled. Must be paired with O28 (liveness) — the loud
             form alone under-grades the severity.
noise:       failpoints may widen the window but cannot be the semantic trigger. Always state
             which failpoint is only a timing aid, and include a control that proves ordinary DDL
             and the concurrent DML are both valid.
```

## O23 target_schema_constraint_reference
```text
obligation:  a DDL transition that accepts a schema spec with an embedded constraint must publish
             and enforce the same target constraint that direct target-schema creation or an
             explicit safe-path sequence would publish.
form:        build three arms: direct target schema, sequential safe path, and compact transition
             path. Compare SHOW CREATE / information_schema constraint metadata, then execute a
             violating write or equivalent behavior that the constraint must reject.
red:         direct and sequential reference paths publish/enforce the constraint, but the compact
             transition path succeeds without warning and loses the constraint; a violating row or
             operation succeeds.
catches:     embedded child-owner obligations dropped by a parent DDL job. Used by id30032:
             `ALTER TABLE t ADD COLUMN b INT DEFAULT 1 CHECK(b > 0)` succeeded with
             `@@warning_count=0`, published no CHECK in SHOW CREATE or information_schema, and
             accepted `b=0`; direct CREATE and sequential ADD CHECK both rejected `b=0` with ERROR
             3819.
blind to:    product decisions that explicitly reject or warn on the embedded syntax, documented
             compatibility modes where the child obligation is intentionally ignored, and cases
             where direct and transition forms are not meant to produce the same target schema.
sensitivity: USED — caught id30032. The named inline CHECK sibling also lost the constraint,
             separating the root cause from anonymous-name generation.
specificity: GOOD when both reference arms agree and the transition path returns success with no
             warning. The behavior step prevents classifying a display-only metadata difference as
             a confirmed bug.
classification:
  RED: direct/sequential references enforce the target constraint; compact transition succeeds
       without warning and loses enforcement.
  GREEN(triggered): transition publishes the same constraint, rejects atomically, or emits an
       explicit warning/error that the child obligation is unsupported.
  INVALID: CHECK constraints are disabled, references disagree, the violating write is not covered
       by the constraint, or the compact syntax is rejected before reaching the target owner.
status:      USED. Needs held-out injection before TRUSTED.
noise:       include both metadata and behavior. SHOW CREATE alone is not enough, and
             information_schema alone may miss owner-specific display quirks. Use at least one
             write or management operation that proves the requested constraint changes behavior.
```

## O24 partition_exchange_validation_oracle
```text
obligation:  EXCHANGE PARTITION WITH VALIDATION must accept exactly the standalone rows that belong
             to the target partition, using the same partition semantics visible to ordinary DML.
form:        build four arms: direct target-state routing into the partition, a sibling exchange
             validation control, the target WITH VALIDATION statement, and a WITHOUT VALIDATION
             boundary for the same rows. RED requires direct membership plus validation failure
             or wrong acceptance.
red:         a row that ordinary DML routes to the target partition makes EXCHANGE WITH VALIDATION
             fail; or a row that belongs elsewhere is accepted and published by validation.
catches:     internal validation SQL builders that omit partition semantic dimensions. Used by
             id630025: LIST DEFAULT membership is a complement of explicit LIST values, but the
             builder iterated only current `InValues` and emitted `not ()`.
blind to:    user-opted `WITHOUT VALIDATION` misplacement, environment failures before validation,
             and product semantics where a partition type is explicitly unsupported for exchange.
sensitivity: USED — caught id630025 on testbed 8192975.
specificity: GOOD when the matrix includes direct partition routing and a sibling partition type
             whose validation succeeds. The WITHOUT VALIDATION arm is only a boundary, not a proof
             that validation should accept invalid rows.
classification:
  RED: direct membership and sibling validation are green, but WITH VALIDATION fails or accepts a
       non-member row.
  GREEN(triggered): validation succeeds for members and rejects non-members with
       ErrRowDoesNotMatchPartition.
  INVALID: direct target-state membership is not proven, the row is deliberately exchanged WITHOUT
           VALIDATION, or the statement never reaches validation.
status:      USED. Needs held-out injection before TRUSTED.
noise:       syntax errors from restricted SQL are valid RED only after the direct membership
             oracle proves the data itself is acceptable.
```

## O25 join_domain_reference
```text
obligation:  replacing a join comparison with a narrower typed-key comparison must preserve the
             original SQL-visible equality semantics for every row accepted by the replacement
             guard.
form:        fast arm: original query shape with trigger evidence that the rewrite fired.
             reference arm: a CASE-wrapped join predicate or a rule-disabled/no-shortcut plan that
             preserves the original scalar equality. Compare ordered rowsets.
red:         fast arm misses or adds rows compared with the reference, and scalar contracts show
             the differing value is a valid original equality case.
catches:     semantic-domain narrowing in optimizer join rewrites. Used by id30040:
             `10='1e1'` is true in the original mixed numeric comparison, but
             `join_key_type_cast` filters `s='1e1'` through its signed-int round-trip guard.
blind to:    cases where the reference also triggers the same rewrite, storage inconsistency, and
             product-contract ambiguities in the scalar comparison itself.
sensitivity: USED — caught id30040 on testbed 8192975. Default rule returned
             `1:1,2:2e0,10:10,10:10.0`; CASE and rule-disabled references returned
             `1:1,2:2e0,10:10,10:10.0,10:1e1`.
specificity: GOOD for the current matrix: canonical integer and decimal integer-valued controls
             match in both arms; fractional and nonnumeric controls stay no-match.
classification:
  RED: rewrite triggered, reference preserves original equality, ordered rowsets differ, and a
       scalar contract explains the differing row.
  GREEN(triggered): rewrite triggered and ordered rowsets match the reference across adversarial
       domain-boundary values.
  INVALID: no trigger evidence, the reference is also rewritten, row order/projection is not
       deterministic, or the scalar contract is ambiguous.
status:      USED. Needs held-out injection before TRUSTED.
noise:       keep the reference semantically identical. `CASE WHEN a=b THEN 1 ELSE 0 END = 1`
             is useful because it blocks the join-key rewrite while preserving the same scalar
             equality; a manually rewritten cast is not a safe reference unless it is the contract
             being tested.
```

## O27 savepoint_stack_semantics_oracle
```text
obligation:  transaction savepoint stack operations must match the reference SQL contract:
             RELEASE removes the named savepoint only, while ROLLBACK TO restores transaction
             state and discards later savepoints.
form:        build a two-marker stack (`sp1`, `sp2`) with visible writes around each marker, apply
             the operation under test, then consume the remaining marker with `ROLLBACK TO` and
             compare both error/no-error behavior and the ordered rowset inside the transaction.
red:         RELEASE of an earlier marker makes a later marker unavailable, or preserves/restores
             transaction rows differently from the reference contract.
catches:     txn state-stack operation splits where one operation accidentally uses another
             operation's truncation semantics. Used by id1200002: `ReleaseSavepoint` truncates the
             slice before `sp1`, so `sp2` disappears and `ROLLBACK TO sp2` returns 1305.
blind to:    savepoint name resolution quirks, transaction durability after COMMIT, and cases
             where the product explicitly documents a non-reference savepoint contract.
sensitivity: USED — caught id1200002 on testbed 8192975. The red matrix returned
             `ERROR 1305 SAVEPOINT sp2 does not exist`; green controls showed `ROLLBACK TO sp1`
             is the operation that should delete later `sp2`, and `RELEASE sp2` preserves `sp1`.
specificity: GOOD for the current matrix because the controls separate "named marker deletion"
             from "restore-and-truncate" semantics.
classification:
  RED: reference contract says a marker should remain reachable, but the stack consumer errors or
       leaves rows in a state the reference would have rolled back.
  GREEN(triggered): the operation mutates exactly the marker set the reference contract allows.
  INVALID: no active transaction, savepoint creation did not occur, names differ unintentionally,
           or the reference contract is intentionally not MySQL-compatible.
status:      USED. Needs held-out injection before TRUSTED.
noise:       keep the matrix small and consume the marker after the operation. Inspecting an
             internal savepoint list alone is not a user-visible oracle.
```

## O28 ddl_job_liveness
```text
obligation:  a DDL job under concurrent workload must make progress and reach a terminal state
             (success or clean rollback); it must not wedge — retrying an internal error forever
             without advancing SchemaState or returning to the client.
form:        while the DDL runs under a concurrent DML load, poll `ADMIN SHOW DDL` / `ADMIN SHOW
             DDL JOBS`. Read three signals on the running job across >=2 polls: State,
             SchemaState, and either ErrCount or repeated same-step rerun evidence from the
             subtask/global-task view. Confirm workload-driven by checking it reaches a terminal
             state once the concurrent DML stops or the held fault is removed.
red:         a job that loops on the same internal error (ErrCount monotonically climbing, or the
             same distributed subtask rerunning) while SchemaState does not advance and it does
             not return to the client. Bonus-red if `ADMIN CANCEL DDL JOBS` itself errors
             (id30038 draft observed `invalid encoded key` on cancel).
catches:     online-DDL liveness failures — a job that neither succeeds nor cleanly rolls back
             under exactly the concurrent traffic online DDL exists to tolerate. Realizes the old
             `liveness_watchdog` HYPOTHESIS with a concrete TiDB-native probe. id30038:
             ErrCount 386 -> 467 climbing, stuck in write-reorg, rolled back only when DML
             stopped. id1350002: distributed `ADD INDEX` job 2319 and global task 270003 stayed
             `running` on repeated `SetTSBeforeImportEngine` `engine-not-found` until the held
             fault was removed. id1410001: the same lane stayed `running` on persistent
             `context deadline exceeded`, with task 300008 logging 247 repeated retryable reruns
             in 87 seconds before fault removal.
blind to:    hangs with no error surfaced in ADMIN SHOW DDL (pure deadlock, no ErrCount); merely
             slow progress (needs the RowCount-delta check to tell from a wedge); non-DDL liveness
             (txn/lock waits need a different watchdog).
sensitivity: TRUSTED — fired on three distinct live wedge families: id30038 (upstream nightly
             MVI reorg wedge with climbing ErrCount), id1350002 (non-MVI DXF distributed
             runtime-fundamental retry loop), and id1410001 (non-MVI DXF distributed retryable
             timeout with no terminal budget). This is now a held-out-proven DDL-hang oracle,
             not an id30038-specific one.
specificity: distinguish wedge from slow progress with >=2 polls — a healthy job advances
             RowCount/SchemaState or reaches terminal after a one-shot fault, a wedged one only
             repeats the same error/subtask. Controls: the same DDL without the trigger completes;
             for id1350002 the one-shot same-altitude control (`job 2322`) synced and the held RED
             recovered immediately after fault removal; for id1410001 the one-shot control (`job
             4002`) synced while the persistent held fault (`job 4007` / `task 300008`) only
             cleared after external fault removal.
classification:
  RED: ErrCount climbing + SchemaState frozen + no client return; terminal only after the trigger
       workload is removed.
  GREEN(triggered): job advances RowCount/SchemaState and reaches success or a clean rollback under
       concurrent load.
  INVALID: only one poll taken (cannot tell wedge from slow), or the "hang" was environment
       cancellation / node restart rather than a retry loop.
status:      TRUSTED — id30038 plus held-out non-MVI distributed retry-loop proofs from id1350002
             and id1410001.
noise:       a single ErrCount snapshot is not evidence; always poll twice. Throttled reorg workers
             can look stalled — read RowCount deltas, not wall time. For distributed DDL, pair the
             job poll with subtask/task state so a same-step rerun loop does not masquerade as slow
             progress.
```

## O29 ntdml_current_rowset_oracle
```text
obligation:  a non-transactional DML statement must derive its split jobs from the same current
             rowset that the write statement is allowed to modify. It must not silently plan from
             a stale/read-only snapshot and then commit only those ranges.
form:        create row r1, capture @ts, create row r2 after @ts. Verify `AS OF @ts` sees only r1.
             Then compare three arms:
             (1) ordinary DML under `SET TRANSACTION READ ONLY AS OF TIMESTAMP @ts`,
             (2) NT-DML without tx_read_ts,
             (3) NT-DML with tx_read_ts. Clear tx_read_ts before the final full-table read.
red:         ordinary DML rejects the stale read-only write; no-tx_read_ts NT-DML updates r1+r2;
             tx_read_ts NT-DML reports success but updates only r1.
catches:     stale transaction state leaking into internal split/validation SELECTs. Used by
             id1230001: `HandleNonTransactionalDML` clears `ReadStaleness` but leaves `TxnReadTS`,
             so `buildShardJobs` enumerates stale ranges.
blind to:    storage corruption (ADMIN CHECK is only a sanity control), concurrent writes racing
             between split and job execution, and product choices that explicitly reject NT-DML
             under any pending transaction state before planning.
sensitivity: USED — caught id1230001 on testbed 8220955. The red run showed `AS OF` rowset `1:10`,
             NT-DML job count `1`, and final current rowset `1:110,2:20`.
specificity: GOOD when all three controls are present. Without the ordinary-DML reject control, a
             partial update could be misclassified as expected stale-write semantics; without the
             no-tx_read_ts control, it could be a BATCH planning bug unrelated to stale state.
classification:
  RED: stale-state arm reports success and final current rowset differs from the no-stale arm.
  GREEN(triggered): stale state is rejected before NT-DML planning, or NT-DML demonstrably clears
       all stale inputs and updates the current rowset.
  INVALID: @ts does not exclude the control row, tx_read_ts was not actually set, or concurrent DML
           changes the target rowset during the probe.
status:      USED. Needs held-out injection before TRUSTED.
noise:       always clear `@@tx_read_ts` before the final full-table SELECT; otherwise the final
             read itself may be stale and hide the current row.
```

## O30 pending_tx_read_ts_preserved_across_internal_sql
```text
obligation:  a current-session internal SQL lookup must not silently consume a pending user
             one-shot stale-read state unless that is the explicit product contract.
form:        create row r1, capture @ts, create row r2 after @ts. Verify AS OF @ts sees only r1.
             Then set `SET TRANSACTION READ ONLY AS OF TIMESTAMP @ts`, run the candidate
             internal/management statement, and run a plain user SELECT. Compare against:
             (1) no-internal-SQL AS OF control, (2) current-rowset control, and where applicable
             (3) ordinary statement contract control.
red:         the internal/management statement reports success, and the next user SELECT reads the
             current rowset when the preserved-pending-state contract expects the stale rowset.
catches:     state-ingress leaks where `ExecuteInternal` or current-session restricted SQL enters
             `staleread.Processor` and calls `TxnReadTS.UseTxnReadTS()` before the user's intended
             statement. Validated by the binding-history and index-advisor source-target RED/GREEN
             pairs.
blind to:    product contracts where any next statement is allowed to consume tx_read_ts, and
             wrappers that use a separate sys session or explicitly save/restore all stale-read
             session state.
sensitivity: USED — current `13282a8` produced a root-boundary RED plus a TSO-stable user-visible
             RED for binding-history:
             root-boundary restricted SQL consumed pending `TxnReadTS`; user-visible
             `CREATE SESSION BINDING FROM HISTORY USING PLAN DIGEST` made the next SELECT return
             `[1,2]` instead of `[1]`. The same oracle then caught planner index advisor:
             `RECOMMEND INDEX RUN` made the next SELECT return current rows, with RED log
             `before=467570885856329728 after=0 next_select_rows=[[1] [2]]`.
specificity: USED. Binding-history passed under a temporary `ExecuteInternal` state restoration
             probe. Index advisor passed under a stronger ingress-isolation probe:
             `before=467570913639661568 after=467570913639661568 next_select_rows=[[1]]`.
             The generated negative screens also sharpen specificity: foreign-key and
             user-management are retired because their internal SQL does not share the user's
             session state.
classification:
  RED: contract says pending tx_read_ts should survive the wrapper, trigger evidence proves the
       wrapper ran current-session internal SQL, and the next user SELECT observes current rows
       instead of the stale AS OF control.
  GREEN(triggered): wrapper either uses an isolated session, rejects pending tx_read_ts explicitly,
       or preserves the pending state and the next user SELECT matches the AS OF control.
  LOW_VALUE/INVALID: product contract says tx_read_ts is consumed by any next statement, the AS OF
       control did not isolate r1 from r2, or the candidate internal SQL did not actually run on
       the current session.
status:      USED. Needs product-contract decision before filing/escalating as a confirmed product
             bug, but the oracle itself has RED/GREEN calibration.
noise:       prefer commitTS-derived controls: insert r1, take `LastCommitTS`, set @ts to
             `oracle.GetTimeFromTS(LastCommitTS)+small_offset`, sleep, then insert r2. This avoids
             NULL user variables and raw-TSO timestamp round-trip precision loss. Always verify
             AS OF @ts sees only r1, and verify the hidden pending state before and after the
             wrapper when possible. For GREEN/fix probes, do not trust flag restoration alone:
             result-set drain/close may consume and clean up pending state after `ExecuteInternal`
             returns. Prefer an ingress-isolation probe that hides pending one-shot state before
             internal SQL enters the generic session path, then restores it afterward.
```

## O31 ddl_retryable_fault_terminal_oracle
```text
obligation:  a transient control-plane fault during an active DDL stage should stay on a
             retryable recovery path; once the dependency is healthy again, the job must not
             terminally roll back after a single retry-family error if a sibling path/control
             proves the operation is otherwise valid.
form:        prove the fault lands in the relevant active window (live DDL session, schema_state
             gate, minimum running duration, or an equivalent deterministic hold point), then
             collect four signals:
             (1) user-visible error family,
             (2) terminal job state from `ADMIN SHOW DDL JOBS` / `mysql.tidb_ddl_history`,
             (3) `err_count`,
             (4) a sibling control that removes the risky recovery bridge (for example
             `fast_reorg=OFF` / txn path) or proves the candidate window never triggered.
red:         the transient fault family hits the active window, the user sees a retry-family
             error, terminal state is rollback/failure, and `err_count=1` (or equivalent) shows
             the job never really entered a retry budget; a sibling control is GREEN.
catches:     DDL availability failures where a transient external fault is misclassified as fatal
             or bypasses a needed retry loop. id1290001: fast-reorg `ADD INDEX` under two short PD
             bounces in write reorganization returned `create TSO stream failed, retry timeout`;
             testbed 8220955 jobs 1192 and 1204 both had
             `PD:client:ErrClientCreateTSOStream`, `err_count=1`, and `rollback done`, while the
             sibling `fast_reorg=OFF` txn path was GREEN. `ddl-ingest-retryable-kv-family-
             misclassified-fatal`: on the same testbed, lower-bridge live `Ingest:NotLeader` /
             `KV:ErrNoLeader` stayed GREEN inside `ingestctrl`, but bridge-proximal live
             `ErrNoLeader`, `ErrKVNotLeader`, and `ErrKVRegionNotFound` all produced immediate
             `rollback done` on `ADD INDEX` / `ADD PRIMARY KEY`, with same-environment GREEN
             controls after clearing the failpoint. id1350001: bridge-level `driver: bad
             connection`, `connection reset by peer`, and `grpc unavailable` made `MODIFY
             COLUMN` jobs `1726/1764/1770` terminalize to `rollback done`, while sibling
             `ADD INDEX` jobs `1723/1761/1767` stayed GREEN under the same one-shot fault
             shapes.
blind to:    pure wedges/no terminal state (use O28), permanent outages where retry really should
             fail, and source-only classifier probes with no live sibling control.
sensitivity: EXECUTION-CONFIRMED on three transient-fault families — fired on testbed
             8220955/current master for jobs 1192 and 1204 after the live-history recheck on
             2026-07-10, fired again on the live bridge-altitude matrix for jobs
             1557/1563/1584/1587 with explicit same-environment GREEN controls, and fired a
             third time on the live modify-column bridge matrix for jobs 1726/1764/1770 against
             sibling GREEN controls `1723/1761/1767` plus the shared GREEN control
             `1755/1758`.
specificity: GOOD when the matrix includes active-window proof, a sibling green control, and live
             history evidence proving "single-hit terminalization" rather than exhausted retries.
             Example boundary: `fast_reorg=ON` with only 600k rows finished before the second
             bounce; that cell is INVALID_NO_ACTIVE_WINDOW, not GREEN.
classification:
  RED: transient fault lands in the active window, the user gets a retry-family error, terminal
       job state is rollback/failure, and `err_count=1` shows the failure was classified as fatal
       immediately rather than recovered through retries.
  GREEN(triggered): the same transient fault lands in the active window but the job reaches
       success or a clean recovery path, or the sibling safe path proves recoverability while the
       candidate path is repaired.
  INVALID: no trigger proof, control missing, cluster never recovered, the terminal state only
       reflects environment kill/noise rather than the retry bridge under test, or the injected
       foreign fault family is not proven natural at that altitude and both siblings fail the same
       way.
status:      TRUSTED for the narrow claim "single-hit retry-family fault was fatalized at the DDL
             retry bridge" after three executed transient-fault families:
             PD TSO timeout in fast-reorg live chaos, the ingest/TiKV retryable family in the
             live bridge-altitude matrix, and the modify-column connection/grpc family in the live
             sibling matrix.
noise:       do not grade a plain chaos RED without reading `err_count`. Without the single-hit
             terminal evidence, the result could just be exhausted retries or environment
             flakiness. Also do not grade a synthetic all-RED matrix as product evidence if the
             same fault family is not shown to be naturally reachable at that altitude.
```

## O32 schema_change_old_schema_commit_oracle
```text
obligation:  when source/runtime contract says a DDL path leaves a safe old-schema commit window
             for async commit / 1PC, the concurrent transaction should commit successfully and
             preserve the exact semantic result under that old schema instead of failing with
             `ErrInfoSchemaChanged`.
form:        hold the transaction shape and DDL shape constant, use a natural same-start schedule,
             and vary only the claimed protection sibling (for example MDL OFF vs MDL ON). Collect
             three signals:
             (1) transaction terminal result (`success` vs `ErrInfoSchemaChanged`),
             (2) a strong post-success oracle (`ADMIN CHECK TABLE` + exact final rowset),
             (3) source/test contract anchors that explicitly claim the safe window exists.
red:         protection-off arm returns `ErrInfoSchemaChanged` or equivalent stale-schema reject
             while the protection-on sibling is GREEN and preserves the exact final rowset.
catches:     DDL/txn availability failures where a code comment or test contract says "old-schema
             commit is safe here", but the current runtime still rejects the commit. id1440001:
             `pkg/ddl/ddl.go` says `delayForAsyncCommit` makes async commit and 1PC safe; the
             skipped realtikv tests expect success; yet plain `ADD INDEX + async commit +
             insert+update` on current-master testbed `8220955` is RED with MDL OFF and GREEN
             with MDL ON.
blind to:    liveness wedges where the transaction never returns (use O28), silent mis-amend/data
             corruption when both siblings return success but one preserves the wrong rowset, and
             cases with no explicit protection sibling or no product contract to anchor expected
             behavior.
sensitivity: EXECUTION-CONFIRMED on id1440001. The natural MDL-off arm returned
             `Error 8028 Information schema is changed`; the MDL-on sibling on the same
             current-master front stayed GREEN and preserved the exact final rowset under
             `ADMIN CHECK TABLE`.
specificity: GOOD when the matrix includes an explicit sibling protection path and a strong
             post-success oracle. Without the green sibling, a red commit could just be ordinary
             schema drift; without the exact-row/admin-check green oracle, a "success" sibling
             could hide silent mis-amend.
classification:
  RED: natural same-shape transaction returns stale-schema error on the candidate path, while the
       sibling protection path succeeds and preserves the exact semantic outcome.
  GREEN(triggered): the same natural schedule commits and preserves the exact final rowset under
       the claimed safe path.
  INVALID: no natural overlap proof, no sibling control, or the green arm lacks a strong
           post-success oracle.
status:      USED — execution-confirmed on id1440001. Promote to TRUSTED only after another
             independent contract-level old-schema safe path fires or after a fix-validation cycle
             proves both sensitivity and specificity again.
noise:       do not grade a red cell from injected delays alone if the natural no-failpoint shape
             was never tried. The value of O32 is precisely that it tests the product contract
             directly, not a synthetic failure path.
```

## Registered-but-unvalidated (HYPOTHESIS — held-out targets)
```text
no_panic_probe        catches panic          — process-alive check, never injected
```
(`liveness_watchdog` graduated: realized by O28 `ddl_job_liveness`, EXECUTION-CONFIRMED via the
id30038 wedge. The ddl-hang class now has a firing oracle.)
These are the next held-out priorities: an oracle in a suite with no sensitivity evidence is a
hypothesis. Inject a panic / a metadata-staleness bug and confirm each fires (and stays
silent on controls) before trusting it.

## Symptom-class → oracle map (coverage view)
```text
data-inconsistency        O1 (TRUSTED narrow index==record; NOT a health cert), O2->O2'
wrong-result (planner)    O2 REFUTED(count form) -> O2' rowset_hash_same_snapshot; O3 (TRUSTED);
                          O25 (USED for join semantic-domain rewrites)
wrong-result (extractor)  O4' (USED + EXECUTION-HARDENED), O5 (USED, held-out pending)
wrong-result (cache)      O10, O11               (USED)
wrong-error (prepared)    O12                    (USED)
txn state semantics       O27                    (USED)
txn/NT-DML stale state     O29, O30               (O29 USED; O30 USED)
data-loss (DDL)           O13                    (USED)
wrong-error (DDL check)   O14, O22, O24          (USED)
wrong-acceptance (DDL)    O19, O20, O23          (USED)
wrong-error (DDL flag)    O18                    (USED)
metadata corruption (DDL) O16, O17               (USED)
contract-ambiguous        O6                     (USED)
metadata / dangling ref   O7, O8                 (USED)
metadata staleness        O9 REFUTED -> O9' stats_value_differential (LLM-VERIFIED + partial exec)
restore special-object state O33 sequence_no_reuse_after_flashback_db
                         (USED + EXECUTION-CONFIRMED via id1500003)
parameterized state split O34 cross_task_stateful_differential
                         (USED + HELD-OUT RED via PR #66217 review P1; not a new bug)
ddl-hang / stuck DDL      O28 ddl_job_liveness   (TRUSTED via id30038 + id1350002)
ddl transient-fault availability O31 ddl_retryable_fault_terminal_oracle
                         (USED + EXECUTION-CONFIRMED via id1290001 single-hit rollback)
ddl old-schema safe-window availability O32 schema_change_old_schema_commit_oracle
                         (USED + EXECUTION-CONFIRMED via id1440001 MDL-off red / MDL-on green)
online-DDL x concurrency  O22 (+ O28 for liveness) (USED + EXECUTION-CONFIRMED)
panic                     no_panic_probe         (HYPOTHESIS)
```
The remaining HYPOTHESIS rows are where the method is still blind-by-assumption: it believes it
has an oracle, but held-out has never proven it fires. `metadata_sync_check` graduated from this
list in run #3 — the same path every hypothesis oracle must take before being trusted.

## O35 ttl_refreshed_row_survival

```text
obligation:  an irreversible delete-time recheck must preserve a row that is current under the
             semantic cutoff used by the scan that selected its identity.
form:        capture the scan/job token and context C0; pause after scan handoff; refresh the row
             into a C0-safe value; optionally mutate one context component; release the real delete
             worker; observe job terminal state and final row existence.
red:         predicate under C0 is false, job completes successfully, and the row is absent.
green:       identical pause/refresh schedule with stable context completes and preserves the row.
catches:     scan/delete context drift where a stable token is reinterpreted under mutable time
             zone, locale, collation, SQL mode, schema, or policy state.
blind to:    rows still expired under C0, scans that never selected the handle, and generated-SQL
             mismatches that do not reach the actual action owner.
sensitivity: EXECUTION-CONFIRMED on id1620002; UTC->+08 deleted a refreshed DATETIME row.
specificity: GOOD; same worker schedule under UTC->UTC preserved the row, and old #41043's pre-job
             scenario stayed GREEN.
status:      USED + EXECUTION-CONFIRMED.
```

## O36 br_live_restore_registry_retention

```text
obligation:  an abort/takeover observer must not delete state owned by a proven-live heartbeat
             writer, including when the observer holds locks needed by that writer.
form:        prove heartbeat timestamp progress before the observer transaction; acquire the target
             lock; continue the same writer; record writer result, deleted ID, and final row count.
red:         pre-lock progress, post-lock write conflict, nonzero deleted ID, registry row absent.
green:       pre-lock live-owner classification retains the row; a no-heartbeat stale task is deleted.
catches:     observer-induced stale evidence, lease/heartbeat self-suppression, unsafe takeover.
blind to:    tests without an independently proven live writer, mock stores without lock semantics,
             and conflict-only probes that do not observe the terminal action.
sensitivity: EXECUTION-CONFIRMED on id1650002; live-owner RED 3/3 on real TiKV.
specificity: GOOD; truly stale control deleted correctly 3/3 under the same compressed clock.
status:      USED + EXECUTION-CONFIRMED.
```

## O37 br_terminal_status_artifact_coherence

```text
obligation:  a successful BR terminal result requires the operation's irreversible action and
             success artifact to exist.
form:        inject failure at a checked pre-action boundary; capture process exit, internal summary,
             action observer, and storage artifact; rerun no-fault and one-variable counterfactual.
red:         exit 0 while summary says failed and backupmeta is absent.
green:       no-fault exits 0 with backupmeta; checked-error counterfactual exits nonzero without it.
catches:     stale error identity, swallowed terminal result, false-success acknowledgement.
blind to:    tests that inspect only logs/errors, or artifacts that pre-existed the operation.
sensitivity: EXECUTION-CONFIRMED on id1680003.
specificity: GOOD; same real-TiKV command without fault produced a 285-byte backupmeta.
status:      USED + EXECUTION-CONFIRMED.
```

## O38 resource_group_metadata_runtime_coherence

```text
obligation:  DDL terminal state, metadata definition, and PD runtime definition must describe one
             committed resource-group state.
form:        pause after PD mutation, cancel the DDL through SQL, release the worker, then read ALTER
             result, DDL history, SHOW CREATE, and INFORMATION_SCHEMA.RESOURCE_GROUPS.
red:         cancelled terminal/history, old metadata, new runtime definition.
green:       normal ALTER success and both views equal the new definition.
catches:     external-before-local commit, missing compensation, split control-plane truth.
blind to:    tests using only InfoSchema or only PD, and direct in-memory job cancellation.
sensitivity: EXECUTION-CONFIRMED on id1710003 with real PD.
specificity: GOOD; normal real-PD ALTER aligned both owners.
status:      USED + EXECUTION-CONFIRMED.
```
