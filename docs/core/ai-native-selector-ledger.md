# Selector Ledger
> Started 2026-07-02 per methodology v2. Every selector records its nominations and outcomes.
> Rules: invalid(untriggered) outcomes count neither way; retire after several consecutive green(triggered) nominations; rank the target queue by this ledger.
> Counting: nominations are counted by **root cause**, not by surface — see `ai-native-root-cause-ledger.md`. A prose reopen condition below ("another owner of the same shape") does not by itself justify a new nomination; every reopen must pass the Reopen test in methodology-v2 → Blast-Radius Stop Rule (fix-locus / new-reasoning / consequence-escalation). Same-fix siblings are blast-radius reach on one root, not new hits.

## S1: sibling-path state reconstruction
```text
selector:   success path records state; sibling rollback/cancel/restore path reconstructs it
            and can drop an owner/type bit that a later consumer trusts
born from:  id30009 (add-index global rollback delete-range)
predictions:
  - restore-path placement refs (jointly with S2) → RED (id30011)
  - ADD UNIQUE MVI + sibling multi-column UNIQUE under concurrent DML → RED (id30038)
status:     active — 2/2 hits.
            New from id30038: "reconstructs it" also includes flattening generated artifacts and
            later reconstructing their owner from ordinal. If one owner can emit multiple artifacts
            (MVI keys, expanded partition refs, generated hidden objects, multi-range task outputs),
            the flattened record must carry its owner/type bit explicitly. `i%len(owners)` is only
            safe when every owner emits exactly one artifact.
```

## S2: reference owner without reverse scan
```text
selector:   create/alter validates a reference exists; drop/rename/drop-container/restore paths
            have no reverse dependency scan
born from:  id30005 (sequence-default dangling refs)
predictions:
  - restore-path placement refs (jointly with S1) → RED (id30011)
status:     active — 1/1 hits
```

## S3: shortcut/extractor lossy prefilter
```text
selector:   custom system-table/cache/shortcut path performs a lossy semantic prefilter and
            drops the original predicate; CASE-wrapped/no-shortcut oracle exists
born from:  id30010 (InfoSchema LIKE collation)
predictions:
  - cluster_log time-range extractor x session time_zone → RED (id30012)
  - cluster diagnostic type= over bin collation → INFO/candidate (id30013, contract-ambiguous;
    MySQL reference differential settled the general contract, owner rules the exemption)
  - cluster_log time equality x sub-millisecond literal → RED (id30015)
  - InfoSchema object-name scalar pushdown x value normalization → RED (id30018)
  - metrics_summary METRICS_NAME x value normalization → RED (id30019; representative
    cross-owner blast-radius, not a new selector)
  - statements_summary coarse interval range x `summary_begin_time <= A AND summary_end_time >= B`
    with `A < B` → RED (id30021; new interval-overlap skip sub-shape, not helper blast-radius)
  - tikv_region_peers REGION_ID point lookup x missing backend object → RED (id30022;
    backend not-found must map to SQL empty rowset, not PD error)
  - tidb_hot_regions_history UPDATE_TIME x request timezone/render timezone split → RED
    (id30023; returned rows fail their own SQL-visible time predicate)
  - tikv_region_peers REGION_ID/STORE_ID x negative/out-of-domain uint64 conversion → RED
    (id30026; parse failure emptied the backend filter after the SQL predicate was dropped)
  - inspection_result cluster_config detail x table-name-only inspection cache → RED
    (id30027; cache hit reused a full cluster_config snapshot after `type=tikv` had been
    consumed into extractor dimensions and removed from scalar Selection)
  - cluster_log message LIKE x custom ESCAPE → RED (id30030; the extractor kept the
    pattern string but ignored the ESCAPE argument before dropping scalar recheck)
  - information_schema.tables TABLE_NAME LIKE x custom ESCAPE → RED (id30031;
    representative cross-owner blast-radius for operator semantic arity; stop enumerating
    InfoSchema pattern-matchable columns)
  - cluster_log message REGEXP_LIKE x match_type='i' → RED (id30033; same
    operator-semantic-arity selector, but a different omitted input from LIKE ESCAPE:
    regexp match_type/flags)
  - tidb_hot_regions_history update_time sub-millisecond equality → GREEN (triggered; same
    millisecond-looking EXPLAIN range, but execution returned no scalar-false rows)
status:     active — family total is 14 semantic violations (13 confirmed + 1 candidate)
            plus 1 useful GREEN calibration. Strongest selector. Note: id30013 is the same
            collation sub-family as id30010 across a different extractor class — genuine new
            source location, but flag blast-radius risk: do NOT keep probing level/rule/
            metrics_name variants (that is id30013's blast radius).
sub-heuristic (add to battery): sibling extractors pass session tz; one passes server-local tz.
                                "N siblings do X, one does Y" is a high-signal extractor red flag.
                                Sharper form seen in id30013: the SAME column name gets different
                                normalization flags across siblings (type: true x3, false x1).
                                New from id30015: if the backend request has lower precision than
                                the SQL predicate, shortcut extraction is safe only with scalar
                                recheck or exact precision preservation.
                                Green calibration after id30017: `tidb_hot_regions_history` prints
                                second-level EXPLAIN windows but did not return rows for
                                `update_time = ts.000500` when only `ts.000000` rows existed; do not
                                classify display precision alone as RED.
                                New from id30018: scalar-function pushdown and value/key
                                normalization are separate proof claims. If `LOWER/UPPER(col)=const`
                                is used only as a prefilter, either preserve the scalar predicate
                                or prove the normalized key comparison is exactly equivalent.
                                New from id30019: once a generic helper issue is proven across a
                                second owner, record one representative blast-radius case and stop
                                enumerating all `valueToLower=true` users.
                                New from id30021: if a shortcut maps interval rows to a coarse
                                point/range abstraction, `start > end` may mean "the requested
                                containment interval is non-empty", not "the predicate is
                                unsatisfiable". Skip paths must prove SQL predicate
                                unsatisfiability, not only empty shortcut range.
                                New from id30022: if a shortcut delegates a SQL filter to an
                                external point lookup, backend object-not-found usually means
                                "no matching SQL row", not query failure. IN-list lookup must
                                handle missing ids independently and still return valid ids.
                                New from id30023: when a shortcut uses one context to request
                                backend rows and another path constructs SQL-visible rows, both
                                contexts must be proven equivalent. Time conversion helpers like
                                `Time.In(tz)` return a new value; dropping the scalar predicate
                                turns a missed assignment into wrong-result.
                                New from id30026: type-domain conversion is part of the shortcut
                                proof. If a SQL value is converted into a narrower backend
                                request domain, conversion failure cannot be ignored after the
                                original predicate has been dropped; either keep scalar recheck,
                                mark skip_request, or prove the empty/invalid request semantics.
                                New from id30027: shortcut caches need key-granularity proof.
                                A table-name-only cache is unsafe when a later query depends on
                                extractor-consumed dimensions such as node type/address and those
                                dimensions are not rechecked as scalar predicates on cache hit.
                                New from id30030: pattern syntax is not just a string. LIKE
                                replacement must preserve the ESCAPE character, or keep scalar
                                recheck when the escape differs from the default.
                                New from id30031: once an omitted scalar-operator input is
                                confirmed across a second extractor owner, record one
                                representative blast-radius case and stop enumerating every
                                table/column using the same helper.
                                New from id30033: operator semantic arity is not exhausted by
                                LIKE ESCAPE. REGEXP_LIKE has a third semantic input,
                                `match_type`; if the extractor records only column+pattern and
                                drops the scalar predicate, `match_type='i'` vs `'c'` gives a
                                compact red/green matrix. Do not enumerate all regexp flags; this
                                single case proves the omitted-match_type dimension.
oracle gate: every future S3 nomination must pass O4' classification: trigger-evidenced fast
             arm, scalar/no-shortcut reference, ordered rowset comparison, and explicit
             RED/GREEN/INVALID/INFO classification. Contract-ambiguous splits route to O6 and
             must not be counted as confirmed selector hits until the product contract is settled.
```

## S4: stale owner/container key
```text
selector:   side state stores object ID + owner/container key; move/rekey path updates the ID
            binding but cleanup trusts the stale owner key
born from:  id30008 (table-lock cross-schema rename)
predictions:
  - stats lock x EXCHANGE PARTITION table/partition ID swap → RED (id30017)
  - masking policy x EXCHANGE PARTITION table/partition ID swap → RED (id630014)
  - TTL status/timer x EXCHANGE PARTITION TTL/non-TTL ID swap → RED-low (id630024)
status:     active — 3/3 post-birth predictions hit, plus id30039 as blast-radius reach on the
            same `exchange-idswap-orphan` root (persisted analyze options). id630024 remains a
            quality calibration.
            Oracle lesson: count rows in a side table only proves survival, not ownership.
            For ID-swap DDL, require SHOW/current-object mapping plus a cleanup/round-trip
            behavior gate such as LOCK STATS t -> UNLOCK STATS t, or
            DISABLE/ENABLE/DROP masking policy by the logical table after the swap.
            New from id630024: split the oracle into two tiers. Tier 1 is storage-vs-current-owner
            diff; tier 2 is a management operation, cleanup round trip, or active scheduling/data
            behavior failure. Rank severity by tier 2. TTL exchange reached tier 1 with real TTL
            job evidence, but timer sync created the current-ID timer and disabled the old timer,
            so the finding is confirmed but low severity.
            New from id30039: tier 2 is not limited to cleanup commands. A future behavior
            consumer can be the round trip: after `EXCHANGE PARTITION`, standalone `nt` consumed
            old `pt.p0` persisted analyze options and analyzed only the inherited column list.
```

## S5: independent sibling iterator
```text
selector:   common path green, but a sibling path has its own full iterator that must visit
            every remaining partition/index/ref and can terminate early
born from:  id30007 (reorg-partition global index misses non-touched partitions)
predictions:
  - REMOVE PARTITIONING / repartition UPDATE INDEXES x global index rowset → GREEN
    (triggered; no non-touched phase, index scan/table scan/ADMIN CHECK matched)
  - REORGANIZE PARTITION duplicate nonclustered rowid repair x identical raw rows → RED
    (id600001; the iterator visited both old partitions, but the per-row fast path skipped
    the second row because target key and raw bytes matched)
status:     active — 1 post-birth RED, 1 useful GREEN calibration.
            Boundary: the high-risk shape is not "any partition/global-index rewrite"; it is a
            sibling iterator with an explicit non-touched phase after adding/dropping partitions,
            or a repair fast path whose proof of "already visited" omits source identity.
            2026-07-09 QA revalidation on testbed 8220955/current master kept id30007 RED:
            `USE INDEX(idx_b)` returned `12:120`, `IGNORE INDEX(idx_b)` returned
            `12:120,30:300`, and `ADMIN CHECK TABLE` reported 8223. Treat this as selector
            health/cross-build persistence evidence, not a new root-cause nomination.
            2026-07-09 follow-up GREEN: final-state DROP PARTITION overlap/global-index reuse was
            checked on RANGE and LIST DEFAULT shapes. After dropping the old partition, reusing an
            old global-unique key in the overlapping/default partition succeeded; `USE INDEX(idx_b)`
            and `IGNORE INDEX(idx_b)` rowsets matched, and `ADMIN CHECK TABLE` passed. This lowers
            final-state drop-overlap as a target; mid-DDL StateWriteOnly/StateDelete* windows still
            need failpoint/pause evidence before they can be judged.
            2026-07-09 pause feasibility probe: ordinary `ADMIN PAUSE DDL JOBS` is not sufficient
            on testbed 8220955 for this target. With 60k and 200k dropped-partition rows, the job
            was observed in `delete reorganization`, but pause only completed after the job reached
            `synced` (`row_count=60000/200000`). Classify as INVALID(harness), not GREEN. A real
            mid-state verdict requires a failpoint-enabled TiDB/status API or another deterministic
            hold point before rowset/admin-check oracles are meaningful.
```

## New from id30011 — S6: restore-path container bypass
```text
selector:   restore/undelete/import path re-materializes CONTAINER metadata verbatim while the
            sibling OBJECT path sanitizes references; create-time validation absent on restore
born from:  id30011 (FLASHBACK DATABASE dangling placement ref)
predictions:
  - recover table × FK parent-reference validator → RED (id30016)
  - recover table/schema × TTL schedule state → GREEN (TTL_ENABLE forced OFF)
  - recover cached table → GREEN/BLOCKED (cached table cannot be dropped)
  - recover × TiFlash replica available state → SKIPPED(environment: no TiFlash store/PD label target)
  - recover table × sequence default after sequence drop → INFO(boundary; ordinary CREATE also allows missing sequence)
  - recover table × masking policy sys-table state → INFO(boundary; static drop/recover asymmetry, but policy is DDL-consumed only and lacks a user-facing behavior oracle)
  - TTL parent creation after dangling child FK → GREEN (symmetric validator; later parent `CREATE TABLE ... TTL` is rejected with 8152)
status:     active — 1/1 high-signal validator-backed prediction hit, plus useful boundary samples.
            Sharpened rule: do not enumerate every restored field. Prefer fields whose normal
            create/alter path has an explicit validator and whose post-recover behavior has a
            low-noise oracle. FK passed both filters; TTL/table-cache/TiFlash define boundaries.
            New boundary from sequence-default: a broken executable default after recover is not
            enough for S6 unless the ordinary create/alter path would have rejected the same
            missing reference. New calibration from masking/TTL-FK: static metadata asymmetry is
            insufficient without a user-facing behavior oracle, and an explicit validator is not a
            gap if a sibling entrypoint has the symmetric guard.
            2026-07-12 identity-drift extension: the same FK oracle with a same-name empty parent is
            RED even though future INSERT plans still contain Foreign_Key_Check. The recovered
            existing row is already orphaned, so this is a distinct row-membership/identity proof
            obligation rather than another missing-parent future-write bypass. A stronger cascade
            sibling shows a same-key replacement parent can delete that recovered row through a
            normal ON DELETE CASCADE operation while ADMIN CHECK remains silent. Recorded as high
            candidate id1500002; same-name/original-row and FLASHBACK DATABASE controls remain GREEN.
```

## New from id30020 — S7: cache payload purity
```text
selector:   cache/reuse fast path keys only stable inputs, but the cached payload stores evaluated
            results whose value may depend on volatility, side effects, session state, or time
born from:  id30020 (Apply cache reuses UUID() scalar subquery results)
predictions:
  - Parallel Apply cache x volatile inner scalar expression -> RED (id30020)
  - Prepared plan cache x `tidb_sysdate_is_now` session semantic switch -> RED (id30024)
  - Plan cache timezone-offset key x TIMESTAMP DST target date -> GREEN (triggered; cache hit
    reused the plan across same-current-offset zones, but range rebuild used current session
    timezone and matched the cache-flush baseline)
  - Prepared plan cache timezone-offset key x `UNIX_TIMESTAMP(datetime literal)` historical
    offset -> RED (id30025)
  - Prepared plan cache x single-argument `WEEK(date)` constant folded under
    `default_week_format` -> RED (id30034)
  - Prepared plan cache x constant decimal division folded under `div_precision_increment`
    -> RED (id30035)
  - Prepared plan cache x `AVG()` decimal scale inferred under `div_precision_increment`
    -> RED (id30036)
  - Prepared plan cache x `_utf8mb4` literal collation inferred under
    `default_collation_for_utf8mb4` -> RED (id30037)
  - Prepared DML x `foreign_key_checks` switch -> GREEN (triggered; plan-cache key includes the
    flag and FK trigger payloads with `FKChecks` are not cloned)
  - Partial-index eligibility x parameter-sensitive `a > ?` -> GREEN (triggered; sampled partial
    index plan stayed uncached and rowsets matched after parameter change)
  - Prepared plan cache x user-variable type/collation change -> GREEN/uncached (direct semantics
    changed, but the second EXECUTE did not hit plan cache)
  - Prepared plan cache x `RAND()` volatile payload -> GREEN (cache hit observed, values changed
    across executions)
status:     active — 7 RED + 5 useful GREEN calibrations.
            Sharpened rule: key completeness is only half the proof. A cache is safe only if the
            cached payload is a pure function of that key. For SQL execution caches, the red-cell
            battery should include volatile/non-deterministic expressions and a cache-disabled
            reference. Deterministic-expression controls are mandatory so the oracle does not
            collapse into "cache changed row shape".
            New from id30024: add "semantic-switch coverage" to S7. If a session/config variable
            is consumed while building the cached object (expression construction, rewrite, plan
            generation), that variable must be in the cache key or force a rebuild. `sysdate()`
            is rewritten to `now()` when `tidb_sysdate_is_now=ON`, but prepared plan cache reused
            the old scalar-function tree because the key omitted that switch.
            Green calibration from timezone plan cache: an omitted/coarse key dimension is not a
            bug by itself if the hit path rebuilds the semantic boundary under current context.
            Always ask whether the safe path is actually skipped after the cache hit.
            New from id30025: add "coarse-key sufficiency" to S7. A key dimension may be an
            approximation, such as current timezone offset instead of the full timezone rule set.
            This is safe for boundaries rebuilt after cache hit, but not for folded/evaluated
            constants whose value depends on omitted historical rules.
            New from id30034: add "implicit semantic input" to S7. A cached payload is unsafe if a
            scalar function reads session/config state that is neither explicit in the SQL nor
            represented in the cache key/deferred-constant guard. `WEEK(date)` without an explicit
            mode reads `default_week_format`; when all arguments are constants, constant folding
            stores the old result as a plain `Constant` and a later cache hit skips re-evaluation.
            New from id30035: the same shape is not date-specific. Decimal division reads
            `div_precision_increment` while building/evaluating the expression; all-constant
            `1/7` can be folded into a cached `Constant`, so switching the session variable later
            leaves stale projected values and stale predicates until plan-cache flush.
            New from id30036: the payload need not be a folded scalar value. `AVG()` type inference
            reads `div_precision_increment` and stores the derived scale in the aggregate
            descriptor. A cache hit can run decimal division with current session precision but
            still round/render through the old cached `RetTp.Decimal`.
            New from id30037: key-covered adjacent state is not enough. The plan-cache key includes
            connection charset/collation, but `_utf8mb4` literals without explicit `COLLATE` read
            `default_collation_for_utf8mb4` during expression rewrite. A cache hit can reuse the old
            literal field-type collation and turn a current true equality into false.
oracle gate: trigger evidence must prove the cache was actually ON and the reference had it OFF;
             compare semantic output, not plan shape. For volatile values, use effectively
             collision-free functions such as UUID() and require deterministic green controls.
             For semantic switches, use `@@last_plan_from_cache=1` plus a same-prepared-statement
             cache-flush or cache-disabled reference; keep observability variables outside the
             cached query so they do not make the statement uncacheable.
             For coarse-key cases, include both a RED date/value where the omitted detail matters
             and a GREEN control where the omitted detail is irrelevant.
             For implicit-input constant-fold cases, require direct session-variable contract,
             cache-hit red, flush/off-cache reference, non-folded control, and explicit-argument
             control.
             After id30036/id30037, prioritize a getter-level scan (`EvalContext` / `BuildContext` hidden
             inputs) over function-name enumeration. Classify each survivor by cached payload class:
             folded scalar value, semantic tree/boundary, type/descriptor metadata, or
             rebuilt-at-execution green.
             Latest green calibration: a direct semantic old/new contract is not sufficient if the
             cache does not hit (`@user_var` type/collation), and a cache hit is not sufficient if
             the payload remains volatile (`RAND()`). 2026-07-09 QA probe added one more green:
             `GROUP_CONCAT` with changed `group_concat_max_len` hit prepared plan cache on the
             second execute but matched the cache-disabled/current-session reference, so that
             aggregate payload appears rebuilt/consulted at execution for this shape.
```

## New from id30028 — S8: prepared/preprocess semantic freeze
```text
selector:   PREPARE-time preprocessor/validator consumes a session semantic switch, stores the
            AST/resolve context, and EXECUTE reuses it without revalidating the current session
            switch unless schema changes
born from:  id30028 (prepared statements bypass tidb_enable_noop_functions after OFF)
predictions:
  - `tidb_enable_noop_functions` x `SQL_CALC_FOUND_ROWS` / `GROUP BY expr DESC`
    prepared under ON then executed under OFF -> RED (id30028)
  - non-strict `sql_mode` x overlong `VARCHAR` auto-conversion in prepared CREATE TABLE,
    executed later under STRICT_TRANS_TABLES -> CANDIDATE (id30029; real direct-vs-prepared
    split, but contract-ambiguous because PREPARE emitted the auto-convert warning and mutated
    the AST before EXECUTE)
status:     guarded — 1 confirmed RED + 1 contract-ambiguous candidate.
            This is deliberately separate from S7. `ADMIN FLUSH SESSION PLAN_CACHE` and a
            plan-cache-disabled control still reproduce, so the missing proof is not a physical
            plan-cache key; it is prepared AST/preprocessor semantic freshness.
            Strongest source pattern: `GeneratePlanCacheStmtWithAST` runs `Preprocess` during
            PREPARE, the preprocessor reads a session variable, and `planCachePreprocess` only
            re-runs `Preprocess` on schema-version change.
            New from id30029: S8 has two sub-shapes. First, stale validation result (id30028).
            Second, stale AST mutation (id30029), where the preprocessor rewrites DDL under
            one session mode and EXECUTE later uses the rewritten AST under another. The second
            is more contract-sensitive and should be candidate until owner/product semantics
            decide whether PREPARE-time DDL normalization is authoritative.
oracle gate: direct current-session SQL is the reference; prepared ON->OFF is the reuse arm.
             Require either `ADMIN FLUSH SESSION PLAN_CACHE` or plan-cache OFF to prove the bug
             survives physical plan rebuild. Prefer a sibling internal control such as
             sql_mode/ONLY_FULL_GROUP_BY to avoid overclaiming that all prepare-time semantics
             are intentionally frozen.
stop rule:  do not enumerate every `tidb_enable_noop_functions` syntax. Reopen only for another
            preprocessor/session switch with a different consequence oracle or a stronger
            non-DDL/current-session contract. After id30028 + id30029, ordinary S8 session-switch
            enumeration is low novelty.
```

## New from id600001 — S9: identity proof fast path
```text
selector:   code treats payload equality or target-key existence as proof that two operations
            refer to the same logical object, then skips a repair/write path
born from:  id600001 (reorg partition drops duplicate nonclustered rows when target rowid and
            raw row bytes are both equal across different old partitions)
predictions:
  - nonclustered REORGANIZE PARTITION after EXCHANGE WITHOUT VALIDATION, same rowid and same
    raw row bytes across two old partitions -> RED (id600001)
  - same rowid but different raw row bytes -> GREEN repair control, rowid regenerated
  - same raw row bytes but different rowid -> GREEN no-collision control
status:     active but guarded for this owner. The selector is reusable, but the reorg-partition
            instance is already minimized.
            Sharpened rule: equality is an unsafe identity proof when the omitted dimension is
            an owner/container/source ID. Always ask which dimensions define "same object" before
            accepting a fast path that skips the safe repair.
oracle gate: use row-multiset/cardinality preservation as the behavioral oracle, and use partition
             or internal-key observations only as trigger evidence. Guard cells must show that the
             red cell depends on the missing identity dimension, not on the DDL itself.
stop rule:  do not enumerate reorg syntax variants. Reopen S9 only for a different fast path that
            converts equality into identity while omitting source/owner/container information, or
            for fix validation of retry/crash/concurrent-DML behavior.
```

## New from id630001/id630002/id630003/id630023 — S10: DDL validation metric mismatch
```text
selector:   a metadata-only/no-reorg/target-state DDL validator runs a simplified validity check
            and treats that metric as equivalent to the target type or target-state contract
born from:  id630001 (MODIFY COLUMN shrink uses LENGTH(bytes) > newFlen to reject rows that
            valid utf8mb4 char/varchar targets accept by character length)
generalized by:
            id630002 (FK MODIFY COLUMN uses newFlen >= originalFlen / relatedFlen to reject
            target FK schemas that direct CREATE TABLE accepts)
            id630003 (partition-column MODIFY only allows string length extension and rejects
            safe shrink where target partition literals and existing rows fit)
            id630023 (partition-column MODIFY treats NULL -> NOT NULL as an unsafe flag change
            and rejects before the generic NULL data-fit check)
predictions:
  - varchar(4) utf8mb4 value `中中中` -> varchar(3) utf8mb4 -> RED (id630001)
  - char(4) utf8mb4 value `中中中` -> char(3) utf8mb4 -> RED (id630001)
  - direct insert into varchar(3)/char(3) with `中中中` -> GREEN target reference
  - ASCII `abc` varchar(4) -> varchar(3) -> GREEN metric-aligned control
  - child FK varchar(20) -> varchar(10/15), parent varchar(10) -> RED (id630002)
  - parent FK varchar(10) -> varchar(15), child varchar(20) -> RED (id630002)
  - direct target FK schemas p10/c10, p10/c15, p15/c20 -> GREEN target references
  - child widen 20->25 and parent widen 10->20 -> GREEN checker-aligned controls
  - LIST/RANGE/KEY partition column varchar(6) -> varchar(5), literals/data max length 3 -> RED
    (id630003)
  - direct target partition schemas with varchar(5) and same literals/data -> GREEN references
  - non-partition varchar(6) -> varchar(5) with max length 3 -> GREEN data-fit reference
  - partition column varchar(6) -> varchar(7) -> GREEN checker-aligned control
  - direct partition schemas with NOT NULL partition columns -> GREEN target references
  - non-partition partition-key-shaped column NULL -> NOT NULL with no NULL rows -> GREEN
  - RANGE/LIST/KEY/expression partition column NULL -> NOT NULL with no NULL rows -> RED
    (id630023)
  - non-partition NULL -> NOT NULL with a NULL row -> GREEN unsafe-data reject control
status:     active but guarded for this owner. The selector is reusable, but the current
            modify-column string-length, FK-varchar-length, and partition-column varchar-shrink
            plus partition-column nullability root causes are minimized.
            Sharpened rule: for DDL prechecks, record the unit of measure used by P_check and
            the unit of measure required by Q_claim. Byte length, character length, display
            width, encoded key bytes, collation weight, and restored-data form are different
            proof dimensions. Also compare transition validators with sibling target-state
            validators such as CREATE TABLE / ADD FOREIGN KEY. New from id630003: also compare
            partition-column transition allowlists against direct target partition definitions
            and the generic data-fit contract. New from id630023: flag/nullability allowlists
            need the same treatment; adding NOT NULL is a row-data proof, not a partition
            placement proof.
oracle gate: use direct target-type acceptance as the primary oracle. If the target schema can
             directly store the value or directly create the FK pair, a fit-check/target-state
             DDL should not reject it without an explicit stricter DDL contract. Keep binary and
             non-binary controls separate.
stop rule:  do not enumerate all charsets/string/FK type pairs or all partition/string variants.
            Reopen S10 only for a different validation metric, a silent acceptance bug, or fix
            validation across binary/indexed/FK/partition/nullability boundaries.
```

## New from id630004 — S11: DDL dependency gate overbroad
```text
selector:   dependency checker proves object A is referenced by object B, and DDL treats that as
            proof that every operation touching A is unsafe, even when the requested change is
            metadata-only or otherwise outside B's semantic dependency
born from:  id630004 (MODIFY COLUMN rejects COMMENT/DEFAULT changes on a base column used by a
            generated column)
predictions:
  - base column `a int` used by generated `b as (a+1)`, MODIFY `a int COMMENT ...` -> RED
    (id630004)
  - base column `a int` used by generated `b as (a+1)`, MODIFY `a int DEFAULT 5` -> RED
    (id630004)
  - direct target schemas with the same generated expression and comment/default -> GREEN
  - non-dependent column COMMENT change -> GREEN
  - generated column's own COMMENT change with same expression -> GREEN
  - true base-column type change -> GREEN reject
  - base column `a int` used by expression index `idx_expr((a+1))`, MODIFY
    `a int COMMENT ...` -> RED (id630007; companion/blast-radius of id630004)
  - same expression-index base column, MODIFY `a int DEFAULT 5` -> RED (id630007)
  - direct target expression-index schemas with comment/default -> GREEN, including `ADMIN CHECK`
  - non-dependent column COMMENT and DROP INDEX then COMMENT -> GREEN
  - expression-index base-column true type change -> GREEN reject
  - partial-index condition column `b`, MODIFY `b int COMMENT ...` -> RED (id630009)
  - same partial-index condition column, MODIFY `b int DEFAULT 5` -> RED (id630009)
  - direct target partial-index schemas with comment/default -> GREEN, including `ADMIN CHECK`
  - non-condition column COMMENT and DROP INDEX then condition-column COMMENT -> GREEN
status:     active but guarded. Three user-facing owners are proven. id630007 shares the generated
            column / expression-index hidden-column MODIFY gate with id630004; id630009 adds a
            distinct dependency checker, `checkColumnReferencedByPartialCondition`. Do not
            enumerate generated, expression-index, or partial-index predicate syntax. The reusable
            insight is dependency existence vs semantic-change proof.
oracle gate: use direct target-schema acceptance plus behavior that exercises the dependency.
             For generated columns, insert/select should prove the generated expression still
             evaluates correctly after the target metadata change. For expression indexes, require
             direct target creation plus a query or `ADMIN CHECK TABLE` proving the index remains
             valid. For partial indexes, require direct target creation plus `ADMIN CHECK TABLE`
             and a query that exercises the partial-index condition.
stop rule:  reopen only for a different dependency owner, a silent wrong-acceptance consequence,
            or fix validation across virtual/stored generated columns, functional indexes,
            partial-index conditions, defaults, comments, rename, drop, type change, collation,
            and nullability.
```

## New from id630005 — S13: DDL shallow-copy target mutation
```text
selector:   a DDL path constructs target object metadata from source object metadata using a
            top-level copy, then mutates target-only nested fields in place without proving the
            nested fields are target-owned
born from:  id630005 (CREATE TABLE LIKE renames source table CHECK constraints in SHOW CREATE
            TABLE while constructing the target table)
            id1200001 (CREATE TABLE LIKE copies source READ ONLY table lock to the new target
            table)
predictions:
  - direct sibling CREATE TABLE with anonymous CHECK constraints -> GREEN independent names
    (`d1_chk_1`, `d2_chk_1`)
  - `CREATE TABLE dst LIKE src` where `src` has anonymous CHECK -> RED: source SHOW CREATE changes
    from `src_chk_1` to `dst_chk_1`
  - new SQL connection after LIKE -> RED if source metadata remains polluted
  - runtime violation on source -> RED if error names target constraint
  - information_schema/show-create cross-check -> RED if surfaces disagree
  - source `ALTER TABLE src READ ONLY`; `CREATE TABLE dst LIKE src`; `INSERT INTO dst` -> RED
    if dst is also READ ONLY even though the user never locked dst
  - `ALTER TABLE dst READ WRITE`; `INSERT INTO dst` succeeds while `src` remains READ ONLY ->
    RED isolation proof for target runtime-state cloning
status:     active but guarded. Two sub-shapes are proven: pointer-backed source mutation and
            target runtime-state clone. Do not enumerate LIKE options or CHECK expression syntax.
            The reusable insight is that a top-level clone is not an ownership proof.
oracle gate: compare source metadata before/after target reconstruction and include an independent
             direct-create sibling control. A source/target isolation oracle must inspect the
             source object for mutation cases. For target-state clone cases, prove the target-only
             behavior can be cleared independently while the source behavior remains unchanged.
stop rule:  reopen only for another pointer-backed metadata owner, another unreset runtime or
            non-schema target-state field with a behavior oracle, a stronger consequence, or fix
            validation across CHECK constraints and table-lock state. Do not reopen by enumerating
            ordinary schema fields that are intentionally copied by LIKE.
```

## New from id630006 — S14: DDL recovery namespace validation bypass
```text
selector:   recover/flashback/import path re-materializes stored metadata into the current schema
            and validates only table/container identity, while sibling create/add paths validate
            schema-level namespaces or references
born from:  id630006 (FLASHBACK TABLE can restore duplicate CHECK constraint names in one schema)
predictions:
  - normal CREATE/ADD CHECK with duplicate schema-level CHECK name -> GREEN reject, error 3822
  - dropped table `f` with `f_chk_1`, recreated current table `f` with `f_chk_1`,
    then `FLASHBACK TABLE f TO f_old` -> RED: schema has two public `f_chk_1` rows
  - violating inserts into both recovered/current tables -> RED: both errors name `f_chk_1`
  - `CREATE TABLE like_copy LIKE f` -> GREEN target reconstruction, `like_copy_chk_1`
status:     active but guarded. This is not a license to enumerate every recovered field. The
            high-density shape is "normal create/add has an explicit validator, recovery publishes
            old metadata without rerunning it, and a SQL-visible namespace/reference oracle exists".
oracle gate: after recovery, query the schema-level namespace surface, not only the recovered
             table. Include sibling create/add duplicate-name rejection as the control. Runtime
             errors are supporting evidence when duplicate names make user-visible diagnostics
             ambiguous.
stop rule:  reopen only for another create/add validator skipped by recovery, a stronger
            behavioral consequence, or fix validation for CHECK names, FK references, placement
            refs, table cache, and other recovered side metadata.
```

## New from id630008 — S15: DDL idempotence flag dropped
```text
selector:   parser/AST accepts an idempotence flag such as IF NOT EXISTS, and one sibling DDL
            branch propagates it to execution while another sibling branch silently drops it; or a
            parent spec stores the flag and a split/rewrite path moves work into a child owner that
            reads a different flag slot
born from:  id630008 (ADD FOREIGN KEY IF NOT EXISTS still errors on an existing FK)
generalized by:
            id630010 (ADD IF NOT EXISTS table-element list drops the outer flag when constraints
            are split into AlterTableAddConstraint specs)
            id630015 (DROP PARTITION IF EXISTS keeps the flag, but an earlier aggregate
            count precheck uses raw requested names before missing names are classified)
            id630016 (ADD PARTITION IF NOT EXISTS keeps the flag and has a duplicate catch, but
            an earlier LIST DEFAULT capability gate returns before duplicate classification)
            id630017 (DROP INDEX IF EXISTS `PRIMARY` keeps the flag and has a generic missing-index
            catch, but an earlier PRIMARY special-name classifier returns before it)
            id630018 (CREATE TABLE IF NOT EXISTS keeps the flag and has a target-exists no-op, but
            candidate LIKE source / index / partition validation can return before target existence
            is classified)
            id630019 (CREATE SEQUENCE IF NOT EXISTS keeps the flag and has the same shared
            target-exists no-op, but candidate sequence option validation can return before it)
            id630020 (CREATE RESOURCE GROUP IF NOT EXISTS keeps the flag and has a duplicate
            classifier, but candidate resource-group option building can return before it)
            id630021 (CREATE MASKING POLICY IF NOT EXISTS keeps the flag and has a duplicate
            classifier, but candidate masking expression validation can return before it)
            id630022 (CREATE SPATIAL INDEX IF NOT EXISTS keeps the flag and has a duplicate
            classifier, but the unsupported index-type gate can return before it)
            id1020001 (CREATE USER IF NOT EXISTS keeps the flag and has a duplicate user
            classifier, but anonymous-user PASSWORD EXPIRE validation can return before it)
predictions:
  - first `ALTER TABLE c ADD CONSTRAINT fk_pid FOREIGN KEY IF NOT EXISTS ...` -> GREEN
  - same statement again -> RED, ERROR 1826 duplicate foreign key constraint name
  - plain duplicate `ADD FOREIGN KEY` without IF NOT EXISTS -> GREEN reject
  - sibling `ADD INDEX IF NOT EXISTS idx_a(a)` -> GREEN note and unchanged schema
  - `information_schema.referential_constraints` after the failed duplicate -> GREEN one FK row,
    proving wrong-error rather than duplicate metadata insertion
  - `ALTER TABLE idx_outer ADD IF NOT EXISTS (KEY idx_a(a))` twice -> RED, ERROR 1061
    duplicate key name (id630010)
  - `ALTER TABLE ck_outer ADD IF NOT EXISTS (CONSTRAINT ck_a CHECK (a > 0))` twice -> RED,
    ERROR 3822 duplicate check constraint name (id630010)
  - `ALTER TABLE col_outer ADD IF NOT EXISTS (b INT)` twice -> GREEN, Note 1060
  - `ALTER TABLE idx_inner ADD IF NOT EXISTS (KEY IF NOT EXISTS idx_a(a))` twice -> GREEN,
    Note 1061, proving index idempotence works when the child constraint flag is present
  - index/check metadata counts after the red cells -> GREEN one object each, proving wrong-error
    rather than duplicate metadata insertion
  - `ALTER TABLE onep DROP PARTITION IF EXISTS px` on a one-partition table -> RED, ERROR 1508
    Cannot remove all partitions, even though `px` is missing
  - `ALTER TABLE twop DROP PARTITION IF EXISTS px` on a two-partition table -> GREEN, Note 1507
    and both partitions remain
  - `ALTER TABLE twop DROP PARTITION IF EXISTS px, py` or `p0, px` -> RED, ERROR 1508 because
    requested-name count reaches current partition count before existence filtering
  - LIST table without DEFAULT, duplicate `ADD PARTITION IF NOT EXISTS p0` -> GREEN, Note 1517
  - LIST table with DEFAULT, duplicate `ADD PARTITION IF NOT EXISTS p0` -> RED, ERROR 8200 before
    duplicate-name no-op (id630016)
  - LIST table with DEFAULT, new `ADD PARTITION IF NOT EXISTS p1` -> GREEN capability-control,
    ERROR 8200 is still expected
  - LIST table without DEFAULT, new `ADD PARTITION IF NOT EXISTS p1` -> GREEN ordinary add
  - ordinary missing index, `ALTER TABLE no_pk DROP INDEX IF EXISTS missing_i` -> GREEN,
    Note 1091
  - special missing index, `ALTER TABLE no_pk DROP INDEX IF EXISTS `PRIMARY`` -> RED,
    ERROR 1091 `Can't DROP 'PRIMARY'`
  - top-level `DROP INDEX IF EXISTS `PRIMARY` ON no_pk` -> RED, same ERROR 1091
  - existing `PRIMARY`, `ALTER TABLE pk_nc DROP INDEX IF EXISTS `PRIMARY`` -> GREEN success
  - target exists, valid `CREATE TABLE IF NOT EXISTS t(b BIGINT, c VARCHAR(60))` -> GREEN,
    Note 1050 and target unchanged
  - target exists, valid `CREATE TABLE IF NOT EXISTS t LIKE src` -> GREEN, Note 1050
  - target exists, missing source `CREATE TABLE IF NOT EXISTS t LIKE missing_src` -> RED,
    ERROR 1146
  - target exists, invalid candidate index `CREATE TABLE IF NOT EXISTS t(a INT, INDEX idx_b(b))`
    -> RED, ERROR 1072
  - target exists, invalid candidate partition expression `PARTITION BY RANGE(b)` -> RED,
    ERROR 1054
  - target absent with the same invalid candidate/source -> GREEN hard-error controls
  - target exists, duplicate-column candidate `CREATE TABLE IF NOT EXISTS t(a INT, a INT)` ->
    GREEN Note 1050, showing only earlier candidate validators are suspect
  - target sequence exists, valid duplicate `CREATE SEQUENCE IF NOT EXISTS seq START WITH 20 ...`
    -> GREEN, Note 1050 and old sequence definition unchanged
  - target sequence exists, invalid candidate `INCREMENT 0` -> RED, ERROR 4136
  - target sequence exists, invalid candidate `MAXVALUE 1 START WITH 2` -> RED, ERROR 4136
  - target sequence exists, unsupported table option `CHARSET=utf8` -> RED, ERROR 8227
  - target sequence absent with the same invalid options -> GREEN hard-error controls
  - target resource group exists, valid duplicate
    `CREATE RESOURCE GROUP IF NOT EXISTS ai_rg_s15 RU_PER_SEC=2000` -> GREEN, Note 8248 and old
    definition unchanged
  - target resource group exists, unused invalid candidate `BACKGROUND=()` -> RED, ERROR 1105
  - target resource group absent with the same `BACKGROUND=()` option -> GREEN hard-error control
  - target masking policy exists on the same table/column, valid duplicate candidate
    `CREATE MASKING POLICY IF NOT EXISTS p_mp ON t(a) AS concat(a,'_x')` -> GREEN, Note 1105 and
    old expression/status unchanged
  - target masking policy exists on the same table/column, unused invalid expression `AS b` ->
    RED, ERROR 8275
  - target masking policy absent with the same invalid expression -> GREEN hard-error control
  - target index exists, valid duplicate candidate `CREATE INDEX IF NOT EXISTS idx_a ON t(b)` ->
    GREEN, Note 1061 and old `idx_a` remains on column `a`
  - target index exists, unsupported candidate type
    `CREATE SPATIAL INDEX IF NOT EXISTS idx_a ON t(a)` -> RED, ERROR 8200
  - target index absent with the same unsupported type -> GREEN hard-error control
  - target account exists, valid duplicate `CREATE USER IF NOT EXISTS ''@'ai_s15_host'` ->
    GREEN, Note 3163 and `mysql.user.Password_expired` stays `N`
  - target account exists, unused invalid account attribute
    `CREATE USER IF NOT EXISTS ''@'ai_s15_host' PASSWORD EXPIRE` -> RED, ERROR 3016
  - target account absent with the same invalid attribute -> GREEN hard-error control, ERROR 3016
    and no `mysql.user` row
negative calibration:
  - FK names are not schema-level unique across different child tables in this build; ordinary
    `CREATE TABLE c1 ... CONSTRAINT fk_dup` and `CREATE TABLE c2 ... CONSTRAINT fk_dup` both
    succeed, so FK-name recovery is not an S14 schema-namespace red cell.
  - `DROP FOREIGN KEY IF EXISTS` is not accepted by the parser here, so it is outside id630008.
  - `ALTER TABLE ADD UNIQUE INDEX IF NOT EXISTS` was parser-invalid in this build, so the unique
    branch that passes `false` is not user-triggerable through that syntax.
  - Bare `PRIMARY` is parser-special in `DROP INDEX` syntax; quoted `` `PRIMARY` `` is required
    to exercise the Identifier path for id630017.
  - `CREATE VIEW IF NOT EXISTS` is parser-unsupported here, so it is not an executable
    idempotence promise.
  - `CREATE PLACEMENT POLICY IF NOT EXISTS` is a green control for the obvious invalid-option
    sub-shape because policy existence is checked before `checkPolicyValidation`.
  - `CREATE DATABASE IF NOT EXISTS` is a green control in source order: `CreateSchemaWithInfo`
    checks schema existence before charset/collation and placement validation.
  - `ALTER SEQUENCE IF EXISTS` is a green control in source order: it checks schema/table
    existence before `alterSequenceOptions` validates new options.
  - `ALTER RESOURCE GROUP IF EXISTS` is a green control in source order: it checks resource-group
    existence before `buildResourceGroup` and `checkResourceGroupValidation`.
status:     active but guarded. Six distinct S15 sub-shapes are proven (each adds a checklist
            step, so each passes the Reopen test as its own root):
              1. sibling executor branch drops a child flag (id630008)
              2. parent spec splitting loses a flag before the child owner executes (id630010)
              3. raw-name-count precheck returns before existence classification (id630015)
              4. capability/default gate returns before the duplicate classifier (id630016)
              5. special-name classifier returns before the missing-object catch (id630017)
              6. candidate/builder/validator runs before the target-exists classifier (id630018)
            Sub-shape 6 is ONE root with a wide blast radius: id630019 (sequence), id630020
            (resource-group), id630021 (masking-policy), id630022 (create-index), id1020001
            (create-user) all reach it by the same audit step ("find the existence/duplicate
            classifier, audit every validator/builder before it") and stay consequence-1. They
            are 5 blast-radius surfaces of root 6, NOT five more sub-shapes — the earlier "seven
            sub-shapes" count was surface inflation. Recorded as "root 6 affects 6 create-like /
            account owners". Consequence cap is in force: S15 is a consequence-1 family, so it
            reopens only on a genuinely new ordering mechanism (a 7th checklist step) or a
            consequence escalation (silent duplicate-write / wrong-acceptance), never on another
            owner of sub-shape 6. Do not enumerate more create-like/account owners.
oracle gate: compare flagged duplicate behavior with unflagged duplicate behavior and with a
             sibling/inner DDL owner that already implements the same flag. Include a schema-count
             check to distinguish wrong-error from duplicate-write. For `IF EXISTS`, include
             boundary cells where requested-name count differs from existing-name count, and
             special-name cells where a helper classifies an absent object before the generic
             missing-object handler. For
             `IF NOT EXISTS`, include capability-gate controls where the requested object already
             exists versus genuinely new objects, plus target-exists/candidate-invalid controls
             for create-like owners. Treat candidate builders and option setters as potential
             validators, not harmless construction. For policy-like owners, first pin the target
             identity to the same name/table/column so an invalid candidate is not mistaken for a
             different object. For account-like owners, first pin identity to the same
             username+host; then vary only candidate account attributes.
stop rule:  reopen only for another DDL idempotence flag owner, a silent duplicate-write or
            wrong-acceptance consequence, another spec-splitting/AST-rewrite flag loss, another
            raw-request precheck ordering gap, another capability/default precheck ordering gap,
            another special-name/helper-before-existence-catch gap, another target-exists delayed
            behind candidate-validation gap, an identity-pinned account-DDL ordering gap, or fix
            validation across FK duplicate/missing-parent/missing-index cases, index/check
            table-element lists, sibling index behavior, DROP PARTITION IF EXISTS count
            boundaries, ADD PARTITION IF NOT EXISTS on LIST DEFAULT tables, DROP INDEX IF EXISTS
            `PRIMARY`, CREATE TABLE IF NOT EXISTS prevalidation, CREATE SEQUENCE / RESOURCE GROUP
            / MASKING POLICY / INDEX IF NOT EXISTS prevalidation, and CREATE USER IF NOT EXISTS
            account-attribute prevalidation.
```

## New from id630011 — S16: DDL validator ordering gap
```text
selector:   a transition DDL validator runs before the target object state is fully materialized,
            then later option/default/nullability/collation processing changes a dimension that the
            validator should have proven
born from:  id630011 (MODIFY COLUMN allows a child FK column to become NOT NULL even though the FK
            action is ON DELETE SET NULL or ON UPDATE SET NULL)
predictions:
  - direct child `pid INT NOT NULL` + `ON DELETE SET NULL` -> GREEN reject, ERROR 1830
  - direct child `pid INT NOT NULL` + `ON UPDATE SET NULL` -> GREEN reject, ERROR 1830
  - nullable child FK with `ON DELETE SET NULL`, then `MODIFY pid INT NOT NULL` -> RED, ALTER
    succeeds and parent DELETE later fails with ERROR 1048
  - nullable child FK with `ON UPDATE SET NULL`, then `MODIFY pid INT NOT NULL` -> RED, ALTER
    succeeds and parent UPDATE later fails with ERROR 1048
  - nullable child FK with `ON DELETE RESTRICT`, then `MODIFY pid INT NOT NULL` -> GREEN, because
    the action never needs to write NULL
  - direct parent `INT`, child `INT UNSIGNED`, FK -> GREEN reject, ERROR 3780
  - signed/signed child FK, then `MODIFY a INT UNSIGNED` -> RED, ALTER succeeds and parent
    `ON UPDATE CASCADE` to `-1` later fails with ERROR 1264 (id630012)
  - drop/re-add the same FK after the red signedness ALTER -> GREEN reject, ERROR 3780
  - child FK collation change with same type/flen/decimal -> GREEN; later indexed-column
    collation validation rejects before the invalid FK state is published
  - primary-key column `MODIFY ... NULL` -> GREEN; later primary-key/default checks preserve the
    NOT NULL state and reject NULL inserts
status:     active but guarded. Two target-state ordering dimensions are proven: nullability for
            SET NULL actions and signedness for integer FK compatibility. Do not enumerate FK
            actions, FK type pairs, or multi-column syntax. The reusable insight is validator
            timing plus proof precision: the checked `newCol` was not always the final target
            `newCol`, and type/flen/decimal equality is not proof of full FK compatibility.
oracle gate: compare transition DDL against direct target-state rejection, then exercise the
             invalid target state with a behavior oracle. A plain ALTER success is insufficient;
             the final schema must either be rejected by a sibling target-state validator or fail
             when the missing dimension is used.
stop rule:  reopen only for another validator-before-options/defaults/collation/nullability gap,
            a silent wrong-acceptance consequence, or fix validation that aligns transition and
            target-state validators.
```

## New from id630013 — S17: DDL reorg constraint bypass
```text
selector:   a DDL reorg/backfill path decodes existing rows, transforms values or row layout, and
            writes the converted row through a low-level/raw path that bypasses normal DML row
            invariants
born from:  id630013 (MODIFY COLUMN converts CHECK-valid fractional positive values to INT 0 and
            leaves rows violating CHECK(a > 0))
predictions:
  - DECIMAL(10,2) value 0.40 with CHECK(a > 0), then MODIFY a INT -> RED, ALTER succeeds,
    SHOW WARNINGS empty, final row has a=0 and a>0=0
  - DOUBLE value 0.4 with CHECK(a > 0), then MODIFY a INT -> RED, same post-conversion violation
  - VARCHAR value '0.4' with CHECK(a > 0), then MODIFY a INT -> RED, same post-conversion violation
  - ADD CHECK(a > 0) to an INT table already containing 0 -> GREEN reject, ERROR 3819
  - INSERT 0 into the altered table -> GREEN reject, ERROR 3819, proving ordinary DML CHECK
    enforcement is alive
  - ADMIN CHECK TABLE on the altered table -> INFO, returns success because it checks record/index
    consistency, not CHECK predicate validity
status:     active but guarded. One row-invariant owner is proven: existing CHECK constraints on
            MODIFY COLUMN type reorg. Do not enumerate source/target type pairs.
oracle gate: require three facts before RED: old rows satisfied the CHECK before ALTER; conversion
             succeeds and changes the predicate truth value; a safe-path oracle such as ADD CHECK
             or ordinary DML rejects the same final value.
stop rule:  reopen only for another raw DDL writer, another row invariant owner, a silent
            data-integrity consequence, or fix validation. The target is the writer/invariant
            boundary, not the type conversion matrix.
```

## New from id30032 — S18: embedded constraint owner loss
```text
selector:   a parent DDL owner accepts a syntax node that embeds a child semantic obligation, but
            only submits the parent job and never transfers the child obligation to its real owner
born from:  id30032 (ALTER TABLE ADD COLUMN b INT DEFAULT 1 CHECK(b > 0) succeeds without
            warnings but publishes no CHECK constraint and accepts b=0)
predictions:
  - direct CREATE TABLE with inline column CHECK -> GREEN reference, SHOW CREATE includes CHECK,
    INSERT b=0 rejects with ERROR 3819
  - sequential ALTER ADD COLUMN; ALTER ADD CONSTRAINT CHECK -> GREEN reference, SHOW CREATE
    includes CHECK, INSERT b=0 rejects with ERROR 3819
  - inline ALTER ADD COLUMN ... CHECK -> RED, ALTER succeeds with @@warning_count=0, SHOW CREATE
    has no CHECK, information_schema.check_constraints has no row, INSERT b=0 succeeds
  - named inline ALTER ADD COLUMN ... CONSTRAINT ck CHECK -> RED sibling, same root: named CHECK
    is also silently dropped and b=0 succeeds
  - table-level ALTER ADD COLUMN b ..., ADD CONSTRAINT CHECK(b > 0) -> INFO/secondary: currently
    fails with unknown column b because the ADD CHECK owner sees the pre-add schema; this is a
    target-schema handoff smell but not the primary silent-loss bug
  - column-level REFERENCES in CREATE/ALTER -> BOUNDARY: direct CREATE also ignores
    `pid INT REFERENCES p(id)` with no FK metadata and accepts bad parent keys, so this is not an
    ALTER ADD COLUMN-specific owner-handoff red cell
  - same-statement ADD COLUMN b + ADD INDEX/CHECK/generated depending on b -> BOUNDARY: direct
    target and sequential paths succeed, but TiDB documentation says multi-schema ALTER validates
    against the pre-execution schema and can reject references to columns added earlier in the same
    statement; do not count as a confirmed product bug without an owner ruling
status:     active but guarded. One embedded-sub-obligation owner is proven: column-level CHECK
            inside ADD COLUMN. Do not enumerate column options. The reusable insight is owner
            handoff, not CHECK syntax.
oracle gate: require a target-schema reference and a behavior oracle. Direct CREATE and sequential
             ADD CHECK must both preserve/enforce the child obligation; the transition path must
             succeed while losing it; a violating write or equivalent user-visible behavior must
             prove the loss matters.
stop rule:  reopen only for a different embedded child owner (constraint/index/reference/default
            with its own validator or job), same-root fix validation, or a stronger consequence
            than constraint absence plus violating write.
```

## New from id630025 — S19: internal validation SQL builder gap
```text
selector:   a DDL safe-path validator hand-builds an internal SQL predicate for a richer semantic
            relation, then uses that predicate as proof that the target state is safe
born from:  id630025 (EXCHANGE PARTITION WITH VALIDATION on LIST DEFAULT emits invalid restricted
            SQL because DEFAULT partition membership is a complement, not current InValues)
predictions:
  - direct INSERT value 3 into LIST DEFAULT table -> GREEN, row routes to pdef
  - ordinary LIST p1 exchange with row 1 -> GREEN, validation succeeds
  - LIST DEFAULT pdef exchange with row 3 -> RED, ERROR 1064 near ") limit 1"
  - LIST DEFAULT pdef exchange with row 3 WITHOUT VALIDATION -> GREEN boundary, swap succeeds
  - LIST COLUMNS DEFAULT exchange with row (3,3) -> RED sibling, same root
status:     active but guarded. One validation-builder owner is proven. This is consequence-1,
            so do not let it reopen partition syntax enumeration; the value is the selector
            refinement, not the severity.
oracle gate: prove direct target-state membership first, then compare the internal validation path
             against a sibling validation builder and a boundary path that bypasses the builder.
             A syntax error alone is not enough.
stop rule:  reopen only for another internal validation SQL builder that omits a different
            semantic dimension, a wrong-acceptance/data-placement consequence, or fix validation
            for DEFAULT partition exchange.
```

## New from id30040 — S20: semantic-domain rewrite narrowing
```text
selector:   optimizer/executor replaces a general semantic domain with a cheaper/narrower domain,
            proves only a local guard, then drops or bypasses the original comparison/predicate
born from:  id30040 (join_key_type_cast replaces INT/VARCHAR numeric comparison with INT equality
            and a signed-int round-trip guard, dropping `10 = '1e1'` matches)
predictions:
  - INT id 10 joined with VARCHAR '1e1' -> RED, default rule misses the row while CASE and
    rule-disabled references return it
  - canonical strings '1'/'10' and decimal integer-valued '10.0' -> GREEN controls
  - fractional/non-numeric strings -> GREEN no-match controls
status:     active. This is a new root and a useful non-DDL selector because it is orthogonal to
            S3 extractor loss and S7 cache reuse. The audit move is to name D_old and D_new before
            writing SQL: here D_old is DOUBLE/numeric-string comparison, D_new is signed INT
            equality after a guard.
oracle gate: require direct scalar contract, trigger evidence that the rewrite fired, and a safe
             path that preserves the original scalar comparison (CASE, no-shortcut, or rule
             blacklist). Plan shape alone is not enough.
stop rule:  do not enumerate numeric string spellings. Reopen only for another semantic-domain
            rewrite, a consequence escalation, or fix validation.
```

## New from id1200002 — S21: txn stack operation semantic split
```text
selector:   several SQL operations share one ordered transaction-state stack, but each operation
            has a different contract; implementation uses the wrong sibling's stack mutation
born from:  id1200002 (RELEASE SAVEPOINT truncates later savepoints, so `ROLLBACK TO sp2`
            fails after `RELEASE SAVEPOINT sp1`)
predictions:
  - savepoint stack `sp1,sp2`, then RELEASE sp1, then ROLLBACK TO sp2 -> RED, sp2 is gone
  - savepoint stack `sp1,sp2`, then ROLLBACK TO sp1 -> GREEN, later sp2 is deleted by design
  - RELEASE sp2, then ROLLBACK TO sp1 -> GREEN, earlier sp1 remains usable
status:     active but guarded. One txn state-stack semantic split is proven. The useful move was
            not to enumerate transaction modes; it was to list operation contracts side by side:
            ADD replaces duplicate name then appends, RELEASE removes only the named marker,
            ROLLBACK TO restores state and discards later markers.
oracle gate: require an external/reference contract for the stack operation, a minimal two-marker
             matrix, and a user-visible consequence after the mutated stack is consumed. Source
             comments or existing tests are not enough because they may encode product drift.
stop rule:  do not enumerate savepoint names, case variants, or autocommit modes. Reopen only for
            another txn state-stack operation split, stronger consequence, or fix validation.
```

## 2026-07-09 calibration — S22-candidate: IndexMerge residual/filter preservation
```text
selector:   planner synthesizes an IndexMerge OR path from branch access filters and top-level AND
            filters; it may push filters into partial paths, clear TableFilters/IndexFilters, or
            rely on KeepIndexMergeORSourceFilter to preserve the original predicate
source:     pkg/planner/core/indexmerge_unfinished_path.go —
            mergeANDItemIntoUnfinishedIndexMergePath() says it does not do precise checks and
            collects whole useful AND items; buildIntoAccessPath() later clears/preserves filters
            and installs IndexMergeORSourceFilter/TableFilters.
oracle:     O2'-style hinted fast path vs safe path: USE_INDEX_MERGE(...) must match
            NO_INDEX_MERGE(), with EXPLAIN proving IndexMerge actually fired; ADMIN CHECK is only a
            storage sanity control here, not the primary planner oracle.
matrix:     testbed 8220955/current master: 4/4 GREEN(triggered), 0 RED, 0 INVALID.
            Cases: top-level AND folded into composite ranges; top-level residual expression kept
            as Probe Selection; binary-collation residual kept as Probe Selection; branch-local CNF
            with composite ranges. Rowsets matched in all cases and ADMIN CHECK was green.
status:     candidate calibration, not promoted as a high-density selector yet. The useful boundary
            is not "any IndexMerge"; reopen only when source shows a filter class can be removed,
            weakened, or made parameter-sensitive after path synthesis (e.g. MVI alternatives,
            non-pushdown filters, plan-cache parameter guards), and keep the NO_INDEX_MERGE rowset
            reference mandatory.
            2026-07-09 MVI-specific calibration: reopened only the promised special path in
            `pkg/planner/core/indexmerge_path.go` (MV access-filter mutation, same-MVI multi partial
            paths, MVI+normal/MVI intersection, and plan-cache guards). testbed 8220955 produced
            GREEN(triggered) for dual-MVI intersection (`1,4,8`), residual Probe Selection (`1,4,8`),
            JSON_CONTAINS multi-value intersection (`1`), and JSON_OVERLAPS union (`1,2,3,4,8`).
            `ADMIN CHECK TABLE` was green. Prepared-cache guard: `? MEMBER OF(a)` cache-hit on the
            second execute still tracked the current parameter/reference; parameterized
            `JSON_CONTAINS(a, ?)` / `JSON_OVERLAPS(a, ?)` did not hit prepared cache, so those are
            INVALID_NO_CACHE_HIT for S7, not product GREEN/RED. Boundary tightened: do not fuzz MVI
            IndexMerge generally; reopen only for a concrete owner/type-bit loss, array predicate
            removal/weakening, or a demonstrated cache guard bypass with stale output.
```

## Queued targets (scored, not yet probed)
- prefix-index full-predicate re-check × collation/multibyte boundary (S3-adjacent; sampled green on 2026-07-03 because Selection/TopN safe paths remained)

## New from id1230001 — S23: stale transaction input leak into split-range planning
```text
selector:   a statement wrapper tries to neutralize session state by clearing one stale/read-only
            input, then internally runs SELECT/validation SQL through the generic session path;
            a sibling input still drives the internal read
born from:  id1230001 (NT-DML clears ReadStaleness but not TxnReadTS, so the split-range SELECT
            runs at SET TRANSACTION READ ONLY AS OF TIMESTAMP and derives stale ranges)
predictions:
  - ordinary DML under tx_read_ts -> GREEN control, write rejected as read-only stale transaction
  - NT-DML without tx_read_ts -> GREEN control, current rowset fully updated
  - NT-DML with tx_read_ts and rows inserted after @ts -> RED, only stale-visible ranges are
    updated while the statement reports success
  - CREATE SESSION BINDING FROM HISTORY after tx_read_ts -> RED/GREEN validated target. The binding path runs
    a statement-summary lookup through current-session `ExecuteInternal`; the lookup consumes
    pending `TxnReadTS`, so the next user SELECT reads the current rowset instead of the intended
    stale rowset. A TSO-stable probe recorded current RED
    `before=467570589524557824 after=0 next_select_rows=[[1] [2]]`; the same probe passed under
    a temporary `ExecuteInternal` save/restore of `TxnReadTS` and `SnapshotInfoschema` with
    `next_select_rows=[[1]]`. Stored as
    `target.source.binding-history-executeinternal-txreadts.v1`, state `validated`.
  - generated state-ingress negatives -> retired before execution. DDL foreign-key matched
    `ExecOptionUseCurSession` in source, but target-analysis proved the SQL runs in a DDL worker /
    internal session rather than the user's session. User-management matched `ExecuteInternal`, but
    target-analysis proved it uses sys sessions. These are stored negative screens, not GREEN cells.
  - RECOMMEND INDEX RUN after tx_read_ts -> RED/GREEN validated target. The executor passes the
    current session into index advisor; `indexadvisor.exec` calls `ExecuteInternal` and drains the
    result set on that session. Current RED recorded
    `before=467570885856329728 after=0 next_select_rows=[[1] [2]]`; local ingress-isolation GREEN
    recorded `before=467570913639661568 after=467570913639661568 next_select_rows=[[1]]`. Stored as
    `target.source.planner-index-advisor-executeinternal-state-ingress.v1`, state `validated`.
status:     validated selector, product-contract pending. This is distinct from S21 savepoint stacks: the state object is not an ordered
            marker stack, but a stale-read ingress channel consumed by an internal SQL execution.
            2026-07-10s refinement: the selector generalizes from NT-DML to
            STATE_INGRESS_INTERNAL_SQL. Do not require the outer statement to be a write; the key
            shape is "user sets one-shot state -> wrapper performs internal SQL on the current
            session -> user's intended statement observes changed state." 2026-07-10u refinement:
            before execution, require session-ownership proof. A source marker is not enough if the
            internal SQL runs on a DDL worker session or a sys session.
oracle gate: require a current-rowset control, an AS OF control proving the stale snapshot excludes
             the missed row, and a sibling ordinary-write/control path proving the stale
             transaction should not be silently consumed by the wrapper. For management/internal
             SQL wrappers, add a contract gate: if the product defines tx_read_ts as consumed by any
             next statement, the cell is LOW_VALUE rather than RED.
stop rule:  do not fuzz BATCH syntax variants or every `ExecuteInternal` caller. Reopen only for
            another one-shot session state, a wrapper that runs internal SQL inside the same user
            operation, a stronger DELETE/INSERT-SELECT consequence, or an upstream-quality
            contract/fix validation. Next automation step is a source-target generator for
            `ExecuteInternal` / `ExecRestrictedSQL UseCurSession` crossed with one-shot state
            fields plus the session-ownership gate, not more manual variants of binding history or
            index advisor.
```

## New from id1290001 — S24: transient fault retry classifier
```text
selector:   a transient control-plane fault crosses an error-domain/classification boundary before
            the DDL retry gate, so a foreign retryable error is treated as fatal and bypasses the
            recovery path
born from:  id1290001 (fast-reorg ADD INDEX rolls back on PD TSO retry timeout after one hit)
predictions:
  - active-window fast-reorg DDL x PD/TiKV transient retry-family error -> RED when terminal
    evidence shows `err_count=1` and immediate rollback/failure
  - sibling safe path (txn / non-fast-reorg / preserved retry loop) under the same transient
    schedule -> GREEN
  - ingest-mode DDL x retryable ingest/TiKV family at the DDL classifier bridge -> RED when
    lower-bridge ingest recovery is GREEN but bridge-proximal injection flips the same family to
    immediate rollback
status:     active — 3/3 hits.
            New rule: distinguish "retry budget exhausted" from "never admitted to retry" with
            `err_count=1` plus a terminal rollback. The most useful boundary is not more chaos;
            it is whether the same transient fault family stays GREEN on a sibling path that
            avoids the suspect classifier/recovery bridge.
            New from `ddl-ingest-retryable-kv-family-misclassified-fatal`: also separate
            **fault family** from **fault altitude**. The same leader-change family can stay
            GREEN below the bridge (`ingestctrl` retry/rescan) yet turn RED at the bridge
            (`runIngestReorgJob` -> `isRetryableJobError`). For S24, the fastest live proof is a
            small altitude matrix, not a broader chaos schedule.
            New from `modify-column-reorg-transient-unknown-fatal`: a good S24 hit does not need
            a new fault family if it adds a new sibling owner and preserves a same-altitude GREEN
            control. `MODIFY COLUMN` versus `ADD INDEX` under one-shot
            `driver_bad_conn/net_conn_reset/grpc unavailable` is exactly that shape.
oracle gate: require trigger proof that the fault landed in the relevant active window, a sibling
             green control proving the operation is otherwise valid/recoverable, and terminal
             evidence (`err_count`, job state, user error family). A source-only local classifier
             probe can nominate the selector, but a confirmed hit needs live terminal evidence.
negative calibration:
  - 2026-07-11: synthetic `KV/Ingest` leader-change shapes injected at the common txn backfill
    worker made current-master `ADD INDEX` and `MODIFY COLUMN` both go `rollback done`. Do not
    count this as a new S24 hit: at that altitude the family had no sibling GREEN control and no
    product-feasible reachability proof. This is domain-mismatch calibration, not selector lift.
stop rule:  do not enumerate PD error strings or bounce counts. Reopen only for another
            cross-domain retry-classification boundary, a different external transient fault
            family, a stronger silent consequence, or fix validation.
```

## New from id1350002 — S25: DXF runtime fundamental retry loop
```text
selector:   a distributed reorg executor sees a source-native import/setup error, assumes the
            subtask is retry-safe, and leaves the job on an idempotent rerun loop instead of
            failing or cleanly rolling back
born from:  id1350002 (distributed ADD INDEX hangs on persistent SetTSBeforeImportEngine
            engine-not-found)
predictions:
  - distributed ADD INDEX / DXF import path x source-native runtime fundamental error at the
    subtask boundary -> RED when the job stays `running` in the same SchemaState and the same
    subtask reruns
  - same point, same owner, one-shot injection -> GREEN if the rerun path is genuinely a
    transient-recovery path
  - clearing the held fault after the RED interval -> GREEN terminal completion, proving the user
    symptom is a retry wedge rather than unrelated environment breakage
status:     active — 1/1 hits.
            The high-signal move is not broad chaos. It is a same-altitude matrix:
            baseline GREEN, one-shot GREEN, persistent RED, then fault-removal GREEN. That shape
            isolates retry-loop bugs from plain failure bugs and upgrades liveness evidence from
            "stuck once" to "the system keeps choosing the wrong recovery path."
            New rule: when the runtime error is source-native, prefer the real owner's own error
            strings over generic synthetic wrappers; this gave a much stronger proof than the
            earlier generic write/scan error shapes.
oracle gate: require a same-altitude GREEN control, repeated same-step rerun evidence
             (`mysql.tidb_global_task` or owner logs), and post-removal recovery. A plain
             "running for a while" observation is not enough.
negative calibration:
  - do not collapse this into S24. S24 is "retryable transient fault misclassified as fatal";
    S25 is the mirror image: "runtime fundamental error misclassified as retryable and wedged."
stop rule:  do not enumerate more string variants from the same owner just because they are plain
            errors. Reopen only for another distributed reorg owner, another source-native
            fundamental at the same retry bridge, a stronger user-visible consequence, or fix
            validation.
```

## New from id1410001 — S26: DXF retryable runtime no-budget loop
```text
selector:   a distributed reorg executor sees a source-native runtime error that is plausibly
            retryable, but the outer DXF task layer has no terminal retry budget or escalation
            path, so the subtask stays `running` and reruns forever
born from:  id1410001 (distributed ADD INDEX hangs on persistent SetTSBeforeImportEngine
            context-deadline-exceeded)
predictions:
  - same owner, same point, one-shot retryable timeout -> GREEN if the retry path is valid
  - same point, persistent held retryable timeout -> RED when the same subtask keeps rerunning,
    the job stays `running`, and the client DDL never returns
  - clearing the held fault after the RED interval -> GREEN terminal completion, proving the
    symptom is an unbounded retry wedge rather than unrelated environment breakage
status:     active — 1/1 hits.
            This is not the same claim as S25. S25 is "the error never should have been admitted
            into retry." S26 is subtler: "even a retryable error still needs a budget and an
            escalation rule." The high-signal move is to contrast one-shot GREEN with persistent
            RED at the same point, then add a lower-layer bounded-retry contrast if source shows
            one exists.
oracle gate: require a same-altitude GREEN control, repeated same-step rerun evidence with a real
             count/timestamp window, and post-removal recovery. Strong bonus evidence is a nearby
             sibling/lower layer that already has an explicit retry cap.
negative calibration:
  - do not collapse this into S25. S25 is a wrong retryability admission of a runtime
    fundamental; S26 is a missing retry budget for a runtime timeout that can legitimately begin
    life in the retry bucket.
stop rule:  do not widen this into "all retryable errors hang". Reopen only for another
            source-native retryable error at the same distributed bridge, another module that
            lacks a retry budget, a stronger user-visible consequence, or fix validation.
```

## New from id1440001 — S27: schema-change safe-window gap
```text
selector:   source/runtime contract says a DDL path leaves a safe old-schema commit window for
            async commit / 1PC, but a natural same-start transaction still fails with
            `ErrInfoSchemaChanged` unless a sibling protection path is enabled
born from:  id1440001 (MDL-off ADD INDEX lets concurrent async-commit txn fail despite
            delayForAsyncCommit safe-window protection)
predictions:
  - same DDL, same txn shape, same natural schedule, MDL/safe sibling off -> RED with
    `Information schema is changed`
  - same shape with the sibling protection path on (here: metadata lock) -> GREEN with exact
    final rowset and index/table consistency
  - more complex sibling DDLs may widen the natural red band, but plain `ADD INDEX` is already a
    sufficient proof surface
status:     active — 1/1 hits.
            This is not a retry-classifier story. The high-signal move is to treat source comments
            and skipped expected-success tests as executable proof obligations, then compress the
            natural red into an OFF/ON sibling matrix instead of adding new injected failures.
oracle gate: require a natural red cell, a sibling protection-path green control, and a strong
             post-success oracle (for example exact rowset + `ADMIN CHECK TABLE`) so the green arm
             proves real semantic success rather than mere absence of the error.
negative calibration:
  - do not collapse this into "all concurrent DDL and transactions conflict". Autocommit shells,
    single-op shapes, and some siblings are green; the selector is specifically the path that
    claims old-schema commit safety under MDL-off coordination.
stop rule:  do not enumerate every DDL kind or every DML mix once the contract-level red/green
            split is established. Reopen only for another DDL that relies on the same safe-window
            contract, a stronger consequence such as silent mis-amend, or fix validation.
```

## New from id1470001 — S28: control-plane worker removal must preserve terminal results
```text
selector:   a runtime control-plane action removes or cancels an in-flight worker, but the
            removed worker still owns a terminal result/error that must be accepted by the parent
            before the job can publish success
born from:  id1470001 (common-reorg ADD INDEX downscale drops a canceled tail worker's post-batch
            error and publishes an incomplete index)
predictions:
  - active common-reorg ADD INDEX x busy tail worker x downscale to a smaller prefix worker set
    -> RED if the removed worker's terminal error/result can be dropped and the DDL still reaches
    `synced/public`
  - same injected worker error without downscale -> GREEN control if the job rolls back or retries
    after collecting the error
  - sibling DDLs that share the executor but have different result-acceptance/publish behavior may
    stay GREEN; this is a boundary, not a reason to overgeneralize the root
status:     active — 1/1 severe hit plus multiple useful negative boundaries.
            The high-signal move is to treat runtime control-plane actions (`THREAD` downscale,
            pause/resume, owner handoff, cancel) as first-class matrix dimensions. For this
            selector, evidence must prove three things at once: the worker was actually removed,
            its terminal result/error was actually produced, and the parent still published a
            success state without accepting it.
oracle gate: require a publish-end-state oracle, not just an injected error. The strong oracle is
             `ADMIN CHECK TABLE` plus table-scan/index-scan differential and an exact witness row.
             Owner logs should show the downscale, the injected tail-worker error, and the absence
             of normal parent-side worker-failed handling.
negative calibration:
  - MODIFY COLUMN row-rewrite and merge-temp-index siblings can remain safe even with tail-worker
    post-batch error plus downscale; record these as boundaries instead of weakening the original
    hit.
  - A green run is invalid unless it proves the exact target race landed: workerCnt must be >1,
    downscale must really adjust worker count, and the tail-worker error must be injected.
stop rule:  do not enumerate all DDLs that share `txnBackfillExecutor`. Reopen only for another
            owner where a removed worker's terminal result can be dropped, a stronger publish-time
            consequence, or fix validation proving result draining/acceptance closes the root.
```

## Retired Candidate: Optimistic Retry Omits Read-Only Session-State Assignment
candidate: BEGIN; SELECT @v := source_value; UPDATE target SET value=@v; COMMIT
           with one optimistic commit retry and a source change before retry
source:   pkg/session/tidb.go finishStmt plus pkg/session/session.go retry/StmtHistory
local_red: retry can commit the old @v value after the source changed from 10 to 20
contract:  documented behavior: automatic optimistic retry replays only write
           statements and warns that query-derived writes can violate Repeatable Read
status:    retired / known-documented-semantic-boundary
bug_count: excluded
asset:     docs/method-cases/ai-native-txn-retry-user-variable-known-boundary.md
stop rule: do not enumerate user variables, GET_LOCK, LAST_INSERT_ID, or other
           session side effects under this retry mode. Reopen only if a supported
           default mode promises full replay, or a different retry owner loses a
           state dimension outside the documented limitation.

## S40: REPLAY_COMPENSATION_CLOSURE
```text
selector:   a rollback/cancel/undo path restores materialized state but does not truncate a retry
            or resume log; before treating that as missing state, enumerate whether the log also
            retains the compensating control event.
born from:  current-source savepoint screen on TiDB 13282a8bd06b, with PR/review/issue/history
            excluded from candidate generation.
prediction: RED only when retry really occurs and the replay omits, reorders, or changes the owner
            context of ROLLBACK TO; final committed state must then differ from no-retry control.
observed:   GREEN. Failpoint-enabled retry replayed BEGIN, SAVEPOINT, INSERT(1), ROLLBACK TO,
            INSERT(2), COMMIT in order. ExecRetryCount=1 and final rowset was only (2,20), equal to
            the no-retry control.
asset:      selector.replay-compensation-closure.v1;
            oracle.retry-final-rowset-with-history-trace.v1;
            schedule.txn-savepoint-retry-compensation.v1.
refinement: snapshot-field inventory is necessary but insufficient for event-sourced owners.
            Compute checkpoint + forward events + compensation events before admission.
stop rule:  do not reopen savepoint-history snapshot variants unless the exact replay trace proves
            a compensation edge is absent or interpreted under a different semantic owner.
```

## Promoted from id30001 — S29: semantic proof gate with normalization asymmetry
```text
selector:   a planner fast-path checker claims that an access path is safe by proving a semantic
            implication, but metadata predicates enter through a different normalization path
            than query predicates
born from:  id30001 (partial-index implication check keeps pi for a>=0 although pi contains a<3)
predictions:
  - partial predicate and query predicate overlap without implication -> RED under USE/FORCE
    versus IGNORE/table-scan rowset differential
  - fast path naturally satisfies ORDER BY/LIMIT under pseudo or incomplete stats -> RED without
    a hint; changing statistics may alter plan selection but cannot make an unsafe path correct
  - exact implication, point predicates, and lower-bound controls -> GREEN
  - nullable, OR, excluded-point, cast, and collation variants are separate semantic families,
    not automatic new roots
status:     active — 1/1 high-impact hit, issue filed, with negative boundaries recorded.
            The important refinement is that normalization of the proof input is itself part of
            Q_claim. Textually equal predicates can have different range semantics if metadata
            and query expressions took different preparation paths.
oracle gate: require a stable reference rowset, forced or natural fast-path evidence, and a
             blocked-path differential. `ADMIN CHECK TABLE` is only a storage-consistency control;
             it cannot prove planner applicability.
negative calibration:
  - do not count USE/FORCE, default no-hint, or different predicate shapes as separate bugs when
    the same proof checker and fix locus explain them
  - do not treat ANALYZE changing the plan as a fix; it is a selection-state control
  - do not promote a source-level range anomaly without a user-visible rowset mismatch
stop rule:  do not enumerate all partial-index syntax. Reopen only for proof-input normalization
            fix validation, another proof owner, or a stronger consequence.
```

### S23 live lift: index-advisor state ingress

2026-07-12:
  - Testbed `8220955`, no failpoints, explicit endpoint `127.0.0.1:14000`.
  - Direct AS OF control returned `[1,10]`; `RECOMMEND INDEX RUN` followed by the next
    user SELECT returned `[1,10],[2,20]`; no-pending control returned `[1,10],[2,20]`.
  - This upgrades the index-advisor sibling from local RED/GREEN method evidence to a
    current-master user-visible RED. It does not close the product-contract gate.
  - Asset: `assets/store/txn-index-advisor-txreadts-testbed-results.jsonl`.

## S30: restore special-object runtime-state rebuild

```text
selector:   a restore/recover path republishes a generic TableInfo but the object kind has
            runtime state stored outside the generic table/AutoIDGroup keys
born from:  id1500003 (FLASHBACK DATABASE restores sequence TableInfo but not sequence value)
prediction:
  - object-specific create/drop uses an extra meta key or scheduler/cache row;
  - generic recover path enumerates TableInfo and calls a table helper;
  - the first post-recover object action consumes the missing state and shows rollback,
    duplicate value, stale scheduler behavior, or wrong rowset.
oracle gate:
  - require a direct post-recover behavior oracle, not only SHOW CREATE or system-table diff;
  - include a no-recovery control for the same object behavior;
  - include a generic-object control when possible, to isolate special-object state from broad
    recover-schema failure.
negative calibration:
  - reject lazy-name-resolution cases where ordinary CREATE is already permissive;
  - reject recover paths that explicitly disable/strip the field by design;
  - do not enumerate options within the same object kind before the root is fixed.
status:     validated/terminal — 1/1 current-master C3 hit; remote id1500003 confirmed.
```

## S31: parameter key dominates stateful grouping

```text
selector:   a stateful operator is moved below a parameterized executor, while admission checks
            only that the parameter key occurs syntactically inside the state/group expression
P/Q/F:      key a occurs in GROUP BY a+b -> guard assumes per-a execution is safe -> different a
            values can share one a+b group and different lookup tasks publish partial groups
oracle:     prove forced parameterized/global-reference plans; compare one-task GREEN against
            cross-task RED and require runtime task-count evidence
validated:  PR #66217 review P1 held-out: IndexJoin task:33 returned sums 10/20 while HashJoin
            returned global sum 30
status:     selector validated; held-out target retired as known review finding, not a new bug
stop rule:  do not enumerate expressions or batch sizes; reopen for a different stateful operator
            or a distinct missing functional-dominance proof
```

## S32: scan/delete context stability

```text
selector:   a scan phase materializes candidate identities under predicate R(x, token, C0), while
            a later irreversible phase rebuilds the safety recheck from the same token but reloads
            semantic context as C1
born from:  id1620002 (DATETIME TTL carries expire epoch E, but scan/delete independently reset to
            global time_zone before evaluating FROM_UNIXTIME(E))
prediction:
  - mutate only the context between phases and move current state into the C0-safe/C1-action window;
  - the irreversible phase acts even though the state is safe under the selecting phase's meaning;
  - keeping context stable is GREEN under the same pause/update schedule.
oracle gate:
  - prove scan handoff happened;
  - prove the current state is safe under C0 before release;
  - observe real irreversible state after phase B completes;
  - include a no-context-drift control.
negative calibration:
  - token equality is not semantic equality when time zone, locale, collation, SQL mode, schema, or
    policy participates in decoding;
  - generated SQL alone is insufficient; lift to the real worker/action owner;
  - history may be used only after RED to distinguish old context-initialization bugs.
status:     validated/terminal - actual TTL worker RED plus no-drift GREEN; remote id1620002 high.
stop rule:  do not enumerate offsets, batch sizes, or DATE variants. Reopen only for a different
            context owner or fix validation.
```

## S33: observation lock suppresses liveness signal

```text
selector:   an observer retains resource R while waiting for another owner's heartbeat/progress,
            but the signal writer also needs R
born from:  id1650002 (BR abort locks a live restore registry row, suppresses its heartbeat UPDATE,
            declares it stale, and deletes the row)
prediction:
  - signal advances before the observer acquires R;
  - the same writer conflicts or blocks after R is acquired;
  - unchanged signal authorizes an irreversible transition.
oracle gate:
  - name the signal producer and its lock/write set;
  - prove pre-lock progress and post-lock interference;
  - observe the terminal state, not only the conflict;
  - include a genuinely stale control.
status:     validated/terminal - local real-TiKV live-owner RED 3/3 and stale GREEN 3/3;
            remote id1650002 high.
stop rule:  do not enumerate heartbeat intervals, statuses, or restore filters. Reopen only for fix
            validation or a different observer/signal resource cycle.
```

## S34: checked error must dominate terminal result

```text
selector:   a branch checks fresh failure E, but returns/acks/commits a stale sibling value S
born from:  id1680003 (BR checks scheduler-removal error e but returns earlier nil err)
prediction:
  - the failure branch is taken;
  - the required action/artifact is skipped;
  - the public terminal owner still reports success.
oracle gate:
  - trace exact error identity from producer to public terminal result;
  - name the skipped irreversible action and success artifact;
  - jointly observe terminal status and action/artifact;
  - include no-fault and one-variable counterfactual controls.
status:     validated/terminal - local real-TiKV command RED, action GREEN, identity-counterfactual
            GREEN; remote id1680003 high.
stop rule:  one representative surface per root. Keep syntactic siblings as blast radius rather
            than counting or executing each one.
```

## S35: external effect precommit rollback coherence

```text
selector:   local transactional state remains abortable after an external owner commits a mutation
born from:  id1710003 (cancelled ALTER RESOURCE GROUP leaves the new definition active in PD)
reused by:  id1800003 (cancelled ALTER TABLE PLACEMENT leaves an uncommitted replica rule in PD)
            id1830003 (cancelled TiFlash replica removal deletes the active PD rule)
prediction:
  - external state changes before local durable publication;
  - a supported cancel/conflict/owner-loss aborts local state;
  - rollback has no compensation or reconciliation edge;
  - metadata and runtime owners disagree after terminal failure.
oracle gate:
  - name both durable boundaries and every post-call abort edge;
  - pause only after external success;
  - use a supported local abort;
  - compare terminal result, history, metadata view, and runtime view;
  - include normal publication and counterfactual controls.
status:     validated/terminal - three independent DDL handlers produced local and real-owner RED;
            remote id1710003, id1800003, and id1830003 high.
severity calibration:
  - TRUNCATE affinity produced the same owner split locally, but the highest consumer is an
    experimental Region-colocation optimization. Missing the group silently disables the declared
    latency optimization; it does not weaken replica safety or corrupt data. Keep it as a moderate
    RED and retire it from the severe queue.
  - Before injection, trace each external owner to the official user promise. Selector reuse proves
    where to look; it does not inherit the consequence class of earlier hits.
stop rule:  reuse the selector across durable owners, but require a new handler-specific obligation
            and owner-coherence oracle. Values and policy options are blast radius.
```

## S36: GC protection acknowledgement dominates historical read

```text
selector:   a successful protection RPC must prove the effective boundary covers the requested read
born from:  BR GC-protection source screen and real-TiKV negative matrix
prediction:
  - a lease/barrier/safepoint API returns success without acquiring the requested boundary;
  - the caller proceeds as if historical state were protected;
  - no downstream owner rejects the stale read;
  - a terminal artifact can be published from unavailable history.
oracle gate:
  - physically advance the destructive boundary;
  - force only the primary guard to pass;
  - enumerate every downstream independent owner;
  - observe process status, required artifact, and restored state.
status:     execution-screened GREEN for BR: TiKV snapshot rejected with 9006 and no backupmeta.
stop rule:  do not enumerate primary error codes while the same downstream owner remains dominant.
```

## S37: failed publication retains retry ownership

```text
selector:   a fallible publisher reports error, then resets/acks/closes the only retry payload
born from:  id1740003 (runaway watch batch is discarded after one SQL flush error)
prediction:
  - producer-local state makes the operation appear effective;
  - durable/shared publication is absent;
  - a fresh consumer observes stale or missing policy/state;
  - recovery cannot retry because no owner retains the exact payload.
oracle gate:
  - force one failure followed by a healthy publication window;
  - prove the second attempt receives the original payload;
  - observe a fresh consumer rather than only producer-local state;
  - include no-fault and retain-on-error counterfactual controls.
status:     validated/terminal - local RED/counterfactual GREEN and real-PD/TiKV two-frontend RED;
            remote id1740003 high.
stop rule:  one root per destroyed retry owner. Payload types and policy actions are blast radius.
```

## S38: deferred terminal error dominates success

```text
selector:   a deferred Close/Flush/Commit/Finalize owns the last transfer from private state to a
            durable owner, but its error is logged or metered after the public result was chosen
born from:  id1770003 (IMPORT INTO discards per-chunk writer Close errors, reports success, and
            publishes rows without secondary-index entries)
prediction:
  - normal work returns nil before deferred finalization runs;
  - finalization fails before transferring all private state;
  - cleanup destroys or detaches the failed private state;
  - the caller treats the unchanged nil result as permission to publish or acknowledge success.
oracle gate:
  - prove the deferred action is a durability boundary, not best-effort cleanup;
  - inject after normal work and before the terminal transfer;
  - jointly observe public status, durable artifact, and semantic consistency;
  - change only error ownership and repeat the same fault.
status:     validated/terminal - local exact-error RED/GREEN and real-PD/TiKV success+3/0+ADMIN
            8223 RED; named-return counterfactual error+0/0+ADMIN green; remote id1770003 high.
stop rule:  one root per discarded terminal error owner. Data/index writer and error-type variants
            are blast radius; reopen only for a different public owner or fix validation.
```

## S39: persisted state must bind lineage

```text
selector:   a persisted token authorizes skipping scan/replay, but stores progress without the
            identity or generation of the producer and object lineage that made it true
born from:  id1860003 (CRR accepts a fixed-path resume file from another replication lineage)
prediction:
  - keep weak keys such as task name and object path constant;
  - replace producer cluster/task generation/source lineage;
  - stale progress takes a fast path and reaches a current consumer without revalidation.
oracle gate:
  - compare persisted and current lineage evidence;
  - count whether skipped objects were revalidated;
  - observe the highest consumer of the token;
  - include same-lineage and no-state controls.
status:     validated/terminal - service returns 100 over current upstream 10 with zero object checks,
            and PITR max-recoverable consumer also returns 100; remote id1860003 high.
stop rule:  do not enumerate task names or storage schemes. Reopen for another state owner only when
            the missing lineage dimensions or highest consumer are distinct.
```

## S40: pushdown equivalence dominates recheck elision

```text
selector:   successful serialization/pushability disables a local semantic checker
born from:  current-source partial-index fast-reorg screen
prediction:
  - the remote engine and local checker disagree on an admitted type boundary;
  - one-index fast reorg skips the local checker;
  - the disagreement becomes a missing or extra durable index key.
oracle gate:
  - name both semantic owners;
  - use an equivalent reference that cannot be partially pushed;
  - verify owner altitude in both plans;
  - lift only a semantic RED to FORCE/IGNORE and physical-index consistency oracles.
negative calibration:
  - CONNECTION_ID() folded to a constant and made the first reference INVALID;
  - LAST_INSERT_ID(id) kept the corrected reference in root;
  - 15/15 current partial-index grammar boundary cells were GREEN.
status:     selector execution-validated; target source-retired before ADD INDEX.
stop rule:  do not enumerate more literals. Reopen only for a new admitted expression class or an
            independently evidenced remote/local semantic owner split.
```

## S41: restore domain covers runtime dependencies

```text
selector:   restore publishes historical primary metadata while excluding a side owner required by
            the restored capability's current mandatory consumer
born from:  id1980003 (FLASHBACK CLUSTER restores CACHED ON but excludes table_cache_meta)
prediction:
  - restore code has an explicit include/exclude range or object boundary;
  - a restored metadata bit selects a runtime protocol whose state lives outside that boundary;
  - a supported post-target mutation removes or replaces the side owner;
  - restore reports success without rebuilding or reconciling the dependency.
oracle gate:
  - jointly observe restore terminal state, primary metadata, and side owner;
  - follow the state bit to its highest mandatory consumer, not only SHOW output;
  - distinguish safe fallback consumers from terminal consumers;
  - restore only the missing owner and repeat the same operation.
negative calibration:
  - FLASHBACK split retry was not admitted: the mock removed client-go backoff, and retry-until-
    success is the documented product contract.
status:     validated/terminal - local cached-table consumer RED, actual SQL-only FLASHBACK RED on
            testbed 8220955, and one-row compensation GREEN; remote id1980003 high.
stop rule:  one root for this restore boundary and mandatory owner. Do not enumerate DML verbs,
            lease values, or CACHE syntax variants.
```

## S42: derived execution context must be keyed or rebuilt

```text
selector:   a cache key proves raw payload identity, but the hit path copies derived semantic state
            whose session/config/time/identity producers are absent from the key
born from:  id2010003 (COM_STMT_PREPARE dedup copies an old stale-read evaluator)
prediction:
  - the cached object contains an evaluator, folded value, semantic flag, or owner token;
  - its producer reads context not represented by the key;
  - a hit either skips the owning build stage or overwrites fresh analysis with the cached field;
  - identical payload under changed context reaches a different semantic consumer.
oracle gate:
  - keep payload identity fixed and change exactly one context owner;
  - compare fast-path hit against same-payload bypass;
  - observe a semantic result, not only a cache-hit marker;
  - replace only the derived owner for counterfactual GREEN.
status:     validated/terminal - local wrong-result RED, same-SQL dedup-off GREEN, fresh-evaluator
            GREEN, and real COM_STMT_PREPARE RED on testbed 8220955; remote id2010003 high.
stop rule:  one root per cache layer and derived owner. SQL forms, staleness durations, and client
            libraries are blast radius; audit sibling copied fields without counting them anew.
```

## S43: retry-attempt derived payload atomicity

```text
selector:   a retry closure mutates publishable state captured outside the closure, then can fail
            after a nonempty prefix and before the payload is complete
born from:  id2040003 (distributed ADD INDEX ReadIndex planning keeps subTaskMetas across attempts)
prediction:
  - swallowing the suffix error publishes a partial payload as success;
  - propagating the error alone retries onto failed-attempt residue and publishes duplicates;
  - an attempt-local or reset payload produces exact source coverage.
oracle gate:
  - force at least two source batches and fail only after the first append;
  - measure source-domain coverage, not only error identity or nonempty output;
  - run the 2->1 current, 2->3 error-only, and 2->2 attempt-local matrix;
  - lift to the highest consumer before severity admission.
status:     validated/terminal - local planner matrix and real DXF testbed RED; ALTER ADD INDEX
            finished synced while FORCE INDEX missed a committed row and ADMIN CHECK returned 8223;
            remote id2040003 high.
stop rule:  one root per retry payload owner. Batch counts, TSO error strings, and region layouts are
            blast radius; reopen only for a distinct published payload or highest consumer.
```

## S44: cloned canonical/active view identity

```text
selector:   a clone/copy routine independently duplicates multiple slices, maps, indexes, or
            filtered views that originally reference the same mutable objects
born from:  id2070003 (CorrelateSolver clones canonical and active AccessPath views separately)
prediction:
  - one cloned view is populated, normalized, or refreshed in place;
  - another cloned view is consumed by a shortcut such as empty, complete, or cached;
  - value equality at clone time hides that later mutations no longer propagate;
  - a conditional rebuild path makes one sibling GREEN and masks the broken alias graph.
oracle gate:
  - draw the pre-clone and post-clone alias graphs, not only field inventories;
  - compare the shortcut with a feature/bypass reference on the same data;
  - vary whether the downstream rebuild/repair owner reaches the consumed object;
  - change only the alias mapping and require the same selected strategy to become GREEN.
status:     validated/terminal - local nine-cell matrix, exact identity-preserving GREEN, and
            SQL-only real-TiKV RED on testbed 8220955; remote id2070003 high, upstream #69790.
stop rule:  one root per clone owner and alias graph. Aggregate functions, SQL forms, indexes, and
            cost values are blast radius; reopen only for a different producer/consumer view pair.
```

## S45: attempt-scoped side-effect rollback closure

```text
selector:   an automatic retry can occur after the attempt mutates state outside the primary
            transactional rollback owner, and the rebuilt operation can consume that survivor
born from:  id2100003 (pessimistic DML retries after SETVAR but does not restore UserVars)
prediction:
  - an error before the side effect is GREEN while the same error after it is RED;
  - non-idempotent side effects change a later key, predicate, row image, action, or terminal error;
  - rolling back only KV/statement state is insufficient even when the executor is rebuilt;
  - restoring the missing state dimension, or declining retry, preserves the original semantics.
oracle gate:
  - draw mutation, rollback, and retry-consumer edges around the retry point;
  - compare no retry, pre-mutation error, post-mutation error, and idempotent mutation;
  - lift internal state drift to terminal error plus durable state whenever possible;
  - require a boundary-landed observer before accepting timing-based controls.
status:     validated/terminal - local injected and natural unistore REDs, exact restore GREEN,
            SQL-only real-TiKV RED on testbed 8220955; remote id2100003 high, upstream #69791.
stop rule:  one root per retry owner and missing state owner. DML forms, variable types, expressions,
            conflict keys, and retry counts are blast radius.
```

### S45 calibration: typed receiver effects and edge witnesses

`id2160003` extends S45 from direct closure assignments to state changes hidden behind receiver
methods. `CleanupIndexExec.cleanTableIndex` looked like three ordinary method calls until a typed
one-level effect summary expanded them into `lastIdxKey`, `scanRowCnt`, `batchKeys`, `idxValues`, and
`removeCnt` mutations. A retryable Commit error rolls back index deletes but not those fields: 3
dangling entries report 9, while 20001 entries panic at the fixed 20000-entry buffer boundary.

Two precision gates are now mandatory:

1. **post-mutation edge reachability:** prove a retryable call or Commit can fail after mutation;
2. **edge witness:** test output must prove the injected retry edge actually ran.

The first gate retired `Deleter.gatherKeysToDelete`, whose only error is before buffer mutation. The
second rejected an initially passing oracle where the runtime failpoint was configured but TiDB's
source conversion had not been enabled. `id2160003` remains moderate/C2 because no wrong durable
index state was proved; it calibrates the generator but does not satisfy the severe-bug gate.

### S45 extension: terminal publication after zero-work re-entry

`id2190003` adds a consumer that the original S45 wording underweighted. A failed attempt executes
`LAST_INSERT_ID(99)` before a natural unique-key conflict. The rebuilt pessimistic RC executor sees
a newly committed gate and matches zero rows, so it never reads or overwrites the survivor. Statement
completion nevertheless publishes `StmtCtx.LastInsertID=99`, and the next INSERT persists it.

S45 source generation and ranking now have two consumer classes:

1. **re-entry consumer:** a survivor changes the rebuilt attempt's key, predicate, row image, action,
   or terminal error;
2. **terminal publication consumer:** the rebuilt attempt omits the operation, then completion
   publishes a failed-attempt value/validity pair.

Mandatory matrix addition: force the successful attempt to perform zero work, and compare it with a
run that starts from the same final database state. For id2190003, the retry arm returned zero rows
but published/persisted 99; the direct zero-match control published/persisted 7. Clearing only
`LastInsertID` and `LastInsertIDSet` in `ResetForRetry` made the exact natural-conflict arm GREEN.
Status: issue-filed high, remote id2190003, upstream #69796. This is distinct from id2100003 by
missing state owner and terminal consumer; SQL forms, sleep durations, IDs, and gate shapes are
blast radius.

## S46: deferred terminal error return-slot ownership

```text
selector:   a deferred Commit/Close/Flush/publish action writes an error variable that is not the
            function's actual returned error slot
born from:  id2130003 (IMPORT conflict-deletion Commit error is assigned after return nil fixes an
            unnamed result)
prediction:
  - the terminal action is reached and its error is assigned, so a shallow review looks correct;
  - an unnamed result or shadowed variable prevents that assignment from changing the public error;
  - a retry/task owner consumes nil and publishes success without durable completion;
  - naming or explicitly owning the result exposes the same fault to the existing recovery path.
oracle gate:
  - resolve language-level return slots and shadowing, not only variable names;
  - inject one transient fault before durable publication and prove the action did not commit;
  - run a same-process no-fault control after the one-shot fault is consumed;
  - lift to terminal status plus exact durable semantic consistency.
status:     validated/terminal - local rollback/error RED, real-TiKV finished plus PRIMARY/unique/
            secondary 2/1/2 and ADMIN 8223, no-fault 1/1/1 GREEN, named-return one-retry 1/1/1
            GREEN; remote id2130003 high, upstream #69792.
stop rule:  one root per terminal owner and return-slot mechanism. Input conflicts, index shapes,
            batch sizes, and retryable error strings are blast radius.
```
