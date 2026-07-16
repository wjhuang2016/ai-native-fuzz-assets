# Live Snapshot Identity Lost During Statement-To-Cursor Handoff

> New master-current bug, confirmed 2026-07-17. Runtime RED on `d573e284da`; source root still
> present on GitHub master `94b834d94b`. Remote bug DB id2760003. Severity moderate.

## Why This Candidate Was Fast

The historical `DefaultNotFound` replay supplied a proven obligation rather than a keyword:

```text
Every live read snapshot must appear in the registry consumed by ReportMinStartTS.
```

Current master made the active-statement cell GREEN. The next mutation changed only the owner phase:

```text
active statement -> detached lazy cursor -> later Region fetch
```

The cursor handoff copied `TxnCtx.StartTS`; stale reads keep their identity in
`TxnCtx.StaleReadTs`. That made the owner cell RED before any cluster work.

## Compressed Matrix

| Owner phase | Read mode | Registered identity | Verdict |
| --- | --- | ---: | --- |
| active statement | ordinary transaction | `TxnCtx.StartTS` | GREEN |
| active statement | autocommit stale read | `TxnCtx.StaleReadTs` | GREEN after #61329 |
| detached cursor | ordinary transaction | `TxnCtx.StartTS` | GREEN existing test |
| detached cursor | autocommit stale read | zero | RED new root |
| detached cursor with fallback | autocommit stale read | exact stale TS | GREEN counterfactual |

The productive selector is therefore not only registry completeness. It is
`LIVE_RESOURCE_IDENTITY_ACROSS_OWNER_HANDOFF`:

```text
candidate = live resources
          x owner transitions
          x alternate identity modes
          - handoffs that preserve the collector's exact semantic key
```

## Oracle Ladder

1. Read identity: stale TS is nonzero while transaction start TS is zero.
2. Handoff identity: cursor tracker stores zero.
3. Aggregate: `ReportMinStartTS` chooses a later transaction/frontier.
4. Runtime owner: protocol cursor remains open while processlist is `Sleep/TxnStart=NULL`.
5. Legal collection frontier: PD accepts the TiDB-reported GCV2 upper bound above the live TS.
6. Highest consumer: a later Region fetch returns exact error 9006.
7. Counterfactual: changing only cursor identity registration makes both stale and ordinary owner
   tests GREEN.

The first three levels take seconds. The real cluster was used only after they were RED.

## Strongest Runtime Result

On one master TiDB, three TiKV nodes, MDL ON, and default DistSQL scan concurrency:

```text
snapshot TS:       467725273248038917
table Regions:     64
rows/value bytes:  64000/256
cursor fetch size: 1
reported minStart: 467725284651040769
public result:     Error 9006 on first FETCH
```

The accelerated test changed time, not ownership: it advanced GCV2 only to TiDB's own unblocked
frontier. No compaction or data rewrite was needed for the user-visible failure.

## Negative Screens

- A formatted TSO time loses logical bits. When the snapshot and last commit share one physical
  millisecond, `AS OF TIMESTAMP '<time>'` may select before the write. Use the numeric TSO.
- A small single-Region cursor can finish because the storage reader is already established. Require
  a deferred Region request or retry before judging the highest consumer.
- Advancing the safe point past a correctly registered owner would test only forced-GC behavior.
  Use the GCV2 blocker/accepted frontier as an oracle and never bypass it.
- `cursor.StartTS=0` alone proves the root but not severity. Promotion still requires a supported
  protocol client and a public terminal result.

## Method Improvement

For every registry asset, store both creation modes and owner transitions:

```text
resource identity -> owner A field -> handoff function -> owner B field -> collector field
```

Then generate a pairwise mutation matrix over identity mode and handoff. This reuses an old bug's
oracle while still discovering a new root from current source. It also prevents a common AI error:
declaring a historical fix complete after checking only the owner that the fix edited.

Severity remains a separate gate. This hit validates the method but does not satisfy the campaign's
critical/data-integrity target because lazy cursor fetch is opt-in and the result is a clear error.
