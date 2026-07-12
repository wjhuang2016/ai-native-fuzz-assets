# IMPORT INTO can leave durable rows without secondary indexes after an index-engine terminal error

## Summary

Standalone `IMPORT INTO ... FROM SELECT` imports the record/data engine before it closes and
imports the secondary-index engine. If the index engine fails to close or import after the data
engine has succeeded, the statement returns an error, but the already imported record KVs remain.
The deferred cleanup removes local engine state and cannot roll back those record KVs.

The resulting table is physically inconsistent: a table scan sees rows, a forced secondary-index
scan misses them, and `ADMIN CHECK TABLE` reports error 8223.

Severity: **High**. The trigger is an index-engine terminal failure during standalone import, but
the terminal state is durable row/index corruption rather than a temporary failed statement.

Bug library: `id1590002` (`confirmed`, root cause
`importinto-data-before-index-finalization`).

## Source proof

`pkg/executor/importer/table_import.go` owns two sibling artifacts:

- lines 769-782 close and irreversibly import the data engine;
- lines 784-793 only afterward close and import the index engine;
- lines 729-737 defer `closeAndCleanupEngine` for both opened engines;
- lines 704-715 show that cleanup only closes and removes engine state. It does not undo imported
  TiKV KVs.

The code proves only that the data-engine terminal actions succeeded (`P`), then assumes the
stronger claim that index finalization and post-processing will also succeed (`Q`). The first
irreversible publish happens before that assumption has been proved.

## Minimal fault injection

Insert this test-only failpoint immediately after the successful data-engine import and before
`indexEngine.Close(ctx)`:

```go
failpoint.Inject("mockIndexEngineCloseErrAfterDataImport", func() {
    failpoint.Return(0, errors.New("mock index engine close error after data import"))
})
```

Build TiDB with failpoints enabled and enable:

```text
github.com/pingcap/tidb/pkg/executor/importer/mockIndexEngineCloseErrAfterDataImport=return(true)
```

## Reproduction

```sql
SET GLOBAL tidb_enable_dist_task = OFF;

DROP DATABASE IF EXISTS ai_import_partial;
CREATE DATABASE ai_import_partial;
USE ai_import_partial;

CREATE TABLE src(a INT PRIMARY KEY, b INT);
INSERT INTO src VALUES (1,10),(2,20),(3,30);
CREATE TABLE dst(a INT PRIMARY KEY, b INT, INDEX ib(b));

IMPORT INTO dst FROM SELECT a,b FROM src;
-- ERROR 1105: mock index engine close error after data import

SELECT COUNT(*) FROM dst IGNORE INDEX(ib);
-- 3

SELECT COUNT(*) FROM dst USE INDEX(ib) WHERE b >= 0;
-- 0

ADMIN CHECK TABLE dst;
-- ERROR 8223: data inconsistency in table: dst, index: ib
```

## Controls

1. With the failpoint disabled, the same import succeeds, both scan paths return 3 rows, and
   `ADMIN CHECK TABLE` succeeds.
2. With the existing `mockImportFromSelectErr` fault before `closedDataEngine.Import`, the statement
   returns an error, both scan paths return 0 rows, and `ADMIN CHECK TABLE` succeeds.

This altitude control proves that the corruption is caused by crossing the data-engine durable
boundary before the sibling index artifact is ready, not by generic cleanup or failed imports.

## Observed testbed evidence

Authorized QA testbed 8220955, current source commit `13282a8bd06b` with only the test failpoint:

```text
ERROR 1105: mock index engine close error after data import
red_table  3
red_index  0
ERROR 8223: data inconsistency in table: dst_red2, index: ib, handle: 3

green_table  3
green_index  3

pre_table  0
pre_index  0
```

Evidence log: `logs/0022-import-partial-live-red.log` in the campaign context pack.

## Fix direction

Closing both engines before importing either one removes the demonstrated Close-error window, but
index-engine Import can still fail after record KVs are durable. The complete fix must enforce one
of these contracts:

1. prepare/close every sibling artifact before the first irreversible import, then retain a
   resumable recovery owner until all imports and post-processing complete;
2. ingest record and index artifacts under one recoverable commit/checkpoint protocol; or
3. on any post-data-import failure, reliably resume/repair the missing index artifact rather than
   cleaning away the only recovery source.

A fix test must inject both index Close failure and index Import failure after data import, then
prove that a returned error cannot leave table/index inconsistency.
