# id30039: EXCHANGE PARTITION leaks saved ANALYZE options to the exchanged table

Status: confirmed on testbed `8192975`; inserted into remote `found_bug` as id30039.

This is a **blast-radius surface** of `root_cause_id=exchange-idswap-orphan`, not a new distinct
root. Remote state after insert: `COUNT(*)=67`, `COUNT(DISTINCT root_cause_id)=45`.

## User-Visible Symptom

`ANALYZE TABLE pt PARTITION p0 COLUMNS a WITH 1 TOPN, 3 BUCKETS` persists an analyze option row
for the physical partition ID. After:

```sql
ALTER TABLE pt EXCHANGE PARTITION p0 WITH TABLE nt WITHOUT VALIDATION;
```

the old `p0` physical ID becomes `nt`'s table ID. The saved `mysql.analyze_options` row follows
that physical ID, so `nt` inherits `column_choice=LIST,column_ids=<a>`.

Then:

```sql
ANALYZE TABLE nt WITH 2 TOPN, 2 BUCKETS;
```

analyzes only column `a` and `PRIMARY`; columns `b` and `c` remain `stats_ver=0`. A no-exchange
standalone control under the same statement analyzes `a`, `b`, `c`, and `PRIMARY`.

## Minimal Repro Shape

```sql
SET GLOBAL tidb_persist_analyze_options = 1;
CREATE DATABASE ai_analyze_ex;
USE ai_analyze_ex;
SET SESSION tidb_analyze_version = 2;
SET SESSION tidb_partition_prune_mode = 'static';
SET SESSION tidb_stats_load_sync_wait = 20000;

CREATE TABLE pt(a INT, b INT, c INT, PRIMARY KEY(a))
PARTITION BY RANGE(a) (
  PARTITION p0 VALUES LESS THAN (1000),
  PARTITION p1 VALUES LESS THAN (2000),
  PARTITION pmax VALUES LESS THAN (MAXVALUE)
);
CREATE TABLE nt(a INT, b INT, c INT, PRIMARY KEY(a));

INSERT INTO pt VALUES
  (1,1,1),(2,2,2),(3,3,3),
  (1001,1001,1001),(1002,1002,1002),(2001,2001,2001);
INSERT INTO nt VALUES (101,101,101),(102,102,102),(103,103,103);

ANALYZE TABLE pt PARTITION p0 COLUMNS a WITH 1 TOPN, 3 BUCKETS;
ALTER TABLE pt EXCHANGE PARTITION p0 WITH TABLE nt WITHOUT VALIDATION;
ANALYZE TABLE nt WITH 2 TOPN, 2 BUCKETS;

SELECT o.*
FROM mysql.analyze_options o
JOIN information_schema.tables t ON t.tidb_table_id=o.table_id
WHERE t.table_schema='ai_analyze_ex' AND t.table_name='nt';

SELECT h.table_id, h.is_index, h.hist_id, h.distinct_count, h.stats_ver
FROM mysql.stats_histograms h
JOIN information_schema.tables t ON t.tidb_table_id=h.table_id
WHERE t.table_schema='ai_analyze_ex' AND t.table_name='nt'
ORDER BY h.is_index,h.hist_id;
```

Observed after the exchange:

```text
nt current table_id = old p0 partition_id
mysql.analyze_options for nt:
  buckets=2, topn=2, column_choice=LIST, column_ids=1

mysql.stats_histograms for nt:
  hist_id=1(a),       stats_ver=2
  hist_id=2(b),       stats_ver=0
  hist_id=3(c),       stats_ver=0
  hist_id=1(PRIMARY), stats_ver=2
```

Control without exchange:

```text
ANALYZE TABLE ctrl WITH 2 TOPN, 2 BUCKETS
mysql.stats_histograms for ctrl:
  a stats_ver=2
  b stats_ver=2
  c stats_ver=2
  PRIMARY stats_ver=2
```

## Source Anchors

- `/Users/bba/pc/tidb/pkg/ddl/partition.go`: `onExchangeTablePartition` swaps
  `partDef.ID, nt.ID = nt.ID, partDef.ID`.
- `/Users/bba/pc/tidb/pkg/statistics/handle/ddl/subscriber.go`: the exchange subscriber updates
  global stats count/modify count, but does not remap or clear `mysql.analyze_options`.
- `/Users/bba/pc/tidb/pkg/executor/analyze.go`: `AnalyzeExec.saveAnalyzeOptions` persists options
  by `opts.PhyTableID`.

## Method Lesson

This is why O21 cannot stop at "the side row survived" or a `SHOW` diff. The side row became a real
behavior bug only after a round trip:

```text
pre-DDL option created through logical owner pt.p0
EXCHANGE swaps physical IDs
post-DDL ANALYZE TABLE nt consumes inherited option
safe standalone control analyzes all columns
```

It should be counted as extra blast-radius reach for S4, not as a new root cause.
