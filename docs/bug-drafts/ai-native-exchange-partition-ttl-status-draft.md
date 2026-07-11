# id630024 Draft: EXCHANGE PARTITION Leaves Stale TTL Status

Remote `found_bug` row:

```text
id:        630024
status:    confirmed
severity:  low
title:     EXCHANGE PARTITION leaves stale TTL status after swapping a TTL table ID
oracle:    O21_SIDE_STATE_OWNER_REMAP_ORACLE
method:    S4_ID_SWAP_OWNER_MAPPING
```

## User-Visible Symptom

`ALTER TABLE ... EXCHANGE PARTITION` allows a standalone TTL table to be exchanged with a
non-TTL partitioned table. After the swap, TTL status and timer rows for the old standalone table ID
remain visible even though that ID now belongs to a partition of the non-TTL table.

The active timer syncer creates a new timer/status row for the TTL table's current ID, so the
observed impact is stale and misleading TTL observability metadata rather than an immediate wrong
delete. That is why this is recorded as low severity.

## Minimal Repro

Confirmed on testbed `8192975` / `fp-tidb` on 2026-07-03.

```sql
DROP DATABASE IF EXISTS ai_ttl_exchange;
CREATE DATABASE ai_ttl_exchange;
USE ai_ttl_exchange;

CREATE TABLE pt (id INT PRIMARY KEY, t DATETIME)
PARTITION BY RANGE(id) (
  PARTITION p0 VALUES LESS THAN (100),
  PARTITION p1 VALUES LESS THAN MAXVALUE
);

CREATE TABLE nt (id INT PRIMARY KEY, t DATETIME)
TTL = `t` + INTERVAL 1 DAY TTL_ENABLE='ON' TTL_JOB_INTERVAL='1h';

INSERT INTO pt VALUES (1, NOW()), (120, NOW());
INSERT INTO nt VALUES (10, NOW() - INTERVAL 3 DAY), (20, NOW());

-- Wait for TTL to run, or use the status API when manual trigger is allowed.
SELECT table_name, tidb_table_id
  FROM information_schema.tables
 WHERE table_schema = DATABASE()
   AND table_name IN ('pt','nt')
 ORDER BY table_name;

SELECT partition_name, tidb_partition_id
  FROM information_schema.partitions
 WHERE table_schema = DATABASE()
   AND table_name = 'pt'
 ORDER BY partition_name;

SELECT table_id, parent_table_id, last_job_id, last_job_start_time, last_job_finish_time
  FROM mysql.tidb_ttl_table_status
 WHERE table_id IN (
       SELECT tidb_table_id
         FROM information_schema.tables
        WHERE table_schema = DATABASE() AND table_name = 'nt'
     );

ALTER TABLE pt EXCHANGE PARTITION p0 WITH TABLE nt WITHOUT VALIDATION;

SELECT table_name, tidb_table_id
  FROM information_schema.tables
 WHERE table_schema = DATABASE()
   AND table_name IN ('pt','nt')
 ORDER BY table_name;

SELECT partition_name, tidb_partition_id
  FROM information_schema.partitions
 WHERE table_schema = DATABASE()
   AND table_name = 'pt'
 ORDER BY partition_name;

SELECT table_id, parent_table_id, last_job_id, last_job_start_time, last_job_finish_time
  FROM mysql.tidb_ttl_table_status
 WHERE table_id IN (
       SELECT tidb_table_id
         FROM information_schema.tables
        WHERE table_schema = DATABASE() AND table_name = 'nt'
       UNION
       SELECT tidb_partition_id
         FROM information_schema.partitions
        WHERE table_schema = DATABASE() AND table_name = 'pt' AND partition_name = 'p0'
     )
 ORDER BY table_id;

SELECT timer_key, CAST(timer_data AS CHAR) AS timer_data, enable, event_status, watermark,
       CAST(summary_data AS CHAR) AS summary_data
  FROM mysql.tidb_timers
 WHERE timer_key LIKE '/tidb/ttl/physical_table/%'
 ORDER BY timer_key;
```

Observed run:

```text
before EXCHANGE:
  nt table_id = 16104
  pt.p0 partition_id = 16101
  mysql.tidb_ttl_table_status has table_id=16104,parent_table_id=16104
  TTL deleted the expired row from nt

after EXCHANGE:
  nt table_id = 16101
  pt.p0 partition_id = 16104
  mysql.tidb_ttl_table_status still has the old row:
    table_id=16104,parent_table_id=16104,last_job_id=ab399fe9...

after timer sync:
  mysql.tidb_ttl_table_status contains both:
    table_id=16101,parent_table_id=16101,last_job_id=9bb49650...
    table_id=16104,parent_table_id=16104,last_job_id=ab399fe9...

  mysql.tidb_timers contains:
    /tidb/ttl/physical_table/16101/16101 enable=1
    /tidb/ttl/physical_table/16104/16104 enable=0 with old TTL summary
```

## Source Anchors

```text
pkg/ddl/executor.go:3119-3140
  checkExchangePartition rejects views/sequences, wrong partition shape, affinity, and
  standalone-table foreign keys, but it does not inspect TTLInfo.

pkg/ddl/executor.go:3035-3118
  checkTableDefCompatible compares table shape, columns, indexes, IDs, charset/collation,
  handles, shard bits, and TiFlash, but not TableInfo.TTLInfo.

pkg/ddl/partition.go:2765-3051
  onExchangeTablePartition swaps partDef.ID and nt.ID and updates placement/TiFlash-related
  metadata, but it does not reconcile TTL table status or timer records.

pkg/meta/metadef/system_tables_def.go
  mysql.tidb_ttl_table_status and mysql.tidb_ttl_task are keyed by table_id; job_history stores
  table_id and parent_table_id plus schema/table names.

pkg/ttl/ttlworker/timer_sync.go
  TTL timer/status lookup uses the current TTL table physical ID. After EXCHANGE, sync creates a
  new timer for the new ID and disables the old timer, but the old table_status row remains.
```

## Quality

This is a confirmed S4 hit, but not a high-quality data correctness bug:

- Strong evidence: SQL-visible system tables show a stale ID/name mapping after an ID-swap DDL.
- Strong evidence: the red cell is not static only; a real TTL job ran and wrote status/history.
- Weakness: the timer syncer self-heals active scheduling by creating a current-ID timer and
  disabling the old timer.
- Weakness: no immediate wrong delete, stale lock, or unmanageable policy action was observed.

The main value is methodological: S4 predicts another vulnerable owner, but side-state red cells
must be ranked by a cleanup/round-trip behavior oracle, not just by a surviving row in a system
table.

## Fix Direction

Pick one explicit ownership contract:

1. Reject `EXCHANGE PARTITION` when TTL definitions differ between the partitioned table and the
   standalone table.
2. Or reconcile TTL side metadata during `ActionExchangeTablePartition`: delete/remap
   `mysql.tidb_ttl_table_status`, `mysql.tidb_ttl_task`, `mysql.tidb_ttl_job_history` policy as
   appropriate, and update/disable/recreate timer rows for both swapped physical IDs.

A regression oracle should run a TTL job before the exchange, perform the swap, wait for timer
sync, and assert that visible TTL status/timer rows map only to current TTL physical IDs.
