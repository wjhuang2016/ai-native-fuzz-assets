INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(3240003,
 'Indexed virtual generated columns can drift across session contexts and corrupt secondary indexes',
 'high',
 'data-corruption',
 'CREATE TABLE/INSERT/DELETE',
 'indexed virtual generated column',
 'A context-sensitive virtual generated value is persisted as an index key under one writer session context and recomputed under later read or mutation contexts. The index can return predicate-false rows, violate logical uniqueness, and a successful DELETE can leave a real row without its index plus a stale index entry for a deleted row.',
 'Strongest witness: create g INT AS (WEEK(d)) VIRTUAL with UNIQUE(g). Under default_week_format=0 insert id 1/date 2021-01-01; under mode 3 insert id 2/same date. Both source rows now project g=53, while the index contains 0->id1 and 53->id2. DELETE WHERE g=0 succeeds and affects 1; the source-owned twin affects 0. After success, the record store contains only id2/g53 while the covering index contains only stale id1/g0. ADMIN CHECK reports 8223. Explicit WEEK(d,3) rejects the second insert with 1062. A second witness uses DATE(TIMESTAMP) across time zones and deletes a predicate-false row.',
 'An indexed virtual generated column must map one base row to one stable key for every supported INSERT, UPDATE, DELETE, uniqueness-check, and read context. Successful DML must preserve a bijection between live records and index entries.',
 'Before DELETE, source rows are id1:g53,id2:g53 while index entries are id1:g0,id2:g53; the g=0 index hit projects g=53 and predicate false. DELETE affects 1 versus source-owned 0. After success, source is id2:g53 and index is stale id1:g0. ADMIN CHECK reports 8223 before and after. The explicit-mode control stays consistent.',
 'TiDB rejects direct context-sensitive expression indexes as unsafe under default config, but checkIllegalFn4Generated enforces the non-GA gate only for genType=typeIndex. A virtual generated column is admitted as typeColumn and an ordinary index does not revalidate its expression. Every row mutation reevaluates the virtual expression with the current session EvalContext, so a later DELETE can compute a different key from the one originally persisted.',
 'Apply expression-index safety admission to every indexed generated-column composition. Reject implicit-context indexed expressions, canonicalize and persist their semantic context, or retain the original physical key needed by later mutations. Recheck base-row generated predicates before irreversible DML and make consistency checks compare both physical directions.',
 'generated-index-bidirectional-physical-parity-after-delete',
 'composable-safety-gate-closure',
 'indexed-virtual-generated-session-context-safety-gate-bypass',
 'TiDB nightly ed2376acc6; current TiDB master 05b396fb66; TiKV nightly 730be34f95',
 1,
 'confirmed',
 NULL,
 'One TiDB, one PD, one real TiKV; default strict sql_mode; MDL ON; allow-expression-index remains disabled by default; no concurrency, retry, failpoint, source patch, process pause, or infrastructure fault. The WEEK witness reproduced 3/3. default_week_format values 0 and 3 are ordinary MySQL session settings. Direct WEEK(d) expression-index syntax returns ERROR 8200, proving the same safety gate. Relevant source files are unchanged between runtime and current master. This generalizes the original DATE(TIMESTAMP)/time_zone witness and upgrades id3240003 instead of adding a new root because both share the admission owner and generic fix. It remains distinct from id3180003 TiKV pushdown omission and id30034 plan-cache folding. Critical persistent physical-corruption consequence; stored as high by catalog convention.');
