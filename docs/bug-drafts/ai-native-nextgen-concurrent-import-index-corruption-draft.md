# Concurrent same-table IMPORT INTO jobs can leave persistent row/index corruption

Status: confirmed on current master with real TiKV. Remote bug DB ID: `id2940003`.
Catalog severity: high; consequence: critical persistent data corruption.

## Summary

`IMPORT INTO` checks whether the target already has an active import job, then creates the new job
later. The read and the ownership claim are not atomic. Two requests can both observe zero active
jobs and both start bulk ingest against the same empty table.

NextGen skips Classic's `TableModeImport` guard. Classic sets the guard, but table mode carries no
owner identity and explicitly permits `Import -> Import`. Two sessions that race before either
schema change is visible can therefore both publish jobs. A later sequential request is blocked,
which makes this an admission race rather than a generally missing check.

For a table without a clustered primary key, both importers allocate the same hidden row handles
starting at 1. Their record and secondary-index KV groups are ingested independently. One set of
record values wins for handles 1 and 2, while index entries from both inputs remain.

NextGen runs made both jobs report `failed`. Classic runs made one job report `finished` while the
other failed checksum. In both cases the physical writes were already durable and were not rolled
back. A normal table scan and a forced unique-index scan disagree, point lookups can return the
wrong row, and `ADMIN CHECK TABLE` reports error 8223.

## Production trigger

The production-shaped trigger is small:

1. Create an empty table without a primary key and with a secondary unique index.
2. Submit two `IMPORT INTO ... WITH DETACHED` statements for that table at nearly the same time.
3. Use disjoint input files large enough that both accepted jobs naturally overlap physical ingest.
4. Let both jobs reach a terminal state.

The input files do not need duplicate logical values. The reproduced files contained `a1,a2` and
`b1,b2`. Common sources of the duplicate submission include two operators starting the same load,
an orchestrator retry racing the original request, or two pipeline workers claiming the same target.

Classic reproduced with one TiDB, one PD, one real TiKV, MDL enabled, and default import write
speed. The strongest run used 1,000,000 rows per input. NextGen natural concurrency hit the race on
three consecutive runs. None of the production-shaped RED runs used product source modification,
failpoints, process pauses, node failures, or network/disk faults.

## Reproduction

Create two disjoint files:

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

On Classic, local paths are sufficient. From two sessions, submit these statements concurrently:

```sql
IMPORT INTO t FROM '/absolute/path/a.csv' WITH DETACHED, thread=1;
```

```sql
IMPORT INTO t FROM '/absolute/path/b.csv' WITH DETACHED, thread=1;
```

For NextGen, use the equivalent object-store statements:

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
The Classic command-line repro is
[`ai_native_classic_concurrent_import_owner_race.sh`](../../scaffolds/top-level/ai_native_classic_concurrent_import_owner_race.sh).

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

A Classic default-config run returned:

```text
job 8: failed, ErrChecksumMismatch, remote total_kvs=3000000 vs local total_kvs=2000000
job 9: finished, imported_rows=1000000

record scan:       1000000 rows, all from b.csv
forced index scan: 2000000 rows, 1000000 from each file
lookup a0000001:   handle 1, value b0000001
ADMIN CHECK TABLE: ERROR 8223
```

The sequential Classic control rejected the second request with error 8258 while the first job was
running. The first job finished with equal record/index counts and a successful structural check.

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
- NextGen skips `TableModeImport`.
- Classic sets `TableModeImport`, but `TableMode` has no owner ID and `CanTransitionTo` permits
  `Import -> Import`. Two concurrently planned submissions can both publish the same state.
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
metadata row locked in the same transaction, or an owner-bearing table mode/lease. Same-mode
transitions from a different owner must fail. A second request must fail before planning or SST
ingest. Terminal cleanup must release only its own claim, and worker recovery must verify that the
claim still belongs to its job.

Checksum should remain a defense-in-depth oracle; it cannot serve as admission or rollback.

## Scope and deduplication

Verified against TiDB source `231dad5225f0d3c9cf38d4ab7ebc03a5326785c7`. Classic execution used
nightly `ed2376acc6`, with no relevant source diff to current master, one real TiKV, local files,
default import write speed, and MDL enabled. NextGen execution used TiKV/CSE `ce46fc5067`, real
PD/TiKV, user and SYSTEM keyspaces, object storage, and MDL enabled.

Post-RED searches of TiDB issues and the remote `found_bug` table found no exact root. This differs
from:

- `id1590002`, where one standalone importer commits data before a later index-engine error;
- `id2850003`, where one NextGen job writes a retired table generation;
- `id2880003`, where BR overlaps a normal logical writer without a write fence.

This root is the missing atomic singleton-owner claim that permits two healthy import jobs to ingest
the same live target concurrently.
