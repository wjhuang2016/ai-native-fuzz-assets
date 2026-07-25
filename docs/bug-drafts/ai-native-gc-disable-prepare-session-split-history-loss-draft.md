# Disabling GC can return before an in-flight GC advances the safe point

Status: confirmed on current master with one TiDB, one PD, and one real TiKV; no exact upstream
issue found.

## Summary

`SET GLOBAL tidb_gc_enable=OFF` can return successfully while an already running GC prepare still
advances and later broadcasts a newer safe point. The variable reads `0`, yet historical versions
that were readable when the command returned become unavailable and eligible for physical deletion.

This can become direct data loss during `FLASHBACK DATABASE`: GC can load and physically delete the
dropped tables' delete-range tasks after flashback has accepted the old safe point but before it
removes those tasks. Flashback then reports success and publishes the tables with zero rows.

## Production trigger

The official DR migration workflow tells operators to disable GC and verify
`@@global.tidb_gc_enable=0` before relying on retained history. The bug needs this ordinary command
to overlap a periodic GC prepare after it has read `ON` and before it publishes the new safe point.

The interval is usually short. PD safe-point RPC latency, internal SQL latency, and control-plane
load can widen it. The trigger does not require multiple TiDB nodes, MDL off, a storage failure,
network partition, process crash, or nondefault transaction isolation.

## Strong reproduction

The focused real-TiKV test:

1. writes two committed MVCC versions at exact timestamps between the stored and next GC frontiers;
2. proves that disabling GC before the enable check skips prepare and preserves history;
3. pauses prepare immediately after it reads `tidb_gc_enable=ON`;
4. executes `SET GLOBAL tidb_gc_enable=OFF` and verifies that it returns with value `0`;
5. resumes prepare and requires it to select a safe point beyond the old version;
6. runs the production GC job through its synchronization wait, lock resolution, delete-range
   phase, and PD safe-point broadcast;
7. reads both the old exact snapshot and the latest value.

Observed RED:

```text
disable terminal: success
@@global.tidb_gc_enable: 0
prepare: true
new safe point: greater than v1 commit TS
old exact snapshot after production GC: unreadable
latest version: v2
final tidb_gc_enable: 0
focused test: PASS in 102.949s
```

The scheduler callbacks choose timing and a deterministic historical target. They inject no error
and do not replace the production GC terminal path.

## Direct data-loss consumer

A second real-TiKV test joins the same root with the production recovery consumer:

1. create one database with a 64-row table and a unique index, then drop the database;
2. start a due periodic GC prepare and pause it immediately after it reads `ON`;
3. start `FLASHBACK DATABASE`; it writes `OFF`, validates the old safe point, and pauses before
   removing the drop job's delete-range task;
4. resume GC prepare and run the full production job, including the fixed 100-second wait;
5. GC loads five eligible ranges, calls `UnsafeDestroyRange`, and moves the tasks to done;
6. resume flashback and observe a successful `public/synced` DDL terminal;
7. read the recovered table through a fresh session.

Observed RED on current master:

```text
GC delete ranges: 5
FLASHBACK DATABASE terminal: success, schema public/synced
recovered row count: 0
ADMIN CHECK TABLE: success because both record and index ranges are empty
focused real-TiKV test: PASS in 100.87s
```

The trigger is a large database recovery that remains in progress across the GC worker's 100-second
wait. More tables, metadata loading, and schema synchronization widen that ordinary production
window. It needs no TiKV failure, network fault, process crash, multiple TiDB nodes, or MDL change.

## Root cause

The 2018 implementation added a transaction specifically to order the GC enable read and safe-point
update against a user disabling GC. At that time, both `prepare` and the sys-table helpers used the
same `GCWorker.session`.

PR #14403 removed the long-lived session to fix a panic. `prepare` now opens transaction A, while
`loadValueFromSysTable` and `saveValueToSysTable` each create fresh sessions B through N. Their
`SELECT ... FOR UPDATE` and writes auto-commit outside A. The transaction whose comment claims the
ordering contains no relevant read or write.

## Matched GREEN

The fix experiment had two stages:

1. Route every enable, interval, lifetime, last-run, and safe-point operation through transaction A.
   With plain `BEGIN`, the disable still returned immediately.
2. Change A to `BEGIN PESSIMISTIC`. The disable remained blocked while prepare held the row lock,
   prepare committed first, and only then did `OFF` return.

The original history GREEN passed in 0.59 seconds. In the direct-consumer GREEN, the same
`FLASHBACK DATABASE` waited while prepare held the fence, then returned error 8055 after the newer
safe point committed. The database was not published and its delete-range task remained active.
That focused test passed in 1.16 seconds.

## Expected behavior

Once `SET GLOBAL tidb_gc_enable=OFF` returns, no GC safe-point publication admitted before that
command may remain unordered after it. Either the in-flight prepare commits before the command
returns, or it observes `OFF` and aborts.

## Fix direction

Run `prepare` in an explicit pessimistic transaction and pass that session through all config reads
and metadata writes. Keep the `tidb_gc_enable` row lock until the safe-point metadata update commits.
The competing `SET GLOBAL` then linearizes after that prepare.

External PD advancement also deserves a separate failure-order audit because it cannot participate
in the SQL transaction. The confirmed bug here is the user-visible disable ordering.

## Impact and severity

The consequence is direct recovery data loss: a successful `FLASHBACK DATABASE` can publish empty
tables after GC physically deletes their ranges. Trigger probability is lower because recovery must
overlap a GC prepare after its enable read and remain active through the GC wait. The bug library
keeps `high / critical consequence` rather than conflating impact with frequency.

## Dedup and history

- PR #8282 introduced `tidb_gc_enable` and the original transaction obligation in 2018.
- PR #14403 split the session ownership in 2020 while fixing a worker panic.
- Upstream master still has the transaction comment and independent helper sessions.
- Post-RED searches across open and closed TiDB issues found no exact root.
