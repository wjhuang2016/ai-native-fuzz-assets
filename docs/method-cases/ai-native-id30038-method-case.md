# Method Case: id30038

## Selector

```text
S1_FLATTENED_KEY_OWNER_MAPPING
```

A sibling path flattens generated artifacts, then later reconstructs the owner
from position instead of carrying an explicit owner bit.

## Why This Was Efficient

The target came from a source proof, not from SQL enumeration:

```text
P_check:
  batchCheckUniqueKey stores recordIdx while flattening generated keys.

Q_claim:
  flattened key ordinal modulo len(indexes) still identifies the owning index.

D_dims:
  multi-valued indexes can emit more than one key per idxRecord.
  sibling indexes can have different index-column counts and decode rules.

F_effect:
  found-key duplicate classification decodes the existing key using the
  reconstructed owner metadata.

R_redflag:
  ADD UNIQUE MVI together with a sibling multi-column UNIQUE index, plus
  concurrent DML that writes the new index entries before backfill reaches the
  row.

O_oracle:
  online DDL outcome plus controls:
    - single MVI succeeds,
    - MVI + one-column sibling succeeds,
    - MVI + multi-column sibling fails,
    - table remains healthy after rollback.
```

The small matrix was enough because every axis had a job:

| Axis | Why it matters |
| --- | --- |
| MVI on/off | proves one `idxRecord` can emit N keys |
| sibling index column count 1 vs 2 | proves the owner metadata matters, not just multi-index presence |
| concurrent DML | puts existing new-index keys into storage before backfill classification |
| DDL outcome + `ADMIN CHECK` | separates false DDL failure from storage corruption |

## Reopen Test

This minted a new root cause:

- It is not `addindex-rollback-deleterange`: that root loses `IsGlobal` in
  rollback delete-range reconstruction.
- It is not S15/S10 wrong-error enumeration: the failing code is online backfill
  duplicate classification under concurrent DML, not static prevalidation.
- A fix needs a new checklist step: flattened generated artifacts must carry
  their owner/type bit all the way to the consumer.

Root cause id:

```text
addindex-mvi-key-owner-mismatch
```

## Method Upgrade

Add this to the high-consequence lane selector:

```text
If a state-transforming path flattens per-owner artifacts and a later consumer
reconstructs owner/type from ordinal, search for a shape where one owner emits
multiple artifacts. MVI, MV index entries, generated columns, multi-range
tasks, and partition-expanded objects are the first places to look.
```

This is a better target selector than "try more add-index options" because it
names the missing proof dimension before any SQL is run.

