# id2580003: GC retirement must close every effectful consumer

Remote `found_bug id2580003`, issue-filed high severity / critical consequence:
[TiDB #69833](https://github.com/pingcap/tidb/issues/69833).

## Missed proof obligation

```text
P: an old active transaction is omitted from min-start-TS reporting, so GC may pass it.
Q: that transaction can no longer create durable effects from its retired snapshot.
F: reads call CheckVisibility, but effectful Commit reaches prewrite without it; the
   configurable 600s exclusion horizon is shorter than client-go's fixed 24h age guard.
```

The original source scan found a negative space, not a suspicious branch: every snapshot read
consumer checked the safe point, while the mutation consumer did not have `CheckVisibility` in its
store contract at all.

## Small matrix

| Consumer / proof shape | FAST | STRICT | OFF |
| --- | --- | --- | --- |
| Read after GC | rejected by `CheckVisibility` | rejected by `CheckVisibility` | rejected by `CheckVisibility` |
| Existing-row UPDATE before GC | ordinary write conflict | ordinary write conflict | ordinary write conflict |
| Existing-row UPDATE after GC | masked by `Assertion=Exist` | masked by `Assertion=Exist` | **COMMIT success; row resurrected** |
| Absent-row INSERT after insert-delete ABA + GC | **COMMIT success; row appears** | **COMMIT success; row appears** | **COMMIT success; row appears** |
| Child INSERT after parent DELETE + GC | not needed | **COMMIT success; orphan child remains** | not needed |
| Same cells plus prewrite-side visibility check | rejected | rejected | rejected |

The first SQL run was GREEN because TiDB's FAST assertion observed that an UPDATE target no longer
existed. That was only a mask for the `AssertExist` representation. The incremental matrix changed
the proof shape rather than the SQL spelling: lazy INSERT uses `AssertUnknown`, while an optimistic
foreign-key check contributes a lock-only parent key. After GC removes the write history, neither
FAST nor STRICT can reconstruct the retired conflict/FK proof. Classic upgrades can still expose the
original UPDATE shape through `OFF`; current source keeps `OFF` as the registered Classic fallback,
while NextGen uses STRICT. The INSERT and FK cells show that neither stronger value closes the owner
gap.

## Concrete production trigger

This is not a crash-only or debug-only schedule. One realistic production chain is:

1. A Classic cluster reports `@@global.tidb_txn_assertion_level=OFF`. This can occur on an older
   upgraded cluster because `FAST` is an initial-bootstrap override, while the registered fallback
   remains `OFF`. An operator sets `tidb_gc_max_wait_time=600`, the supported minimum, so abandoned
   or unusually long transactions do not indefinitely block GC and retain MVCC history.
2. An order-reconciliation or settlement worker starts an optimistic transaction and buffers an
   `UPDATE` to an order row. It then holds the transaction open during an external API call, a paused
   batch task, a long retry, or an application stall. TiDB's normal session timeout permits this
   transaction to remain connected for the required tens of minutes.
3. A retention, cancellation, or cleanup worker deletes the same order row and commits normally.
   The old optimistic transaction has not prewritten a lock, so it does not block the delete.
4. After the old transaction exceeds 600 seconds, TiDB omits its start TS from min-start-TS
   reporting. A subsequent normal GC round advances the safe point beyond it. On a write-active
   table, routine RocksDB compaction then removes the newer delete tombstone and the older row
   version as obsolete MVCC history.
5. The stalled worker resumes and commits before client-go's fixed 24-hour maximum transaction age.
   Current code returns success and recreates the deleted row with its buffered value.

With the default 10-minute GC lifetime and 10-minute GC run interval, allowing roughly 20-30 minutes
for exclusion, the next GC round, and compaction is a practical trigger window; exact timing depends
on phase and write load. The deterministic real-TiKV test advances the safe point and requests the
same production compaction filter directly. It compresses only this nondeterministic wall-clock wait.

Fresh Classic installations initialize the hidden assertion variable to `FAST`, which rejects this
specific SQL `UPDATE` shape before resurrection. It does not reject an absent-row INSERT whose lazy
duplicate-check assertion is `Unknown`, and STRICT does not reject that shape either. A still stronger
production consequence uses the defaults `tidb_enable_foreign_key=ON`, `foreign_key_checks=ON`, MDL
ON, and ordinary 2PC: a stalled optimistic transaction validates a parent and buffers a child row;
another transaction deletes the parent; after the old startTS and parent write history are retired,
the child COMMIT succeeds and leaves a permanent orphan. The deterministic current-master matrix
proved both no-GC controls fail with write conflict and both post-GC effects persist in fresh reads.

The practical wall-clock trigger still requires the old transaction to outlive its GC protection.
With the supported `tidb_gc_max_wait_time=600` tuning, the original 20-30 minute schedule applies.
With the 24-hour source default, the window is much narrower because it approaches client-go's own
maximum transaction age; this distinction must remain explicit in severity and frequency claims.

## Strong oracle

```text
T2 DELETE success + GC safe point > T1.startTS
  => T1 COMMIT must fail and a fresh session must see no row
```

Current source instead returned `COMMIT => nil`, and a fresh SQL session read `(1,11)`. This is
durable row resurrection, not an error-message or stale-read problem. In the foreign-key cell,
`COMMIT => nil` and a fresh anti-join returned child `(1,1)` with no parent. The same schedule without
GC returned error 9007 and no orphan, making reclaimed conflict evidence the isolated causal
dimension.

## Why the method worked

The efficient move was to treat safe-point advancement as retirement of a capability and enumerate
all consumers of the retired timestamp. Searching for GC code alone would emphasize reads because
that is where `CheckVisibility` already appears. Comparing the full consumer set exposed the absent
effectful path immediately.

Production reachability required a second matrix over masks, logical proof representations, and
configuration provenance. The key improvement was to mutate `existing value -> AssertExist` into
`absent value -> AssertUnknown` and `referenced parent -> lock-only proof`, not to enumerate UPDATE
syntax. This prevented a false negative after the first FAST GREEN and also prevented an exaggerated
frequency claim: the assertion defaults are broadly affected, but GC must still retire the startTS
before commit.

## Selector improvement

Add `SAFE_POINT_RETIREMENT_CONSUMER_CLOSURE`:

1. Identify the event that stops protecting an old owner, timestamp, generation, or snapshot.
2. Enumerate every later read and effectful consumer of that retired identity.
3. Subtract guards by ownership, not by accidental masks.
4. Build a small matrix over guard provenance: mandatory, default-new-install, upgrade-compatible,
   session-configurable, and best-effort.
5. Vary the semantic representation of the proof: existing/absent, row/index, lock-only, FK, or
   proof-only mutation. Do not stop after one DML happens to synthesize a secondary assertion.
6. Erase the conflict evidence that retirement promises may be reclaimed.
7. Invoke the highest durable consumer and compare terminal result with fresh state.
8. Use an exact owner counterfactual, then separately audit atomicity/TOCTOU of the proposed fix.

The reusable rule is: once a system stops preserving evidence for an owner, every effectful path
that can still act as that owner needs a fail-closed admission proof.
