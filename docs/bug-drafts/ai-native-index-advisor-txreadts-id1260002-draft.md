# RECOMMEND INDEX RUN can consume a pending stale-read timestamp

## Triage status

- Asset id: `id1260002`
- Current status: `contract-needed`, not yet confirmed upstream
- Current root candidate: `executeinternal-consumes-pending-tx-read-ts`
- Current source commit: `13282a8bd06bd33324a4dbfd3c1c03685f3cd9aa`
- Live environment: authorized testbed `8220955`, no failpoints
- Evidence: [testbed log](../../assets/store/logs/txn-index-advisor-txreadts-testbed8220955-20260712.log)

## User-visible symptom

After a user sets a one-shot stale-read timestamp, `RECOMMEND INDEX RUN` succeeds, but
the following user `SELECT` reads newly committed rows that should be outside the stale
snapshot. The helper statement does not report an error; it silently changes which
snapshot the next user read observes.

## Reproduction

Use two connections to the same TiDB. The timestamp below was captured after row `1` was
committed and before row `2` was committed.

```sql
CREATE DATABASE ai_native_txn_advisor_live_20260712;
USE ai_native_txn_advisor_live_20260712;

CREATE TABLE t_red(id INT PRIMARY KEY, v INT);
INSERT INTO t_red VALUES (1, 10);

-- Capture this value as stale_ts, then wait at least two seconds.
SELECT NOW(6) AS stale_ts;

INSERT INTO t_red VALUES (2, 20);
```

In a fresh connection, replace `<stale_ts>` with the captured value:

```sql
SET TRANSACTION READ ONLY AS OF TIMESTAMP '<stale_ts>';
RECOMMEND INDEX RUN FOR
  'SELECT * FROM ai_native_txn_advisor_live_20260712.t_red WHERE id >= 1';

SELECT id, v FROM t_red ORDER BY id;
```

Observed on testbed `8220955`:

```text
RECOMMEND INDEX RUN: success
SELECT: (1, 10), (2, 20)
```

The direct stale-read control in a separate fresh connection is:

```sql
SET TRANSACTION READ ONLY AS OF TIMESTAMP '<stale_ts>';
SELECT id, v FROM t_red ORDER BY id;
-- (1, 10)
```

The no-pending-state control is also current and returns both rows:

```sql
RECOMMEND INDEX RUN FOR
  'SELECT * FROM ai_native_txn_advisor_live_20260712.t_red WHERE id >= 1';
SELECT id, v FROM t_red ORDER BY id;
-- (1, 10), (2, 20)
```

## Why this is a candidate bug

The outer statement passes the current user session to the index advisor:

- `pkg/executor/recommend_index.go:75` calls `indexadvisor.AdviseIndexes`.
- `pkg/planner/indexadvisor/utils.go:533-549` calls `ExecuteInternal` on that same session
  and drains the result set.
- `pkg/session/session.go:1875-1901` scopes restricted SQL but does not isolate pending
  `TxnReadTS`.
- `pkg/sessiontxn/staleread/processor.go:233-257` consumes the pending timestamp when the
  internal helper `SELECT` enters the generic stale-read path.

A temporary local experiment that hid pending `TxnReadTS` and `SnapshotInfoschema` before
the internal SQL and restored them after the internal boundary made the next user read
return only `(1,10)`. That is a fix-boundary probe, not a submitted fix.

## Contract gate

The TiDB stale-read design says `SET TRANSACTION READ ONLY AS OF TIMESTAMP` applies to the
next interactive transaction or query statement. The remaining product question is whether
the internal helper query executed inside the user-facing management statement is allowed
to consume that one-shot state. If the contract is user-statement scoped, this is a silent
wrong-snapshot bug; if it is any internal query scoped, the live behavior is expected and
the asset should remain a contract-negative boundary.

## Strong oracle

The direct `AS OF` rowset is the reference. A valid RED requires all of the following:

1. the timestamp is proven to fall between the two commits;
2. `RECOMMEND INDEX RUN` succeeds;
3. the next user rowset differs from the direct `AS OF` rowset; and
4. the same wrapper without pending stale-read state remains a normal current-rowset control.

The internal `TxnReadTS` value is only a diagnostic observer; it is not the bug oracle.
