# id3030003: a safety repair inherits the severity of the state it closes

Status: current-master RED/GREEN complete; recorded as high/major.

## Proof obligation

```text
P: one table's final allocator rebase failed.
Q: warning and continuing still leave the restored cluster safe for writes.
F: the rebase is the only step that closes stale autoid state, and no later validation checks it.
```

The invariant is:

```text
A repair that establishes a required safety invariant cannot be best effort unless another owner
proves the same invariant before the public success terminal.
```

## Small matrix

| Final metadata transaction | Public helper | Next generated REPLACE |
| --- | --- | --- |
| existing failpoint returns commit error | success | reuses id=2 and overwrites restored row |
| identical state, error disabled | success | allocates 1004001 and preserves id=2 |

Only repair success changes. Raw state, schema, allocator history, SQL, and consumer are fixed.

## Selector

```text
candidate = code intentionally repairs a known unsafe intermediate state
            intersect repair errors are logged, counted, or downgraded
            intersect the public operation can still report success
            intersect no later owner validates the repaired invariant
            intersect a normal consumer can make the residual state irreversible
            minus independently owned retry, fencing, or closure validation
```

Use the name `SAFETY_REPAIR_ERROR_DOWNGRADED_TO_BEST_EFFORT`.

## Why this worked

The original regression already identified a strong proof obligation: raw PiTR replay must not leave
the autoid service behind persisted state. Instead of repeating the original scenario, the test
mutated the repair's error contract:

1. locate the only state-closing operation;
2. enumerate its natural error exits;
3. follow each exit to the public terminal;
4. reuse the original highest consumer;
5. compare against the same repair without the error.

This converts a historical root into a new candidate generator without using its PR review as the
finding. The source states why the repair exists; execution tests whether every exit preserves that
reason.

## Severity calibration

The consumer proves silent persistent data loss, but severity includes trigger probability:

- PiTR is required;
- the table must use `AUTO_ID_CACHE=1`;
- one final repair must fail;
- traffic must resume before allocator refresh;
- the first relevant write must use destructive upsert semantics.

The sample therefore belongs in the high/major library and method training set. It is not promoted
to critical.

## Loop improvement

For every fix, recovery phase, or integrity repair:

1. name the unsafe pre-repair state;
2. list every operation that can close it;
3. enumerate all repair error exits;
4. trace each exit to the public terminal;
5. require a closure owner after any downgraded error;
6. reuse the original bug's strongest consumer;
7. run one-error RED and exact no-error GREEN;
8. grade consequence and trigger probability separately.

This selector applies to restore rebases, checkpoint repair, index rebuild, GC recovery, orphan
cleanup, schema reload, placement reconciliation, and metadata backfill.

## Stop rule

TiKV error codes, table counts, restored ID values, REPLACE payloads, and equivalent transient
failures are blast radius. Reopen only for a different safety invariant, repair owner, public
terminal, or higher-probability consumer.
