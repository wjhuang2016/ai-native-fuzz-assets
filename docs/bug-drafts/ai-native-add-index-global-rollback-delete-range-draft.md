# id30009: ADD GLOBAL INDEX rollback registers delete ranges on partition IDs

## Status

- Status: confirmed
- Severity: medium
- Area: DDL / ADD INDEX / global index / rollback cleanup
- Verified on testbed `8192975`, namespace `testbed-tps-8192975-1-14`
- TiDB: `8.0.11-TiDB-v8.4.0-this-is-a-placeholder`

## Symptom

On a partitioned table, a failed or cancelled `ADD INDEX ... GLOBAL` rolls back the DDL, but the generated `mysql.gc_delete_range` rows target partition physical IDs instead of the table ID.

Global index keys use the table ID prefix. Therefore rollback cleanup misses global-index key ranges produced by the failed add-index job. This is observable as orphan index KV under the table ID after the table schema has no such index.

## Minimal Repro

```sql
DROP DATABASE IF EXISTS ai_addidx_global_rollback_dr_min;
CREATE DATABASE ai_addidx_global_rollback_dr_min;
USE ai_addidx_global_rollback_dr_min;

SET GLOBAL tidb_ddl_enable_fast_reorg = ON;
SET GLOBAL tidb_enable_dist_task = OFF;
SET GLOBAL tidb_ddl_reorg_batch_size = 32;

CREATE TABLE t(
  id INT,
  p INT NOT NULL,
  c INT NOT NULL
)
PARTITION BY RANGE(p) (
  PARTITION p0 VALUES LESS THAN (10),
  PARTITION p1 VALUES LESS THAN (20),
  PARTITION pmax VALUES LESS THAN MAXVALUE
);

INSERT INTO t VALUES (1,5,100),(2,15,100),(3,25,200);

SELECT TIDB_TABLE_ID
FROM information_schema.tables
WHERE table_schema = DATABASE() AND table_name = 't';

SELECT PARTITION_NAME, TIDB_PARTITION_ID
FROM information_schema.partitions
WHERE table_schema = DATABASE() AND table_name = 't'
ORDER BY PARTITION_ORDINAL_POSITION;

ALTER TABLE t ADD UNIQUE INDEX gu(c) GLOBAL;
-- expected duplicate error

ADMIN SHOW DDL JOBS 10;

SELECT job_id, element_id, start_key, end_key
FROM mysql.gc_delete_range
WHERE job_id = <add-index-job-id>
ORDER BY element_id;
```

## Observed Evidence

For the tiny repro on testbed:

- table ID: `13171`
- partition IDs: `13172`, `13173`, `13174`
- failed add-index job: `13176`, state `rollback done`

Decoded `gc_delete_range.start_key` table IDs:

```text
table_id  index_raw             key
13172     1                     7480000000000033745f698000000000000001
13172     18446462598732840961  7480000000000033745f69ffff000000000001
13173     1                     7480000000000033755f698000000000000001
13173     18446462598732840961  7480000000000033755f69ffff000000000001
13174     1                     7480000000000033765f698000000000000001
13174     18446462598732840961  7480000000000033765f69ffff000000000001
```

There is no delete range for table ID `13171`.

## Raw KV Confirmation

To avoid relying only on metadata shape, a second repro used a failpoint to let a non-unique global index finish writing KV and then cancel the job before it became public:

```sql
DROP DATABASE IF EXISTS ai_prove_30009;
CREATE DATABASE ai_prove_30009;
USE ai_prove_30009;

CREATE TABLE t(id INT, p INT NOT NULL, c INT NOT NULL)
PARTITION BY RANGE(p) (
  PARTITION p0 VALUES LESS THAN (10),
  PARTITION p1 VALUES LESS THAN (20),
  PARTITION pmax VALUES LESS THAN MAXVALUE
);

INSERT INTO t VALUES
  (1,5,100),(2,15,101),(3,25,102),
  (4,6,103),(5,16,104),(6,26,105);

ALTER TABLE t ADD INDEX g(c) GLOBAL;
-- held at create-index-stuck-before-public, then:
ADMIN CANCEL DDL JOBS 13195;
```

Observed IDs:

```text
table ID      13190
partition IDs 13191, 13192, 13193
job ID        13195
job state     rollback done
```

After rollback, `SHOW CREATE TABLE ai_prove_30009.t` shows no index `g`, but raw TiKV scan still finds six global index entries under table ID `13190`, origin index ID `1`:

```text
table13190 origin indexID=1: 6 logical keys
table13190 temp indexID=1:   0 keys
```

The same scan over the six ranges actually registered in `mysql.gc_delete_range` is empty:

```text
pid13191 origin/temp: 0 keys
pid13192 origin/temp: 0 keys
pid13193 origin/temp: 0 keys
```

So the cleanup tasks are not merely encoded oddly; they point at empty partition-ID ranges while the orphan global-index KV remains in the table-ID range.

## Green Controls

### Dropping a successfully created global index uses table ID

On the same kind of partitioned table:

- table ID: `13159`
- partition IDs: `13160`, `13161`, `13162`
- `ALTER TABLE tg ADD INDEX g(c) GLOBAL;`
- `ALTER TABLE tg DROP INDEX g;`

Decoded delete range:

```text
table_id  index_raw  key
13159     1          7480000000000033675f698000000000000001
```

This confirms the expected prefix for global-index cleanup is the table ID.

### Local unique rollback uses partition IDs

For a local unique index that includes the partition key:

```sql
ALTER TABLE tlocal ADD UNIQUE INDEX lu(p,c);
```

with duplicates, rollback delete ranges use the partition IDs. That is correct for local index cleanup.

## Extra Rebuild Evidence

After fixing the duplicate rows in the failed global-index table:

```sql
UPDATE tr SET c = 101 WHERE id = 2;
ALTER TABLE tr ADD UNIQUE INDEX gu(c) GLOBAL;
ADMIN CHECK TABLE tr;
ALTER TABLE tr DROP INDEX gu;
```

The later successful drop used table ID `13171` and index ID `2`:

```text
table_id  index_raw  key
13171     2          7480000000000033735f698000000000000002
```

So the failed rollback's tableID/indexID=1 range was not reused by the later successful index.

## Expected

Rollback of `ADD INDEX ... GLOBAL` on a partitioned table should enqueue delete ranges with the table ID prefix for both:

- the origin global index ID
- the temp global index ID

## Actual

Rollback enqueues delete ranges with each partition physical ID for both index IDs, as if the failed index were local.

## Likely Root Cause

`convertAddIdxJob2RollbackJob` records partition IDs and index names when converting add-index to rollback:

- `/Users/bba/pc/tidb/pkg/ddl/rollingback.go`

When rollback reaches the final `StateDeleteReorganization -> StateNone` transition, it rebuilds finished add-index args:

- `/Users/bba/pc/tidb/pkg/ddl/index.go`

The rebuilt `IndexArg` contains `IndexID` and `IfExist`, but does not preserve whether the index was global. Later, delete-range generation decides cleanup prefix by `indexArg.IsGlobal`:

- `/Users/bba/pc/tidb/pkg/ddl/delete_range.go`

Because `IsGlobal` is false/missing, `ActionAddIndex` rollback cleanup loops over `PartitionIDs` and enqueues partition-ID ranges.

## Fix Direction

Preserve `IsGlobal` when rollback finished args are rebuilt, or re-derive it from the original `IndexInfo` before filling `ModifyIndexArgs`.

Validation should include:

- partitioned table + failed `ADD UNIQUE INDEX ... GLOBAL`
- partitioned table + cancelled `ADD INDEX ... GLOBAL` after KV has been written
- successful `DROP INDEX` global control
- local unique rollback control
- decoded delete-range prefixes proving global cleanup uses table ID and local cleanup uses partition IDs
- raw TiKV scan proving orphan table-ID global index KV remains after rollback
