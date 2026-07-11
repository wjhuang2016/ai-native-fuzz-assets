# Method Case: id30039

## Classification

- Selector: S4 `stale owner/container key`
- Oracle: O21 `side_state_owner_remap`
- Root cause id: `exchange-idswap-orphan`
- Counting: blast-radius surface, not a new distinct root
- Quality: medium. It is user-visible behavior in future `ANALYZE`, but it is not data
  correctness loss.

## Why This Worked

The proof obligation came from the same shape as id30017/id630014/id630024:

```text
DDL swaps physical object IDs
side metadata is keyed by physical ID
the public command that created it names a logical owner
later management/behavior uses current InfoSchema to resolve that ID
```

The earlier pause rule said not to mine stats display variants. This case passed the Reopen test
only because it added a behavior round trip: the stale side row changed which columns a later
`ANALYZE TABLE nt` analyzed.

## The Small Matrix

| Cell | Result |
| --- | --- |
| `pt PARTITION p0` saved option then `EXCHANGE PARTITION p0 WITH nt` | old p0 option row resolves to current `nt` |
| `ANALYZE TABLE nt WITH 2 TOPN, 2 BUCKETS` after exchange | only `a` and `PRIMARY` analyzed; `b/c` stay `stats_ver=0` |
| standalone `ctrl`, no exchange, same `ANALYZE TABLE ... WITH` | `a/b/c/PRIMARY` all analyzed |

## Improvement To The Method

For S4, treat side-state tests as two-stage:

1. Storage/current-owner diff: does the row map to a different current object after DDL?
2. Behavior round trip: can a current user command reach, clear, or consume that row incorrectly?

Only stage 2 should raise severity. Stage 1 alone is a candidate or low-severity metadata finding.
