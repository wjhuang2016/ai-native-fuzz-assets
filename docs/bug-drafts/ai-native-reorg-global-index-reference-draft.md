# Candidate id30007: `REORGANIZE PARTITION` can leave replacement global index incomplete

## Status

Candidate DDL correctness bug found by the DDL reference-ownership matrix.

Probe:

```bash
python3 /Users/bba/pc/ai_native_ddl_reorg_global_index_reference_probe.py
```

Current result:

```text
SUMMARY total=2 findings=1 skipped=0
```

The green control is partition placement policy rewrite during the same `REORGANIZE PARTITION` shape. The red cell is specific to replacement global-index backfill.

Additional environment check on 2026-07-02:

```text
testbed: 8192975
namespace: testbed-tps-8192975-1-14
SQL: 127.0.0.1:14000 -> pod/fp-tidb:4000
status/failpoint: 127.0.0.1:18080 -> pod/fp-tidb:10080
SELECT VERSION(): 8.0.11-TiDB-v8.4.0-this-is-a-placeholder
managed TiDB replicas: 0
```

The same probe result reproduced on this older-looking fp-tidb environment:

```text
SUMMARY total=2 findings=1 skipped=0
```

This makes the candidate stronger than a master-only regression signal. It also adds a harness lesson: record the testbed capability/version fingerprint separately from the finding, because some environments may not expose newer variables such as `@@tidb_version` even when the target DDL syntax is available.

## Minimal Repro

```sql
DROP DATABASE IF EXISTS ai_native_reorg_min;
CREATE DATABASE ai_native_reorg_min;
USE ai_native_reorg_min;

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

SELECT GROUP_CONCAT(CONCAT(a, ':', b) ORDER BY b)
FROM t USE INDEX(idx_b)
WHERE b >= 0;

SELECT GROUP_CONCAT(CONCAT(a, ':', b) ORDER BY b)
FROM t IGNORE INDEX(idx_b)
WHERE b >= 0;

ADMIN CHECK TABLE t;
```

Observed on the current fp-tidb testbed:

```text
ALTER succeeds.

USE INDEX(idx_b):    12:120
IGNORE INDEX(idx_b): 12:120,30:300

ERROR 8223 (HY000): data inconsistency in table: t, index: idx_b,
handle: 2, index-values:"" != record-values:"handle: 2, values: [KindInt64 300]"
```

`SHOW CREATE TABLE` still reports a valid global index and the expected final partition layout:

```sql
UNIQUE KEY `idx_b` (`b`) /*T![global_index] GLOBAL */
PARTITION BY RANGE (`a`)
(PARTITION `p0` VALUES LESS THAN (10),
 PARTITION `p1a` VALUES LESS THAN (15),
 PARTITION `p1b` VALUES LESS THAN (20),
 PARTITION `pmax` VALUES LESS THAN (MAXVALUE))
```

## Expected

After `ALTER TABLE ... REORGANIZE PARTITION` succeeds, the replacement global index must contain entries for every live row in every live partition. `USE INDEX(idx_b)` and `IGNORE INDEX(idx_b)` should see the same row set, and `ADMIN CHECK TABLE` should pass.

## Source Chain

- `/Users/bba/pc/tidb/pkg/ddl/executor.go:2524` enters `ReorganizePartitions` and submits `ActionReorganizePartition`.
- `/Users/bba/pc/tidb/pkg/ddl/partition.go:4048` states the intended flow: copy rows from dropped partitions, build indexes on added partitions, then update new global indexes from non-touched partitions.
- `/Users/bba/pc/tidb/pkg/ddl/partition.go:4115` builds indexes for `AddingDefinitions`, then switches `reorgInfo.elements` to replacement global indexes.
- `/Users/bba/pc/tidb/pkg/ddl/partition.go:4136` looks for the first partition that is neither adding nor dropping.
- `/Users/bba/pc/tidb/pkg/ddl/index.go:3524` decides whether the next partition is still in `AddingDefinitions`; otherwise it calls `findNextNonTouchedPartitionID`.
- `/Users/bba/pc/tidb/pkg/ddl/index.go:3621` `findNextNonTouchedPartitionID` skips partitions in `DroppingDefinitions`, but does not skip `AddingDefinitions`.

Working hypothesis:

```text
When backfilling replacement global indexes for non-touched partitions,
iteration starts at the first non-touched partition.
If that partition is before the reorganized range, the next step can enter
AddingDefinitions again.
Once the AddingDefinitions iterator reaches its end, the job considers index
backfill done, so later non-touched partitions such as pmax are never added to
the replacement global index.
```

This matches the repro: the row in `pmax` remains in the table, but its entry is missing from the replacement global index.

## Fix Direction

The non-touched-partition phase should iterate over:

```text
pi.Definitions - pi.AddingDefinitions - pi.DroppingDefinitions
```

It should not fall back into the adding-partition phase after the non-touched phase has started. A narrow fix can either:

- teach `findNextNonTouchedPartitionID` to skip both adding and dropping definitions; or
- carry an explicit reorg phase so `getNextPartitionInfo` uses non-touched iteration after replacement global-index backfill starts.

The important contract is phase separation:

```text
after adding-partition indexes are complete,
replacement global-index backfill must not re-enter the AddingDefinitions iterator
```

Fix validation should prove the set semantics, not only replay the exact repro:

| Validation shape | What it proves |
|---|---|
| reorganized range in the middle, row in later non-touched partition | current red case; later partitions are not skipped |
| reorganized range in the middle, rows in earlier and later non-touched partitions | non-touched iteration does not terminate after crossing adding definitions |
| reorganized first range, rows in later non-touched partitions | fix does not depend on an earlier non-touched partition |
| reorganized last range, rows in earlier non-touched partitions | existing earlier non-touched backfill remains correct |
| all partitions are reorganized, no non-touched partitions remain | empty non-touched set is handled cleanly |

For each validation shape:

```text
USE INDEX(idx_b) rowset == IGNORE INDEX(idx_b) rowset
AND ADMIN CHECK TABLE passes
```

## Method Takeaway

This hit came from the refined DDL selector:

```text
same owner family already has green coverage for DROP/TRUNCATE/REMOVE PARTITIONING
+ sibling DDL path has a different multi-stage reorg iterator
+ current-schema oracle exists: global-index rowset + ADMIN CHECK TABLE
= build a tiny matrix instead of expanding all partition DDL uniformly
```

The key method improvement is to look for owner-path asymmetry, not just owner existence.
