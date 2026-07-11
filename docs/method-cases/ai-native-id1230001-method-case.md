# Method Case: id1230001 NT-DML `tx_read_ts` State Leak

## Finding

TiDB non-transactional DML can silently use a stale `tx_read_ts` snapshot while deriving shard
ranges. `BATCH ON a LIMIT 1 UPDATE t SET ...` reports success, but updates only rows visible at
the stale timestamp and misses newer current rows.

## P/Q/D/F/O/R/S Card

```text
Target:
  Non-transactional DML split-range planning in pkg/session/nontransactional.go.

Source anchors:
  nontransactional.go:80-82 clears ReadStaleness because NT-DML is a write.
  nontransactional.go:467-468 executes the split-range SELECT through the normal session path.
  staleread/processor.go:233-257 consumes TxnReadTS for statements without AS OF.

T_tests:
  Existing integration tests cover tidb_read_staleness, but the same stale-read proof obligation
  also has a sibling input: SET TRANSACTION READ ONLY AS OF TIMESTAMP / @@tx_read_ts.

P_check:
  Code clears SessionVars.ReadStaleness before running NT-DML.

Q_claim:
  NT-DML split-range planning is not affected by stale-read state.

D_dims:
  Stale-read input channel:
  - tidb_read_staleness session variable.
  - SET TRANSACTION READ ONLY AS OF TIMESTAMP / TxnReadTS.
  - tidb_snapshot is separately rejected by checkConstraint.

F_effect:
  buildShardJobs uses se.Execute(selectSQL), so TxnReadTS makes the SELECT read an old snapshot.
  runJobs then executes and commits only the stale-derived ranges.

O_oracle:
  Control A: ordinary UPDATE under tx_read_ts must reject read-only stale transaction.
  Control B: BATCH UPDATE without tx_read_ts updates both current rows.
  Red: BATCH UPDATE with tx_read_ts reports success but updates only stale-visible rows.

R_redflag:
  One row before @ts, one row after @ts, batch size 1 so job count reveals the stale split.

S_selector:
  S23 stale transaction input leak into split-range planning.
```

## Minimal Matrix

| Case | Setup | Expected | TiDB |
| --- | --- | --- | --- |
| control | ordinary `UPDATE` after `SET TRANSACTION READ ONLY AS OF TIMESTAMP @ts` | reject read-only stale write | `ERROR 1105` |
| control | `BATCH UPDATE` with no `tx_read_ts` | updates `1` and `2` | `1:110,2:120` |
| red | `BATCH UPDATE` after `SET TRANSACTION READ ONLY AS OF TIMESTAMP @ts` | reject or update current rowset | reports 1 job, leaves `1:110,2:20` |

## Why This Worked

The proof obligation was in a comment: "NT-DML is a write operation, and should not be affected by
read_staleness". The bug appeared only after turning that into a complete input inventory. The
code cleared one stale-read input but left another one live.

The small matrix was enough because it had a strong current-rowset oracle:

1. `AS OF` control proves the old timestamp sees only row `1`.
2. normal `UPDATE` proves writes should not proceed under the read-only stale transaction.
3. no-`tx_read_ts` NT-DML proves the same BATCH statement can update both rows.
4. the red cell proves the split planner used the stale rowset.

## Next Use

For txn-adjacent modules, search for "state ingress" comments like "ignore/clear/reset X" and
audit sibling channels that create the same semantic state. This is especially useful when a
statement internally runs another SQL statement: the internal statement may pass through a generic
session path that still consumes hidden state.
