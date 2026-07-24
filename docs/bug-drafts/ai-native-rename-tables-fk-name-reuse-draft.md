# Multi-table RENAME can silently bind a foreign key to the wrong parent

Status: confirmed as remote `found_bug id2820003` on TiDB master
`231dad5225f0d3c9cf38d4ab7ebc03a5326785c7`. A local unit-level RED and a real-TiKV RED reproduce
the defect; a narrow counterfactual fix turns the same test GREEN. Severity is high because one
successful DDL permanently changes which table authorizes and blocks child writes.

## Production trigger

- `workload`: a schema migration rotates table names in one atomic `RENAME TABLE`, typically while
  promoting a replacement table and retaining an archive or staging table.
- `required shape`: the referenced parent moves through an intermediate name, then another physical
  table receives that same intermediate name later in the statement.
- `example`: `p1 -> tmp`, `p2 -> p1`, `tmp -> p2`, `p3 -> tmp`. The original `p1` object ends as
  `p2`; the original `p3` object ends as `tmp`.
- `settings`: foreign keys enabled and default metadata locking enabled. The validated environment
  reported `tidb_enable_foreign_key=1` and `tidb_enable_metadata_lock=1`.
- `topology`: one TiDB and one real TiKV are sufficient. No concurrency, failpoint, node failure,
  retry, or non-default transaction setting is involved.
- `frequency`: the name-rotation shape is specific. It is plausible in generated migration,
  blue-green table replacement, and archive rotation workflows that combine several renames into
  one statement.

## Reproduction

```sql
SET GLOBAL tidb_enable_foreign_key = 1;
DROP DATABASE IF EXISTS ai_rename_fk;
CREATE DATABASE ai_rename_fk;
USE ai_rename_fk;

CREATE TABLE p1 (id INT PRIMARY KEY);
CREATE TABLE p2 (id INT PRIMARY KEY);
CREATE TABLE p3 (id INT PRIMARY KEY);
CREATE TABLE c1 (
  id INT PRIMARY KEY,
  pid INT,
  INDEX (pid),
  CONSTRAINT fk_c1 FOREIGN KEY (pid) REFERENCES p1(id)
);
INSERT INTO p1 VALUES (1);
INSERT INTO p3 VALUES (3);
INSERT INTO c1 VALUES (1, 1);

RENAME TABLE p1 TO tmp, p2 TO p1, tmp TO p2, p3 TO tmp;

SELECT referenced_table_name
FROM information_schema.referential_constraints
WHERE constraint_schema='ai_rename_fk'
  AND table_name='c1'
  AND constraint_name='fk_c1';

INSERT INTO c1 VALUES (3, 3);
DELETE FROM p2 WHERE id=1;

SELECT c.id, c.pid,
       EXISTS(SELECT 1 FROM p2 WHERE p2.id=c.pid) AS correct_parent_exists,
       EXISTS(SELECT 1 FROM tmp WHERE tmp.id=c.pid) AS bound_parent_exists
FROM c1 AS c ORDER BY c.id;

ADMIN CHECK TABLE c1;
```

Observed:

```text
referenced_table_name = tmp

id  pid  correct_parent_exists  bound_parent_exists
1   1    0                      0
3   3    0                      1

ADMIN CHECK TABLE c1 = success
```

The complete reusable probe, including a normal single-rename control, is
`scaffolds/tidb-tests/ai_native_rename_tables_fk_name_reuse.sql`.

## Expected result

The foreign key follows the referenced table object. The original `p1` object ends as `p2`, so the
constraint must reference `p2`. The existing child `(1,1)` remains valid, inserting `(3,3)` fails,
and deleting `p2.id=1` is blocked.

## Actual result

The constraint remains attached to the name `tmp`. That name belongs to the original `p3` object at
statement completion:

- the existing valid child becomes an orphan immediately after successful DDL;
- a row accepted by the wrong parent can be inserted;
- the correct parent row can be deleted;
- `ADMIN CHECK TABLE` remains green.

## Source chain

- `pkg/ddl/table.go:849-898` processes every member of `RenameTableInfos` sequentially while sharing
  one `foreignKeyHelper`.
- `pkg/ddl/table.go:957-996` discovers child foreign keys through
  `InfoSchema.GetTableReferredForeignKeys(oldSchema, oldTable)`.
- That InfoSchema is the pre-statement name graph. After `p1 -> tmp`, the loaded child metadata says
  `tmp`, but the snapshot has no referred-FK entry under that newly created name.
- During `tmp -> p2`, `len(referredFKs)==0` returns early. The modified child table held by
  `foreignKeyHelper.loaded` is never treated as the current reference graph.
- The later `p3 -> tmp` makes the stale name resolve to a different existing table.

## Counterfactual

A diagnostic patch first updates matching foreign keys already present in `foreignKeyHelper.loaded`
and persists those loaded tables even when the old InfoSchema lookup is empty. The exact RED test
then passes:

```text
reused_temporary_name PASS
single_rename_control PASS
```

This isolates the stale lookup owner. A production fix should model the whole rename batch by table
identity or maintain an evolving reference graph, then persist each affected child once.

## Evidence

- Unit RED: `assets/store/logs/ddl-rename-fk-reused-name-red-20260725.log`
- Real-TiKV RED: `assets/store/logs/ddl-rename-fk-reused-name-realtikv-red-20260725.out`
- Real-TiKV TiDB log:
  `assets/store/logs/ddl-rename-fk-reused-name-realtikv-tidb-20260725.log`
- Counterfactual GREEN:
  `assets/store/logs/ddl-rename-fk-reused-name-local-fix-green-20260725.log`
- Tested binary SHA-256:
  `32ae35f357231570a27597db7c7c4c776cdc1bbff646bea08fa58f2fb72c66a9`
