# TRUNCATE affinity cancellation and severity calibration

## Source proof

Current nonpartition `TRUNCATE TABLE` changes two owners. It stages a new `TableID` in the DDL
metadata transaction, then creates the new affinity group and deletes the old group in PD before
schema-version publication.

```text
P: PD has accepted the old-to-new group transition.
Q: no later abort can leave the old metadata committed.
F: ADMIN CANCEL rolls back the local TableID but has no affinity compensation edge.
```

The first three experiments used an existing failpoint that looked like a pre-schema-version fault.
Source ordering showed that it actually fired before affinity creation and deletion. Those runs are
INVALID: retry logs and a stable job ID do not prove the target effect occurred.

The corrected injection was placed immediately after both affinity mutations and before
`updateSchemaVersion`. With that schedule, `ADMIN CANCEL` completed, InfoSchema retained the old
table ID and `AFFINITY='table'`, and the old PD group was absent. Rebuilding groups from the current
committed `TableInfo` restored coherence. The existing normal TRUNCATE test remained GREEN.

## Why it is not a severe finding

The owner split is real, but impact must be classified from the product promise rather than from the
importance of the code path. Table affinity is documented as an experimental PD scheduling feature
that colocates a table's Regions on a small TiKV subset to reduce cross-node query latency. Losing
the group silently disables that optimization; it does not weaken replica count, corrupt rows, or
make a required query path unavailable.

Therefore this is retained as a moderate bug sample and retired from the severe-bug queue. It is not
lifted to the testbed and is not filed as a high-severity issue.

## LOOP improvement

Add a user-promise calibration before fault injection:

1. trace the suspected state split to its highest actual consumer;
2. read the current official contract for that consumer;
3. name the direct user consequence without relying on words such as policy, placement, or affinity;
4. admit `C3_DIRECT` only when the consumer can lose data, violate an invariant, leak state, or cause
   a concrete availability failure;
5. admit `C2_WITH_LIFT` only when an executable downstream oracle could prove such an escalation;
6. otherwise record the source obligation as a lower-severity asset before expensive injection.

This gate complements S35. The same external-effect ordering root can be high for replica placement
or TiFlash availability and moderate for an experimental latency optimization. Selector strength
does not determine consequence strength.
