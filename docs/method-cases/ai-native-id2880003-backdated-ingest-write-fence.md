# id2880003: backdated physical ingest needs a live-target write fence

Status: confirmed high severity / critical data-integrity consequence on official nightly BR and
real TiKV.

## Proof obligation

```text
P: BR created a valid empty target and froze physical rewrite rules.
Q: the target remains exclusively owned and logically unchanged until physical ingest completes.
F: the table is published as Normal, so an application writes the same row/index owner first.
```

The invariant is:

```text
Historical physical writes must not overlap unfenced logical writes to the same record/index owner.
```

## Small matrix

| Restore schedule | Concurrent mutation | Oracle | Verdict |
| --- | --- | --- | --- |
| Official BR, rate limited | none | primary/index counts equal; ADMIN CHECK passes | GREEN |
| Official BR, rate limited | same PK, different unique key | stale lookup, count split, 8223 | RED |
| BR paused after create | TRUNCATE target generation | success writes retired ID | RED diagnostic |

The DML row is the strongest result. It needs no DDL, process fault, product failpoint, or source
modification.

## Strong oracle

Join four independent views:

1. BR terminal state and restored KV count;
2. primary-record count and checksum-like aggregate;
3. forced unique-index count and aggregate;
4. predicate self-check plus `ADMIN CHECK TABLE`.

For the witness row:

```text
lookup key:       u=100001
returned record:  u=900000000
self predicate:   false
```

This proves a persistent wrong index entry, not a reporting or statistics defect.

## Selector

```text
candidate = physical restore/import/repair applies historical or backdated KV
            intersect target becomes live before the physical write finishes
            intersect logical DML can mutate the same record/index owner
            intersect physical writer bypasses SQL index-maintenance ownership
            intersect success acknowledgement
            minus table mode, write lease, write epoch, or fail-closed revalidation
```

Use the name `BACKDATED_PHYSICAL_INGEST_WITHOUT_WRITE_FENCE`.

## Why this worked

The prior generation-retirement result (`id2850003`) showed that async bulk work trusted a target
too long. Moving laterally to BR exposed a similar temporal gap, but replacement was not the highest
consumer. The more dangerous mutation kept the same table ID and changed one row through normal
SQL.

The source supplied the missing proof:

- ordinary snapshot restore leaves the table in `Normal`;
- the physical writer preserves older backup timestamps;
- logical writes can therefore win at the record while an unrelated old index key survives.

The compact matrix changed only one application write and used the row/index bijection as a hard
oracle.

## Loop improvement

After finding a stale resource or generation gap:

1. test replacement/retirement to localize identity ownership;
2. test same-generation live mutation before the delayed physical consumer;
3. ask whether the consumer writes current state or historical state;
4. split the oracle by physical owners such as record and every index;
5. force each access path and project the predicate back onto returned rows;
6. require a matched no-mutation control before severity admission.

This branch is useful for BR, Lightning, IMPORT INTO, snapshot apply, repair, index rebuild, log
restore, and bulk backfill.

## Stop rule

Count one root per missing target fence / timestamp domain / physical consumer tuple. More keys,
indexes, data sizes, restore speeds, or DML verbs are blast radius. Reopen for another writer only
when it has a different ownership fence or can damage a different durable invariant.
