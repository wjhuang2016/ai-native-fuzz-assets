# Draft: EXCHANGE PARTITION leaks a stats lock to the exchanged table (id30017)
> 2026-07-03. Confirmed on testbed 8192975 / fp-tidb. Selector S4 (side-state owner key/ID swap), DDL-only lane.

## Minimal Reproduction

```sql
DROP DATABASE IF EXISTS ai_stats_exchange;
CREATE DATABASE ai_stats_exchange;
USE ai_stats_exchange;

CREATE TABLE t(a INT, b VARCHAR(10), INDEX idx_b(b))
PARTITION BY RANGE(a) (
  PARTITION p0 VALUES LESS THAN (10),
  PARTITION p1 VALUES LESS THAN (20)
);
INSERT INTO t VALUES (1,'a'),(11,'b');

CREATE TABLE t1(a INT, b VARCHAR(10), INDEX idx_b(b));
INSERT INTO t1 VALUES (2,'x');

ANALYZE TABLE t;
ANALYZE TABLE t1;

LOCK STATS t;
SHOW STATS_LOCKED WHERE db_name='ai_stats_exchange';
-- ai_stats_exchange  t  global  locked
-- ai_stats_exchange  t  p0      locked
-- ai_stats_exchange  t  p1      locked

ALTER TABLE t EXCHANGE PARTITION p0 WITH TABLE t1;

SHOW STATS_LOCKED WHERE db_name='ai_stats_exchange';
-- ai_stats_exchange  t   global  locked
-- ai_stats_exchange  t1      locked
-- ai_stats_exchange  t   p1  locked
```

The leak is visible after the matching table-level unlock:

```sql
UNLOCK STATS t;

SHOW STATS_LOCKED WHERE db_name='ai_stats_exchange';
-- ai_stats_exchange  t1      locked
```

## User-Visible Symptom

A user explicitly locks statistics for table `t`. TiDB represents that as visible locks on `t/global`, `t/p0`, and `t/p1`. After `ALTER TABLE t EXCHANGE PARTITION p0 WITH TABLE t1`, the old `p0` lock is displayed as a lock on standalone table `t1`.

This is visible and behavioral:

- `SHOW STATS_LOCKED` changes from `t/global,t/p0,t/p1` to `t/global,t1,t/p1`.
- `UNLOCK STATS t`, the natural counterpart of `LOCK STATS t`, removes the current `t` locks but leaves `t1` locked.

## Probe Result

Probe: `/Users/bba/pc/ai_native_ddl_stats_lock_exchange_partition_probe.py`

```text
FINDING  stats_lock_exchange_partition  LOCK STATS t followed by EXCHANGE PARTITION leaves the exchanged table t1 locked after UNLOCK STATS t; after_show=['ai_stats_exchange_xxxxxxxx\tt\tglobal\tlocked', 'ai_stats_exchange_xxxxxxxx\tt1\t\tlocked', 'ai_stats_exchange_xxxxxxxx\tt\tp1\tlocked']; after_unlock_t=['ai_stats_exchange_xxxxxxxx\tt1\t\tlocked']
SUMMARY total=1 findings=1 skipped=0
```

The probe has trigger-evidenced controls before exchange:

- `SHOW STATS_LOCKED` initially reports the table-level lock expansion `t/global,t/p0,t/p1`.
- after exchange, `UNLOCK STATS t` proves the side state can survive outside the object the user locked.

## Source Chain

- `pkg/executor/lockstats/lock_stats_executor.go:137-166`: `LOCK STATS` resolves the current table/partition IDs and stores them in `mysql.stats_table_locked`.
- `pkg/statistics/handle/lockstats/lock_stats.go:37`: the lock row is keyed only by `table_id`.
- `pkg/executor/show_stats.go:144-203`: `SHOW STATS_LOCKED` maps locked IDs back to the current InfoSchema object. After ID swap, the old partition ID maps to `t1`.
- `pkg/ddl/partition.go:2939-2963`: `EXCHANGE PARTITION` swaps `partDef.ID` and `nt.ID`.
- `pkg/ddl/partition.go:3051-3054`: exchange emits a stats DDL event.
- `pkg/statistics/handle/ddl/subscriber.go:442-512`: exchange updates global stats count/modify_count, but does not rewrite `mysql.stats_table_locked` rows to preserve logical lock ownership.
- Existing test `pkg/statistics/handle/handletest/lockstats/lock_partition_stats_test.go:408-436` is named `TestExchangePartitionShouldChangeNothing`, but it only checks the row count in `mysql.stats_table_locked`; it does not check the SQL-visible object mapping or whether a matching `UNLOCK STATS t` removes the side state created by `LOCK STATS t`.

## Root Cause

`LOCK STATS t` records the table ID and every physical partition ID in `mysql.stats_table_locked`. `EXCHANGE PARTITION` then swaps the locked `p0` physical ID with the standalone table ID:

```text
P_check:  stats lock rows exist for t and its physical partitions
Q_claim:  UNLOCK STATS t can clean up the side state created by LOCK STATS t
F_effect: EXCHANGE PARTITION swaps old_p0_id with t1.ID without rewriting stats_table_locked
```

The system later resolves locked IDs through the current InfoSchema, so the row for `old_p0_id` now points to `t1`. `UNLOCK STATS t` resolves the current IDs of `t` and cannot see the old `p0` ID anymore, so the `t1` lock remains.

## Expected Behavior

`LOCK STATS t` followed by `UNLOCK STATS t` should not leave an unrelated standalone table locked. After exchange, expected SQL-visible behavior is one of:

- exchange rewrites the lock rows so the logical table-level lock remains attached to current `t`; or
- exchange deliberately transfers the lock to `t1`, but then the behavior must be documented and tests should assert the object mapping, not only row count.

The current behavior is neither self-cleaning nor explicit: the table-level lock command pair leaves `t1` locked.

## Fix Direction

When handling `ActionExchangeTablePartition`, rewrite or reconcile `mysql.stats_table_locked` rows for the swapped IDs so table-level lock ownership stays coherent:

- if the product wants logical-table semantics, move the `p0` lock from `old_p0_id` to the new `p0` ID;
- if the product wants physical-data semantics, make that explicit and ensure `UNLOCK STATS t` / `UNLOCK STATS t1` behavior is tested for the transferred lock;
- in either case, strengthen the existing row-count test to assert SQL-visible mapping and lock/unlock round-trip behavior.

The existing `TestExchangePartitionShouldChangeNothing` should be strengthened from row-count oracle to object-mapping and lock/unlock behavior oracle.

## Methodology Note

This is a clean S4 hit:

```text
DDL-created side state stores object ID
+ DDL swaps/rekeys that object ID
+ existing test only checks row count
+ strong oracle maps ID back to SQL-visible object and checks lock/unlock round-trip
```

The important improvement was upgrading the oracle. Counting rows in `mysql.stats_table_locked` says "some lock record survived"; it does not prove the lock still belongs to the object the user can later clean up. `SHOW STATS_LOCKED` plus the `LOCK STATS t` / `UNLOCK STATS t` round trip make the owner-key drift visible.
