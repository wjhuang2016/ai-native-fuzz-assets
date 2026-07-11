# id600001 Method Case: Identity Proof Fast Path

## Selector

```text
S9_REORG_BACKFILL_IDENTITY_FAST_PATH
```

The code has a repair path for duplicate `_tidb_rowid` during partition reorganization. It first probes whether the target key already exists. If the bytes are identical, it assumes the row was already copied by retry or double-write and skips the write.

This is a high-signal selector whenever:

```text
P_check:  duplicate target key exists and payload/bytes are equal
Q_claim:  this must be the same logical row or an idempotent retry
fast path: skip the expensive repair/write path
D_dim:    source owner/partition/table identity is not part of P
```

## Matrix

| Cell | D1: target key | D2: raw row bytes | D3: source physical partition | Oracle | Result |
| --- | --- | --- | --- | --- | --- |
| Green baseline | distinct | any | normal old partitions | count before == count after | GREEN |
| Guard A | same | different | different old partitions | count before == count after; regenerated rowid | GREEN |
| Guard B | different | same | different old partitions | count before == count after | GREEN |
| Red | same | same | different old partitions | count before == count after | RED: 2 -> 1 |

## Oracle

```text
O13_ROWSET_CARDINALITY_INVARIANT
```

For a DDL that claims to reorganize storage without deleting logical rows, compare the visible row multiset/cardinality immediately before and after the DDL. Use local partition queries only as trigger evidence, not as the main oracle.

## Why The Method Worked

The first source clue was not "partition DDL is complicated." It was a narrower proof obligation in a comment:

```text
there may be duplicates across different partitions, due to EXCHANGE PARTITION
```

The next line of reasoning was:

```text
If the code distinguishes different raw bytes as "not the same row,"
then it may be relying on same raw bytes as "same row."
But two SQL rows are allowed to be duplicates.
So raw bytes are not enough to prove identity.
```

That immediately yields a 2x2 matrix around rowid equality and raw-row equality. The red cell was found without exploring partition types, index variants, or concurrency schedules.

## Quality

High.

- User-visible symptom: successful DDL silently changes `COUNT(*)`.
- Strong oracle: ordinary DDL row cardinality preservation.
- Good minimization: one table, one exchange, one reorganize.
- Root cause localized: `/Users/bba/pc/tidb/pkg/ddl/partition.go:3906`.
- Guard cells identify the exact missing dimension.

## Pause Gate

Do not keep enumerating `REORGANIZE PARTITION` syntax variants for this same root cause. Reopen only if a new candidate changes one of these dimensions:

- another DDL path uses payload equality as identity proof;
- another owner has duplicate target keys from a different source identity;
- a fix needs validation across retry/crash/concurrent-DML cases.
