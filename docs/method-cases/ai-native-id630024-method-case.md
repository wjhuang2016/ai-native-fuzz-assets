# id630024 Method Case: TTL Side State After EXCHANGE PARTITION

## What was found

`ALTER TABLE ... EXCHANGE PARTITION` allows a standalone TTL table to be exchanged with a non-TTL
partition. After the swap, `mysql.tidb_ttl_table_status` and `mysql.tidb_timers` still expose rows
for the old TTL table ID, which now belongs to the non-TTL partition.

Remote bug DB: `found_bug` id630024, confirmed, low severity, DDL stale side state.

## Why this was fast

The selector came directly from S4:

```text
side state stores object ID plus owner semantics
DDL swaps object IDs across owners
cleanup/sync later trusts the old owner mapping
```

The source clue was compact:

```text
checkTableDefCompatible:
  compares columns, index IDs, charset/collation, handles, TiFlash
  does not compare TTLInfo

onExchangeTablePartition:
  swaps partDef.ID and nt.ID
  updates placement/TiFlash-related state
  does not update TTL status/timer rows
```

That made the proof obligation:

```text
P_check:  table definitions are "compatible" for EXCHANGE PARTITION
Q_claim:  all owner-sensitive side metadata remains coherent after the ID swap
D_dim:    TTL ownership is keyed by physical table ID but semantically belongs to current TTL tables
F_effect: EXCHANGE swaps IDs without TTL compatibility check or TTL side-state reconciliation
```

## Matrix

```text
red cell:
  pt: non-TTL partitioned table
  nt: standalone TTL table
  run one TTL job on nt
  EXCHANGE pt.p0 WITH nt
  oracle: old nt ID becomes pt.p0, but TTL status/timer rows still mention old nt ID
  classification: RED, low severity

control already known from source/tests:
  placement/TiFlash-sensitive metadata has explicit compatibility/remap logic
  classification: GREEN owner precedent

post-sync observation:
  timer sync creates a new timer/status row for current nt ID and disables old timer
  classification: RED remains stale observability metadata, not active wrong scheduling
```

## Method Lesson

S4 is high-yield, but its oracle has two tiers:

```text
tier 1: storage-vs-current-owner diff
  proves a stale mapping exists

tier 2: cleanup/round-trip behavior
  proves the stale mapping affects a real user operation
```

id630014 masking-policy exchange reached tier 2: the policy became unmanageable. id630024 reaches
tier 1 plus real TTL-job evidence, but timer sync limits the active impact. So the method should
keep S4 as productive while adding a quality gate: do not over-rank a side-state hit unless it
breaks management DDL, causes wrong data behavior, or survives the owner's self-healing loop in an
active state.

## Improved Selector

Use this sharper S4 version:

```text
Find side metadata keyed by physical ID.
Find a DDL that swaps/moves that ID across logical owners.
Check whether common sibling paths have owner-specific rewrite/cleanup helpers.
If the swap path lacks the helper, run a two-tier oracle:
  1. current owner map vs side table/timer rows
  2. owner management operation or active scheduling/data behavior
Rank severity by tier 2, not by tier 1 alone.
```

## Pause Rule

Do not enumerate all TTL options or all system tables under EXCHANGE PARTITION. Reopen this family
only for:

- a TTL exchange case that causes active wrong scheduling/deletion;
- another side-state owner with a cleanup/round-trip behavior failure; or
- fix validation for TTLInfo compatibility or TTL side-state reconciliation.
