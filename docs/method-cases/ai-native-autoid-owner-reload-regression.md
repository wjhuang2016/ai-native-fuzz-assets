# Persisted owner breadcrumbs must close every reconstruction path

Status: validated by `id2910003` on current nightly and real TiKV.

## Proof obligation

```text
P: DDL persisted AutoIDSchemaID because the resource remains owned by the original schema identity.
Q: every allocator reconstruction and publication uses that durable owner and its high-water mark.
F: a cold peer rebuilds from the current schema identity and publishes a lower counter.
```

The invariant is:

```text
The durable auto-ID high-water mark must never decrease across rename, peer refresh, or full reload.
```

## Small matrix

| Owner state | Consumer | Result |
| --- | --- | --- |
| warm TiDB that allocated IDs | read after cross-database rename | IDs 1 and 2 preserved |
| cold unmodified TiDB | normal INSERT into PK | duplicate key 2 |
| cold unmodified TiDB | INSERT into nonunique auto-ID column | success with two rows using ID 2 |
| cold unmodified TiDB | REPLACE into PK | success, generated ID 2, old row silently overwritten |
| cold patched TiDB | identical REPLACE | generated ID 3, all old rows preserved |

## Selector

```text
candidate = persisted migration or owner breadcrumb
            intersect resource reconstruction after peer, restart, reload, or failover
            intersect consumer chooses owner by current location or local state
            intersect lower sequence, epoch, lease, offset, or generation can be published
            intersect irreversible write, delete, routing, or success acknowledgement
            minus complete breadcrumb consumption or monotonic owner transfer
```

Use the name `PERSISTED_OWNER_IDENTITY_CONSUMER_CLOSURE`.

## Why this worked

The source had already encoded the proof obligation. `AutoIDSchemaID` exists only because schema
location and allocator ownership can diverge after cross-database rename. That makes every reader
of `TableInfo` a candidate reconstruction boundary.

The high-signal asymmetry was:

1. one writer persists a nonzero owner breadcrumb;
2. several generic constructors accept the surrounding database ID;
3. no constructor reads the breadcrumb;
4. a cold process has no in-memory high-water mark to hide the mismatch.

This is cheaper to test than broad restart fuzzing. The source narrows the matrix to one DDL, three
owner temperatures, and one monotonicity oracle.

## Consumer lifting

Stopping at the first duplicate-key error would classify only availability. The generated identity
was then routed through different SQL consumers:

- PK `INSERT` turns reuse into an explicit error;
- a nonunique ID column turns reuse into silent duplicate identity;
- `REPLACE` interprets the reused identity as ownership of an existing row and silently deletes it.

This lifting step changed the consequence from an obvious failure to successful persistent data
loss without changing the root.

## Loop improvement

When source persists an old owner, generation, epoch, mapping, or migration marker:

1. name the exact semantic fact represented by the field;
2. find every writer and every reader;
3. enumerate incremental refresh, healthy peer, full reload, restart, and recovery constructors;
4. compare warm and cold process-local state;
5. make the durable owner publish a nonzero high-water mark before migration;
6. assert monotonicity on the cold consumer;
7. route a wrong identity through `INSERT`, `REPLACE`, upsert, ignore, cleanup, and routing semantics;
8. select the highest successful irreversible consumer;
9. change only owner resolution for GREEN;
10. perform issue and fix-history lookup after RED to classify new root versus regression.

The three mandatory owner states are:

```text
warm writer -> healthy cold peer -> brand-new full-load process
```

Testing only the DDL owner or one in-process InfoSchema delta is insufficient.

## Reusable oracle

For monotonic resources, join:

1. producer-side last allocated or published value;
2. persisted owner identity;
3. cold-consumer visible next value;
4. generated identity returned by the SQL statement;
5. fresh durable rows grouped by identity;
6. row-preservation count for replacement/upsert consumers.

Structural checks such as `ADMIN CHECK TABLE` are secondary because identity reuse can produce a
physically valid but semantically destructive table.

## Stop rule

Count one root per persisted owner field, missed reconstruction family, and highest irreversible
consumer. Peer versus restart, different schemas, more rows, ID values, and SQL spellings are blast
radius. Reopen only when another constructor ignores a different durable owner fact or another
consumer reaches a higher consequence.
