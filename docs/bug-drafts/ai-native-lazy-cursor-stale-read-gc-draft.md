# Draft: lazy server cursor loses stale-read GC ownership and fails with 9006

> Confirmed on 2026-07-17 by real protocol execution on TiDB
> `d573e284da773c820c1c313105b73d587378381b`. The same root is still present in GitHub
> master `94b834d94b604b1940ecc2c3064168337863269d`. Classification: moderate. The result is
> a definite query failure in an opt-in feature, not silent corruption.

## Summary

When an autocommit stale read is detached into a lazy MySQL protocol cursor, TiDB registers the
cursor with `TxnCtx.StartTS`. That field is zero for the stale-read path; the actual snapshot is in
`TxnCtx.StaleReadTs` or `SnapshotTS`.

After statement execution finishes, processlist naturally changes to `Sleep` with an empty
`TxnStart`. `ReportMinStartTS` then sees neither a statement owner nor a usable cursor owner, so the
reported GC frontier can move past the still-open cursor. A later Region read returns:

```text
Error 9006 (HY000): GC life time is shorter than transaction duration
```

This is an adjacent incomplete fix to #61325/#61329: current master preserves the stale timestamp
while a statement owns the read, but loses it during the statement-to-cursor ownership handoff.

## Production Trigger

One TiDB is sufficient. A concrete production shape is a reporting or export client that uses a
server-side prepared cursor and fetch-size streaming to read a stale snapshot from a large table:

1. `tidb_enable_lazy_cursor_fetch=ON` is enabled for the session. It is OFF by default.
2. The client opens a read-only protocol cursor for an autocommit stale read, for example `SELECT
   ... AS OF TIMESTAMP ...`, and consumes it slowly or leaves it idle.
3. The query spans more Regions than the active DistSQL scan workers, so some Region reads are
   started only during later fetch progress. The 64-Region RED used the default scan concurrency.
4. The cursor remains open long enough for normal GC to pass its stale snapshot. The default GC
   lifetime makes this a long-query condition; the isolated test advances the same frontier
   directly to compress time.
5. A later `COM_STMT_FETCH` needs another Region read. TiKV rejects the old snapshot with 9006.

No DDL, partitioning, multiple TiDB nodes, MDL change, Write CF compaction, or data corruption is
required. A small single-Region query whose storage reader was already established can finish even
after the frontier moves; that is a negative control, not evidence that registration is correct.

## Exact Source Root

`pkg/session/session.go`, `execStmtResult.TryDetach`, still does this on GitHub master
`94b834d94b604b1940ecc2c3064168337863269d`:

```go
cursorHandle := rs.se.GetCursorTracker().NewCursor(
    cursor.State{StartTS: rs.se.GetSessionVars().TxnCtx.StartTS},
)
```

For the confirmed stale read:

```text
TxnCtx.StartTS     = 0
TxnCtx.StaleReadTs = actual read TS
cursor.StartTS     = 0
```

`pkg/domain/infosync/info.go`, `ReportMinStartTS`, consumes only
`cursor.GetState().StartTS`. The collector is locally correct, but its registry entry contains the
wrong semantic identity.

## Fast Owner Repro

Copy `scaffolds/tidb-tests/stale_cursor_gc_probe_test.go` to
`pkg/executor/staticrecordset/` in an unmodified master checkout and run:

```bash
go test -tags=intest ./pkg/executor/staticrecordset \
  -run TestProbeStaleReadCursorProtectsItsReadTS -count=1 -v
```

The unmodified source reports:

```text
read_ts=467724462924234754
cursor_start_ts=0
second_txn_start_ts=467724462924496896
reported_min_start_ts=467724462924496896
```

The assertions require the cursor and aggregate frontier to preserve `read_ts`, so the test fails.

## Real Testbed Repro

The reusable protocol client is
`scaffolds/top-level/ai_native_stale_cursor_gc_probe.go`. From the TiDB repository, where its MySQL
cursor driver dependency is already available:

```bash
go run ../ai-native-fuzz-assets/scaffolds/top-level/ai_native_stale_cursor_gc_probe.go \
  --host <tidb-host> --port <tidb-port> \
  --rows 64000 --value-size 256 --split-regions 64 \
  --fetch-size 1 --hold 120s
```

While `CURSOR_OPEN` is holding the cursor, observe that processlist is `Sleep` with an empty
`TxnStart` and that `/tidb/server/minstartts/<server-id>` advances above `snapshot_ts`. On a dedicated
disposable cluster only, advance GCV2 to exactly the TiDB-reported value using
`scaffolds/pd-tools/force-gc-safepoint-gcv2/main.go`.

Testbed 8196300, with default DistSQL scan concurrency, produced:

```text
CURSOR_OPEN snapshot_ts=467725273248038917 ... rows=64000 regions=64 scan_concurrency=0
reported minStartTS=467725284651040769
txn_old=467725237465120769 requested=467725284651040769
txn_updated=467725284651040769 gc_updated=467725284651040769
CURSOR_ERROR fetched=0 wrong=0 error="Error 9006 (HY000): GC life time is shorter
than transaction duration, transaction start ts is 467725273248038917 ...
txn safe point is 467725284651040769"
```

The tool did not bypass a registered owner: PD accepted only the frontier TiDB itself had reported.

## Counterfactual

Changing only cursor registration to fall back to the stale-read timestamp makes the owner test
GREEN and leaves the existing ordinary-cursor test GREEN:

```go
vars := rs.se.GetSessionVars()
cursorStartTS := vars.TxnCtx.StartTS
if cursorStartTS == 0 {
    cursorStartTS = vars.TxnCtx.StaleReadTs
}
if vars.SnapshotTS != 0 {
    cursorStartTS = vars.SnapshotTS
}
cursorHandle := rs.se.GetCursorTracker().NewCursor(cursor.State{StartTS: cursorStartTS})
```

```text
read_ts=467725198085324805
cursor_start_ts=467725198085324805
reported_min_start_ts=467725198085324805
TestCursorWillBlockMinStartTS PASS
TestProbeStaleReadCursorProtectsItsReadTS PASS
```

## Severity And Dedup

Severity is moderate: the bug can fail a long-running stale report after substantial work, but lazy
cursor fetch is disabled by default and the observed result is a clear read error, not wrong data or
durable corruption. Searches for `lazy cursor stale read GC`, `tidb_enable_lazy_cursor_fetch GC`, and
`cursor minStartTS StaleReadTs` found no exact TiDB issue or PR. PR #54527 introduced lazy cursor
fetch and covered ordinary `TxnCtx.StartTS`, but not stale-read ownership.
