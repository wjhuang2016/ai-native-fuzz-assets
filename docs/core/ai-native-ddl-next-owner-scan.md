# DDL Next Owner Scan After id30008

Date: 2026-07-02

## Purpose

This scan continues the DDL-only proof obligation:

```text
after DDL changes or removes an object,
every object reference owned by another feature must rewrite, cleanup, or block.
```

The goal is not to add broad tests. The goal is to decide where AI should spend the next bug-finding step, using source structure and oracle quality before running SQL.

## Selector

Score a candidate owner before building a matrix:

1. object-identity binding is proven, not only name-like text;
2. metadata is stored outside the main table definition or has a separate cache/display layer;
3. sibling DDL paths already contain explicit rewrite, cleanup, or block logic;
4. a broader path might bypass that logic, such as `DROP DATABASE`, multi-table rename, partition reorg, truncate with new IDs, recover, flashback, or batch GC;
5. the oracle can verify both sides: old reference removed and new reference protected;
6. existing tests do not already cover the interesting container/state paths.

## Next Target After id30006

Do not expand more hypo-index variants before owner feedback or fix-semantics discussion. The next search should use id30006 as a selector proof:

```text
DDL-created auxiliary metadata
+ session/cache/side-table storage
+ name or object-ID keying
+ public SHOW / information_schema / DDL-like output
+ no obvious DDL cleanup/rekey helper on column/table/container paths
= high-value invalidation matrix target
```

Prioritize owners with a cheap immediate oracle. Session-local and cache-local metadata should come before async workers or historical job records, because the latter can be noisy by design.

Success for the next step is not "more cases". It is either:

- one new DDL-only stale-reference hit predicted by the selector; or
- one strong negative sample where source inspection finds a cleanup/rekey helper and the matrix confirms it.

## Candidate Scan

| Candidate owner | Selector signal | Current decision |
|---|---|---|
| table attributes / PD label rules | Strong object owner: label rule IDs encode schema/table/partition names, rules carry table/partition key ranges, and DDL has explicit rewrite/cleanup helpers. | Downweight as a next bug target. It is a good green-control owner, but existing coverage already exercises the likely red paths. |
| TTL job status/task metadata | ID-keyed side tables: `mysql.tidb_ttl_table_status`, `mysql.tidb_ttl_task`, `mysql.tidb_ttl_job_history`. | Downweight for now. Cleanup is owned by the TTL worker and background GC, so immediate post-DDL rows are a noisy oracle unless we first find a low-cost deterministic trigger. |
| index usage metadata | Looks ID-keyed, but current public surface is in-memory `information_schema.tidb_index_usage`; old `mysql.schema_index_usage` is dropped by upgrade. | Downweight. This is mostly query/runtime usage tracking, not a DDL-owned reference matrix. |
| stats lock / analyze options / column usage | High signal: ID-keyed stats side metadata with public `SHOW` surfaces. | Keep paused. id30003 already hit this owner family; do not mine stats variants before owner feedback or fix validation. |
| region split policy | SQL-visible table/index split hints, with DDL action `ActionAlterTableSetRegionSplitPolicy`. | Downweight after negative screen. The policy is nested in `TableInfo`/`IndexInfo`, so rename/drop naturally moves or deletes it; no independent side-owner was found. |
| masking policy side metadata | Basic rename/column/drop/truncate matrix was green because owner-specific helpers exist, but `EXCHANGE PARTITION` swaps table and partition IDs without an equivalent remap helper. | Positive hit. id630014 leaves an old policy row on the partition ID and makes it unreachable by logical-table management DDL. Pause this family until fix direction or a genuinely new owner-changing DDL entrypoint appears. |
| sequence default reference | Column default stores `nextval(db.seq)` as executable schema expression. The referenced sequence is a separate DDL object. | Positive hit. `DROP SEQUENCE`, `RENAME TABLE seq TO seq2`, and cross-DB `DROP DATABASE` leave a live table default pointing at a missing sequence. Pause and do not expand variants. |
| affinity | Creates PD affinity groups with table/partition ID-based group IDs, while `SHOW AFFINITY` is enumerated from live InfoSchema. | Downweight after negative screen. The SQL-visible owner follows current `TableInfo`; partition drop/remove partitioning are blocked; cleanup paths already cover drop/truncate/drop database. |
| functional index hidden column | Expression index references user columns through hidden generated columns. | Downweight after negative screen. Base column DDL blocks with the correct `3837` owner error; dropping the index releases the dependency; multi-schema drop-index+alter-column blocks consistently against the original dependency graph. |
| database placement default | `DBInfo.PlacementPolicyRef` is a real policy ref, and new tables inherit it into `TableInfo`. | Downweight after negative screen. `DROP PLACEMENT POLICY` scans DB/table/partition refs; DB rewrite/release/drop-database and inheritance boundary all passed a 6-cell live probe. |
| view SELECT text | `ViewInfo.SelectStmt` stores create-time validated SQL text that can name base tables/columns. | Downweight after negative screen. Base table/column rename and drop can make the view invalid, but the stored text is name-bound rather than a maintained object-identity dependency. |
| resource group `SWITCH_GROUP` | `ResourceGroupRunawaySettings.SwitchGroupName` names another resource group in `QUERY_LIMIT`. | Downweight after negative screen. Current create/alter validation only checks non-empty name, not target existence, so drop-time non-blocking is not a maintained-reference violation. |
| hypo index session metadata | `SessionVars.HypoIndexes` stores DDL-created hypothetical indexes by schema/table/index name and `SHOW CREATE TABLE` merges them into table DDL. | Positive hit. Column rename/change/drop and table/database drop/recreate can leave stale or resurrected hypo indexes. Pause this family until fix semantics are agreed. |
| hypo TiFlash replica session metadata | `SessionVars.HypoTiFlashReplicas` is also session-local and table-name keyed. | Downweight as a sibling negative. It is only consulted in `EXPLAIN`/planner paths and is not merged into `SHOW CREATE TABLE` or a DDL-like public table definition. |
| SQL binding session/global metadata | Session binding cache and `mysql.bind_info` store `BindSQL` that can contain table/index hints, and `SHOW BINDINGS` exposes it. | Downweight as a policy-text negative. `CREATE BINDING` validates via internal `EXPLAIN`, but existing tests intentionally keep a binding after `DROP INDEX`; this is not a maintained object-identity edge unless product semantics change to auto-disable/rewrite bindings. |
| local temporary table session metadata | Local temporary tables live in `SessionVars.LocalTemporaryTables` and `SessionExtendedInfoSchema`. | Downweight as an explicit design boundary. Source comments state local temp tables have a loose database relationship and can survive `DROP DATABASE`; many ALTER paths are already blocked for local temp tables. |
| reorganize partition + replacement global index | Same global-index owner was green on `DROP/TRUNCATE PARTITION` and `REMOVE PARTITIONING`, but `REORGANIZE PARTITION` uses a separate multi-stage iterator to rebuild indexes for adding and non-touched partitions. | Positive hit. A 2-row matrix found `REORGANIZE PARTITION p1` can miss rows from later non-touched partition `pmax` in the replacement global index. Pause this family until fix direction is agreed. |
| table lock session/runtime metadata | `LOCK TABLES` stores `SchemaID+TableID` in session state and `TableInfo.Lock`; `ALTER TABLE READ ONLY` writes runtime lock state into `TableInfo`; `CREATE TABLE LIKE` shallow-copies `TableInfo`. | Positive hits. Cross-schema locked rename leaves a stale lock after `UNLOCK TABLES` (id30008); `CREATE TABLE LIKE` from a READ ONLY source copies the lock to the new target (id1200001). Pause this family until fix direction is agreed. |
| import jobs / index advisor / workload values | Store table IDs or object-like names. | Downweight. These are historical job records, recommendations, or learning artifacts; not clearly DDL-owned references that must rewrite. |

## Follow-up Screens After id30006

id30006 suggested a tempting broad rule:

```text
session/cache side metadata + public display = likely DDL invalidation bug
```

The follow-up scan made that sharper:

```text
session/cache/side-table metadata
+ create path validates the referenced object
+ public surface presents the metadata as current schema or DDL output
+ no existing product/test semantics say it is historical or user policy text
= high-value target
```

Three candidates were screened out:

- **Hypo TiFlash replica**: `pkg/ddl/executor.go:3769-3804` stores `HypoTiFlashReplicas[schema][table]`, but `pkg/planner/core/operator/logicalop/logical_datasource.go:865-876` only uses it when `StmtCtx.InExplainStmt`. There is no `SHOW CREATE TABLE` merge like hypo index, so it is not a DDL-metadata oracle.
- **SQL binding**: `pkg/bindinfo/binding.go:566-589` validates binding SQL through internal `EXPLAIN FORMAT='hint'`, and `pkg/executor/show.go:333-384` exposes the stored `BindSQL`. A live check confirmed `ALTER TABLE ... DROP/RENAME INDEX` can leave `SHOW SESSION BINDINGS` with `Status=enabled` and old `USE INDEX`. However, `pkg/bindinfo/binding_operator_test.go:1182-1184` already expects `SHOW GLOBAL BINDINGS` to keep one row after `DROP INDEX`, so this owner is treated as saved policy SQL text, not a DDL-maintained object reference.
- **Local temporary table**: `pkg/infoschema/infoschema.go:1232-1235` explicitly says local temporary tables have a loose relationship with database and still exist after database drop. That is product semantics, not a cleanup miss.

Selector update:

```text
Reject public surfaces that are intentionally historical/user-policy text.
Only build the DDL invalidation matrix when the surface claims current schema state.
```

## Follow-up Screen After id30007

id30007 adds a second useful selector that is not about side metadata:

```text
same reference owner is green on common DDL paths
+ a sibling DDL path uses a different multi-stage iterator
+ source/comment says every remaining object must be visited
+ a low-noise rowset or ADMIN CHECK oracle exists
= high-value small matrix target
```

The concrete hit:

- **REORGANIZE PARTITION + global index**: `/Users/bba/pc/ai_native_ddl_reorg_global_index_reference_probe.py` found that `ALTER TABLE ... REORGANIZE PARTITION p1` can succeed while the replacement global index misses rows from later non-touched partition `pmax`. `USE INDEX(idx_b)` returns `12:120`, `IGNORE INDEX(idx_b)` returns `12:120,30:300`, and `ADMIN CHECK TABLE` reports `8223`.
- Green control in the same probe: partition placement refs are rewritten correctly by `REORGANIZE PARTITION`, so this is not a generic reorg-partition problem.
- 2026-07-03 follow-up green: `REMOVE PARTITIONING` followed by `PARTITION BY KEY ... UPDATE INDEXES (idx_a GLOBAL)` kept `USE INDEX(idx_a)` and table-scan rowsets equal, and `ADMIN CHECK TABLE` passed. Source inspection matches the result: these paths are full migration/rebuild paths and do not have the id30007-style non-touched phase.

Selector update:

```text
Do not stop at "owner family is green".
Before leaving an owner, check whether a sibling DDL path has a distinct iterator/finalizer.
The best cells put rows or refs before and after the changed range, not only inside it.
```

Boundary update:

```text
Do not widen this into all partition/global-index DDL.
Prioritize only sibling paths whose source has:
  AddingDefinitions and DroppingDefinitions,
  then a separate "non-touched partitions" iterator/finalizer.
Full migration paths such as REMOVE PARTITIONING are lower density.
```

## Follow-up Screen After id30008

id30008 adds a third selector focused on owner-key rewrite:

```text
DDL-created side state stores object ID plus owner/container key
+ DDL move/rekey path preserves object ID but changes owner/container key
+ cleanup path later trusts the old owner/container key
+ cleanup has a low-noise behavior oracle
= high-value stale-cleanup target
```

The concrete hit:

- **Table lock + cross-schema RENAME TABLE**: local DDL harness and testbed `8192975` both reproduced that `LOCK TABLES ai_lock_src.t WRITE; RENAME TABLE ai_lock_src.t TO ai_lock_dst.t; UNLOCK TABLES` can leave `ai_lock_dst.t` locked. Another session's `INSERT INTO ai_lock_dst.t VALUES (1)` fails with `[schema:8020] Table 't' was locked in WRITE by ... session: 1`.
- Green control: existing same-schema `TestRenameTableWithLocked` passes, so the red dimension is cross-schema owner-key rewrite, not table-lock rename support in general.

Selector update:

```text
Do not reject all session-local metadata.
If DDL syntax creates it and cleanup affects other sessions' behavior,
the owner/container key must be checked across move/rekey DDL.
```

Stop rule:

```text
Do not expand table-lock variants now.
First settle the repair contract:
  cross-schema rename rewrites session lock SchemaID
  OR unlock resolves current schema by stable TableID.
```

## Follow-up Screen After id1200001

id1200001 adds a sibling S13 refinement that is not an owner-key rewrite:

```text
source object -> target object reconstruction
+ shallow/top-level copy
+ selective reset list proves some fields are target-only
+ an unreset field is runtime state, not schema definition
+ target-only behavior cleanup oracle exists
= high-value target-state clone candidate
```

The concrete hit:

- **CREATE TABLE LIKE + READ ONLY source**: on testbed `8192975`,
  `ALTER TABLE src READ ONLY; CREATE TABLE dst LIKE src; INSERT INTO dst VALUES (2)` returned
  `ERROR 8020 Table 'dst' was locked in READ ONLY ...`, even though the user never locked `dst`.
- Green/isolation control: `ALTER TABLE dst READ WRITE` made `dst` writable, while `src` remained
  read-only. Cleaning only `dst` showed the same isolation.
- Source reason: `BuildTableInfoWithLike` starts with `tblInfo := *referTblInfo` and resets several
  target-only fields, but not `TableInfo.Lock`.

Selector update:

```text
When a clone/reconstruction path uses a shallow copy, inspect the reset list as a proof.
Fields explicitly reset tell us the code knows copied state can be target-invalid.
For every remaining field, ask: schema definition or runtime/management state?
Only run the matrix when there is a target behavior oracle, not merely a display difference.
```

Stop rule:

```text
Do not enumerate CREATE TABLE LIKE options or every TableInfo field.
Reopen only for another unreset runtime/non-schema field with a behavior oracle,
or for fix validation of CHECK/source-mutation and READ ONLY/target-clone cases.
```

## Follow-up Screen After id30017

id30017 keeps the S4 shape, but with an ID-swap DDL instead of a cross-schema move:

```text
side state stores physical object IDs
+ DDL swaps/rekeys those IDs
+ public SHOW surface resolves IDs through current InfoSchema
+ cleanup command later resolves the current object IDs
= high-value stale-owner target
```

The concrete hit:

- **Stats lock + EXCHANGE PARTITION**: `/Users/bba/pc/ai_native_ddl_stats_lock_exchange_partition_probe.py` reproduces that `LOCK STATS t` creates visible locks for `t/global,t/p0,t/p1`; after `ALTER TABLE t EXCHANGE PARTITION p0 WITH TABLE t1`, `SHOW STATS_LOCKED` reports `t/global,t1,t/p1`; after `UNLOCK STATS t`, `t1` remains locked.
- Existing coverage was close but weak: `TestExchangePartitionShouldChangeNothing` checks only the row count in `mysql.stats_table_locked`, so it cannot distinguish "the row survived" from "the lock still has the owner the user can clean up".

Selector update:

```text
Never treat side-state row count as an ownership proof.
For ID-swap DDL, require:
  1. pre-DDL SHOW/current-object mapping,
  2. post-DDL SHOW/current-object mapping,
  3. a cleanup or command-roundtrip oracle.
```

Stop rule:

```text
Do not expand stats-lock variants now.
First settle the repair contract:
  logical table/partition locks are rewritten across EXCHANGE PARTITION,
  OR physical-data-following semantics are documented and lock/unlock behavior is explicitly tested.
```

2026-07-03 follow-up: **persisted analyze options + EXCHANGE PARTITION** reached the same
`exchange-idswap-orphan` root as blast-radius, not a new root. The useful method refinement is that
the O21 round trip can be a future behavior consumer, not only a cleanup command: after `pt.p0`
saved `ANALYZE` options for column `a`, `EXCHANGE PARTITION p0 WITH nt` made the old `p0` physical
ID become `nt`; `ANALYZE TABLE nt WITH 2 TOPN,2 BUCKETS` then analyzed only `a/PRIMARY`, while a
no-exchange standalone control analyzed `a/b/c/PRIMARY`.

Updated stop rule:

```text
Do not mine more stats/analyze_options variants from this owner family.
Record id30039 as S4 blast-radius reach and reopen only for:
  a different owner with a behavior round trip,
  a consequence escalation,
  or fix validation.
```

## Follow-up Screen After id30038

id30038 adds an S1 refinement that is adjacent to, but distinct from, rollback-owner-bit loss:

```text
state-transforming backfill path
+ generated per-owner artifacts are flattened
+ later consumer reconstructs owner/type from ordinal
+ at least one owner can emit multiple artifacts
+ a concurrent-DML window can pre-create the artifact
= high-value online-DDL target
```

The concrete hit:

- **ADD UNIQUE MVI + sibling multi-column UNIQUE**:
  `/Users/bba/pc/ai-native-add-index-mvi-owner-mismatch-draft.md` reproduces that
  `ALTER TABLE t ADD UNIQUE INDEX u_mvi((CAST(j AS SIGNED ARRAY))), ADD UNIQUE INDEX u_ab(a,b)`
  can return a false `ERROR 1062 Duplicate entry '90000' for key 't.u_mvi'` when concurrent DML
  writes the new index entries before backfill reaches the row.
- Green controls: add only `u_mvi` succeeds; add `u_mvi` plus one-column `u_b(b)` succeeds; table
  consistency after rollback is clean.
- Source inspection matches the result: `batchCheckUniqueKey` records only `recordIdx` while
  flattening MVI-generated keys, then uses `flattenedOrdinal % len(indexes)` to recover index
  metadata for found-key duplicate classification.

Selector update:

```text
When a DDL path flattens generated artifacts, do not assume row-major ordinal still identifies
the owner. Carry the owner/type bit explicitly, and search for the smallest shape where one owner
emits N artifacts while a sibling owner has a different decode/type shape.
```

Stop rule:

```text
Do not enumerate MVI cast types, array element counts, or sibling index spellings.
Reopen only for another flattened-artifact owner/type bit loss, a silent corruption outcome, or
fix validation.
```

## Follow-up Screen After id630025

id630025 adds a validation-builder selector inside the high-risk DDL lane:

```text
state-transforming DDL safe path builds internal SQL
+ that SQL is meant to prove partition/object membership
+ source TODO or shape omits a semantic dimension
+ direct target-state oracle proves membership independently
= validation wrong-error or possible wrong-acceptance
```

The concrete hit:

- **EXCHANGE PARTITION WITH VALIDATION + LIST DEFAULT**:
  `/Users/bba/pc/ai-native-exchange-partition-default-validation-draft.md` reproduces that
  exchanging a standalone table containing `3` into `PARTITION pdef DEFAULT` fails with
  `ERROR 1064 ... near ") limit 1"`, even though direct `INSERT` routes `3` to `pdef`.
- Green controls: ordinary no-DEFAULT LIST exchange validation succeeds; the same legal DEFAULT
  row swaps with `WITHOUT VALIDATION`.
- Source inspection matches the result: `buildCheckSQLConditionForListPartition` and
  `buildCheckSQLConditionForListColumnsPartition` iterate the current partition `InValues`; for
  DEFAULT there is no ordinary value list, so the builder emits an empty `not ()` predicate.

Selector update:

```text
When a DDL safe path hand-builds SQL to replace a richer semantic relation, the proof obligation
is not "the SQL parses"; it is "the SQL is equivalent to the relation for every semantic
dimension." DEFAULT partitions are complements, not value lists.
```

Stop rule:

```text
Do not enumerate partition syntax.
Reopen S19 only for:
  another validation SQL builder with a different omitted semantic dimension,
  a wrong-acceptance/data-placement consequence,
  or fix validation for DEFAULT exchange.
```

## Follow-up Screen After id30011 / id30016

id30011 introduced S6:

```text
restore/undelete/import path re-materializes historical metadata
+ sibling object path sanitizes or validates references
= possible dangling reference after restore
```

The first broad S6 screen after returning to DDL-only sharpened it:

| Candidate | Result | Lesson |
|---|---|---|
| TTL recover table/schema | GREEN | Source explicitly disables TTL scheduling on recover, so the restored field is intentionally sanitized. |
| table-cache recover | GREEN/BLOCKED | Cached tables cannot be dropped, so `FLASHBACK TABLE` is not a reachable stale-cache path. |
| TiFlash replica recover | SKIP(environment) | Static code still looks suspicious around available-state preservation, but current testbed has no TiFlash store/PD placement target. |
| FK child-table recover after parent drop | RED id30016 | Normal create has FK parent validation; recover skips it and publishes old `ForeignKeys`, so DML can skip enforcement while parent is absent. |
| sequence default recover after sequence drop | INFO(boundary) | `FLASHBACK TABLE` restores a default that points at a missing sequence, but ordinary `CREATE TABLE ... DEFAULT NEXT VALUE FOR missing_seq` also succeeds and fails only at insert time. This is id30005's lazy-name-resolution family, not a new S6 validator gap. Probe: `/Users/bba/pc/ai_native_ddl_sequence_recover_boundary_probe.py`. |
| masking policy recover after table drop | INFO(boundary) | Drop table deletes `mysql.tidb_masking_policy`, truncate/rename have sync helpers, and recover does not restore rows; however current masking policy is only consumed by DDL validation paths, so there is no strong user-facing behavior oracle. |
| TTL parent create after dangling child FK | GREEN | Even when the child FK was created with `foreign_key_checks=0` before the parent exists, later `CREATE TABLE parent ... TTL` is rejected with `8152`; the TTL validator sees the reverse FK, so this is not an asymmetric gap. |
| ordinary column/object owner matrices | GREEN calibration | Current 28-cell column/reference and 17-cell object/reference matrices produced no new findings beyond known controls; ordinary rewrite/block paths are saturated and should not be widened blindly. |

Selector update:

```text
Do not enumerate every restored field.
Prioritize restore candidates where:
  ordinary create/alter has an explicit validator,
  recover does not appear to call it,
  a sibling entrypoint does not already provide symmetric protection,
  and post-recover behavior can prove enforcement, not just metadata display.
```

Next S6 targets should therefore be BR/IMPORT/recover paths only if they can be paired with a concrete skipped validator and a behavior oracle. Pure display-only metadata, static sys-table asymmetry without behavior oracle, deliberate disable-on-recover fields, environment-only features, symmetric validators, and lazy-name-resolution references whose normal create path is also permissive should be downweighted.

## Attribute Owner Evidence

`ATTRIBUTES` is the closest structural sibling of table-cache:

- `pkg/ddl/table.go:1474` handles `ActionAlterTableAttributes`.
- `pkg/ddl/table.go:1506` handles `ActionAlterTablePartitionAttributes`.
- `pkg/ddl/table.go:1658` reads old table/partition label rules.
- `pkg/ddl/table.go:1672` rewrites label rules after rename/truncate/recover style ID/name changes.
- `pkg/ddl/schema.go:194` removes schema label rules during `DROP DATABASE`.

Existing coverage is unusually strong:

- `pkg/ddl/attributes_sql_test.go:122` covers truncate table with table and partition attributes.
- `pkg/ddl/attributes_sql_test.go:167` covers table rename, including multi-table cross-schema rename.
- `pkg/ddl/attributes_sql_test.go:227` covers recover table.
- `pkg/ddl/attributes_sql_test.go:266` covers flashback after drop and after truncate.
- `pkg/ddl/attributes_sql_test.go:324` covers drop table plus GC label rule cleanup.
- `pkg/ddl/attributes_sql_test.go:377` covers drop/recreate with same name.
- `pkg/ddl/attributes_sql_test.go:441` covers drop partition, truncate partition, and exchange partition.
- `tests/integrationtest/t/ddl/attributes_sql.test:13` covers `DROP DATABASE` cleanup.

This is exactly why it should not be the next live matrix. It satisfies the selector, but the most valuable cells have already been encoded as direct regression coverage.

## TTL Owner Evidence

TTL has real side metadata:

- `pkg/meta/metadef/system_tables_def.go:475` defines `mysql.tidb_ttl_table_status(table_id, parent_table_id, ...)`.
- `pkg/meta/metadef/system_tables_def.go:495` defines `mysql.tidb_ttl_task(job_id, table_id, ...)`.
- `pkg/meta/metadef/system_tables_def.go:513` defines `mysql.tidb_ttl_job_history(table_id, parent_table_id, table_schema, table_name, partition_name, ...)`.
- `pkg/ttl/ttlworker/job_manager.go:60` deletes TTL tasks whose parent status row disappeared.
- `pkg/ttl/ttlworker/job_manager.go:69` deletes idle status rows for table IDs no longer in the current TTL table list.

But this owner fails the low-noise oracle gate for now:

```text
DDL removes table
  -> TTL worker eventually notices and cleans status/task rows
  -> immediate SQL snapshot can look red even when design is async cleanup
```

Do not mark TTL rows after DDL as a bug unless the experiment also controls the TTL cleanup trigger and proves the row is user-visible stale state beyond the designed async window.

## Region-Split Policy Evidence

Region split policy looked tempting because it is DDL-owned and visible in `SHOW CREATE TABLE`:

- `pkg/ddl/table.go:1841` handles `ActionAlterTableSetRegionSplitPolicy`.
- `pkg/executor/show.go:1424` prints table-level split policy.
- `pkg/executor/show.go:1454` prints index-level split policy.
- `pkg/ddl/index.go:3939` renames the existing `IndexInfo` object.
- `pkg/meta/model/index.go:286` stores `RegionSplitPolicy` inside `IndexInfo`.

The negative-screen probe:

```bash
python3 /Users/bba/pc/ai_native_ddl_region_split_policy_probe.py
```

Result:

```text
SUMMARY total=5 findings=0 skipped=0
```

What it proved:

- `RENAME INDEX` moves the printed split policy from old index name to new index name.
- `DROP INDEX` removes the split policy with the index object.
- `DROP INDEX old, ADD INDEX new` does not leak old split policy onto the replacement index.
- `CHANGE COLUMN a aa DATE` keeps the index column rewrite and leaves a replayable split hint.
- cross-schema `RENAME TABLE` moves both table and index split policies with `TableInfo`.

Method conclusion:

```text
SQL-visible metadata
+ stored inside the same object metadata that DDL already moves/drops
= negative selector, not a high-priority side-owner
```

Only reopen this owner if a future scan finds a separate region-split cache, async application record, or invalidation/version surface.

## Affinity Owner Evidence

Affinity looked tempting because it has an external PD state plane:

- `pkg/ddl/affinity.go:33` and `:39` build group IDs from table/partition IDs.
- `pkg/ddl/affinity.go:104` creates PD groups for create/alter/truncate paths.
- `pkg/ddl/affinity.go:135` deletes table/partition PD groups for drop/truncate paths.
- `pkg/ddl/affinity.go:159` batch-deletes groups for `DROP DATABASE`.
- `pkg/ddl/schema.go:214` calls the batch cleanup during `DROP DATABASE`.

But the public SQL surface is primarily live metadata:

- `pkg/executor/show_affinity.go:44` lists current InfoSchema tables with the affinity attribute.
- `pkg/executor/show_affinity.go:73` and `:84` derive group IDs from current table/partition IDs.
- `pkg/executor/show_affinity.go:144` still emits a row from table metadata even if the PD state is missing.

There is also explicit block/cleanup coverage:

- `pkg/ddl/executor.go:2612` blocks `REMOVE PARTITIONING` for affinity tables.
- `pkg/ddl/partition.go:2034` blocks `DROP PARTITION` for affinity tables.
- `pkg/ddl/affinity_test.go:199`, `:212`, `:227`, and `:258` cover drop table, truncate table, truncate partition, and drop database cleanup.

Probe:

```bash
python3 /Users/bba/pc/ai_native_ddl_affinity_reference_probe.py
```

Result:

```text
SUMMARY total=6 findings=0 skipped=0
```

What it proved:

- table affinity is visible in `SHOW CREATE TABLE` and `SHOW AFFINITY`;
- `RENAME TABLE` moves visible table affinity to the new name;
- `TRUNCATE TABLE` preserves one visible table affinity row;
- `DROP TABLE` removes the visible affinity row;
- `TRUNCATE PARTITION` preserves partition affinity rows, while `DROP PARTITION` and `REMOVE PARTITIONING` are blocked;
- `DROP DATABASE` removes visible table and partition affinity rows.

Method conclusion:

```text
external PD state
+ SQL-visible surface comes from live InfoSchema
+ cleanup/block paths already exist for container and partition DDL
= negative selector unless a separate stale public PD-state oracle is found
```

## Sequence Default Reference Evidence

Sequence defaults beat the current filters even though they are not side tables:

- the reference is an executable schema expression inside `TableInfo`;
- the referenced sequence is a separate DDL object;
- `CREATE TABLE` / `ALTER TABLE ... DEFAULT NEXT VALUE FOR seq` validates the sequence at DDL time;
- runtime resolves the sequence by schema/name;
- sequence removal/rename paths do not scan reverse dependencies.

Probe:

```bash
python3 /Users/bba/pc/ai_native_ddl_sequence_default_reference_probe.py
```

Result:

```text
SUMMARY total=5 findings=3 skipped=0
```

The red cells are:

- `DROP SEQUENCE seq` succeeds while `t.a DEFAULT NEXT VALUE FOR seq` exists; later default insert fails with `1146`.
- `RENAME TABLE seq TO seq2` succeeds and does not rewrite `t.a` default; later default insert fails with `1146`.
- `DROP DATABASE seq_db` succeeds even when another database has a table default referencing `seq_db.seq`; later default insert fails with `1146`.

Draft:

```text
/Users/bba/pc/ai-native-sequence-default-reference-draft.md
```

Method case:

```text
/Users/bba/pc/ai-native-id30005-method-case.md
```

Method conclusion:

```text
executable schema expression references separate DDL object
+ create/alter validates target exists
+ remove/rename path lacks reverse dependency scan
= dangling schema expression after DDL
```

Stop here for this owner. The useful next step is fix-semantics discussion, not more sequence fuzzing.

## Functional Index Hidden-Column Evidence

Functional indexes looked worth a small matrix because they combine an index object with a hidden generated column:

- `pkg/ddl/modify_column.go:1415` checks generated expressions during column rename/change/modify.
- `pkg/ddl/column.go:276` blocks drop-column when a generated column depends on the target.
- `pkg/ddl/index.go:2130` removes dependent hidden columns when dropping the index.
- `pkg/ddl/index.go:2202` implements `RemoveDependentHiddenColumns`.

Probe:

```bash
python3 /Users/bba/pc/ai_native_ddl_functional_index_reference_probe.py
```

Result:

```text
SUMMARY total=5 findings=0 skipped=0
```

What it proved:

- functional index expressions are visible through `SHOW CREATE TABLE`;
- referenced-column rename/change/drop and semantic modify block with `3837` and preserve the original schema;
- metadata-only `MODIFY COLUMN` COMMENT/DEFAULT is no longer part of this green boundary; it is id630007;
- sequential `DROP INDEX idx_expr; ALTER TABLE ... RENAME COLUMN ...` succeeds, proving the dependency really is released by the owner;
- one-statement multi-schema `DROP INDEX + RENAME/DROP COLUMN` blocks in both orders with `3837` and preserves the original schema.

Method conclusion:

```text
owner removal and referenced-object change in separate statements can succeed
+ the same pair in one multi-schema statement blocks against original metadata
= not a red cell unless intra-statement dependency elimination is a product contract
```

This is now a boundary rule for future scans: if a candidate only differs because a single multi-schema statement does not use earlier sub-actions to relax later dependency checks, treat it as a green/gray control unless the owner explicitly supports that pattern elsewhere.

## Resource Group SWITCH_GROUP Evidence

Resource-group runaway action looked like a sequence-default sibling:

- `pkg/meta/model/resource_group.go:33` stores the action and `:34` stores `SwitchGroupName`.
- `pkg/meta/model/resource_group.go:127` prints `ACTION=SWITCH_GROUP(name)`.
- `pkg/ddl/resource_group.go:159` has a TODO for checking whether a resource group is in use before drop.

But the create/alter side fails the object-identity gate:

- `pkg/ddl/resourcegroup/group.go:56` only requires a non-empty switch-group name.
- `pkg/ddl/resourcegroup/group.go:59` says the target-existence validation is still a TODO.
- `pkg/ddl/resource_group.go:342` delegates validation to that same conversion path.

Probe:

```bash
python3 /Users/bba/pc/ai_native_ddl_resource_group_reference_probe.py
```

Result:

```text
SUMMARY total=3 findings=0 skipped=0
```

What it proved:

- `CREATE RESOURCE GROUP ... ACTION=SWITCH_GROUP(missing)` succeeds and the missing name is visible in `information_schema.resource_groups`.
- Dropping a real switch target succeeds and leaves the source group showing the old switch-group name.
- `ALTER RESOURCE GROUP ... QUERY_LIMIT=NULL` clears the stored name.

Method conclusion:

```text
field names another DDL object
+ create/alter does not validate that object exists
= name parameter / unimplemented validation, not a maintained DDL reference edge
```

Only reopen this owner if product semantics change to validate `SWITCH_GROUP` targets, or if a runtime-facing stale-state oracle shows a correctness issue independent of reference ownership.

## Hypo Index Session-Metadata Evidence

Hypo index is the positive sibling of the resource-group negative case:

- `pkg/ddl/executor.go:5121` builds the hypo `IndexInfo` only after normal table/column validation.
- `pkg/ddl/executor.go:5043` stores it in `SessionVars.HypoIndexes[schema][table][index]`.
- `pkg/executor/show.go:1207` merges that session-local map into `SHOW CREATE TABLE`.
- `pkg/executor/show.go:1277` prints `/* HYPO INDEX */`.

Probe:

```bash
python3 /Users/bba/pc/ai_native_ddl_hypo_index_reference_probe.py
```

Result:

```text
SUMMARY total=7 findings=6 skipped=0
```

Red cells:

- column rename/change/drop leave `SHOW CREATE TABLE` printing a hypo index on old or dropped column `a`;
- `DROP TABLE` plus same-name recreate attaches the old hypo index to the new table;
- `RENAME TABLE` plus old-name recreate attaches the old hypo index to the new table;
- `DROP DATABASE` plus same-name schema/table recreate attaches the old hypo index to the new table.

Method conclusion:

```text
session-local side metadata
+ DDL creation validates object names
+ public DDL output merges the side metadata
+ later DDL never invalidates or rekeys it
= stale/resurrected reference after DDL
```

Stop here for this owner. The useful next step is fix-semantics discussion, not more hypo-index variants.

## Method Update

id30004 taught a positive rule:

```text
object-identity side metadata
+ sibling DDL block/cleanup
+ broader container bypass
= high-value red cell
```

This scan adds the negative rule:

```text
high structural similarity is not enough.
Before running SQL, check:
  existing container-path coverage
  whether cleanup is intentionally async
  whether the public surface is DDL-owned or historical/runtime-owned
  whether the metadata is independent side state or just nested object-local state
  whether the SQL-visible surface is live InfoSchema plus optional external state
  whether a schema expression references a separate DDL object that can be removed elsewhere
  whether a multi-schema statement is merely validating against original metadata
  whether create/alter validates the named target exists, or only stores a free-form name
  whether session/cache side metadata is merged into public DDL/API output
```

The next live matrix should only be created for an owner that beats the `ATTRIBUTES` coverage baseline, the TTL oracle-noise filter, the affinity "external state but live InfoSchema surface" filter, and the resource-group "unvalidated name parameter" filter. id30006 adds a positive rule for session/cache side metadata that is surfaced as table DDL.

## Next Target Card

```text
Goal:
  Find one new DDL-only owner whose source shape beats the current filters.

Selection bar:
  object-identity side metadata
  + uncovered container/state DDL entrypoint
  + deterministic post-DDL oracle

Do not run:
  attributes happy-path matrix, because source/tests already cover it
  TTL job-state matrix, unless a deterministic cleanup trigger is found
  region-split policy matrix, unless a separate side cache/invalidation owner appears
  affinity matrix, unless a deterministic stale PD-state oracle is found
  functional-index stale-reference base matrix, because rename/change/drop dependency preservation already blocks with 3837 and multi-schema behavior is a boundary control; metadata-only MODIFY is not skipped and is recorded as id630007
  ordinary placement matrix, because DB/table/partition refs are now covered by green probes unless a new state/container bypass is found
  view reference matrix, because view SELECT text is name-bound and invalidation after base-object DDL is outside the maintained-reference contract
  resource-group SWITCH_GROUP matrix, because create/alter currently allows missing switch targets and therefore does not establish object-identity reference semantics
  hypo-index variants, until id30006 fix semantics are agreed
  stats variants, unless owner feedback or fix validation reopens stats
  sequence-default variants, until id30005 fix semantics are agreed
```

If no owner passes this bar, the right next result is a documented negative scan, not a low-signal fuzz expansion.
