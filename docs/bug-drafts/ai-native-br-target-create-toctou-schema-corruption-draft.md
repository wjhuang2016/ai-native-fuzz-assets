# BR target-existence TOCTOU can restore into an incompatible table

Status: confirmed on official nightly BR and real TiKV; not filed upstream.

## Summary

BR checks that restore targets do not exist, but the check is not bound to table creation. If a
normal DDL creates the target name after the precheck, BR's internal `OnExistIgnore` treats that
foreign table as a successful create. BR then maps backup KVs into it using weak table and index
names.

With a backup index `uk(a)` and a concurrently created target index `uk(b)`, BR exits 0, validates
checksum, and reports `Table Restore success`. The target index physically contains `a` values but
is planned as an index on `b`. Point queries can return rows that fail their own predicates, and
ordinary DML can update the wrong row.

## Environment

```text
BR:   v9.0.0-beta.2.pre-2019-ga942e4684f
TiDB: v9.0.0-beta.2.pre-2017-ged2376acc6
Topology: one TiDB, one PD, one real TiKV
Checkpoint: enabled by default
tidb_enable_metadata_lock = 1
```

No source patch, failpoint, process pause, node fault, or network/disk fault is required. The
relevant restore files are unchanged on current `pingcap/master` `05b396fb6636`.

## Minimal reproduction

Prepare the backup:

```sql
CREATE DATABASE br_schema_race;
USE br_schema_race;
CREATE TABLE t(
  id INT PRIMARY KEY,
  a INT NOT NULL,
  b INT NOT NULL,
  UNIQUE KEY uk(a)
);
INSERT INTO t VALUES (1,10,100),(2,20,200);
```

```bash
br backup db \
  --pd 127.0.0.1:2379 \
  --db br_schema_race \
  --storage local:///tmp/br-schema-race
```

Drop only the table, start `br restore table`, and wait until the default snapshot-restore
checkpoint schema appears. At that point the target-existence precheck has passed, but BR has not
created `t` yet:

```sql
DROP TABLE br_schema_race.t;
```

```bash
br restore table \
  --pd 127.0.0.1:2379 \
  --db br_schema_race \
  --table t \
  --storage local:///tmp/br-schema-race \
  --checksum
```

During checkpoint initialization, run an ordinary schema deployment:

```sql
CREATE TABLE br_schema_race.t(
  id INT PRIMARY KEY,
  a INT NOT NULL,
  b INT NOT NULL,
  UNIQUE KEY uk(b)
);
```

BR reports:

```text
success in validating checksum
Table Restore success summary
```

## Strong oracle

```sql
SHOW CREATE TABLE br_schema_race.t;
SELECT * FROM br_schema_race.t ORDER BY id;

EXPLAIN
SELECT /*+ USE_INDEX(t,uk) */ *
FROM br_schema_race.t
WHERE b=10;

SELECT /*+ USE_INDEX(t,uk) */
       id,a,b,(b=10) AS predicate_holds
FROM br_schema_race.t
WHERE b=10;

SELECT /*+ USE_INDEX(t,uk) */ *
FROM br_schema_race.t
WHERE b=100;

ADMIN CHECK TABLE br_schema_race.t;
```

Observed in two independent runs:

```text
SHOW CREATE:       UNIQUE KEY uk(b)
table rows:        (1,10,100), (2,20,200)
WHERE b=10:        returns (1,10,100), predicate_holds=0
WHERE b=100:       returns no row
ADMIN CHECK:       error 8223, index value 10 != record value 100
```

The first RED was lifted through ordinary DML:

```sql
UPDATE /*+ USE_INDEX(t,uk) */ br_schema_race.t
SET a=999
WHERE b=10;
```

It returned success with one affected row and changed `(1,10,100)` into `(1,999,100)`, even though
that row does not satisfy `b=10`.

## Matched controls

1. Create the incompatible table before BR starts. The precheck rejects the restore with
   `ErrTablesAlreadyExisted` before any ranges are ingested.
2. Do not run the concurrent `CREATE TABLE`. BR restores the original `uk(a)` schema,
   `ADMIN CHECK TABLE` passes, and the same `UPDATE ... WHERE b=10` affects zero rows.

## Root cause

The safety proof is split across four owners:

1. `checkTableExistence` observes that the name is absent.
2. `BRIECreateTables` later uses `BatchCreateTableWithInfo(... OnExistIgnore)`.
3. `SnapClient.createTables` reacquires the target with `GetTableSchema` by name.
4. `GetIndexIDMap` maps old and new indexes when their names match.

Only `IsCommonHandle` equality is checked before physical rewrite rules are generated.

```text
P: target name was absent during precheck
Q: the table later found under that name is the exact BR-created, backup-compatible identity
action: map backup physical artifacts into the reacquired table and publish success
```

`OnExistIgnore` allows another actor to invalidate `P`, while weak-name reacquisition silently
asserts `Q`.

## Expected behavior

BR must atomically reserve the target name or fail if its create sees an existing object. The table
ID used for rewrite must be the exact identity created by BR.

If idempotent reuse is required for resume, BR must validate a full restore-relevant schema
fingerprint before ingest, including columns, types, defaults, generated expressions, primary
handle, indexes, constraints, and special table metadata.

## Production trigger

A selective restore can overlap application deployment automation or a manual schema migration in
a recovery cluster. The default checkpoint initialization gives a seconds-long check-to-create
window even for a two-row table. A newer deployment can legitimately reuse an index name while
moving it to another column, which is enough to produce silent corruption.

## Related history

`#35215`, `#42893`, and `#55087`, plus merged PR `#55044`, establish that restoring into an existing
table is unsafe and add the current precheck. They cover a table that exists before the check. This
RED invalidates the absence proof after the check and reaches a different identity through
`OnExistIgnore`; post-RED searches found no exact concurrent root.
