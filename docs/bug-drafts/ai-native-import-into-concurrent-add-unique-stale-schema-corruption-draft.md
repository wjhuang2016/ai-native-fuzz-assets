# Classic IMPORT INTO can report success with an empty unique index after concurrent ADD INDEX

Status: confirmed on current master with one TiDB and one real TiKV; no exact upstream issue found.

## Summary

Classic `IMPORT INTO` can keep an obsolete target `TableInfo` while another session adds a unique
index to the empty target table. The DDL finishes before the import acquires `TableModeImport`, but
the import workers still encode rows with the old schema.

Both operations report success. The imported records are durable, the new unique index has no
entries, and the default required checksum reports `checksum pass`. A later ordinary insert can
reuse an imported unique value and create two rows that violate the public unique constraint.
`ADMIN CHECK TABLE` reports error 8223.

## Production trigger

This can happen during a normal bulk-load workflow:

1. an empty target table is ready for import;
2. `IMPORT INTO` resolves the table, then spends time listing objects, detecting formats, sampling
   compressed files, estimating size, and running prechecks;
3. an independent schema deployment adds an index to the same empty table during that preparation;
4. the DDL finishes before `IMPORT INTO` switches the table to Import mode.

A large object-storage prefix, many files, or ordinary object-store latency makes the interval
large. The reproduced unique-index case does not require a TiDB/TiKV failure, retry, cancellation,
multiple TiDB nodes, MDL off, a nondefault SQL mode, or an error-injection failpoint.

## Environment

```text
TiDB master: 05b396fb6636f73b3bc06b09107cf43f2c725c35
Topology:    one in-process TiDB, one PD, one real TiKV
Kernel:      Classic
MDL:         enabled
sql_mode:    default strict mode
checksum:    required (default)
```

## Production-shaped reproduction

The real-TiKV test creates one matching CSV and 60,000 unrelated files in the same directory. This
models normal object discovery without changing TiDB behavior. Session A starts `IMPORT INTO`;
250 ms later session B runs:

```sql
ALTER TABLE t ADD UNIQUE INDEX kv(v);
```

The natural timing test passed three consecutive runs:

```text
go test -tags=intest ./tests/realtikvtest/importintotest \
  -run '^TestImportInto/TestAINativeImportIntoAddIndexDuringNaturalFileDiscovery$' \
  -count=3 -timeout 15m

ok github.com/pingcap/tidb/tests/realtikvtest/importintotest 53.555s
```

The complete test is stored at
`scaffolds/tidb-tests/ai_native_import_into_schema_claim_race_test.go`.

## Observed RED

The source file contains:

```csv
1,101
2,102
3,103
```

After both SQL statements return successfully:

```sql
SELECT id, v FROM t USE INDEX() ORDER BY id;
-- 1 101
-- 2 102
-- 3 103

SELECT id, v FROM t FORCE INDEX(kv) WHERE v >= 0 ORDER BY id;
-- empty
```

The default checksum path logs a current index with zero KVs and still passes:

```text
indexID=1  totalKvs=0
indexID=-1 totalKvs=3
checksum pass
```

The missing unique entries make the public constraint ineffective for imported values:

```sql
INSERT INTO t VALUES (4, 101);
-- success

SELECT id, v FROM t USE INDEX() ORDER BY id;
-- 1 101
-- 2 102
-- 3 103
-- 4 101

ADMIN CHECK TABLE t;
-- ERROR 8223: data inconsistency in table: t, index: kv, handle: 3 ...
```

The import job status is `finished`.

## Exact scheduler proof

A second test invokes the same ordinary `ADD UNIQUE INDEX` immediately after
`importer.NewImportPlan` captures the target metadata. The callback is only a deterministic
scheduler barrier; it does not inject an error or alter data. It reproduces the same terminal and
persistent state.

```text
TestAINativeImportIntoAddIndexAfterTargetResolution: PASS
```

## Matched GREEN

Move only the same `ADD UNIQUE INDEX` before `IMPORT INTO` planning:

```text
IMPORT job:       finished
record scan:      1:101, 2:102, 3:103
unique-index scan:1:101, 2:102, 3:103
duplicate insert: ERROR 1062
ADMIN CHECK TABLE:pass
```

This proves that the data, index definition, and import engine are valid. The failure depends on
the schema transition between target resolution and TableMode acquisition.

## Root cause

The lifecycle is:

```text
resolve target and copy tbl.Meta() into Plan
  -> discover files and estimate resources
  -> check active import / empty table / CDC and PiTR
  -> create import job
  -> ALTER TABLE MODE Import
  -> submit workers carrying the old Plan.TableInfo
```

`IMPORT INTO` deliberately skips statement MDL. `TableModeImport` blocks later schema changes, but
its acquisition does not atomically verify that the current table schema is still the one captured
by the plan. It therefore protects only the future and cannot close the gap before acquisition.

The default checksum is self-referential. The local expected checksum comes from KVs encoded with
the obsolete schema. The remote checksum sees the current index, but a completely missing index
contributes zero KVs, zero bytes, and zero checksum, so the row-only totals still match.

## Expected behavior

An import may begin irreversible encoding only if TableMode acquisition atomically validates the
target identity and schema generation used by the import plan. If the schema changed, the import
must abort before ingest or rebuild all schema-dependent state under the acquired fence.

## Fix direction

Bind the TableMode transition to an expected schema token, such as table ID plus schema version or
an equivalent TableInfo generation. In one atomic owner operation:

1. verify that the current target schema matches the plan;
2. transition the table from Normal to Import mode;
3. publish the task only after that claim succeeds.

As defense in depth, the terminal validator should check closure against every public index in the
current schema instead of allowing an entirely absent index group to contribute zero.

## Impact and severity

This is silent persistent corruption under ordinary successful operations and default validation.
Queries can return different rowsets by access path, and a public unique constraint can accept
duplicate business keys. Repair requires rebuilding the affected index and identifying any
duplicates accepted after import. The consequence is critical; the bug database uses its existing
`high` severity taxonomy.

## Dedup

Post-RED GitHub searches covered `IMPORT INTO` with `ADD INDEX`, `schema change`,
`data inconsistency`, and `missing index`, including open and closed issues. Issue #69798 is an
empty umbrella and contains no direct bug. No issue with this stale-schema/TableMode claim root was
found.
