# Transaction corruption checkpoint: owner representations before SQL syntax

## Goal

This pass looked only for successful transactions that leave persistently wrong row, index, or
MVCC state under default or common production settings. It did not count terminal errors, panic,
temporary locks, or cleanup warnings as corruption.

The key change was to enumerate physical owner representations instead of SQL spellings:

```text
logical value owner
  -> integer or common row handle
  -> unique, prefix, collation, generated, expression, or multi-valued index owner
  -> retry attempt or flush generation
  -> primary/secondary transaction status
```

Each cell used a serial or standard-DML baseline, exact fresh-session rows, forced index reads, and
`ADMIN CHECK TABLE`. Retry cells also proved that a real TiKV conflict caused one transparent retry.

## Default-path result

TiDB master `94b834d94b60`, client-go master `3d4b3eae4652`, and real nightly TiKV were GREEN for:

- six dual-unique owner-retarget cases and four old-image retry consumers;
- seven old-image representations: virtual/stored generated columns, expression index,
  multi-valued index, common handle, prefix unique key, and case-insensitive unique key;
- a multi-row self-join retry, common-handle/MVI migration, and partial-statement rollback;
- sixteen failed-statement removability cells across optimistic and pessimistic transactions,
  including late unique conflict, REPLACE after reinsert, and ODKU owner retargeting.

These 36 real-TiKV cells returned the same terminal result and durable state as their control. Two
additional deterministic TiDB-layer cells used natural RC retries to prove duplicate-owner
rebinding for ON DUPLICATE KEY UPDATE and REPLACE. Together they close the current row/index
owner-rebinding family; more UPDATE/REPLACE syntax is blast radius unless source analysis identifies
a new owner representation or consumer.

## Common opt-in result

Eight legal BULK/pipelined DML cells matched STANDARD DML for dual unique keys, clustered composite
handles, nullable unique keys, multi-valued indexes, stored generated columns, and prefix unique
keys. A separate real-TiKV row/index replacement crossed three Regions and recovered to one coherent
new owner. No success-plus-corruption state was observed.

## Rejected source candidates

### Cross-generation CheckNotExists overtakes Put

TiKV's per-key generation rule initially suggested a dangerous schedule: a proof-only newer
`CheckNotExists` could arrive before an older Put and leave the old value to commit. The low-level
mutation pair is real, but the product schedule is not. `PipelinedMemDB.Flush` waits for the current
flush function to finish before starting the next generation, and the implementation explicitly
guarantees that flush functions are not concurrent. The sequential form fails closed with
`AlreadyExist`; it is not persistent corruption.

### Ignored MemDB iterator error truncates mutations

`initKeysAndMutations` ignores the loop-carried `Next()` error, which looks like silent tail loss.
The concrete ART and RBT iterators return nil throughout every valid iteration. ART invalidation by a
concurrent writer panics, while commit construction has no such writer. Without a production error
producer, the code smell is not an admitted corruption candidate.

## LOOP improvement

Before building a fault matrix, require three proofs in this order:

1. **Owner difference:** the cell changes a physical owner or generation, not merely SQL syntax.
2. **Scheduler reachability:** the product can realize the proposed order without an illegal API
   state or a test-only concurrency model.
3. **Terminal-state consequence:** after all normal recovery owners run, the client reports success
   and a fresh durable oracle remains wrong.

Classify a RED before promotion:

- `INVALID(trigger)` when the expected statement error occurs only at commit or not at all;
- `GUARDED(scheduler)` when product ordering forbids the dangerous interleaving;
- `FAIL_CLOSED` when the operation returns an error and no wrong durable state survives;
- `RED(corruption)` only for success plus stable row/index/MVCC divergence.

This ordering saved the expensive real-TiKV phase for executable candidates and prevented warning
logs, unsupported low-level states, and over-strong oracles from inflating the bug count.
