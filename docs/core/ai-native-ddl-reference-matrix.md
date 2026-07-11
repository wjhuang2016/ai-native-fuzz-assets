# AI-Native DDL Reference Ownership Matrix
> 2026-07-01. Current scope: DDL-only. Query/executor behavior can be used as an oracle only after a DDL has changed metadata.

## Goal

Use one proof obligation to drive bug discovery:

```text
After a DDL changes an object, every metadata reference to that object must be either rewritten or blocked.
```

This is not "add more tests". The research target is whether AI can raise bug density by extracting reference owners from code, mapping them to DDL paths, and generating only the cells where the rewrite/block contract is fragile.

## Owner Model

There are only three useful buckets:

| Bucket | Correct behavior | Typical source shape | Search priority |
|---|---|---|---|
| Direct column list owner | Rewrite through normal column/index metadata updates | `idx.Columns`, column offset/name metadata | P2 unless a new index kind bypasses the common path |
| Dedicated reference owner | Rewrite by owner-specific updater | `updateFKInfoWhenModifyColumn`, `updateTTLInfoWhenModifyColumn` | P1 controls and multi-schema/reorg variants |
| Expression or side metadata owner | Block all paths unless a rewriter exists | CHECK expr, partial-index predicate, generated expr, partition expr | P0 |

Bug pattern:

```text
owner has no rewriter
+ owner reference is outside normal column/index lists
+ only some ALTER paths call the dependency check
= silent metadata loss, wrong block/error, or inconsistent DDL behavior
```

## Source Anchors

| Owner | Rewrite/block evidence |
|---|---|
| CHECK constraint | `pkg/ddl/executor.go:3477` blocks `RENAME COLUMN`; `pkg/ddl/constraint.go:411` blocks some `DROP COLUMN`; `CHANGE/MODIFY` path lacks an equivalent check before metadata rewrite |
| partial-index predicate | `pkg/ddl/executor.go:7565` checks `idx.AffectColumn`; `pkg/ddl/modify_column.go:1855` covers `CHANGE/MODIFY`; `pkg/ddl/executor.go:3463` `RENAME COLUMN` lacks this check |
| functional-index hidden column | `pkg/ddl/modify_column.go:1415`; `pkg/ddl/column.go:276`; `pkg/ddl/index.go:2130`; expression index is represented as a hidden generated column and should block semantic referenced-column DDL until the index owner is dropped; metadata-only MODIFY is a separate target-acceptance contract covered by id630007 |
| hypo index session metadata | `pkg/ddl/executor.go:5043`; `pkg/ddl/executor.go:5121`; `pkg/executor/show.go:1207`; session-local index metadata is merged into `SHOW CREATE TABLE` |
| generated column | `pkg/ddl/generated_column.go:153`; `pkg/ddl/modify_column.go:1415`; `pkg/ddl/executor.go:3496`; `pkg/ddl/column.go:276` |
| partition expression/columns | `pkg/ddl/executor.go:3303`; `pkg/ddl/modify_column.go:1433`; `pkg/ddl/modify_column.go:1481` |
| FK | `pkg/ddl/modify_column.go:711`; `pkg/ddl/modify_column.go:740`; `pkg/ddl/foreign_key.go:301`; `pkg/ddl/foreign_key.go:503` |
| TTL | `pkg/ddl/modify_column.go:712`; `pkg/ddl/modify_column.go:729`; `pkg/ddl/modify_column.go:1989`; `pkg/ddl/ttl.go:137` |
| ordinary/vector/columnar index columns | `pkg/ddl/modify_column.go:710`; `pkg/ddl/column.go:967`; `pkg/ddl/modify_column.go:1936` |
| global/local index on partitioned tables | `pkg/ddl/partition.go:4684`; `pkg/ddl/delete_range.go` has special global-index cleanup paths; treat as P1 for partition/index DDL, P2 for simple column rename |
| placement policy | `pkg/ddl/schema.go:120`; `pkg/ddl/executor.go:284`; `pkg/ddl/create_table.go:842`; `pkg/ddl/table.go:1544`; `pkg/ddl/table.go:1609`; `pkg/ddl/partition.go:4395`; DB/table/partition object-policy reference, not a column-owner cell |
| stats side metadata | `pkg/executor/show_stats.go:204`; `pkg/executor/show_stats.go:236`; `pkg/statistics/handle/ddl/subscriber.go:114`; `pkg/statistics/handle/storage/save.go:487`; ID-keyed storage with name-exposing `SHOW STATS_*` |
| privilege grants | `pkg/privilege/privileges/cache.go:66`; `pkg/privilege/privileges/cache.go:67`; `pkg/executor/grant.go:572`; `pkg/executor/grant.go:598`; `pkg/meta/metadef/system_tables_def.go:118`; name-keyed policy, rejected as a DDL object-reference owner |
| table-cache side metadata | `pkg/meta/metadef/system_tables_def.go:360`; `pkg/ddl/executor.go:6935`; `pkg/ddl/job_worker.go:431`; `pkg/ddl/executor.go:4310`; ID-keyed side metadata with block/cleanup obligation |
| region split policy | `pkg/ddl/table.go:1841`; `pkg/executor/show.go:1424`; `pkg/executor/show.go:1454`; `pkg/ddl/index.go:3939`; SQL-visible policy nested in `TableInfo`/`IndexInfo`, rejected as a side-metadata owner after negative screening |
| sequence default reference | `pkg/ddl/add_column.go:667`; `pkg/ddl/add_column.go:908`; `pkg/expression/sessionexpr/sessionctx.go:406`; `pkg/ddl/executor.go:4264`; executable schema expression referencing a separate DDL object |
| affinity | `pkg/ddl/affinity.go:33`; `pkg/ddl/affinity.go:39`; `pkg/executor/show_affinity.go:44`; `pkg/ddl/schema.go:214`; SQL-visible surface is live InfoSchema plus PD state, with ID-keyed PD groups and existing cleanup/block coverage |
| view SELECT text | `pkg/executor/ddl.go:328`; `pkg/ddl/create_table.go:1757`; `pkg/meta/model/table.go:780`; create-time validated SQL text, rejected as a DDL object-identity owner after screening |
| resource group `SWITCH_GROUP` | `pkg/ddl/resourcegroup/group.go:56`; `pkg/ddl/resourcegroup/group.go:59`; `pkg/ddl/resource_group.go:159`; `pkg/meta/model/resource_group.go:33`; create/alter validates only non-empty switch group name, not target existence |

## Column ALTER Matrix

Legend: `rewrite` means DDL succeeds and the referenced metadata changes with the object. `block` means DDL must fail before metadata can become stale. `known` means already found by this project and used as harness control.

| Owner | RENAME COLUMN | CHANGE COLUMN old new | MODIFY COLUMN type | DROP COLUMN | Current risk |
|---|---|---|---|---|---|
| CHECK constraint | block | **known: silent loss** | keep or block | block/drop with constraint semantics | P0 control; expand only for blast radius/fix verification |
| partial-index predicate | **known: wrong error family** | block | block | block | P0 control; RENAME path still models the missing-check pattern |
| functional-index expression | block | block | semantic modify blocks; metadata-only modify should target-accept | block | Split result: stale-reference controls are green, metadata-only MODIFY false reject is id630007 |
| generated column dependency | block | block | block | block | P1 green control; include virtual/stored/multi-col variants |
| partition key/expression | block | block | constrained safe modify only | block | P0/P1; good for multi-schema, algorithm, expr vs columns |
| FK child column | rewrite rename | rewrite rename | block incompatible | block | P1; multi-schema and parent/child cross-table rewrite are worth probing |
| FK parent column | rewrite child ref | rewrite child ref | block incompatible | block | P1; cross-table rewrite is the main oracle |
| TTL column | rewrite TTL column | rewrite TTL column | block non-time | block | P1; reorg vs no-reorg paths both need coverage |
| ordinary index column | rewrite | rewrite or rebuild | type guard/rebuild | drop covered index if legal | P2; mainly a control for normal metadata path |
| vector/columnar index column | rewrite if name-only; block data rewrite | rewrite or block | block data rewrite | block/drop per index rules | P2/P1 if new index kind changes storage path |
| global/local index data column | rewrite | rewrite/rebuild | guard/rebuild | drop index if legal | P1 with partition reorg/drop; P2 for simple rename |
| placement policy | not a column owner | not a column owner | not a column owner | not a column owner | Separate table/partition object-reference matrix |

## First Probe Set

`/Users/bba/pc/ai_native_ddl_reference_matrix_probe.py` runs a small DDL-only matrix:

1. Known controls:
   - CHECK + `CHANGE COLUMN` should not silently drop enforcement.
   - partial-index predicate + `RENAME COLUMN` should not return a misleading unknown-column error.
2. Block controls:
   - generated column dependency under rename/change/drop/modify.
   - partition expression and `RANGE COLUMNS` under rename/change/drop/multi-schema.
3. Rewrite controls:
   - TTL rename/change preserves `SHOW CREATE TABLE` TTL column.
   - FK child/parent rename preserves `SHOW CREATE TABLE` and still enforces.
   - ordinary index column rename rewrites `SHOW CREATE TABLE`.

If this small set finds a new unexpected cell, stop and run the pause gate before expanding.

## 2026-07-01 Smoke Result

Command:

```bash
python3 /Users/bba/pc/ai_native_ddl_reference_matrix_probe.py
```

Result on the `fp-tidb` TiKV testbed:

```text
SUMMARY total=28 findings=0 known_controls=3 skipped=0
```

Known controls reproduced:

| Control | Result |
|---|---|
| CHECK + `CHANGE COLUMN b bb INT` | known bug: CHECK enforcement is silently lost |
| CHECK + `CHANGE COLUMN b bb INT, ADD COLUMN c INT` | known bug: multi-schema path also silently loses CHECK |
| partial-index predicate + `RENAME COLUMN a TO aa` | known bug: blocked with misleading `ERROR 1054 Unknown column 'a' in 'expression'` |

Green controls that raise confidence in the owner model:

| Owner/path | Observed behavior |
|---|---|
| CHECK + same-name `MODIFY COLUMN` | DDL succeeded and CHECK stayed enforced |
| partial-index predicate + multi-schema `CHANGE COLUMN` | blocked with `8272` partial-index dependency |
| generated column dependency | rename/change/drop/modify all blocked with generated-column error family |
| partition expression/columns | rename/change/drop/multi-schema/hash all blocked with `3855` partition dependency |
| TTL | rename/change/multi-schema change rewrote `TTL=` column; drop and non-time modify blocked |
| FK child and parent | rename/change rewrote metadata and enforcement remained; drop/modify incompatible blocked with FK error family |
| ordinary/global index column | rename rewrote `SHOW CREATE TABLE` index columns |

Method update:

- The model predicted the known red cells and did not over-predict new failures in the first 28 high-value cells.
- Error-family checking matters. A first version of the FK parent drop case was blocked by "only column" before reaching the FK owner; the probe was changed to require owner-specific error text for block cases.
- The next search should not expand uniformly. Downweight generated/partition/FK/TTL simple rename/change cells; move to object-reference and partition-index interactions where owner state is not just a column name.

## Functional Index Hidden-Column Boundary

Functional indexes are expression owners implemented through hidden generated columns. This makes them a useful bridge between the generated-column green controls and index-object cleanup paths.

Source anchors:

| Path | Contract |
|---|---|
| `pkg/ddl/modify_column.go:1415` | column rename/change/modify checks generated expressions; hidden generated columns return `ErrDependentByFunctionalIndex` |
| `pkg/ddl/column.go:276` | drop-column path blocks when a hidden generated column depends on the target column |
| `pkg/ddl/index.go:2130` | `DROP INDEX` removes dependent hidden columns through `RemoveDependentHiddenColumns` |
| `pkg/ddl/index.go:2202` | hidden columns used by the dropped index are moved to the end and removed |
| `pkg/ddl/multi_schema_change.go:241` | multi-schema add-index tracks hidden-column dependencies as relative columns |

Command:

```bash
python3 /Users/bba/pc/ai_native_ddl_functional_index_reference_probe.py
```

Result:

```text
SUMMARY total=5 findings=0 skipped=0
```

Observed cells:

| Cell | Observed behavior |
|---|---|
| functional index visible control | `SHOW CREATE TABLE` exposes `KEY idx_expr ((a + 1))` |
| referenced column rename/change/drop and semantic modify | block with `3837` expression-index dependency and preserve schema |
| metadata-only `MODIFY COLUMN` COMMENT/DEFAULT | **red id630007**: direct target schema is accepted, but ALTER over existing expression index is rejected |
| `DROP INDEX idx_expr` then `RENAME COLUMN a TO aa` | sequential DDL succeeds; expression dependency is removed |
| multi-schema `DROP INDEX idx_expr, RENAME COLUMN a TO aa` and reverse order | both block with `3837` and preserve original schema |
| multi-schema `DROP INDEX idx_expr, DROP COLUMN a` and reverse order | both block with `3837` and preserve original schema |

Method update:

- The stale-reference slice is a green boundary, not a bug. Sequential DDL can remove the owner and then alter the referenced column, but a single multi-schema statement validates against the original dependency graph.
- Do not mark "sequential succeeds, multi-schema blocks" as red unless the product explicitly promises intra-statement dependency elimination.
- The useful oracle for stale-reference cells is error-family plus schema preservation: `3837` from the functional-index owner and unchanged `SHOW CREATE TABLE`.
- The useful oracle for id630007 is target-state acceptance: direct expression-index target schemas with COMMENT/DEFAULT succeed and pass `ADMIN CHECK TABLE`, so existing-table metadata-only MODIFY should not be rejected by a drop/rename dependency error.

## Hypo Index Session-Metadata Result

Hypo indexes are created through DDL syntax but stored in session-local metadata:

| Path | Contract |
|---|---|
| `pkg/ddl/executor.go:5043` | `addHypoIndexIntoCtx` stores the index in `SessionVars.HypoIndexes[schema][table][index]` |
| `pkg/ddl/executor.go:5121` | `USING HYPO` builds an `IndexInfo` after normal index validation, then stores it in the session map |
| `pkg/executor/show.go:1207` | `SHOW CREATE TABLE` merges session-local hypo indexes by current schema/table name |
| `pkg/executor/show.go:1277` | `SHOW CREATE TABLE` prints `/* HYPO INDEX */` |
| `pkg/ddl/executor.go:5480` / `:5498` | dropping a hypo index still requires the current real schema/table to resolve |

Command:

```bash
python3 /Users/bba/pc/ai_native_ddl_hypo_index_reference_probe.py
```

Result:

```text
SUMMARY total=7 findings=6 skipped=0
```

Observed cells:

| Cell | Observed behavior |
|---|---|
| live hypo control | `SHOW CREATE TABLE` exposes `KEY idx_a (a) /* HYPO INDEX */` while column `a` exists |
| `RENAME COLUMN a TO aa` | DDL succeeds; `SHOW CREATE TABLE` still prints the hypo index on old column `a` |
| `CHANGE COLUMN a aa INT` | DDL succeeds; `SHOW CREATE TABLE` still prints old column `a` |
| `DROP COLUMN a` | DDL succeeds; `SHOW CREATE TABLE` prints a key on a dropped column |
| `DROP TABLE t; CREATE TABLE t(...)` | old session-local hypo index attaches to the new table |
| `RENAME TABLE t TO t2; CREATE TABLE t(...)` | `t2` has no hypo index, but recreated old name `t` gets the old index |
| `DROP DATABASE db; CREATE DATABASE db; CREATE TABLE t(...)` | old hypo index attaches to the recreated schema/table |

Method update:

- Hypo index is a positive side-metadata owner, unlike resource-group `SWITCH_GROUP`: create validates the column, and the public DDL surface later becomes stale.
- The low-noise oracle is `SHOW CREATE TABLE` validity. After column rename, replaying the emitted key definition fails with `1072 column does not exist`.
- Stop expanding hypo-index variants for now. The red cells share one root: `SessionVars.HypoIndexes` is not invalidated or rekeyed by column/table/database DDL.

## Reorganize Partition + Global Index Result

`REORGANIZE PARTITION` is a sibling partition DDL path that was not covered by the first global-index object-reference matrix. It has a different implementation shape: copy rows from dropping partitions, build indexes on adding partitions, then backfill replacement global indexes from non-touched partitions.

Command:

```bash
python3 /Users/bba/pc/ai_native_ddl_reorg_global_index_reference_probe.py
```

Result:

```text
SUMMARY total=2 findings=1 skipped=0
```

Observed cells:

| Cell | Observed behavior |
|---|---|
| `REORGANIZE PARTITION p1` with `UNIQUE KEY idx_b(b) GLOBAL` | DDL succeeds, but `USE INDEX(idx_b)` misses a row in later non-touched partition `pmax`; `ADMIN CHECK TABLE` reports `8223` missing index entry |
| `REORGANIZE PARTITION p1` with partition placement policies | Green control: old partition policy is released, new partition policies remain protected, and `SHOW CREATE TABLE` shows the rewritten refs |

Minimal red shape:

```sql
CREATE TABLE t(a INT, b INT, UNIQUE KEY idx_b(b) GLOBAL)
PARTITION BY RANGE(a) (
  PARTITION p0 VALUES LESS THAN (10),
  PARTITION p1 VALUES LESS THAN (20),
  PARTITION pmax VALUES LESS THAN (MAXVALUE)
);
INSERT INTO t VALUES (12, 120), (30, 300);
ALTER TABLE t REORGANIZE PARTITION p1 INTO (
  PARTITION p1a VALUES LESS THAN (15),
  PARTITION p1b VALUES LESS THAN (20)
);
```

Oracle:

```text
USE INDEX(idx_b):    12:120
IGNORE INDEX(idx_b): 12:120,30:300
ADMIN CHECK TABLE:   ERROR 8223
```

Source hypothesis:

- `/Users/bba/pc/tidb/pkg/ddl/partition.go:4048` documents that replacement global indexes must be updated from non-touched partitions after adding partitions are indexed.
- `/Users/bba/pc/tidb/pkg/ddl/partition.go:4136` starts the non-touched phase by finding a partition that is neither adding nor dropping.
- `/Users/bba/pc/tidb/pkg/ddl/index.go:3524` routes the next partition through `AddingDefinitions` when possible.
- `/Users/bba/pc/tidb/pkg/ddl/index.go:3621` `findNextNonTouchedPartitionID` skips dropping definitions but not adding definitions.

Method update:

- Do not treat a green owner as exhausted after common paths pass. Look for sibling DDL paths with different prepare/iterate/finalize logic.
- The useful selector is: green common owner + uncommon multi-stage iterator + "all remaining objects" proof obligation + cheap rowset/`ADMIN CHECK` oracle.
- Stop expanding reorg/global-index variants until the fix direction is discussed; the current red cell is already minimized to one root family.

## Object Reference Matrix

The next layer is not "more column ALTER". It is object references where the referenced object is a policy, partition, table ID, or global/local index state.

### Placement Policy References

Source anchors:

| Path | Contract |
|---|---|
| `pkg/ddl/executor.go:628` `AlterTablePlacement` | Normalize policy ref, submit `ActionAlterTablePlacement`, record table and shared policy involvement |
| `pkg/ddl/executor.go:6289` `AlterTablePartitionPlacement` | Normalize policy ref for one partition, submit `ActionAlterTablePartitionPlacement` |
| `pkg/ddl/table.go:1544` `onAlterTablePartitionPlacement` | Rewrite one partition's `PlacementPolicyRef`, update table info, rebuild/clear PD bundle |
| `pkg/ddl/table.go:1609` `onAlterTablePlacement` | Rewrite table `PlacementPolicyRef`, update table info, rebuild/clear PD bundle |
| `pkg/ddl/schema.go:120` `onModifySchemaDefaultPlacement` | Rewrite or clear database-level `PlacementPolicyRef` |
| `pkg/ddl/executor.go:284` create schema | Store database-level placement policy on `DBInfo` |
| `pkg/ddl/create_table.go:842` create table | New tables inherit the database default placement policy when they have no explicit table policy |
| `pkg/ddl/placement_policy.go:355` `checkPlacementPolicyNotInUse` | Drop policy must scan DB, table, partition, and special-range refs before allowing drop |
| `pkg/ddl/placement_policy.go:373` / `:450` | InfoSchema and Meta paths both check database-level refs before table/partition refs |
| `pkg/ddl/placement_policy.go:475` `checkPlacementPolicyNotUsedByTable` | Table and partition refs are both blockers for `DROP PLACEMENT POLICY` |
| `pkg/ddl/tests/partition/placement_test.go:49` matrix | Existing unit matrix says table placement is preserved through `PARTITION BY`, partition placement follows new partition definitions, and table-placement + remove/partition multi-schema is blocked |

High-value cells:

| Owner/path | Expected behavior | Oracle |
|---|---|---|
| Drop policy referenced by table | block | error family `8241` / policy in use |
| Drop policy referenced by partition | block | error family `8241` / policy in use |
| Drop policy referenced by database default | block | error family `8241`; `SHOW CREATE DATABASE` still shows the policy |
| Alter database placement `pp1 -> pp2` | rewrite | old policy becomes droppable, new policy remains in-use |
| Alter database placement to `DEFAULT` | release DB ref | policy becomes droppable; `SHOW CREATE DATABASE` has no placement clause |
| Drop database with DB-level placement | release DB ref | policy becomes droppable after schema drop |
| Existing/new tables around DB default rewrite | preserve old table ref; new table inherits new default | old table still protects old policy; DB/new table protect new policy |
| Alter table placement `pp1 -> pp2` | rewrite | old policy becomes droppable, new policy remains in-use |
| Alter partition placement `pp1 -> pp2` | rewrite | old policy becomes droppable, new policy remains in-use |
| Remove partitioning with table+partition policies | release partition refs, preserve table ref | partition policy becomes droppable; table policy remains in-use; no `PARTITION BY` |
| `ALTER TABLE ... PLACEMENT ... REMOVE PARTITIONING` | block | unsupported multi-schema error family `8200` |
| Drop partition with partition policy | release dropped partition ref | dropped partition policy becomes droppable |
| Truncate partition with partition policy | preserve partition ref on new partition ID | partition policy remains in-use; partition is still writable |
| Alter policy used by table | preserve dependent table ref while updating policy settings | `SHOW PLACEMENT` reflects new settings; policy remains in-use by table |
| Alter policy used by partition | preserve dependent partition ref while updating policy settings | `SHOW PLACEMENT` reflects new settings; policy remains in-use by partition |

### Global/Local Index References During Partition DDL

Source anchors:

| Path | Contract |
|---|---|
| `pkg/meta/model/table.go:825` `UpdateIndexInfo` | Stores `UPDATE INDEXES (...)` global/local intent for partition DDL |
| `pkg/ddl/executor.go:4735` `checkCreateGlobalIndex` | Unique indexes not covering all partition columns must be explicit global |
| `pkg/ddl/partition.go:3265` reorg partition index loop | Recreate old/new global indexes and local/global transitions with new index IDs |
| `pkg/ddl/partition.go:3271` `ActionRemovePartitioning` | Force all indexes local when a table becomes non-partitioned |
| `pkg/ddl/partition.go:3283` `ErrGlobalIndexNotExplicitlySet` | Block unsafe local unique index when partition columns are not covered |
| `pkg/ddl/partition.go:3593` old global cleanup | Replaced global indexes are recorded in `OldGlobalIndexes` |
| `pkg/ddl/delete_range.go:333` delete range | Reorg/remove/alter partitioning deletes old partitions plus replaced global indexes |
| `pkg/ddl/executor.go:3080` exchange check | Exchange partition must block source global indexes with `1731` |

High-value cells:

| Owner/path | Expected behavior | Oracle |
|---|---|---|
| `ALTER TABLE ... PARTITION BY` without required `GLOBAL` | block | error family `8264` |
| `ALTER TABLE ... PARTITION BY ... UPDATE INDEXES` | rewrite index global/local state | `SHOW CREATE TABLE`, `ADMIN CHECK TABLE`, index-vs-table rowset |
| `ALTER TABLE ... REMOVE PARTITIONING` | rewrite all global indexes to local and clear partition metadata | no `GLOBAL`, no `PARTITION BY`, `ADMIN CHECK TABLE`, rowset equality |
| `EXCHANGE PARTITION` when source has global index | block | error family `1731` mentioning global index |
| `DROP PARTITION` with global index | cleanup visible index rowset | `ADMIN CHECK TABLE`, global-index rowset equals table rowset |
| `TRUNCATE PARTITION` with global index | cleanup visible index rowset while preserving partition metadata | `ADMIN CHECK TABLE`, global-index rowset equals table rowset |
| `REMOVE PARTITIONING` with placement refs and global index | rewrite both object families | partition policy released, table policy preserved, global marker removed, rowset equality |

Probe entrypoint:

```bash
python3 /Users/bba/pc/ai_native_ddl_object_reference_probe.py
```

Stateful probe entrypoint, requires a failpoint-enabled TiDB status API:

```bash
python3 /Users/bba/pc/ai_native_ddl_stateful_object_probe.py
```

Delete-range metadata probe entrypoint:

```bash
python3 /Users/bba/pc/ai_native_ddl_delete_range_probe.py
```

Placement-bundle failure probe entrypoint:

```bash
python3 /Users/bba/pc/ai_native_ddl_placement_bundle_failure_probe.py
```

DB-level placement reference probe entrypoint:

```bash
python3 /Users/bba/pc/ai_native_ddl_db_placement_reference_probe.py
```

FK table/index object-reference probe entrypoint:

```bash
python3 /Users/bba/pc/ai_native_ddl_fk_object_reference_probe.py
```

Masking-policy side-metadata probe entrypoint:

```bash
python3 /Users/bba/pc/ai_native_ddl_masking_policy_reference_probe.py
```

Stats side-metadata probe entrypoint:

```bash
python3 /Users/bba/pc/ai_native_ddl_stats_reference_probe.py
```

Privilege grant side-metadata screening entrypoint:

```bash
python3 /Users/bba/pc/ai_native_ddl_privilege_reference_probe.py
```

Table-cache side-metadata probe entrypoint:

```bash
python3 /Users/bba/pc/ai_native_ddl_table_cache_reference_probe.py
```

Region-split policy negative-screen entrypoint:

```bash
python3 /Users/bba/pc/ai_native_ddl_region_split_policy_probe.py
```

Sequence-default reference probe entrypoint:

```bash
python3 /Users/bba/pc/ai_native_ddl_sequence_default_reference_probe.py
```

Affinity reference-owner screening entrypoint:

```bash
python3 /Users/bba/pc/ai_native_ddl_affinity_reference_probe.py
```

Functional-index hidden-column reference probe entrypoint:

```bash
python3 /Users/bba/pc/ai_native_ddl_functional_index_reference_probe.py
```

View reference screening entrypoint:

```bash
python3 /Users/bba/pc/ai_native_ddl_view_reference_probe.py
```

## 2026-07-01 Object-Reference Smoke Result

Command:

```bash
python3 /Users/bba/pc/ai_native_ddl_object_reference_probe.py
```

Result on the `5c9198e948` TiKV testbed ordinary path:

```text
SUMMARY total=17 findings=0 skipped=0
```

Green cells:

| Cell | Observed behavior |
|---|---|
| table policy in-use drop | blocked with `8241` |
| partition policy in-use drop | blocked with `8241` |
| table placement rewrite | old policy became droppable; new policy remained in-use |
| partition placement rewrite | old policy became droppable; new policy remained in-use |
| remove partitioning with table+partition policies | partition policy became droppable; table policy remained in-use |
| placement + remove partitioning multi-schema | blocked with `8200` unsupported multi-schema |
| partition by without required global index | blocked with `8264` |
| partition by update indexes | `idx_a` became global, `idx_b` stayed local, rowsets matched |
| remove partitioning with global index | `GLOBAL` marker cleared, `PARTITION BY` cleared, rowsets matched |
| exchange partition with global index | blocked with `1731` and `global index: idx_b` |
| drop partition with global index | `ADMIN CHECK` passed; global-index rowset equaled table rowset |
| drop partition with partition policy | dropped partition policy became droppable |
| truncate partition with partition policy | partition policy remained in-use and p0 stayed writable |
| truncate partition with global index | `ADMIN CHECK` passed; global-index rowset equaled table rowset |
| remove partitioning with placement refs + global index | partition policy released, table policy preserved, global marker removed, rowsets matched |
| alter placement policy used by table | policy settings updated; table dependency remained in-use |
| alter placement policy used by partition | policy settings updated; partition dependency remained in-use |

Method update:

- The object-reference owner model is predictive for ordinary placement/global-index paths.
- Old-ref-droppable/new-ref-in-use is a stronger oracle than `SHOW CREATE` alone for placement rewrites.
- This clean result downweights simple placement, partition drop/truncate, placement policy update, and global/local happy paths. The next high-density layer should add DDL state: rollback/cancel during partition reorg, failures while placement bundles are rebuilt, and failures around old global-index cleanup.

## 2026-07-02 DB-Level Placement Result

Command:

```bash
python3 /Users/bba/pc/ai_native_ddl_db_placement_reference_probe.py
```

Result:

```text
SUMMARY total=6 findings=0 skipped=0
```

Green cells:

| Cell | Observed behavior |
|---|---|
| DB placement visible control | `SHOW CREATE DATABASE` exposed the database placement policy |
| drop policy referenced by database | blocked with `8241` and preserved the DB ref |
| alter database placement `pp1 -> pp2` | old policy became droppable; new policy remained in-use |
| alter database placement to `DEFAULT` | database ref disappeared and old policy became droppable |
| drop database with DB placement | policy became droppable after schema drop |
| DB default inheritance boundary | old table kept old inherited policy; new table inherited the new DB policy; both refs stayed protected |

Method update:

- Placement policy is now a stronger negative screen: the in-use scan covers DB, table, partition, and special ranges.
- Table inheritance from DB placement is a boundary, not a rewrite obligation: `ALTER DATABASE` changes the default for future tables but does not rewrite existing table refs.
- Do not continue ordinary placement matrices unless a new owner, state dimension, or container bypass is found.

## 2026-07-02 View Reference Screening Result

Command:

```bash
python3 /Users/bba/pc/ai_native_ddl_view_reference_probe.py
```

Result:

```text
SUMMARY total=5 findings=0 skipped=0
```

Screened cells:

| Cell | Observed behavior |
|---|---|
| live view control | view selected base rows and `SHOW CREATE VIEW` exposed stored SELECT text |
| base table rename | DDL succeeded; view kept old table name and became invalid |
| base column rename | DDL succeeded; view kept old column name and became invalid |
| base table drop | DDL succeeded; view kept old SELECT text and became invalid |
| cross-DB base database drop | DDL succeeded; external view survived with old cross-DB name and became invalid |

Method update:

- Create-time validation is not enough to classify an object as a rewrite/block owner.
- View metadata stores SQL text, not object IDs or a maintained dependency edge. Treat invalidation after base-object DDL as name-bound semantics, like grants/bindings, not as a sequence-default style dangling-reference bug.
- Do not build a larger view DDL matrix unless product semantics change toward schema-bound views.

## 2026-07-02 Resource Group SWITCH_GROUP Screening Result

Command:

```bash
python3 /Users/bba/pc/ai_native_ddl_resource_group_reference_probe.py
```

Result:

```text
SUMMARY total=3 findings=0 skipped=0
```

Screened cells:

| Cell | Observed behavior |
|---|---|
| missing switch target | `CREATE RESOURCE GROUP ... ACTION=SWITCH_GROUP(missing)` succeeded and `information_schema.resource_groups` showed the missing name |
| drop switch target | source group kept `ACTION=SWITCH_GROUP(target)` after `DROP RESOURCE GROUP target`, but this is consistent with the missing-target behavior |
| alter query limit to null | `ALTER RESOURCE GROUP src QUERY_LIMIT=NULL` cleared the stored switch-group name and target became droppable |

Method update:

- `SWITCH_GROUP` looks like a DDL object reference, but current DDL validation only checks that the switch-group name is non-empty.
- Because create/alter do not prove target existence, drop-time non-blocking is not a sequence-default style dangling-reference bug.
- Keep this as a negative selector: do not promote a name field to the rewrite/block matrix unless create/alter or docs establish object-identity semantics.

## 2026-07-01 Delete-Range Metadata Result

Command:

```bash
python3 /Users/bba/pc/ai_native_ddl_delete_range_probe.py
```

Result:

```text
SUMMARY total=2 findings=0 skipped=0
```

Green cells:

| Cell | Observed behavior |
|---|---|
| `REMOVE PARTITIONING` from partitioned table with global index | inserted one old-global-index delete-range record and four table/partition records |
| `DROP GLOBAL INDEX` on partitioned table | inserted one logical index delete-range record and no table/partition range records |

Method update:

- `mysql.gc_delete_range` is now a low-noise DDL metadata oracle for old global-index cleanup.
- The ordinary enqueue path is green, so the next delete-range target should be the GC worker consumption/redo side, not more SQL-visible rowset checks for the same DDL completion path.

## 2026-07-01 Placement-Bundle Failure Result

Command:

```bash
python3 /Users/bba/pc/ai_native_ddl_placement_bundle_failure_probe.py --require-failpoint
```

Result:

```text
SUMMARY total=5 findings=0 skipped=0
```

Green cells:

| Cell | Observed behavior |
|---|---|
| persistent `putRuleBundlesError` during `ALTER TABLE ... PLACEMENT POLICY` | DDL failed; table metadata stayed unchanged; new policy was droppable |
| one-shot retryable `putRuleBundlesError` during `ALTER TABLE ... PLACEMENT POLICY` | DDL retried successfully; table dependency remained in-use |
| persistent `putRuleBundlesError` during `ALTER TABLE ... PARTITION ... PLACEMENT POLICY` | DDL failed; original partition policy stayed referenced; new policy was droppable |
| persistent `putRuleBundlesError` during `ALTER PLACEMENT POLICY` | DDL failed; policy settings and table dependency stayed unchanged |
| one-shot retryable `putRuleBundlesError` during `ALTER PLACEMENT POLICY` | DDL retried successfully; settings updated and dependency stayed in-use |

Method update:

- This confirms the placement-bundle failure controls already cover the most obvious metadata/bundle atomicity cells.
- Do not keep expanding `putRuleBundlesError` happy/failure variants unless a new placement owner or multi-owner path is found.

## 2026-07-01 FK Table/Index Object Result

Source anchors:

| Path | Contract |
|---|---|
| `pkg/ddl/table.go:824` | table rename calls `adjustForeignKeyChildTableInfoAfterRenameTable` |
| `pkg/ddl/table.go:957` | child FK `RefSchema` / `RefTable` are rewritten after parent table rename |
| `pkg/ddl/foreign_key.go:404` / `421` | drop/truncate parent table must block while child FK exists |
| `pkg/ddl/foreign_key.go:443` | drop supporting index must block unless another index covers the FK columns |
| `pkg/ddl/index.go:676` | rename index changes index object name without changing FK column reference |

Command:

```bash
python3 /Users/bba/pc/ai_native_ddl_fk_object_reference_probe.py
```

Result:

```text
SUMMARY total=10 findings=0 skipped=0
```

Green cells:

| Cell | Observed behavior |
|---|---|
| parent table rename | child FK `REFERENCES` target rewrote and enforcement stayed active |
| child table rename | child FK preserved parent target and enforcement stayed active |
| multi-table rename child-then-parent | child FK target rewrote to renamed parent |
| multi-table rename parent-then-child | child FK target rewrote to renamed parent |
| drop parent table | blocked by FK owner; original FK still enforced |
| truncate parent table | blocked by FK owner; original FK still enforced |
| drop parent supporting index | blocked with `1553` FK-needed error |
| drop child supporting index | blocked with `1553` FK-needed error |
| rename child supporting index | allowed; FK enforcement stayed active |
| drop redundant child index | allowed because another covering index remained |

Method update:

- The multi-table rename order was a plausible red cell from code reading, but the external oracle proved it green.
- FK table/index owner is now a lower-density target for basic rename/drop paths. Future FK work should require a new state dimension or a newer code path, not more basic owner checks.

## 2026-07-01 Masking-Policy Side-Metadata Result

Source anchors:

| Path | Contract |
|---|---|
| `pkg/ddl/table.go:140` | drop table removes masking policies on that table |
| `pkg/ddl/schema.go:222` | drop database removes masking policies by database name |
| `pkg/ddl/table.go:568` | truncate table rewrites masking-policy `table_id` from old to new |
| `pkg/ddl/table.go:837` / `887` | table rename updates masking-policy `db_name` / `table_name` |
| `pkg/ddl/modify_column.go:531` / `1100` | no-reorg and reorg modify/change paths call `syncMaskingPolicyForModifiedColumn` |
| `pkg/ddl/column.go:218` | drop column removes masking policies on that column |
| `pkg/ddl/masking_policy.go:651` | column rename/change rewrites `column_name`, `column_id`, and expression |

Command:

```bash
python3 /Users/bba/pc/ai_native_ddl_masking_policy_reference_probe.py
```

Result:

```text
SUMMARY total=13 findings=0 skipped=0
```

Green cells:

| Cell | Observed behavior |
|---|---|
| table rename | policy `db_name` / `table_name` followed the table |
| multi-table rename | each policy followed its own table by table ID |
| cross-DB rename | policy moved from old DB name to new DB name |
| column rename | `column_name` and expression were rewritten |
| multi-schema `CHANGE COLUMN ... ADD COLUMN` | `column_name` and expression were rewritten |
| supported `MODIFY COLUMN` | policy binding stayed intact |
| unsupported `MODIFY COLUMN ... JSON` | DDL blocked and policy stayed intact |
| drop column | policy row was removed |
| multi-schema `DROP COLUMN ... ADD COLUMN` | policy row was removed |
| truncate table | `table_id` changed and the policy remained operable |
| drop table | policy rows for the table were removed |
| drop database | policy rows for the database were removed |
| alter policy expression to non-target column | DDL blocked and the original expression stayed intact |

Method update:

- This is a useful negative sample because it matches the high-risk shape: owner state lives in a side sys table, references both object IDs and names, and has separate handlers for table, column, truncate, drop, and policy DDL.
- The owner is green because the code has explicit helpers for each DDL identity change and both no-reorg/reorg modify-column completion paths call the sync helper.
- 2026-07-03 update: that stop rule paid off. A new DDL entrypoint, `EXCHANGE PARTITION`, changes the same `table_id` ownership dimension but does not call a masking-policy remap helper. This produced id630014: the policy row keeps `table_name=nt` while its `table_id` becomes `pt.p0`'s partition ID, and `ALTER TABLE nt DISABLE/DROP MASKING POLICY` can no longer reach it.
- Do not continue expanding basic masking-policy rewrite/cleanup cells. Future work on this owner should require id630014 fix validation, another owner-changing DDL entrypoint, or a failure-injection path around sys-table updates.

## 2026-07-01 Stats Side-Metadata Result

Source anchors:

| Path | Contract |
|---|---|
| `pkg/executor/show_stats.go:204` | `fetchShowStatsHistogram` walks the current infoschema and passes live db/table/partition names |
| `pkg/executor/show_stats.go:236` | `appendTableForStatsHistograms` prints column/index names from the cached `statistics.Table` object |
| `pkg/statistics/handle/ddl/subscriber.go:114` | `ActionModifyColumn` handles DDL column changes by inserting column stats if needed |
| `pkg/statistics/handle/storage/save.go:487` | `InsertColStats2KV` uses `insert ignore`, so an existing analyzed column histogram row is not replaced |
| `pkg/statistics/handle/storage/save.go:515` | if the insert is a no-op, `stats_meta.version` / `last_stats_histograms_version` are not advanced |
| `pkg/statistics/handle/cache/statscache.go:136` | stats-cache refresh scans only `mysql.stats_meta` rows with a newer version |
| `pkg/statistics/handle/cache/statscache.go:200` | after a table is selected, `TableInfo.UpdateTS` would force reload when schema changed |
| `pkg/statistics/handle/storage/update.go:159` | `ChangeGlobalStatsID` rewrites table IDs across `mysql.stats_*` tables for partitioning changes |

Command:

```bash
python3 /Users/bba/pc/ai_native_ddl_stats_reference_probe.py
```

Result:

```text
SUMMARY total=7 findings=2 skipped=0
```

Green cells:

| Cell | Observed behavior |
|---|---|
| table rename | visible `SHOW STATS_META` followed the new table name through the same table ID |
| add partitioning | global stats moved from the old single-table ID to the new partitioned global table ID |
| remove partitioning | partitioned global stats moved to the new single-table ID |
| truncate table | new table ID got an empty visible stats row |
| truncate partition | visible stats changed to `global=2,p0=0,p1=2` after truncating `p0` |

Finding:

| Cell | Observed behavior |
|---|---|
| `RENAME COLUMN a TO aa` after `ANALYZE TABLE` | live schema and `SHOW CREATE TABLE` contain only `aa`, but `SHOW STATS_HISTOGRAMS` still displays column stats under old name `a` until `ANALYZE TABLE` is run again |
| `CHANGE COLUMN a aa INT` after `ANALYZE TABLE` | same stale visible column stats name as `RENAME COLUMN`; treated as the same root family, not a separate bug |

Minimal draft:

```text
/Users/bba/pc/ai-native-stats-column-rename-draft.md
```

Method update:

- This is the first new red cell after the DDL-only refocus. It validates the next-owner selection rule: scan for side metadata whose storage is keyed by object IDs but whose public API exposes object names.
- Delayed stats GC is not a good oracle by itself. Drop-index/drop-column stats may remain visible briefly by design, so those cells are noisy. Column rename is cleaner because the live column still exists under the same column ID and the display layer should be able to resolve the current name.
- The root shape is now a reusable search pattern:

```text
side metadata keyed by object ID
+ SHOW/API exposes object name
+ DDL subscriber may not advance the version that drives cache refresh
+ cached display object is not reloaded with the live TableInfo
= stale visible reference after DDL rename
```

Pause gate status:

- Completed to issue-discussion quality in `/Users/bba/pc/ai-native-stats-column-rename-draft.md`.
- `RENAME COLUMN` and `CHANGE COLUMN` are both confirmed; they are treated as one root family.
- The likely root bucket is not generic "cache stale": the DDL path does not advance the stats refresh/version signal when existing analyzed histograms make `InsertColStats2KV` a no-op.
- Do not expand more stats cells before owner feedback or fix-direction validation.

## Privilege Grant Side-Metadata Screening

This owner is intentionally a selector check, not a new bug hunt. `mysql.tables_priv` and `mysql.columns_priv` contain db/table/column names, but the first question is whether those names are DDL-owned object references or user policy names.

Source anchors:

| Path | Signal |
|---|---|
| `pkg/privilege/privileges/cache.go:66` | privilege cache loads `mysql.tables_priv` by `DB` and `Table_name` strings |
| `pkg/privilege/privileges/cache.go:67` | privilege cache loads `mysql.columns_priv` by `DB`, `Table_name`, and `Column_name` strings |
| `pkg/executor/grant.go:572` | table-level GRANT writes `mysql.tables_priv` using resolved/input table name |
| `pkg/executor/grant.go:598` | column-level GRANT writes `mysql.columns_priv` using resolved column name |
| `pkg/meta/metadef/system_tables_def.go:118` | `mysql.tables_priv` primary key is `(Host, DB, User, Table_name)` |
| `pkg/meta/metadef/system_tables_def.go:130` | `mysql.columns_priv` primary key is `(Host, DB, User, Table_name, Column_name)` |

Command:

```bash
python3 /Users/bba/pc/ai_native_ddl_privilege_reference_probe.py
```

Result:

```text
SUMMARY total=3 findings=0 skipped=0
```

Observed cells:

| Cell | Observed behavior |
|---|---|
| table grant + `RENAME TABLE t TO t2` | grant stayed on textual name `t`; selecting `t2` as the grantee was denied; renaming back to `t` reattached the grant |
| column grant + `RENAME COLUMN a TO aa` + add replacement `a` | `mysql.columns_priv` and `SHOW GRANTS` stayed on textual column name `a`; no metadata rewrite to `aa` occurred |
| table grant + `DROP TABLE t` + recreate `t` | grant survived the drop and applied to the new object with the same name |

Method update:

- This is a negative selector example: a sys table containing object-like names is not automatically a DDL object-reference owner.
- The owner is a name-bound policy surface, so "DDL did not rewrite grant rows" is expected behavior, not a red cell.
- Refined filter before building a new matrix:

```text
must prove object-identity binding
before applying rewrite/block proof obligation
```

Do not expand privilege grant rename/drop cases unless a separate product semantic claim says grants should follow object identity.

## Table-Cache Side-Metadata

`mysql.table_cache_meta` is a true object-identity side table: it is keyed by table ID, and cached table state is also visible through `SHOW CREATE TABLE` as `/* CACHED ON */`.

Source anchors:

| Path | Contract |
|---|---|
| `pkg/meta/metadef/system_tables_def.go:360` | `mysql.table_cache_meta` stores cache metadata by `tid` |
| `pkg/ddl/executor.go:6935` | `ALTER TABLE ... CACHE` writes a `mysql.table_cache_meta` row for `t.Meta().ID` |
| `pkg/ddl/job_worker.go:428` / `:431` | successful `ActionAlterNoCacheTable` deletes `mysql.table_cache_meta` by `job.TableID` |
| `pkg/ddl/executor.go:4310` | `DROP TABLE` blocks cached tables |
| `pkg/ddl/executor.go:4438` | `TRUNCATE TABLE` blocks cached tables |
| `pkg/ddl/executor.go:4517` | `RENAME TABLE` blocks cached tables |
| `pkg/ddl/index.go:681` / `:1173` / `:2080` | rename/create/drop index block cached tables |
| `pkg/ddl/executor.go:763` | `DROP DATABASE` builds an `ActionDropSchema` job without scanning cached tables |
| `pkg/ddl/schema.go:158` | `onDropSchema` drops all table metadata and cleans masking policies, but not `mysql.table_cache_meta` |

Command:

```bash
python3 /Users/bba/pc/ai_native_ddl_table_cache_reference_probe.py
```

Result:

```text
SUMMARY total=3 findings=1 skipped=0
```

Green controls:

| Cell | Observed behavior |
|---|---|
| `ALTER TABLE t CACHE` then `ALTER TABLE t NOCACHE` | cache creates the table-id side row; nocache removes it; `SHOW CREATE TABLE` reflects the state |
| cached table + direct table/index/partition DDL | rename/drop/truncate/add-index/rename-index/partitioning all block with cache-table error family and preserve the side row |

Finding:

| Cell | Observed behavior |
|---|---|
| cached table + `DROP DATABASE db` | `DROP DATABASE` succeeds, the table disappears from `information_schema.tables`, but `mysql.table_cache_meta` still has the dropped table ID |

Minimal draft:

```text
/Users/bba/pc/ai-native-table-cache-drop-database-draft.md
```

Method update:

- This validates the refined selector after the privilege negative sample. The owner is ID-keyed, not a name policy.
- The red pattern is not "missing rename rewrite"; it is broader-container DDL bypassing sibling path block/cleanup rules.
- Do not expand table-cache variants before owner discussion or fix validation. The useful next action is to decide whether `DROP DATABASE` should block like `DROP TABLE` or clean `mysql.table_cache_meta` for all dropped table IDs.

## Sequence Default Reference

A column default can reference a sequence object:

```sql
CREATE SEQUENCE seq;
CREATE TABLE t(a INT DEFAULT NEXT VALUE FOR seq);
```

This owner sits between column-expression owners and object-reference owners. The reference is stored in table column metadata, but the target is a separate DDL object that can be dropped or renamed.

Source anchors:

| Path | Contract |
|---|---|
| `pkg/ddl/add_column.go:667` | sequence default is accepted as `ast.NextVal` |
| `pkg/ddl/add_column.go:908` | default expression is restored and stored as SQL text |
| `pkg/ddl/add_column.go:1269` | type guard allows sequence defaults only on integer columns |
| `pkg/expression/sessionexpr/sessionctx.go:406` | runtime resolves the sequence by schema/name |
| `pkg/ddl/executor.go:4264` / `:4317` | `DROP SEQUENCE` only checks that the target object is a sequence |
| `pkg/ddl/executor.go:4516` / `:4569` | `RENAME TABLE` allows renaming the sequence object without sequence-default dependency handling |
| `pkg/ddl/table.go:824` / `:837` | rename path has FK and masking-policy rewrite helpers, but no sequence-default helper |
| `pkg/ddl/schema.go:158` | `DROP DATABASE` handles FK checks and affinity/masking cleanup, but not cross-schema sequence-default dependencies |

Command:

```bash
python3 /Users/bba/pc/ai_native_ddl_sequence_default_reference_probe.py
```

Result:

```text
SUMMARY total=5 findings=3 skipped=0
```

Findings:

| Cell | Observed behavior |
|---|---|
| table default references sequence + `DROP SEQUENCE` | DDL succeeds; `SHOW CREATE TABLE` still points at old sequence; default insert fails with `1146` |
| table default references sequence + `RENAME TABLE seq TO seq2` | DDL succeeds; default is not rewritten; default insert fails with `1146` |
| cross-DB table default references sequence + `DROP DATABASE` for sequence DB | DDL succeeds; external table default points at dropped sequence DB; default insert fails with `1146` |

Green controls:

| Cell | Observed behavior |
|---|---|
| live sequence default | default insert consumes the sequence |
| `CHANGE COLUMN a aa INT DEFAULT NEXT VALUE FOR seq` | column rename with live sequence preserves default behavior |

Draft:

```text
/Users/bba/pc/ai-native-sequence-default-reference-draft.md
```

Method update:

- This is a new positive selector, distinct from stats/table-cache side metadata.
- The red pattern is "executable schema expression references a separate DDL object, but remove/rename path has no reverse dependency scan".
- Stop expansion here. The next action is to discuss/fix semantics: `DROP SEQUENCE` should block, sequence rename should block or rewrite, and `DROP DATABASE` should block when it removes a sequence referenced by live tables outside the dropped schema.

## Region-Split Policy Negative Screen

Region split policy is a useful selector guardrail. It is SQL-visible through `SHOW CREATE TABLE`, but the persistent policy lives inside `TableInfo.TableSplitPolicy` or `IndexInfo.RegionSplitPolicy`, not in an independent side table/cache.

Source anchors:

| Path | Signal |
|---|---|
| `pkg/ddl/table.go:1841` | `ActionAlterTableSetRegionSplitPolicy` writes the policy into the current `TableInfo` or `IndexInfo` |
| `pkg/executor/show.go:1424` / `:1454` | `SHOW CREATE TABLE` restores table/index split policies from the same table metadata |
| `pkg/ddl/index.go:3939` | `RENAME INDEX` mutates the existing `IndexInfo.Name`, so attached policy naturally follows |
| `pkg/meta/model/index.go:286` | `RegionSplitPolicy` is a field on `IndexInfo`, not an external owner keyed by old object identity |
| `pkg/ddl/table_split_test.go:357` | existing round-trip test already proves the public hint is intended to be replayable |

Command:

```bash
python3 /Users/bba/pc/ai_native_ddl_region_split_policy_probe.py
```

Result:

```text
SUMMARY total=5 findings=0 skipped=0
```

Observed cells:

| Cell | Observed behavior |
|---|---|
| split-index policy + `RENAME INDEX idx_a TO idx_b` | `SHOW CREATE TABLE` prints `SPLIT INDEX idx_b`; old split index name disappears |
| split-index policy + `DROP INDEX idx_a` | split policy disappears with the dropped index |
| split-index policy + `DROP INDEX idx_a, ADD INDEX idx_c(b)` | replacement index does not inherit the old split policy |
| split-index policy + `CHANGE COLUMN a aa DATE` | index column follows `aa`; split policy remains and the `SHOW CREATE` text round-trips |
| table+index split policy + cross-schema `RENAME TABLE` | both table and index split policies move with `TableInfo` |

Method update:

- This is a negative selector example, like privilege grants but for the opposite reason: the metadata is object-local rather than name-bound policy.
- SQL-visible DDL metadata is not enough. If the policy is stored inside the same object metadata that the DDL path already moves/drops, rename/drop usually has no separate rewrite obligation.
- Keep this owner downgraded unless a future source scan finds a separate region-split cache, async application record, or version/invalidation layer with its own stale-reference risk.

## Affinity Reference-Owner Negative Screen

Affinity initially looked like a strong owner because it creates PD-side affinity groups. Source reading splits the surface into two parts:

- SQL-visible `SHOW AFFINITY` rows are enumerated from live InfoSchema tables with `Affinity` metadata.
- PD group state is keyed by table/partition IDs and is used only to fill state columns.

Source anchors:

| Path | Signal |
|---|---|
| `pkg/ddl/affinity.go:33` / `:39` | table and partition group IDs are `_tidb_t_<tableID>` and `_tidb_pt_<tableID>_p<partitionID>` |
| `pkg/ddl/affinity.go:104` | create/alter/truncate creates PD affinity groups from current table metadata |
| `pkg/ddl/affinity.go:135` | drop/truncate cleanup deletes PD affinity groups by table/partition IDs |
| `pkg/ddl/affinity.go:159` | `DROP DATABASE` batch-deletes affinity groups for all dropped tables |
| `pkg/ddl/schema.go:214` | `DROP DATABASE` invokes the affinity batch cleanup as best effort |
| `pkg/executor/show_affinity.go:44` / `:73` / `:84` | `SHOW AFFINITY` lists current InfoSchema tables/partitions, then derives group IDs to query PD state |
| `pkg/executor/show_affinity.go:144` | missing PD state still yields a row from live table metadata with NULL state fields |
| `pkg/ddl/executor.go:2612` | `REMOVE PARTITIONING` is blocked for affinity tables |
| `pkg/ddl/partition.go:2034` | `DROP PARTITION` is blocked for affinity tables |
| `pkg/ddl/affinity_test.go:199` / `:212` / `:227` / `:258` | existing tests cover drop table, truncate table, truncate partition, and drop database cleanup |

Command:

```bash
python3 /Users/bba/pc/ai_native_ddl_affinity_reference_probe.py
```

Result:

```text
SUMMARY total=6 findings=0 skipped=0
```

Observed cells:

| Cell | Observed behavior |
|---|---|
| table affinity visible control | `SHOW CREATE TABLE` and `SHOW AFFINITY` expose the table affinity |
| table affinity + `RENAME TABLE t TO tt` | visible affinity follows the new table name; old name disappears |
| table affinity + `TRUNCATE TABLE` | visible affinity is preserved on the new table ID |
| table affinity + `DROP TABLE` | visible affinity row disappears |
| partition affinity + `TRUNCATE PARTITION` | both partition affinity rows remain visible; `DROP PARTITION` and `REMOVE PARTITIONING` are blocked |
| table+partition affinity + `DROP DATABASE` | visible table and partition affinity rows disappear |

Method update:

- External PD side state alone is not enough to prioritize a matrix.
- If the public SQL surface is driven by live InfoSchema and the external state columns are only annotations, stale PD groups are not the same class as stats/table-cache side metadata unless a separate public stale surface is proven.
- Keep affinity downgraded unless a future scan finds a low-noise PD-state oracle for failed cleanup that remains user-visible after the intended best-effort window.

## Stateful Object-Reference Plan

Source anchors:

| Path | Why it is high signal |
|---|---|
| `pkg/ddl/partition.go:3265` | Creates old/new index copies and populates `DDLChangedIndex` before reorg states |
| `pkg/ddl/partition.go:3370` / `3428` / `3463` / `3476` | `reorgPartRollback1..4` force rollback at different partition reorg states |
| `pkg/ddl/rollingback.go:405` | Rollback removes new index copies or reverts old global indexes depending on `DDLState` |
| `pkg/ddl/partition.go:2136` | `rollbackLikeDropPartition` removes adding partitions, cleans placement bundles, and records old global indexes for cleanup |
| `pkg/ddl/partition.go:2549` / `2582` / `2616` / `2652` | `truncatePartCancel1` and `truncatePartFail1..3` cover truncate partition state transitions |
| `pkg/ddl/placement_policy.go:285` | `updateExistPlacementPolicy` updates policy and rebuilds dependent table/partition/range bundles |

`/Users/bba/pc/ai_native_ddl_stateful_object_probe.py` currently has fourteen stateful cells:

| Cell | Failpoint | Expected rollback oracle |
|---|---|---|
| `ALTER TABLE ... PARTITION BY ... UPDATE INDEXES` from non-partitioned table with added partition policy | `reorgPartRollback2/3/4` | original non-partitioned table restored; added partition policy droppable; table policy still in-use; no `GLOBAL`; rowsets match |
| `ALTER TABLE ... REMOVE PARTITIONING` from partitioned table with table+partition policies and global index | `reorgPartRollback2/3/4` | original partition metadata restored; table+partition policies still in-use; original `GLOBAL` marker preserved; rowsets match |
| `ALTER TABLE ... PARTITION BY ... UPDATE INDEXES` from non-partitioned table with added partition policy | `reorgPartFail4/5` with one-shot action | DDL retries and succeeds; partition metadata, table/partition policy refs, global/local markers, and rowsets are correct |
| `ALTER TABLE ... REMOVE PARTITIONING` from partitioned table with table+partition policies and global index | `reorgPartFail4/5` with one-shot action | DDL retries and succeeds; partition metadata removed, partition policy released, table policy preserved, global marker cleared, rowsets match |
| `ALTER TABLE ... TRUNCATE PARTITION` with table+partition policies and global index | `truncatePartCancel1` | original partition metadata, policy refs, global marker, and rowsets preserved |
| `ALTER TABLE ... TRUNCATE PARTITION` with table+partition policies and global index | `truncatePartFail1/2/3` with one-shot action | DDL retries and succeeds; policy refs and global marker preserved; only truncated partition rows removed |

2026-07-01 stateful smoke result:

```text
SUMMARY total=14 findings=0 skipped=0
```

Observed behavior:

| Cell | Result |
|---|---|
| `PARTITION BY ... UPDATE INDEXES` rollback at `reorgPartRollback2/3/4` | restored non-partitioned table; released added partition policy; preserved table policy; rowsets matched |
| `REMOVE PARTITIONING` rollback at `reorgPartRollback2/3/4` | restored partition metadata; preserved table/partition policy refs; preserved original global marker; rowsets matched |
| `PARTITION BY ... UPDATE INDEXES` retry at `reorgPartFail4/5` | kept partition metadata; preserved table/partition policies; kept `idx_a` global and `idx_b` local; rowsets matched |
| `REMOVE PARTITIONING` retry at `reorgPartFail4/5` | removed partition metadata; released partition policy; preserved table policy; cleared `GLOBAL`; rowsets matched |
| `TRUNCATE PARTITION` cancel at `truncatePartCancel1` | preserved partition metadata, policy refs, global marker, and rowsets |
| `TRUNCATE PARTITION` one-shot failures at `truncatePartFail1/2/3` | retried successfully; preserved metadata/policies/global marker; removed only truncated partition rows |

Execution note:

- Managed TiDB was scaled to 0.
- `/private/tmp/.../tidb-server-fp` was started as `fp-tidb` with `github.com/pingcap/tidb/pkg/server/enableTestAPI=return(true)`.
- SQL/status were forwarded to `14000/18080` for the run.
- TiDB's failpoint API returns `204` for successful `PUT`; the stateful probe accepts both `200` and `204`.

## 2026-07-03 Reorg Duplicate Rowid Identity Fast Path

This is a separate `REORGANIZE PARTITION` family from the earlier replacement-global-index bug.

Source obligation:

```text
During nonclustered partition reorg, duplicate _tidb_rowid values can exist across old partitions
after EXCHANGE PARTITION. The repair path probes target keys. If the target key exists and raw row
bytes are equal, it assumes the row was already copied and skips the write.
```

Matrix result:

| Cell | Result |
|---|---|
| ordinary reorg, distinct target rows | GREEN, count 2 -> 2 |
| same rowid, different raw bytes | GREEN, count 2 -> 2 and one `_tidb_rowid` regenerated |
| different rowid, same raw bytes | GREEN, count 2 -> 2 |
| same rowid, same raw bytes, different old physical partitions | RED, count 2 -> 1 |

Bug and method assets:

- Bug draft: `/Users/bba/pc/ai-native-reorg-duplicate-rowid-drop-draft.md`
- Method case: `/Users/bba/pc/ai-native-id600001-method-case.md`
- Remote bug DB: id600001 confirmed, `O13_ROWSET_CARDINALITY_INVARIANT`, `S9_REORG_BACKFILL_IDENTITY_FAST_PATH`

Do not expand this by adding more reorg syntax. Reopen only for fix validation or another equality-as-identity fast path where source/owner/container identity is omitted.

## Expansion Rules

Expand only along a reasoned dimension:

| Expansion | Why it is high signal |
|---|---|
| Multi-schema ALTER | A known source of partial-path checks and rollback/non-revertible gaps |
| `ALGORITHM=COPY/INPLACE/INSTANT` syntax | Can select a different DDL preparation path before the same owner check |
| Reorg vs no-reorg modify | FK/TTL rewrite appears in both direct adjust and reorg completion paths |
| Parent vs child FK rename | Cross-table metadata rewrite has a larger blast radius than same-table rewrite |
| `RANGE(expr)` vs `RANGE COLUMNS` vs `KEY/HASH` partitioning | Partition owners are extracted through different metadata fields |
| Global/local index with partition DDL | Global index cleanup and partition physical IDs add owner state outside normal index columns |
| Placement with partition exchange/reorg | Policy refs are object references, not column refs; use object-reference matrix, not column matrix |

## Method Lesson Being Tested

This matrix is the concrete DDL version of "analysis x fuzz":

```text
AI reads code
  -> extracts reference owners
  -> classifies each owner as rewrite or block
  -> marks missing-check / missing-rewriter cells red
  -> generates only red or high-value green-control SQL
  -> uses behavior and metadata round-trip oracles
  -> feeds hits and negatives back into the matrix
```

The value of a new bug is that it validates a red-cell prediction. The value of a negative result is that it proves the owner model and lets the next run avoid low-density cells.
