# id2460003: consumer-first protocol output retry closure

Remote bug DB: `found_bug id2460003`, confirmed high consequence.
Upstream: [TiDB #69827](https://github.com/pingcap/tidb/issues/69827).

## Starting proof obligation

Transparent retry promises one successful statement execution to the caller. Therefore every
terminal response field must be sourced from the successful attempt or from state explicitly
defined to survive the statement:

```text
failed attempt mutates response owner O
retry rollback accepts the error and rebuilds execution
successful attempt does not overwrite O
terminal encoder publishes O
```

## Why the prior selector missed it

The earlier reset-omission scanner prioritized `value + Set/Valid/Dirty/Changed/Present` pairs.
That worked for `LastInsertID + LastInsertIDSet`, but `StatementContext.InsertID` is a singleton.
Searching from state layout therefore encoded an accidental field-shape assumption.

## Improved selector

New selector: `PROTOCOL_OUTPUT_RESET_DIFFERENTIAL`.

1. Enumerate public terminal outputs: OK-packet IDs, affected rows, warnings, status flags, errors.
2. Backward-slice each consumer to mutable statement/session owners.
3. Intersect with fields that can be mutated before an accepted retry.
4. Subtract all owners covered by retry reset, restore, rebuild, or version binding.
5. Subtract owners guaranteed to be overwritten by every successful attempt.
6. Force zero-work re-entry so surviving residue cannot hide behind a second setter.

This includes singleton fields and makes the public contract, not struct naming, the inventory
root.

## P/Q/F

- **P**: statement KV writes are rolled back and execution is rebuilt.
- **Q**: the MySQL OK packet describes only the successful attempt.
- **F**: failed-attempt `InsertID=42` survives `ResetForRetry`; zero-row re-entry leaves it intact;
  `session.LastInsertID()` publishes it.

## Strong oracle

Store as `ZERO_ROW_RETRY_OK_PACKET_VS_SAME_STATE_CONTROL`:

```text
retry arm: natural conflict -> successful retry -> driver RowsAffected/LastInsertId
control arm: direct execution from the retry arm's final database state
sink: persist both reported IDs
```

Observed RED is `(retry=0/42, control=0/0, sink=42/0)`. The exact `InsertID=0` reset makes it
`(0/0, 0/0, 0/0)` without changing retry count or business rows.

## Generalization and stop rule

The reusable class is terminal-output attempt closure, not insert-ID syntax. Future candidates
must have a distinct owner and a public consumer. Explicit IDs, INSERT SELECT shapes, delays, and
conflict schedules are blast radius for this root. `LastInsertID/LastInsertIDSet` is terminal under
#69796 and must remain excluded.
