# id1230001: Non-transactional DML uses stale `tx_read_ts` to derive split ranges

Status: confirmed on testbed `8220955`; inserted into remote `found_bug` as id1230001.

## User-Visible Symptom

`BATCH ... UPDATE` can succeed but silently miss rows that exist at the time of the write.
The trigger is a pending stale transaction timestamp from:

```sql
SET TRANSACTION READ ONLY AS OF TIMESTAMP @ts;
```

Ordinary DML under that state is rejected as a read-only stale transaction. Non-transactional DML
does not reject it. Instead, it builds its shard jobs from the stale snapshot, then executes only
those stale-derived ranges.

## Minimal Repro

```sql
DROP DATABASE IF EXISTS ai_ntdml_txreadts_final;
CREATE DATABASE ai_ntdml_txreadts_final;
USE ai_ntdml_txreadts_final;

CREATE TABLE t_bug(a INT PRIMARY KEY, b INT);
INSERT INTO t_bug VALUES (1,10);
SET @bug_ts = NOW(6);
DO SLEEP(1.3);
INSERT INTO t_bug VALUES (2,20);

SELECT GROUP_CONCAT(CONCAT(a,':',b) ORDER BY a) AS rows_seen
FROM t_bug AS OF TIMESTAMP @bug_ts;
-- 1:10

SET TRANSACTION READ ONLY AS OF TIMESTAMP @bug_ts;
BATCH ON a LIMIT 1 UPDATE t_bug SET b=b+100;
SET @@tx_read_ts='';

SELECT GROUP_CONCAT(CONCAT(a,':',b) ORDER BY a) AS rows_seen FROM t_bug;
-- actual:   1:110,2:20
-- expected: reject like ordinary UPDATE, or update both current rows: 1:110,2:120
```

Controls from the same run:

```text
ordinary UPDATE after SET TRANSACTION READ ONLY AS OF TIMESTAMP:
  ERROR 1105 only support read-only statement during read-only staleness transactions

BATCH UPDATE without tx_read_ts:
  number of jobs = 2
  rows = 1:110,2:120

BATCH UPDATE with tx_read_ts:
  AS OF control = 1:10
  number of jobs = 1
  rows = 1:110,2:20
```

`ADMIN CHECK TABLE` passed for all tables, so this is not storage corruption; it is a silent
partial-write / wrong-rowset bug in the planning stage of NT-DML.

## Root Cause

`HandleNonTransactionalDML` knows NT-DML is a write operation and must not inherit stale-read
state. It saves and clears `SessionVars.ReadStaleness`:

```text
pkg/session/nontransactional.go:80-82
  originalReadStaleness := se.GetSessionVars().ReadStaleness
  sessVars.ReadStaleness = 0
```

But it does not clear or reject the transaction read timestamp set by
`SET TRANSACTION READ ONLY AS OF TIMESTAMP`.

Later, `buildShardJobs` runs the split-range SELECT through `se.Execute`:

```text
pkg/session/nontransactional.go:467-468
  // NT-DML is a write operation, and should not be affected by read_staleness ...
  rss, err := se.Execute(ctx, selectSQL)
```

The stale-read processor still sees `TxnReadTS`:

```text
pkg/sessiontxn/staleread/processor.go:233-257
  txnReadTS := p.sctx.GetSessionVars().TxnReadTS.UseTxnReadTS()
  ...
  if txnReadTS > 0 { return p.setEvaluatedTS(txnReadTS) }
```

So the job builder enumerates only rows visible at the stale timestamp. The subsequent split DML
jobs then commit those ranges and report success.

## Fix Direction

At NT-DML entry, either:

- reject any pending `TxnReadTS` / `SET TRANSACTION READ ONLY AS OF TIMESTAMP` state, matching the
  ordinary DML behavior; or
- save and clear all stale-read inputs before deriving split ranges, not only `ReadStaleness`.

The first option is less surprising because `SET TRANSACTION READ ONLY AS OF TIMESTAMP` explicitly
asks for a read-only stale transaction.

## Method Lesson

This is S23: stale transaction input leak into a split-range planner.

The useful AI move was not to enumerate transaction isolation levels. It was to read the source
comment "NT-DML should not be affected by read_staleness" and ask whether that code cleared every
state channel that can produce stale reads. `ReadStaleness` was only one input; `TxnReadTS` was the
hidden sibling input.

Pause gate: do not fuzz all BATCH syntaxes. Reopen only for another stale input channel, a stronger
consequence such as DELETE/INSERT-SELECT data loss, or fix validation.
