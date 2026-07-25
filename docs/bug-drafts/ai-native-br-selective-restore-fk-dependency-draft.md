# BR selective table restore can publish an FK child without its parent

Status: confirmed on official nightly BR and real TiKV; not filed upstream.

## Summary

`br restore table` can restore an enforced foreign-key child while excluding its referenced parent.
BR reports success and validates the selected table checksum. The restored rows are immediately
orphaned, and later writes with foreign-key checks enabled can also commit new orphan rows.

This is a persistent data-integrity failure in a documented single-table recovery workflow. It
needs no source modification, failpoint, concurrency, node fault, or unusual server setting.

## Environment

```text
BR:   v9.0.0-beta.2.pre-2019-ga942e4684f
TiDB: v9.0.0-beta.2.pre-2017-ged2376acc6
Topology: one TiDB, one PD, one real TiKV
tidb_enable_metadata_lock = 1
tidb_enable_foreign_key = 1
foreign_key_checks = 1
```

The relevant restore files are unchanged between the verified BR revision and current
`pingcap/master` `05b396fb6636`.

## Minimal reproduction

Create a valid parent/child graph and back up the database:

```sql
CREATE DATABASE br_fk_demo;
USE br_fk_demo;
CREATE TABLE p(id INT PRIMARY KEY);
CREATE TABLE c(
  id INT PRIMARY KEY,
  pid INT,
  INDEX(pid),
  CONSTRAINT fk_cp FOREIGN KEY(pid) REFERENCES p(id)
);
INSERT INTO p VALUES (1);
INSERT INTO c VALUES (1,1);
```

```bash
br backup db \
  --pd 127.0.0.1:2379 \
  --db br_fk_demo \
  --storage local:///tmp/br-fk-demo
```

Drop the database and restore only the child:

```sql
DROP DATABASE br_fk_demo;
```

```bash
br restore table \
  --pd 127.0.0.1:2379 \
  --db br_fk_demo \
  --table c \
  --storage local:///tmp/br-fk-demo \
  --checksum
```

BR reports:

```text
success in validating checksum
Table Restore success summary
```

Inspect the result and perform an ordinary invalid insert:

```sql
SHOW TABLES FROM br_fk_demo;
SHOW CREATE TABLE br_fk_demo.c;
INSERT INTO br_fk_demo.c VALUES (2,999);
SELECT * FROM br_fk_demo.c ORDER BY id;
ADMIN CHECK TABLE br_fk_demo.c;
```

Observed:

```text
tables: c
parent p: absent
fk metadata: fk_cp REFERENCES p(id)
rows: (1,1), (2,999)
INSERT result: success
ADMIN CHECK TABLE: success
```

As a reference, creating the same child directly without the parent fails with error `1824`.

## Matched control

Drop the database again and restore the complete database from the same backup:

```bash
br restore db \
  --pd 127.0.0.1:2379 \
  --db br_fk_demo \
  --storage local:///tmp/br-fk-demo \
  --checksum
```

Both tables are present, the orphan count is zero, and `INSERT INTO c VALUES (2,999)` fails with
error `1452`.

## Expected behavior

Before schema publication, BR should either:

1. include every referenced table required by an enforced FK and report the expanded restore scope;
2. reject the selective restore with a clear dependency error.

A successful restore must not publish an enforced FK graph with missing vertices.

## Root cause

`filterRestoreFiles` applies the public table filter as exact table-name membership and does not
close `TableInfo.ForeignKeys` dependencies.

`BRIECreateTable` and `BRIECreateTables` then set the internal session's `ForeignKeyChecks` to
false. That bypass is necessary to create a complete multi-table restore batch without ordering
problems, but its proof obligation is that the batch actually contains the referenced tables.

The checksum validates the selected table's physical KVs. It does not validate referential closure.

In short:

```text
P: this table belongs to a BR batch
Q: the batch contains every enforced dependency
action: disable FK validation and publish the selected TableInfo
```

The selector proves `P` but never proves `Q`.

## Production trigger

The public snapshot guide documents restoring one table with `br restore table`. A normal recovery
operator can use that command after an accidental table drop, when building a partial recovery
cluster, or when migrating a subset of a database. If the selected table is an FK child and its
parent is not already present with matching data, BR silently certifies an invalid result.

## Related issues

- `#65175` covers runtime DML enforcement after a parent table is missing. It does not prevent BR
  from restoring already-orphaned rows.
- `#65256` contains an unchecked suggestion for a child-only PiTR log-restore case. This result uses
  snapshot `restore table`, an existing FK in snapshot metadata, and a separate schema-publication
  path.

The restore selector root remains unclosed on current master.
