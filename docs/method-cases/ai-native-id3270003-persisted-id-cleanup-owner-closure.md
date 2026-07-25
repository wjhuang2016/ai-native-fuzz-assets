# id3270003: persisted identity must be joined with delayed cleanup owners

This blind run independently rediscovered the critical BR root tracked by TiDB #68709. Its value is
method validation: the selector reached an already known critical bug without using issue or PR
findings for target selection, and the stronger oracle exposed success followed by delayed total
data loss.

## Starting proof obligation

The source showed:

```text
P: backup source, command hash, and preallocated ID mapping match the checkpoint
Q: completed ranges still belong to a live target that can safely reuse that ID
F: reuse the fixed ID and skip completed physical ranges
```

The missing proof was outside the checkpoint owner. A supported DROP creates another durable owner:
`gc_delete_range`. Its terminal action occurs after BR publication.

## Small matrix

| Persisted checkpoint | Partial target dropped | Allocation | Cleanup intersects live ID | Result |
|---|---:|---|---:|---|
| yes | no | checkpoint ID | no | resume control |
| yes | yes | checkpoint ID | yes | RED: BR success, then total loss |
| no | yes | fresh ID | no | GREEN: complete durable rowset |

The key improvement is the last-but-one column. Comparing only rowsets at BR exit misses the bug.

## Strong oracle

Join four time domains:

1. checkpoint persistence;
2. target retirement and cleanup registration;
3. resume publication;
4. cleanup consumption.

Then verify:

- old and resumed TableID;
- exact cleanup key range;
- BR terminal and skipped-KV counters;
- TiKV `UnsafeDestroyRange` terminal;
- fresh primary and index rowsets after cleanup;
- a fresh-ID control.

The post-cleanup query must avoid reusing an identical cached aggregate shape. A changed expression
and representative point gets provide a cache-resistant durable oracle.

## Generalized selector

`PERSISTED_ID_CLEANUP_OWNER_CLOSURE`:

```text
persisted identity reuse
+ natural target retirement
+ delayed cleanup owner from the retired generation
+ success before that owner runs
+ post-cleanup durable oracle
- explicit cleanup cancellation or generation revalidation
```

Apply it to checkpoint repair, restore/import engines, temporary physical objects, tombstones,
leases, orphan cleanup, and background reconcilers. For each persisted identity, enumerate both
future consumers and older owners that can still act on it.

## Dedup discipline

Issue search happened only after the RED and controls were understood. TiDB #68709 matches the same
trigger, identity reuse, and failure boundary, so `id3270003` is stored as `known-duplicate` and does
not increment the new-root count.
