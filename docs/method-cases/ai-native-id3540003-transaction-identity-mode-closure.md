# id3540003: a transaction proof needs identity and mode closure

## Starting proof obligation

```text
P: GC reads tidb_gc_enable=ON while holding the intended config fence.
Q: the surrounding transaction orders any later OFF after this safe-point update.
F: helpers execute on other sessions, and plain BEGIN does not provide the required wait lock.
```

The source advertised the exact safety property in a comment. That made the test target precise:
prove whether the transaction actually owns every read and write in the claimed critical section.

## Small matrix

| Config helper identity | Transaction mode | OFF during prepare | Result |
| --- | --- | --- | --- |
| fresh session per helper | plain `BEGIN` | returns before release | historical frontier RED |
| same outer session | plain `BEGIN` | still returns before release | serialization RED |
| same outer session | `BEGIN PESSIMISTIC` | waits for prepare commit | ordering GREEN |
| OFF before enable read | any | prepare skips | history GREEN |

The middle row was essential. A one-step fix would have produced a plausible patch without proving
that the intended locking semantics actually held.

## Strong oracle

1. Require the disable statement to return successfully and expose value `0`.
2. Record the stored and PD safe-point frontiers.
3. Use committed MVCC versions with timestamps inside the threatened interval.
4. Run the production GC terminal path.
5. Read the old exact snapshot and the latest version independently.
6. In the GREEN, observe whether OFF is blocked while prepare owns the fence.
7. Join the frontier with a recovery consumer: require `FLASHBACK DATABASE` terminal success, the
   drop job's active/done delete-range state, recovered record/index membership, and fresh-session
   row counts.

Current-row checks, config-value checks, and metadata-only checks all miss the history loss.
`ADMIN CHECK TABLE` also misses the direct-consumer RED because GC deletes both record and index
ranges, leaving an internally consistent empty table.

## Consumer lift

The first RED proved that history could disappear after OFF returned. The higher consumer made the
impact concrete:

```text
DROP DATABASE
  -> FLASHBACK validates the old safe point
  -> in-flight GC loads five delete ranges
  -> UnsafeDestroyRange deletes record and index keyspaces
  -> FLASHBACK removes no active task and publishes the schema
  -> SQL terminal succeeds with 64 rows reduced to 0
```

After proving a frontier or ownership violation, enumerate irreversible consumers that can cross
the same boundary. Prefer the consumer whose successful terminal makes the loss visible without
interpreting internal metadata.

## Selector improvement

Add `TRANSACTION_IDENTITY_MODE_CLOSURE`:

1. Translate the transaction comment or API promise into a happens-before relation.
2. Tag every helper operation with its session and transaction identity.
3. Record transaction mode, lock statement, locked key or range, and commit owner.
4. Put the competing public action immediately after the decisive read.
5. First close session identity and rerun.
6. Then vary mode or lock semantics independently.
7. Join the public terminal with the irreversible external consumer.

High-value source shapes:

```text
se := newSession()
se.Begin()
checkUsingHelperThatCreatesAnotherSession()
publishUsingAnotherSession()
se.Commit()
```

```text
BEGIN
SELECT ... FOR UPDATE
// assumes wait-style locking without proving pessimistic mode
```

## Why it worked

The code already contained a proof obligation, but earlier review treated `BEGIN` and
`SELECT FOR UPDATE` as evidence that it was satisfied. Owner tracing showed that the transaction
and the helpers had different identities. The first counterfactual then exposed a second missing
dimension: same-session execution still used the wrong transaction mode.

This is a useful escalation rule for AI-native testing: after a candidate fix closes one dimension,
keep the scheduler and oracle fixed and vary the remaining proof dimensions one at a time.

## Cross-module targets

- DDL metadata transactions that call helpers backed by session pools;
- backup or restore checkpoints that mix SQL transactions and object-store publication;
- owner election paths that combine local transactions with PD or etcd leases;
- TTL and cleanup workers that read configuration before deleting durable data;
- statistics and privilege writers that open nested restricted sessions;
- import and task frameworks whose transaction wraps only bookkeeping, not external publication.
- recovery paths that validate a frontier once and later revoke cleanup tasks.

## Stop rule

Stop this root after one real-TiKV RED and matched identity-plus-mode GREEN. Reopen only when a
different transaction owner, lock mode, or external publication contract is involved.
