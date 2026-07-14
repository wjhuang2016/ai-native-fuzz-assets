# id2460003: failed-attempt explicit insert ID escapes in the OK packet

Status: confirmed high-consequence correctness bug.

Upstream: [TiDB #69827](https://github.com/pingcap/tidb/issues/69827).

The first attempt of a pessimistic `INSERT ... SELECT` evaluates explicit auto-increment ID `42`
and later conflicts on a unique key. A concurrent gate makes the successful retry insert zero
rows. TiDB nevertheless returns `last_insert_id=42` in the successful statement's MySQL OK packet.
A same-final-state direct control returns zero, and persisting both driver results produces
`retry=42, control=0` while the destination contains only the competitor row.

The owner chain is:

```text
explicit nonzero AUTO_INCREMENT input
  -> StatementContext.InsertID
  -> pessimistic StmtRollback + ResetForRetry (InsertID omitted)
  -> successful zero-work re-entry
  -> session.LastInsertID fallback
  -> MySQL OK packet
```

This is not #69796. That issue owns `LastInsertID` and `LastInsertIDSet`, mutated by
`LAST_INSERT_ID(expr)`. This root owns singleton `InsertID`, mutated by explicit auto-increment
input. The exact #69796 reset does not close this owner.

Evidence is in `runs/pessimistic-retry-insert-id-testbed-20260714/` and the reusable test plus
counterfactual are under `scaffolds/tidb-tests/`.
