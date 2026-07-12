# IMPORT INTO can report success with missing secondary-index entries after writer Close failure

Status: confirmed high severity on current source and authorized testbed `8220955`; remote bug row
`id1770003`.

## User impact

During file `IMPORT INTO`, each chunk writes record and secondary-index KV into private local-engine
writers. `Close` performs the final flush. If an index writer encounters an ordinary disk/SST flush
error at this boundary, TiDB logs the error but can still mark the import job `finished`. With table
checksum disabled or optional, rows become visible without their secondary-index entries. Queries
that use the index silently miss rows, and `ADMIN CHECK TABLE` reports physical inconsistency.

## Source proof

`pkg/executor/importer/engine_process.go` defers both `dataWriter.Close` and `indexWriter.Close`.
Each defer logs `err2`, but neither changes the function result. The function returns only the
earlier `ProcessChunkWithWriter` result.

`pkg/ingestor/ingestctrl/engine.go` shows why this is terminal: `Writer.Close` calls `flush`, then
destroys `kvBuffer` and removes the writer. The caller treats nil from `ProcessChunk` as permission
to close/import the engines and advance the import task.

P: successful chunk processing means all required data and index KV reached their engines.

Q: a writer `Close` failure must fail the chunk and dominate engine import/task success.

F: deferred `Close` errors are logged and discarded, while the failed writer's private buffer is
destroyed.

## Reproduction

Build current TiDB with a test-only one-shot error immediately before `ingestctrl.Writer.Close`
flush. Start TiDB with:

```text
GO_FAILPOINTS='github.com/pingcap/tidb/pkg/ingestor/ingestctrl/aiNativeWriterCloseBeforeFlush=1*return(true)'
```

Create `/tmp/ai-import-writer-close.csv` on the TiDB server:

```text
1,10
2,20
3,30
```

Run:

```sql
SET GLOBAL tidb_enable_dist_task=OFF;
CREATE DATABASE ai_import_writer_close;
USE ai_import_writer_close;
CREATE TABLE red(a INT PRIMARY KEY, b INT, INDEX ib(b));

IMPORT INTO red FROM '/tmp/ai-import-writer-close.csv'
  FORMAT 'csv' WITH THREAD=1, CHECKSUM_TABLE='off';

SELECT COUNT(*) FROM red IGNORE INDEX(ib);
SELECT COUNT(*) FROM red USE INDEX(ib) WHERE b >= 0;
ADMIN CHECK TABLE red;
```

Observed on current source:

```text
IMPORT job status: finished
client exit:       0
Imported_Rows:     3
table scan:        3
index scan:        0
ADMIN CHECK:       ERROR 8223, index ib, handle 3
```

No-fault control is `3/3/ADMIN green`. A one-variable counterfactual that combines deferred Close
errors into `ProcessChunk`'s named return produces `ERROR 1105`, `0/0`, and `ADMIN green` under the
same fault.

## Distinctness

- #69756 / `id1260008`: a data-writer Close error is returned, but sibling index Close is skipped.
- `id1590002`: data-engine KVs are irreversibly imported before a later index-engine terminal error;
  the statement returns an error.
- This root is false success inside per-chunk finalization: the failing writer error is discarded,
  the job finishes, and incomplete engines are published.

Post-RED issue and asset searches found no exact duplicate.

## Fix direction

Make `ProcessChunk` own a named error result and append data/index writer Close errors to it. A fix
test should cover data-only, index-only, and simultaneous Close errors, plus command-level checks
for terminal status, row/index equality, and `ADMIN CHECK TABLE`.
