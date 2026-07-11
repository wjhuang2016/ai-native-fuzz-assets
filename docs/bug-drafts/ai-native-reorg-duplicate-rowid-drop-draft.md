# id600001: REORGANIZE PARTITION drops duplicate nonclustered rows after EXCHANGE PARTITION

> 2026-07-03. Confirmed on testbed `8192975` / `fp-tidb`. Inserted into remote `found_bug` as id600001 (`MAX(id)=600001`, `COUNT(*)=32`).

## Summary

`ALTER TABLE ... REORGANIZE PARTITION` can silently drop a visible row from a nonclustered partitioned table after `EXCHANGE PARTITION ... WITHOUT VALIDATION` has produced duplicate `_tidb_rowid` values across old physical partitions.

The minimized red cell has two old partitions that both contain the same logical values and the same `_tidb_rowid`. Before the DDL, full table `COUNT(*)` is 2. After the DDL succeeds, `COUNT(*)` is 1.

## Minimal Repro

```sql
DROP DATABASE IF EXISTS ai_s9_reorg_skip_0703;
CREATE DATABASE ai_s9_reorg_skip_0703;
USE ai_s9_reorg_skip_0703;
SET @@tidb_enable_exchange_partition = 1;

CREATE TABLE t(a INT, b INT)
PARTITION BY RANGE (b) (
  PARTITION p0 VALUES LESS THAN (10),
  PARTITION p1 VALUES LESS THAN (20),
  PARTITION pmax VALUES LESS THAN (MAXVALUE)
);

INSERT INTO t VALUES (1,1);

CREATE TABLE tx LIKE t;
ALTER TABLE tx REMOVE PARTITIONING;
INSERT INTO tx VALUES (1,1);

SELECT a,b,_tidb_rowid FROM t PARTITION(p0); -- 1,1,1
SELECT a,b,_tidb_rowid FROM tx;              -- 1,1,1

ALTER TABLE t EXCHANGE PARTITION p1 WITH TABLE tx WITHOUT VALIDATION;

SELECT COUNT(*) FROM t;                       -- 2
SELECT a,b,_tidb_rowid FROM t PARTITION(p0); -- 1,1,1
SELECT a,b,_tidb_rowid FROM t PARTITION(p1); -- 1,1,1

ALTER TABLE t REORGANIZE PARTITION p0,p1
INTO (PARTITION p01 VALUES LESS THAN (20));

SELECT COUNT(*) FROM t;                       -- 1
SELECT a,b,_tidb_rowid FROM t;               -- 1,1,1
```

## Guard Cells

| Cell | Setup | Expected | Observed |
| --- | --- | --- | --- |
| Ordinary reorg | two normal rows in `p0` and `p1` | count 2 -> 2 | GREEN |
| Same rowid, different raw row | `(1,1,1)` and `(2,1,1)` | count 2 -> 2, one rowid regenerated | GREEN |
| Same raw row, different rowid | `(1,1,1)` and `(1,1,100)` | count 2 -> 2 | GREEN |
| Same raw row, same rowid | `(1,1,1)` in both old partitions | count 2 -> 2 | RED: count 2 -> 1 |

This nails the trigger: it is not generic `REORGANIZE PARTITION`, not merely `WITHOUT VALIDATION`, and not merely duplicate `_tidb_rowid`. The red selector is duplicate target key plus identical raw row bytes.

## Source Chain

- `/Users/bba/pc/tidb/pkg/ddl/partition.go:3140`: the DDL contract says all data is reorganized from old partitions into the new partition set.
- `/Users/bba/pc/tidb/pkg/ddl/partition.go:3859`: nonclustered backfill explicitly handles duplicate `_tidb_rowid` values across partitions caused by `EXCHANGE PARTITION`.
- `/Users/bba/pc/tidb/pkg/ddl/partition.go:3879`: the fast path probes target keys with `BatchGetValue`.
- `/Users/bba/pc/tidb/pkg/ddl/partition.go:3906`: if the target key exists and raw bytes are equal, the code treats the row as already backfilled or double-written.
- `/Users/bba/pc/tidb/pkg/ddl/partition.go:3911`: if raw bytes differ, the code recognizes the "not same row due to earlier EXCHANGE PARTITION" case and regenerates `_tidb_rowid`.
- `/Users/bba/pc/tidb/pkg/ddl/partition.go:4001`: the target key preserves the old handle under the new partition prefix, so two old physical rows with the same handle and target partition collide.

## Root Cause

`reorgPartitionWorker.BackfillData` treats this implication as true:

```text
target key exists AND raw row bytes equal
=> the incoming row has already been backfilled or double-written
=> skip writing it
```

That implication is invalid when the source rows come from different old physical partitions. `EXCHANGE PARTITION ... WITHOUT VALIDATION` can create two distinct physical rows with the same `_tidb_rowid` and identical row bytes. During reorg, both map to the same new partition and target key. The first row writes the target key; the second sees identical bytes and is skipped, so one visible row is lost.

## Expected Behavior

`REORGANIZE PARTITION` should preserve the visible table row multiset/cardinality. In ambiguous duplicate-target-key cases, it should regenerate `_tidb_rowid` for one source row instead of treating raw-row equality as identity.

## Fix Direction

Do not use raw-row equality alone as an identity proof when source physical table IDs differ. Possible narrow repairs:

- carry source physical table ID through the backfill row record and only skip equal raw bytes when the existing target can be proven to come from the same source row/retry;
- conservatively regenerate a new `_tidb_rowid` for same-bytes collisions that arise while copying from distinct old partitions;
- record an explicit retry/double-write marker instead of inferring identity from `target key + raw bytes`.

## Method Lesson

This is the useful refinement of the current proof-obligation method:

```text
code checks P: target key exists and raw bytes are equal
system believes Q: this is the same row, already copied by retry or concurrent double-write
therefore it takes fast path: continue without writing
missing dimension D: source physical partition identity
consequence: row cardinality is not preserved
```

The small matrix worked because it attacked the proof itself, not the SQL syntax surface. Once the source comment said duplicate `_tidb_rowid` can happen after exchange, the right adversarial question was: "what if the duplicate rows are byte-identical?"
