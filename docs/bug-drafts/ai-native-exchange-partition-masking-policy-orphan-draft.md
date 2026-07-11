# id630014 Draft: EXCHANGE PARTITION Can Orphan Masking Policies

Remote `found_bug` row:

```text
id:        630014
status:    issue-filed
severity:  high
title:     EXCHANGE PARTITION can orphan masking policies after table ID swap
issue:     https://github.com/pingcap/tidb/issues/69754
oracle:    O21_SIDE_STATE_OWNER_REMAP_ORACLE
method:    S4_ID_SWAP_OWNER_MAPPING
```

## User-Visible Symptom

A masking policy created on a standalone table can become unreachable after that table is exchanged
with a partition. The policy row is still visible in `mysql.tidb_masking_policy`, but normal table
DDL can no longer disable or drop it by the logical table name. Recreating the same policy creates a
second row on the new table ID, and later management DDL affects only the new row; the old row stays
orphaned.

Minimal repro confirmed on testbed `8192975` / `fp-tidb`:

```sql
DROP DATABASE IF EXISTS ai_exchange_mask_0703;
CREATE DATABASE ai_exchange_mask_0703;
USE ai_exchange_mask_0703;

CREATE TABLE pt(a INT, KEY idx_a(a))
PARTITION BY RANGE(a) (
  PARTITION p0 VALUES LESS THAN (10),
  PARTITION p1 VALUES LESS THAN (100)
);
CREATE TABLE nt(a INT, KEY idx_a(a));
INSERT INTO pt VALUES (1),(20);
INSERT INTO nt VALUES (5);

CREATE MASKING POLICY mp_nt ON nt(a) AS a ENABLE;

SELECT policy_name, db_name, table_name, table_id, column_name, column_id, status
  FROM mysql.tidb_masking_policy
 WHERE db_name = DATABASE();

ALTER TABLE nt DISABLE MASKING POLICY mp_nt;
ALTER TABLE nt ENABLE MASKING POLICY mp_nt;

ALTER TABLE pt EXCHANGE PARTITION p0 WITH TABLE nt;

SELECT table_name, tidb_table_id
  FROM information_schema.tables
 WHERE table_schema = DATABASE()
   AND table_name IN ('pt','nt')
 ORDER BY table_name;

SELECT table_name, partition_name, tidb_partition_id
  FROM information_schema.partitions
 WHERE table_schema = DATABASE()
   AND table_name = 'pt'
 ORDER BY partition_name;

SELECT policy_name, db_name, table_name, table_id, column_name, column_id, status
  FROM mysql.tidb_masking_policy
 WHERE db_name = DATABASE();

ALTER TABLE nt DISABLE MASKING POLICY mp_nt;
ALTER TABLE nt DROP MASKING POLICY mp_nt;
```

Observed on the run:

```text
before EXCHANGE:
  mp_nt  table_name=nt  table_id=15613  column_id=1  status=ENABLED

after EXCHANGE:
  information_schema.tables:     nt tidb_table_id=15610, pt tidb_table_id=15609
  information_schema.partitions:  pt.p0 tidb_partition_id=15613
  masking policy row:             mp_nt table_name=nt table_id=15613 status=ENABLED

ALTER TABLE nt DISABLE MASKING POLICY mp_nt:
  ERROR 1105 (HY000): masking policy mp_nt doesn't exist

ALTER TABLE nt DROP MASKING POLICY mp_nt:
  ERROR 1105 (HY000): masking policy mp_nt doesn't exist

ALTER TABLE pt DISABLE MASKING POLICY mp_nt:
  ERROR 1105 (HY000): masking policy mp_nt doesn't exist
```

After dropping the later valid control policy and recreating `mp_nt` on `nt`, TiDB accepted a second
same-name row on the current table ID. Disabling `mp_nt` then changed only the new row:

```text
policy_id  policy_name  table_name  table_id  status
1          mp_nt        nt          15613     ENABLED
3          mp_nt        nt          15610     DISABLED
```

## Source Anchors

```text
pkg/ddl/executor.go:3119-3136
  checkExchangePartition rejects views/sequences, non-partitioned/partitioned shape mismatches,
  affinity, and standalone-table foreign keys, but it does not inspect masking policies.

pkg/ddl/executor.go:3035-3110
  checkTableDefCompatible compares core table shape, columns, indices, IDs, charset/collation,
  TiFlash, and shard bits, but not masking-policy side metadata.

pkg/ddl/partition.go:2766-3055
  onExchangeTablePartition swaps partDef.ID and nt.ID.

pkg/ddl/table.go:566-568
  truncate has updateMaskingPolicyTableIDAfterTruncate to remap masking-policy table_id.

pkg/ddl/table.go:830-839, 881-889
  rename paths update masking-policy database/table names.

pkg/ddl/masking_policy.go
  masking-policy lookup and validation use mysql.tidb_masking_policy table_id/column_id plus
  db/table/column names.
```

The key contrast is that truncate and rename have owner-specific masking-policy repair helpers,
while `EXCHANGE PARTITION` swaps a table ID with a partition ID without an equivalent remap or block.

## Quality

This is a high-quality DDL side-state bug:

- It is user-visible through ordinary `ALTER TABLE ... DISABLE/DROP MASKING POLICY`.
- The stale row is not merely a display artifact; it cannot be reached through either logical table.
- A safe sibling exists: before the exchange, the same policy can be disabled/enabled normally.
- A repair precedent exists in source: truncate explicitly remaps masking-policy `table_id`.
- It strengthens selector S4 by proving the same ID-swap owner problem on a second side-state
  owner, distinct from stats locks.

## Fix Direction

Either reject `EXCHANGE PARTITION` when the standalone table or exchanged partition has masking
policies, or atomically remap `mysql.tidb_masking_policy` rows for the swapped IDs and invalidate the
masking-policy cache. The regression oracle should create a policy, exchange the table, then verify
`DISABLE`, `ENABLE`, `DROP`, and recreate affect exactly one live logical owner and leave no stale
`table_id` row.
