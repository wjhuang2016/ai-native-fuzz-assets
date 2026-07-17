# id2790003: subset-local proof cannot authorize a global cross-CF side effect

Status: current-master RED/GREEN, high severity with persistent data-loss consequence.

## Why this candidate was found quickly

The search did not begin from snapshot syntax or a random concurrency matrix. It began from a proof owner:

```text
Write compaction input proves P:
  an older long Put is stale inside these selected SST files

Code assumes Q:
  no live Write anywhere can still reference the Put's Default value

Irreversible fast path:
  physically delete that Default key in another CF
```

P does not imply Q when cleanup or recovery can install newer contradictory state outside the selected files. That immediately yields a small two-cell matrix: exclude the recovery layer for RED; include it for GREEN.

## What made the oracle strong

The first read is deliberately after the complete snapshot reapply. This rejects explanations based on partial CF ingestion. The final oracle joins three independent facts:

1. the restored Write record still physically exists;
2. the Default value is physically absent;
3. a fresh MVCC read returns `DefaultNotFound`.

The GREEN changes only compaction input closure. This assigns ownership to the subset/global proof gap, rather than to safe point selection, cleanup, or the snapshot payload.

## Methodology improvement

Add a mandatory scan whenever a maintenance path reads a subset and writes outside it:

1. Name the selected view: files, levels, shards, epochs, generations, or registry rows.
2. Separate facts proved inside that view from claims stated globally.
3. Enumerate side effects written to another owner: CF deletion, publication, reclaim, ack, repair, or checkpoint.
4. Put a newer contradiction in an excluded view.
5. Run the highest durable consumer.
6. Use input-closure, not broad feature disablement, as the first GREEN.

This is more precise than generic race fuzzing. The schedule has only one meaningful dimension and the physical oracle explains exactly why it works.

## Reuse boundary

The historical reset-to-version issue calibrates the selector but is not counted as a new root. The new countable surface is default snapshot cleanup: a common recovery lifecycle reaches the same missing proof after the prior fixes for flashback and per-ingest latching.
