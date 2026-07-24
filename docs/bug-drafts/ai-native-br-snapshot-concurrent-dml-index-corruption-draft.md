# BR snapshot restore can report success with a persistently corrupted unique index

Status: confirmed on current nightly with real TiKV. Remote bug DB: `found_bug id2880003`,
high severity / critical data-integrity consequence.

## Summary

An ordinary table snapshot restore creates its target in `TableModeNormal`. An application can write
the target after BR creates it but before BR ingests the backup SSTs.

If the application writes an existing primary key with a different unique-index value, BR preserves
the older backup MVCC record and index keys. The newer application record wins at the clustered key,
but the old unique-index key has no matching delete. BR exits successfully while the table has two
unique-index entries for one row.

Consequences observed on a fresh connection:

- a point lookup by the stale backup unique key returns a row that does not satisfy the predicate;
- `COUNT(*)` over the unique index overcounts;
- primary and unique-index scans disagree;
- `ADMIN CHECK TABLE` reports data inconsistency.

## Production trigger

The trigger is a long-running table or database restore overlapping application traffic:

1. BR creates the target table.
2. The table is visible as `Normal`, so ordinary DML succeeds.
3. An application inserts or updates a key that also exists in the backup.
4. BR later ingests the older physical record and index keys and reports success.

Large restores, slow object storage, a configured restore rate limit, or resource contention widen
the window naturally. The strongest RED used the official BR binary, `--ratelimit 1`, MDL enabled,
and one normal `INSERT`. It used no failpoint, source modification, process pause, node failure, or
concurrent DDL.

## Reproduction

The reusable end-to-end harness is
[`ai_native_br_concurrent_dml_repro.sh`](../../scaffolds/top-level/ai_native_br_concurrent_dml_repro.sh).
Its essential schedule is:

```sql
-- The backup contains this logical row.
INSERT INTO t VALUES (1, 100001, 'backup-row');

-- Start BR restore after dropping the target.
-- As soon as BR creates an empty target:
SELECT TIDB_TABLE_ID, TIDB_TABLE_MODE
FROM information_schema.tables
WHERE table_schema = 'brgenbig' AND table_name = 't';
-- TIDB_TABLE_MODE = Normal

INSERT INTO brgenbig.t
VALUES (1, 900000000, 'app-write-during-restore');
```

Wait for BR to print `Table Restore success summary`, then run:

```sql
SELECT COUNT(*), SUM(id) FROM brgenbig.t IGNORE INDEX(u);
SELECT COUNT(*), SUM(id) FROM brgenbig.t USE INDEX(u);

SELECT id, u, payload, (u = 100001) AS predicate_holds
FROM brgenbig.t USE INDEX(u)
WHERE u = 100001;

ADMIN CHECK TABLE brgenbig.t;
```

## Actual result

BR reported:

```text
Table Restore success summary
total-kv=256000
total-kv-size=74.62MB
restore-files=36.434177459s
```

The post-restore oracle returned:

```text
PRIMARY: count=128000, sum(id)=8192064000
INDEX u: count=128001, sum(id)=8192064001

id=1, u=900000000, payload=app-write-during-restore, predicate_holds=0

ERROR 8223: index value 100001 differs from record value 900000000
```

A raw prefix scan found `128000` record keys and `256001` total table keys. The same backup and
restore parameters without the concurrent `INSERT` produced `128000/128000`, matching sums, and a
successful `ADMIN CHECK TABLE`.

## Expected result

BR should keep a physical restore target inaccessible to DML and incompatible DDL until ingest and
validation finish. If concurrent target writes are supported, restore must merge them while
preserving record/index bijection. A successful restore must never publish a stale index key.

## Root cause

- `br/pkg/task/restore.go:createDBsAndTables` creates ordinary snapshot targets without setting
  `TableModeRestore`.
- `setTablesRestoreModeIfNeeded` applies that protection only to explicit-filter PiTR.
- `br/pkg/restore/snap_client/client.go:createTables` freezes the created `TableInfo` and physical
  rewrite rules.
- Normal snapshot restore passes `newTS=0`; `GetRewriteRules` therefore preserves backup MVCC
  timestamps.
- `RestoreTables` downloads and ingests SSTs without a target write fence or write-epoch
  revalidation.

TiDB already defines the intended safety primitive: `TableModeRestore` rejects `SELECT`, DML,
`TRUNCATE`, `DROP`, and `ALTER` against the protected table.

## Fix direction

Create or transition every physical snapshot-restore target into `TableModeRestore` before it
becomes visible. Hold the mode through SST ingest, checksum, and metadata publication, then
atomically return it to `Normal` on success. Existing targets need an equivalent write/generation
lease. Revalidate table ID, mode, and write epoch immediately before the first irreversible ingest.

## Scope and deduplication

Verified with TiDB nightly `ed2376acc6`, BR nightly `a942e4684f`, real TiKV, Classic kernel, MDL ON,
and default `--checksum=false`. Post-RED searches found no exact TiDB issue, PR, or `found_bug` root.
The related `id2850003` root retires a target generation during NextGen `IMPORT INTO`; this root
corrupts one live generation because historical physical ingest is allowed to overlap logical DML.
