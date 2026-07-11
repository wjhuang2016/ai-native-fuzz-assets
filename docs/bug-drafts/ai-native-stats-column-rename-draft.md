# Candidate: SHOW STATS_HISTOGRAMS keeps old column name after DDL column rename

## Issue-Ready Summary

Suggested title:

```text
SHOW STATS_HISTOGRAMS keeps the old column name after RENAME/CHANGE COLUMN until ANALYZE TABLE
```

Problem:

```text
After a column is renamed by DDL, the live schema and SHOW CREATE TABLE expose
only the new column name, but SHOW STATS_HISTOGRAMS can continue to show the old
column name for the existing analyzed column stats. Running ANALYZE TABLE again
refreshes the display name.
```

Impact:

```text
This is a stale DDL-visible metadata reference in the statistics display layer.
It does not look like data corruption: mysql.stats_histograms is keyed by table
ID and histogram ID, and re-analyze can associate the same logical stats with the
new column name. The user-visible problem is that SHOW STATS_HISTOGRAMS exposes a
column name that no longer exists in the live schema.
```

Minimal SQL:

```sql
DROP DATABASE IF EXISTS ai_native_stats_col_min;
CREATE DATABASE ai_native_stats_col_min;
USE ai_native_stats_col_min;

CREATE TABLE t(
  id INT PRIMARY KEY,
  a INT,
  b INT,
  KEY idx_a(a)
);
INSERT INTO t VALUES (1,10,100),(2,20,200),(3,30,300),(4,40,400);
ANALYZE TABLE t;

ALTER TABLE t RENAME COLUMN a TO aa;

SHOW CREATE TABLE t;
SELECT column_name
FROM information_schema.columns
WHERE table_schema='ai_native_stats_col_min'
  AND table_name='t'
ORDER BY ordinal_position;

SELECT SLEEP(5);
SHOW STATS_HISTOGRAMS
WHERE db_name='ai_native_stats_col_min'
  AND table_name='t';

ANALYZE TABLE t;
SHOW STATS_HISTOGRAMS
WHERE db_name='ai_native_stats_col_min'
  AND table_name='t';
```

Expected:

```text
Before re-analyze, SHOW STATS_HISTOGRAMS should show the renamed column `aa`,
because the live table schema no longer contains column `a`.
```

Actual:

```text
SHOW CREATE TABLE: KEY `idx_a` (`aa`)
information_schema.columns: id, aa, b
SHOW STATS_HISTOGRAMS before re-analyze: id, a, b, idx_a
SHOW STATS_HISTOGRAMS after re-analyze:  id, aa, b, idx_a
```

Blast radius:

```text
ALTER TABLE t RENAME COLUMN a TO aa;
ALTER TABLE t CHANGE COLUMN a aa INT;
```

Both paths reproduce the same stale visible name, so this is treated as one root family.

Root-cause hypothesis:

```text
RENAME/CHANGE COLUMN updates TableInfo.
The DDL stats subscriber handles ActionModifyColumn by calling insertStats4Col.
For an already analyzed column, InsertColStats2KV uses insert ignore and does not
insert a new stats_histograms row.
When that insert is a no-op, stats_meta.version / last_stats_histograms_version
are not advanced.
StatsCache.Update scans mysql.stats_meta by version > lastVersion, so this table
is not selected for refresh. The existing TableInfo.UpdateTS reload guard does
not get a chance to run.
SHOW STATS_HISTOGRAMS then prints col.Info.Name.O from the stale cached
statistics.Table.
```

Likely fix direction:

```text
Advance the stats refresh/invalidation signal for column rename/change even when
no new histogram row is inserted, or resolve SHOW STATS_HISTOGRAMS column names
from live TableInfo by histogram/column ID.
```

## Summary

DDL renames a column successfully, and the live schema is updated, but `SHOW STATS_HISTOGRAMS` continues to display the old column name for the analyzed column until the table is analyzed again.

This is a DDL reference-owner finding in the stats side metadata/cache layer:

```text
DDL changes column identity/name
  -> stats rows remain keyed by column ID
  -> visible stats metadata should resolve to the live column name
  -> SHOW STATS_HISTOGRAMS still uses the stale name from stats cache
```

Environment used for the current pause gate:

```text
failpoint-enabled local TiDB testbed
Store: tikv
SQL: 127.0.0.1:14000
Status/fail API: 127.0.0.1:18080
```

## Minimal Repro

```sql
DROP DATABASE IF EXISTS ai_native_stats_col_min;
CREATE DATABASE ai_native_stats_col_min;
USE ai_native_stats_col_min;

CREATE TABLE t(
  id INT PRIMARY KEY,
  a INT,
  b INT,
  KEY idx_a(a)
);
INSERT INTO t VALUES (1,10,100),(2,20,200),(3,30,300),(4,40,400);
ANALYZE TABLE t;

ALTER TABLE t RENAME COLUMN a TO aa;

SHOW CREATE TABLE t;
SELECT column_name
FROM information_schema.columns
WHERE table_schema='ai_native_stats_col_min'
  AND table_name='t'
ORDER BY ordinal_position;

SELECT SLEEP(5);
SHOW STATS_HISTOGRAMS
WHERE db_name='ai_native_stats_col_min'
  AND table_name='t';
```

Expected:

```text
SHOW STATS_HISTOGRAMS: id, aa, b, idx_a
```

Observed:

```text
SHOW CREATE TABLE: KEY `idx_a` (`aa`)
information_schema.columns: id, aa, b
SHOW STATS_HISTOGRAMS: id, a, b, idx_a
```

`CHANGE COLUMN a aa INT` shows the same stale stats column name.

Running `ANALYZE TABLE t` again refreshes `SHOW STATS_HISTOGRAMS` to display `aa`, so the issue is not that the raw stats row cannot be associated with the live column ID. It is a stale stats-cache/display-name problem after the DDL.

## Pause-Gate Evidence

The matrix probe now covers `RENAME COLUMN` and `CHANGE COLUMN` as separate cells:

```text
python3 /Users/bba/pc/ai_native_ddl_stats_reference_probe.py
SUMMARY total=7 findings=2 skipped=0
```

The two findings are the same root family:

```text
FINDING column_rename_stats_follow_new_name:
  SHOW STATS_HISTOGRAMS still contains column_name=a

FINDING column_change_stats_follow_new_name:
  SHOW STATS_HISTOGRAMS still contains column_name=a
```

Manual minimization produced the same shape:

```text
RENAME live columns: id,aa,b
RENAME SHOW CREATE TABLE: KEY `idx_a` (`aa`)
RENAME SHOW STATS_HISTOGRAMS after 5s: b, id, a, idx_a
RENAME raw mysql.stats_histograms: table_id=1310, hist_id=1/2/3 plus index hist_id=1
RENAME SHOW STATS_HISTOGRAMS after re-ANALYZE: id, aa, b, idx_a

CHANGE live columns: id,aa,b
CHANGE SHOW CREATE TABLE: KEY `idx_a` (`aa`)
CHANGE SHOW STATS_HISTOGRAMS after 5s: id, a, b, idx_a
```

The raw stats rows do not store the column name in `mysql.stats_histograms`; they are keyed by `table_id`, `is_index`, and `hist_id`. The old visible name is introduced when rendering the cached stats object, not because the storage row itself names `a`.

## Scope / Non-Goals

This candidate intentionally does not use immediate stale stats after `DROP INDEX` or `DROP COLUMN` as the oracle. Those paths can involve delayed stats GC, so they are noisier and may be expected to retain rows briefly.

Column rename is a cleaner DDL reference-owner case:

```text
the column still exists
+ the column ID is still meaningful for the stats row
+ live schema exposes only the new name
+ re-analyze repairs the display name
= stale visible reference after DDL, not merely delayed cleanup of a dropped object
```

## Source Anchors

| Anchor | Observation |
|---|---|
| `pkg/executor/show_stats.go:204` | `fetchShowStatsHistogram` walks current infoschema and passes live db/table/partition names, so table rename is green |
| `pkg/executor/show_stats.go:236` | `appendTableForStatsHistograms` prints `col.Info.Name.O` from the cached `statistics.Table` |
| `pkg/statistics/handle/ddl/subscriber.go:114` | `ActionModifyColumn` inserts stats for the modified column if needed |
| `pkg/statistics/handle/ddl/subscriber.go:343` | `insertStats4Col` delegates to `InsertColStats2KV` for DDL column-change stats initialization |
| `pkg/statistics/handle/storage/save.go:487` | `InsertColStats2KV` uses `insert ignore`; existing analyzed column stats are not rewritten |
| `pkg/statistics/handle/storage/save.go:515` | if the insert was a no-op, `stats_meta.version` / `last_stats_histograms_version` are not advanced |
| `pkg/statistics/handle/cache/statscache.go:136` | stats-cache refresh scans `mysql.stats_meta where version > lastVersion` |
| `pkg/statistics/handle/cache/statscache.go:200` | once a row is selected, `TableInfo.UpdateTS` would force reload when schema changed |

Likely root bucket:

```text
column rename/change DDL updates TableInfo
  -> stats storage row remains attached by column ID
  -> DDL stats subscriber tries insertStats4Col
  -> existing analyzed histogram makes insert ignore a no-op
  -> stats_meta version/last_stats_histograms_version is not advanced
  -> periodic stats-cache refresh does not select this table
  -> stats cache/display object still carries old ColumnInfo name
  -> SHOW STATS_HISTOGRAMS renders stale col.Info.Name.O
```

Two possible fix directions need code-owner confirmation:

1. Advance stats meta or otherwise invalidate/reload the affected stats table after column rename/change even when `InsertColStats2KV` does not insert a new histogram row.
2. When rendering `SHOW STATS_HISTOGRAMS`, resolve column names from the live `TableInfo` by column ID instead of trusting the cached stats column name.

The first direction matches the existing cache design: once a table is selected for refresh, `StatsCache.Update` already compares `tableInfo.UpdateTS` with `oldTbl.TblInfoUpdateTS` and would reload stats with the current `TableInfo`. The failure is that the column rename/change path does not appear to make this table visible to that refresh path when all analyzed histograms already exist.

## Method Lesson

This was found by scanning for side metadata owners after the obvious DDL owners were green. The useful pattern is:

```text
side metadata keyed by object ID
+ SHOW/API layer exposes object names
+ DDL event may not advance the version/invalidation signal that refreshes cached display metadata
= visible stale reference after rename
```

The important distinction: drop index/drop column stats cleanup is intentionally delayed by stats GC, so immediate stale raw rows there are noisy. Column rename is cleaner because the live schema has the new name and the same stats ID can be resolved to it, but the visible stats API still exposes the old name.

Pause-gate conclusion:

```text
This candidate is issue-quality enough to discuss with the stats/DDL owner.
Do not expand more stats cells before owner feedback or fix-direction validation.
The method asset has already been extracted: look for DDL-owned side metadata
where storage rows are ID-keyed but the public surface exposes names, and where
the DDL path may fail to advance the cache invalidation/version signal.
```
