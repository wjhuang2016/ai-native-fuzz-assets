# id2970003: allocator type changes must transfer the semantic owner

Status: validated by current-master RED, default-cache GREEN, and exact owner-only
counterfactual GREEN.

## Proof obligation

```text
P: AUTO_INCREMENT state is valid for conversion to AUTO_RANDOM.
Q: the conversion transfers the old allocator owner's durable high-water mark.
F: validation reads IncrementID, while application rebases from unrelated RowID.
```

The invariant is:

```text
Changing allocator representation must preserve identity ownership and monotonic high-water state.
```

## Small matrix

| Old allocator | Migration owner | Consumer | Result |
| --- | --- | --- | --- |
| default shared RowID | unmodified current master | generated REPLACE | all old rows preserved |
| separated IncrementID (`AUTO_ID_CACHE=1`) | unmodified nightly | generated REPLACE | old rows overwritten |
| separated IncrementID (`AUTO_ID_CACHE=1`) | unmodified current master | generated REPLACE | old rows overwritten |
| separated IncrementID (`AUTO_ID_CACHE=1`) | patched current master, verified DDL owner | generated REPLACE | all old rows preserved |

## Selector

```text
candidate = resource changes representation, type, namespace, or owner
            intersect validation reads old state through one accessor
            intersect application, cleanup, or recovery reads another accessor
            intersect the new owner can publish lower or aliased identity
            intersect successful irreversible consumer
            minus one canonical transfer primitive with post-transfer monotonicity check
```

Use the name `ALLOCATOR_TYPE_MIGRATION_OWNER_TRANSFER`.

## Why this worked

The source already contained a localized contradiction:

1. the check branch knew that `AUTO_ID_CACHE=1` moves `AUTO_INCREMENT` to `IncrementID`;
2. the apply branch always used `RowID`;
3. both branches described the same conversion and were only a few lines apart;
4. the new allocator accepted the unrelated zero as a valid base.

This is stronger than searching for unchecked errors. It compares every phase against one named
semantic owner and asks whether validation, mutation, cleanup, recovery, and publication all use
the same representation.

## Consumer lifting

A duplicate-key `INSERT` demonstrates backward identity movement but mostly affects availability.
Routing the same bad generated identity through `REPLACE` changes the consequence:

```text
lower generated identity
  -> existing primary-key match
  -> REPLACE treats the match as the row to remove
  -> SQL reports success
  -> fresh read proves preexisting payload loss
```

`ADMIN CHECK TABLE` is intentionally secondary. The table is structurally consistent after the
wrong row has been replaced.

## Strong oracle

Join:

1. pre-migration IDs and payload fingerprints;
2. old allocator mode and durable high-water mark;
3. generated IDs after migration;
4. `ROW_COUNT()` for each `REPLACE`;
5. fresh post-migration original-payload count;
6. matched default-representation control;
7. exact owner-only counterfactual;
8. actual DDL owner address and binary revision.

RED requires generated-ID overlap and loss of a pre-migration payload. A duplicate error alone is
not the final oracle.

## Loop improvement

For every representation or ownership migration:

1. name the semantic resource and its current owner;
2. list accessors used by validation, application, cleanup, recovery, and rollback;
3. compare those accessors mechanically;
4. seed a nonzero, recognizable pre-migration state;
5. use a tiny matrix over old representation, new representation, and owner;
6. route aliasing through a successful destructive consumer;
7. validate durable preimages from a fresh session;
8. verify the process that owns the tested control path;
9. change only owner/accessor selection for GREEN;
10. deduplicate by owner-transfer root, not by SQL spelling.

This selector applies beyond transactions and DDL to backup/restore metadata, import checkpoints,
sequence services, statistics versions, placement epochs, cache generations, and background-job
ownership.

## Stop rule

Count one root per semantic resource, mismatched owner-transfer primitive, and highest irreversible
consumer. Cache size, shard bit count, row count, and `INSERT` versus `REPLACE` variants are blast
radius.
