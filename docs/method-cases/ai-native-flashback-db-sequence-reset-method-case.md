# Method Case: Restore special-object runtime state

## Finding

`FLASHBACK DATABASE` restores sequence `TableInfo` through the generic table recovery path, but sequence values are stored in a separate meta key. The restored sequence can therefore allocate values from the beginning again.

## P/Q/D/F/O/R/S card

```text
Target:
  FLASHBACK DATABASE containing a sequence and a sequence-backed default table.

Source anchors:
  ddl/schema.go:304-329      RecoverSchema loads all TableInfo objects from snapshot meta.
  ddl/schema.go:351-359      RecoverSchema calls recoverTable for every recovered object.
  ddl/table.go:292-300       recoverTable uses CreateTableAndSetAutoID.
  ddl/sequence.go:70-80      create sequence uses CreateSequenceAndSetSeqValue.
  meta/meta.go:1010-1017     sequence value is stored under sequenceKey.
  meta/meta.go:1076-1088     DropSequence deletes the sequence key.
  meta/autoid/autoid.go:1120-1172 runtime NEXTVAL reads SequenceValue.

P_check:
  Recovery sees a valid historical TableInfo and passes schema/name/GC checks.

Q_claim:
  Recreating the TableInfo plus generic AutoIDGroup is enough for current behavior.

D_dims:
  - generic table recover versus sequence-specific create/drop helpers;
  - sequence-backed primary-key default versus ordinary AUTO_INCREMENT;
  - recovery versus no-recovery control.

F_effect:
  Sequence value/cycle keys are skipped. Missing sequence value reads as zero, so allocation starts
  from the first sequence values again.

O_oracle:
  After recovery, NEXTVAL must not return values already present in recovered rows. A default insert
  must not hit duplicate key because the sequence moved backward.

R_redflag:
  `NEXTVAL(seq)=1` after rows `1,2` have been recovered, followed by `ERROR 1062`.

S_selector:
  `RESTORE_SPECIAL_OBJECT_STATE_REBUILD`: when a restore path clones generic metadata, enumerate
  object kinds whose behavior depends on kind-specific side/runtime state.
```

## Minimal matrix

| Shape | Expected | Observed |
| --- | --- | --- |
| `FLASHBACK DATABASE` with sequence-default table | next sequence value does not collide | RED: `NEXTVAL(seq)=1`, next insert hits duplicate `2` |
| Same sequence/default table without recovery | monotonic sequence behavior | GREEN: rows `1,2`, `NEXTVAL=3`, next default insert `4` |
| `FLASHBACK DATABASE` with ordinary `AUTO_INCREMENT` table | no ID reuse | GREEN: rows `1,2`, next insert `30001` |

## Why this worked

The useful move was not to enumerate more FK or sequence-default variants. It was to split a restored object into:

```text
identity metadata: TableInfo
runtime state: object-kind-specific keys/caches/counters
```

`RecoverSchema` proved identity metadata only. The strong oracle asked whether the object's next action was still safe.

## Pause gate

Do not enumerate sequence options (`CACHE`, `CYCLE`, negative increment) yet. The representative root is "generic schema recovery skips sequence value/cycle state." Reopen only for owner-requested blast-radius validation, a proposed fix, or another special object with a different runtime-state owner and direct C3 oracle.
