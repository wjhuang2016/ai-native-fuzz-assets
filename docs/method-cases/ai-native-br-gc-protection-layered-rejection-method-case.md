# Method Case: a failed primary guard is not a bug when a downstream owner rejects

## Starting point

This candidate came only from current source and local asset dedup.

```text
P: historical backup must establish GC protection before reading backupTS
Q: a successful protection setup means backupTS remains readable
F: GetGCSafePoint errors are ignored, and global SetServiceSafePoint only warns when the
   returned effective safepoint is already newer than backupTS
```

The candidate had a direct C3 oracle: after physical GC, BR must not return success with a backup
that restores a missing or wrong historical row.

## Selector

`GC_PROTECTION_ACK_DOMINATES_HISTORICAL_READ`

For every protection, lease, barrier, lock, or reservation:

1. distinguish RPC success from effective ownership;
2. compare the returned effective boundary with the requested boundary;
3. enumerate downstream owners that independently reject stale access;
4. observe process status and required artifact, not only the primary warning.

## Minimal matrix

| Schedule | Primary guard | Downstream owner | Artifact | Verdict |
|---|---|---|---|---|
| normal old-TS backup after physical GC | rejects with GC safepoint exceeded | not reached | no backupmeta | GREEN |
| injected GetGCSafePoint failure | swallowed; service write warns but returns nil | TiKV snapshot rejects with 9006 | no backupmeta | GREEN |
| hypothetical vulnerable path | swallowed | accepts stale history | backupmeta restores wrong rowset | RED |

The real-TiKV matrix used a one-second test-only GC clock. The old row was committed, a backup TS
was captured, the row was updated, and a real GC job ran above that TS. With the read failure
injected twice, BR still exited 1 because `BuildBackupRangeAndInitSchema` opened the old snapshot
and TiKV rejected it.

## LOOP improvement

```text
source guard looks unsound
  -> prove the requested owner was not acquired
  -> enumerate downstream independent owners
  -> force the operation past the weak guard
  -> observe terminal status plus required artifact
  -> RED only if every downstream owner accepts an invalid terminal state
```

This prevents overcounting source defects that are contained by a separate safety boundary.

## Stop rule

One no-fault rejection and one injected pass-through followed by downstream rejection are enough.
Do not enumerate PD error codes or backup filters unless a path bypasses the TiKV snapshot check.
