# id3570003: a revertible parent must own every irreversible subjob effect

## Starting proof obligation

```text
P: every subjob is still at its last revertible point.
Q: restoring saved TableInfo and subjob state can roll back the whole composite DDL.
F: AUTO_RANDOM preparation has already migrated allocator ownership and deleted RowID state.
```

The source contained a broad TODO about allocator rebase rollback. The useful step was to combine
that rollback boundary with a type migration whose application has a destructive owner transfer,
then choose a natural later sibling failure.

## Small matrix

| Conversion | Later unique-index failure | Consumer | Result |
| --- | --- | --- | --- |
| no | yes | cold TiDB generated insert | GREEN, ID `30001` |
| yes, succeeds alone | no | cold TiDB generated insert | GREEN, sharded ID |
| yes | yes | warm TiDB | misleadingly usable from cached allocator |
| yes | yes | cold TiDB `INSERT` | RED, duplicate primary key `1` |
| yes | yes | cold TiDB `REPLACE` | RED, old row `2` overwritten |
| conversion rejected before apply | yes | cold TiDB insert | GREEN, ID `30001` |

The warm/cold split is part of the oracle. Testing only the DDL owner would have missed the durable
corruption because its in-memory allocator still held the old high-water range.

## Strong oracle

1. Record the pre-DDL schema, all allocator types, and their high-water marks.
2. Require the composite DDL to return an error and inspect its history terminal.
3. Compare post-error schema flags with every persisted allocator owner.
4. Start a TiDB that has never built an allocator for the table.
5. Run a generated `INSERT` to expose identity reuse without modifying data.
6. Lift the same allocation to `REPLACE` and record `LAST_INSERT_ID` plus `ROW_COUNT`.
7. Fresh-read the exact preimage payload from another TiDB.
8. Keep `ADMIN CHECK TABLE` only as a structural secondary oracle.

## Selector

Add `IRREVERSIBLE_SUBJOB_ROLLBACK_CLOSURE`:

1. For a composite operation, list every subjob's last revertible point.
2. Before trusting the parent's `Revertible` flag, enumerate side effects already performed by each
   child.
3. Tag each effect with its transaction owner and rollback compensator.
4. Prefer a child that migrates, deletes, publishes, or externalizes identity state.
5. Put a natural deterministic failure in a later sibling.
6. Compare the parent rollback snapshot with the side-effect owners.
7. Reconstruct the highest consumer from a cold process, cache, or metadata reload.
8. Prove a minimal guard or delayed-publication counterfactual.

High-value shapes:

```text
prepare child A
  -> migrate owner / delete old owner
  -> mark child A non-revertible
prepare child B
  -> deterministic error
restore parent metadata snapshot
```

```text
parent says rollback done
warm consumer uses cached state
cold consumer reconstructs from mismatched durable owners
```

## Why it worked

The earlier natural test of `AUTO_INCREMENT=100` plus a failing sibling did not produce a strong
terminal consequence. The improved selector reused the existing allocator-migration asset and
asked which preparation step destroys an old owner before the parent commits. `AUTO_RANDOM`
conversion supplied exactly that effect.

The later unique-index failure compressed the schedule without failpoints. The cold consumer then
turned an internal rollback mismatch into duplicate identity, and `REPLACE` raised it to successful
data loss.

## Asset reuse

This bug was found by joining two existing assets:

- multi-schema rollback's explicit source TODO and saved-TableInfo restoration;
- `ALLOCATOR_TYPE_MIGRATION_OWNER_TRANSFER` from id2970003.

The new reusable asset is the join: irreversible child preparation plus a later sibling failure plus
cold reconstruction. Future rounds should query for compatible asset pairs instead of restarting
module analysis from zero.

## Cross-module targets

- BR or import batches that publish one artifact before all sibling artifacts validate;
- DDL changes that enqueue delete ranges, migrate IDs, or alter external placement before parent
  publication;
- restore plans that save a metadata snapshot but mutate object storage or PD state during prepare;
- TTL and GC batches whose parent retry state does not compensate already consumed ranges;
- statistics, cache, or sequence rebuilds that delete an old generation before sibling validation.

## Stop rule

Stop this root after current-master and nightly RED, two sibling GREEN controls, one exact guard
GREEN, and post-RED dedup. Reopen only for another irreversible side-effect owner, rollback
primitive, or materially more common production trigger.

