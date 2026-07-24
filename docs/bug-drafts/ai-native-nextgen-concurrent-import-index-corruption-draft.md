# Concurrent NextGen IMPORT INTO jobs can leave persistent row/index corruption

Status: confirmed on current master with real TiKV. Proposed bug DB ID: `id2940003`.
Catalog severity: high; consequence: critical persistent data corruption.

## Summary

NextGen `IMPORT INTO` checks whether the target already has an active import job, then creates the
new job later. The read and the ownership claim are not atomic. Two requests can both observe zero
active jobs and both start bulk ingest against the same empty table.

For a table without a clustered primary key, both importers allocate the same hidden row handles
starting at 1. Their record and secondary-index KV groups are ingested independently. One set of
record values wins for handles 1 and 2, while index entries from both inputs remain.

Both jobs eventually report `failed` because checksum detects the combined state. The physical
writes are already durable and are not rolled back. A normal table scan and a forced unique-index
scan disagree, and `ADMIN CHECK TABLE` reports error 8223.

## Production trigger

The production-shaped trigger is small:

1. Run NextGen with a user keyspace and the normal DXF/CSE import worker.
2. Create an empty table without a primary key and with a secondary unique index.
3. Submit two `IMPORT INTO ... WITH DETACHED` statements for that table at nearly the same time.
4. Let both jobs finish.

The input files do not need duplicate logical values. The reproduced files contained `a1,a2` and
`b1,b2`. Common sources of the duplicate submission include two operators starting the same load,
an orchestrator retry racing the original request, or two pipeline workers claiming the same target.

The final current-master run used ordinary concurrent SQL with no product source modification,
failpoint, process pause, node failure, or network/disk fault. MDL remained enabled. Natural
concurrency hit the race on three consecutive runs.

## Reproduction

Create two object-store files:

```text
a.csv: a1
       a2

b.csv: b1
       b2
```

Create the target:

```sql
CREATE DATABASE concurrent_import;
USE concurrent_import;
CREATE TABLE t (
  v VARCHAR(100),
  UNIQUE KEY uk(v)
);
```

From two sessions, submit these statements concurrently:

```sql
IMPORT INTO t
FROM 's3://bucket/a.csv?...'
WITH DETACHED, cloud_storage_uri='s3://bucket/sort-a?...';
```

```sql
IMPORT INTO t
FROM 's3://bucket/b.csv?...'
WITH DETACHED, cloud_storage_uri='s3://bucket/sort-b?...';
```

After both jobs reach a terminal state:

```sql
SELECT id, status, error_message
FROM mysql.tidb_import_jobs
WHERE table_schema = 'concurrent_import' AND table_name = 't'
ORDER BY id;

SELECT _tidb_rowid, v FROM t ORDER BY _tidb_rowid;
SELECT _tidb_rowid, v FROM t FORCE INDEX(uk) ORDER BY v;
ADMIN CHECK TABLE t;
```

The drop-in real-TiKV probe is
[`ai_native_nextgen_concurrent_import_data_corruption_test.go`](../../scaffolds/tidb-tests/ai_native_nextgen_concurrent_import_data_corruption_test.go).

## Actual result

One natural current-master run returned:

```text
job 180001: failed, ErrChecksumMismatch, remote total_kvs=6 vs local total_kvs=4
job 180002: failed, ErrChecksumMismatch, remote total_kvs=6 vs local total_kvs=4

table scan:
1 b1
2 b2

forced unique-index scan:
1 a1
2 a2
1 b1
2 b2

ADMIN CHECK TABLE:
ERROR 8223, handle 1 has index value a1 but record value b1
```

The winner reversed on another run: the table contained `a1,a2`, while the index still contained
all four entries. The corrupted shape remained the same.

The matched single-import control finished successfully with two table rows, two index entries, and
a successful `ADMIN CHECK TABLE`.

## Expected result

At most one active import owner may exist for a target table. The ownership claim must be atomic
with admission. One concurrent request should be rejected before any bulk write starts.

A failed import must not leave a table whose record and index access paths disagree.

## Root cause

- `LoadDataController.checkRequirements` calls `GetActiveJobCnt` and accepts `count=0`.
- More prechecks run after that read, which naturally widens the race window.
- `CreateJob` later inserts a pending row into `mysql.tidb_import_jobs`.
- The table has no uniqueness constraint or lock that turns "zero active jobs" into an atomic
  per-target ownership claim.
- Classic submission sets `TableModeImport`; the NextGen user-keyspace path skips it.
- Both accepted plans allocate hidden handles 1 and 2 from the same empty target.
- Data and index KV groups are ingested separately. Competing record keys overwrite by MVCC order,
  while distinct unique-index values from both jobs survive.
- Post-process checksum detects the merged state only after irreversible ingest and cannot roll it
  back.

The source already documents that two concurrent imports can both pass the precheck. This result
closes the missing consequence proof: the race reaches persistent relational corruption under
natural concurrency.

## Fix direction

Acquire a durable per-target admission lease atomically with job creation. The claim should be keyed
by keyspace and stable table identity, enforce uniqueness for active states, and remain held through
all irreversible ingest and post-processing steps.

Possible implementations include a dedicated active-owner row with a unique target key, a stable
metadata row locked in the same transaction, or a NextGen-compatible table mode/lease. A second
request must fail before planning or SST ingest. Terminal cleanup must release the claim, and worker
recovery must verify that the claim still belongs to its job.

Checksum should remain a defense-in-depth oracle; it cannot serve as admission or rollback.

## Scope and deduplication

Verified at TiDB `231dad5225f0d3c9cf38d4ab7ebc03a5326785c7` with NextGen TiKV/CSE
`ce46fc5067`, real PD/TiKV, user and SYSTEM keyspaces, object storage, and MDL enabled.

Post-RED searches of TiDB issues and the remote `found_bug` table found no exact root. This differs
from:

- `id1590002`, where one standalone importer commits data before a later index-engine error;
- `id2850003`, where one NextGen job writes a retired table generation;
- `id2880003`, where BR overlaps a normal logical writer without a write fence.

This root is the missing atomic singleton-owner claim that permits two healthy import jobs to ingest
the same live target concurrently.
