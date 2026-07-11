# Method Case id30017: ID-swap side-state selector
> 2026-07-03. Stats lock + `EXCHANGE PARTITION`. This note records the methodology result, not the bug details.

## What Was Being Tested

After ordinary DDL owner matrices went green, the next rule was:

```text
do not widen happy paths;
find an entrypoint that changes ownership identity differently from common paths.
```

`EXCHANGE PARTITION` is a good S4 candidate because it swaps a standalone table ID with a partition ID. Any side state keyed only by physical ID must decide whether it follows the physical data or the logical SQL object. The oracle must therefore test not only where the row is displayed, but whether the user's lock/unlock workflow still has a coherent owner.

## Why This Target Was Picked

The initial FK idea was rejected before execution because TiDB blocks FKs on partitioned tables:

```text
ERROR 1506: Foreign key clause is not yet supported in conjunction with partitioning
```

The next candidate was table lock, but `LOCK TABLES ...; ALTER TABLE ... EXCHANGE PARTITION` is blocked before the ID swap.

Stats lock passed the selector:

- it is created by SQL (`LOCK STATS`);
- it is stored outside `TableInfo` in `mysql.stats_table_locked`;
- the storage key is `table_id`;
- `EXCHANGE PARTITION` swaps table/partition IDs;
- existing coverage checked row count only, not SQL-visible ownership or lock/unlock symmetry.

## Tiny Matrix

One red cell with built-in controls was enough:

1. Before exchange, `LOCK STATS t` shows `t/global,t/p0,t/p1`.
2. Exchange `p0` with `t1`.
3. After exchange, `SHOW STATS_LOCKED` shows `t/global,t1,t/p1`.
4. After exchange, `UNLOCK STATS t` leaves `t1` locked.

## Why It Worked

The proof obligation was:

```text
P_check:
  stats-lock rows exist for t and its physical partitions

Q_claim:
  the side state created by LOCK STATS t can be cleaned up by UNLOCK STATS t

F_effect:
  EXCHANGE PARTITION swaps physical IDs and later display/analyze resolve the ID through current InfoSchema
```

The existing test had the right target but the wrong oracle: it asserted the lock-row count stayed 2. That only proves rows survived. It does not prove the rows still map to a coherent SQL owner, nor that the matching unlock command removes them.

## Quality

Medium to high. It is not data corruption, but it is a clear SQL-visible control-plane bug:

- a lock created by `LOCK STATS t` is displayed as a lock on `t1` after `EXCHANGE PARTITION`;
- the natural counterpart `UNLOCK STATS t` removes current `t` locks but leaves `t1` locked;
- this is visible through `SHOW STATS_LOCKED` without relying on internal tables or timing.

The bug is also methodologically valuable because it validates the refined selector after broad matrices were saturated.

## Methodology Improvement

For side-state DDL owners, never accept a count oracle when the claim is ownership. Prefer:

```text
side table count  -> weak calibration only
SHOW/current object mapping -> ownership oracle
command round trip / cleanup behavior -> strong oracle
```

This turns "the row survived" into "the user can still understand and clean up the state through SQL."
