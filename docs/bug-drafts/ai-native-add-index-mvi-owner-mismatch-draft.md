# id30038: ADD UNIQUE INDEX mis-detects duplicate MVI keys when added with a sibling multi-column index

> 2026-07-03. Confirmed on testbed `8192975` / `fp-tidb`. Inserted into remote
> `found_bug` as id30038. Remote state after insert: `COUNT(*)=66`,
> `COUNT(DISTINCT root_cause_id)=45`.

## Summary

`ALTER TABLE ... ADD UNIQUE INDEX` can falsely reject a valid online DDL when a
multi-valued index is added in the same multi-schema job as a multi-column unique
index and a concurrent DML has already written the new index entries.

The user-visible symptom is a false duplicate-key error:

```text
ERROR 1062 (23000): Duplicate entry '90000' for key 't.u_mvi'
```

There is no logical duplicate. The existing MVI key belongs to the same row and
should be classified as "already written by concurrent DML, skip it".

## Minimal Repro Shape

The failpoint only widens the online-DDL window. It is not the bug trigger.

Session setup:

```sql
SET GLOBAL tidb_enable_dist_task = OFF;
SET GLOBAL tidb_ddl_enable_fast_reorg = OFF;
SET GLOBAL tidb_ddl_reorg_worker_cnt = 1;
```

Failpoint:

```text
PUT /fail/github.com/pingcap/tidb/pkg/ddl/mockBackfillSlow = return(true)
```

Data:

```sql
DROP DATABASE IF EXISTS ai_mvi_owner;
CREATE DATABASE ai_mvi_owner;
USE ai_mvi_owner;

CREATE TABLE t(a INT PRIMARY KEY, b INT, j JSON);
SPLIT TABLE t BETWEEN (0) AND (100000) REGIONS 50;

SET SESSION cte_max_recursion_depth = 100000;
INSERT INTO t
WITH RECURSIVE seq(n) AS (
  SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n < 100000
)
SELECT n, n, CONCAT('[', n, ',', n+1000000, ']') FROM seq;
```

Session A:

```sql
ALTER TABLE t
  ADD UNIQUE INDEX u_mvi((CAST(j AS SIGNED ARRAY))),
  ADD UNIQUE INDEX u_ab(a,b);
```

Session B while Session A is in write reorganization:

```sql
UPDATE t SET b = b + 7 WHERE a = 90000;
```

Observed:

```text
ERROR 1062 (23000): Duplicate entry '90000' for key 't.u_mvi'
```

`ADMIN SHOW DDL JOBS` after rollback:

```text
JOB_ID=16171
JOB_TYPE=add index /* subjob */
SCHEMA_STATE=none
ROW_COUNT=179998
STATE=rollback done
COMMENTS=thread=1, batch_size=32, max_node_count=3
```

The table remains healthy after rollback:

```sql
SHOW INDEX FROM t;       -- only PRIMARY remains
ADMIN CHECK TABLE t;     -- succeeds
SELECT COUNT(*), SUM(a=90000 AND b=90007) FROM t;
-- 100000, 1
```

## Control Cells

| Cell | Concurrent DML | Observed |
| --- | --- | --- |
| Add only `u_mvi((CAST(j AS SIGNED ARRAY)))` | `UPDATE t SET b=b+7,j='[90000,1190000]' WHERE a=90000` | GREEN: DDL succeeds, `ADMIN CHECK` passes |
| Add `u_mvi` plus one-column `u_b(b)` | `UPDATE t SET j='[90000,1190000]' WHERE a=90000` | GREEN: DDL succeeds, `ADMIN CHECK` passes |
| Add `u_mvi` plus two-column `u_ab(a,b)` | `UPDATE t SET b=b+7 WHERE a=90000` | RED: false duplicate on `u_mvi` |
| Add `u_mvi` plus two-column `u_ab(a,b)` | `UPDATE t SET j='[90000,1190000]' WHERE a=90000` | RED/liveness: DDL stayed in write reorganization at `row_count=179998`; after `ADMIN CANCEL DDL JOBS`, client returned `invalid encoded key` |

The controls separate the trigger from generic MVI support and generic
multi-index add-index support. The bad dimension is a multi-valued index whose
generated keys are flattened next to a sibling index with a different metadata
shape.

## Source Chain

- `/Users/bba/pc/tidb/pkg/table/tables/index.go:663-670`:
  `GenIndexKVIter` returns a multi-value iterator when `idxInfo.MVIndex` is true.
- `/Users/bba/pc/tidb/pkg/table/index.go:177-203`:
  a multi-value iterator can emit multiple index key/value pairs for one
  `idxRecord`.
- `/Users/bba/pc/tidb/pkg/ddl/index.go:2606-2646`:
  `batchCheckUniqueKey` flattens all generated unique keys into
  `w.batchCheckKeys`, while keeping only `recordIdx`.
- `/Users/bba/pc/tidb/pkg/ddl/index.go:2661-2670`:
  after `BatchGetValue`, the code recovers the index owner as
  `w.indexes[i%len(w.indexes)]`, where `i` is now the flattened key ordinal.
- `/Users/bba/pc/tidb/pkg/tablecodec/tablecodec.go:1008-1048`:
  `DecodeIndexHandle` uses `idxColLen`; using the sibling `u_ab(a,b)` metadata
  for a one-column MVI key can misdecode the handle or report `invalid encoded
  key`.

## Root Cause

The code proves this:

```text
flattened-key ordinal i modulo number-of-indexes
=> index owner for this generated key
```

That proof is only valid if every `idxRecord` emits exactly one key. MVI breaks
the proof: one logical index record can emit multiple keys. In the red shape,
the second MVI key is classified with the sibling `u_ab(a,b)` metadata, so the
existing key written by concurrent DML is not recognized as the same row.

## Expected Behavior

Online add-index backfill should treat a found key as already written when it
belongs to the same row/handle, even when that key came from a multi-valued
index and the DDL is adding multiple indexes in one job.

## Fix Direction

Carry the owning index ordinal for every flattened generated key. For example,
store it next to `recordIdx` and `distinctCheckFlags`, and use that stored owner
in the found-key duplicate classification. Do not derive per-key ownership from
`flattenedKeyOrdinal % len(indexes)` after `GenIndexKVIter` may emit more than
one key per `idxRecord`.

## Method Lesson

This is the S1 refinement that made the hit fast:

```text
success/concurrent DML path records per-index state
backfill sibling path reconstructs per-key owner from a flattened ordinal
MVI adds a hidden D_dim: one index record can produce N keys
the later duplicate checker trusts the reconstructed owner
```

The high-yield move was not enumerating more index syntaxes. It was asking
whether a flattened generated artifact still carries the owner/type bit that
later code needs for safe-path classification.

