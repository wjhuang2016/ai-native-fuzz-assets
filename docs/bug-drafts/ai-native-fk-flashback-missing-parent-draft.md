# Draft: FLASHBACK TABLE can restore a child FK without its parent and accept orphan rows (id30016)
> 2026-07-03. Confirmed on testbed 8192975 / fp-tidb. Selector S6 (restore-path container/object bypass), DDL-only lane.

## Minimal Reproduction

```sql
SET @@session.foreign_key_checks = 1;

DROP DATABASE IF EXISTS ai_fk_flashback_child;
CREATE DATABASE ai_fk_flashback_child;
USE ai_fk_flashback_child;

CREATE TABLE p(id INT PRIMARY KEY);
CREATE TABLE c(
  id INT PRIMARY KEY,
  pid INT,
  INDEX idx_pid(pid),
  CONSTRAINT fk_pid FOREIGN KEY(pid) REFERENCES p(id)
);
INSERT INTO p VALUES (1);
INSERT INTO c VALUES (1,1);

-- Baseline: FK enforcement works.
INSERT INTO c VALUES (90,999);
-- ERROR 1452: Cannot add or update a child row

DROP TABLE c;
DROP TABLE p;
FLASHBACK TABLE c;

SHOW CREATE TABLE c;
-- CONSTRAINT `fk_pid` FOREIGN KEY (`pid`) REFERENCES `p` (`id`)

SELECT table_name, referenced_table_name
FROM information_schema.key_column_usage
WHERE table_schema = 'ai_fk_flashback_child'
  AND table_name = 'c'
  AND referenced_table_name IS NOT NULL;
-- c  p

-- Parent table p does not exist, but the orphan child row is accepted.
INSERT INTO c VALUES (2,999);
-- Query OK
```

After recreating the parent table, new invalid inserts start failing again, but the orphan row remains:

```sql
CREATE TABLE p(id INT PRIMARY KEY);
INSERT INTO p VALUES (1);

INSERT INTO c VALUES (3,999);
-- ERROR 1452

SELECT COUNT(*) FROM c WHERE pid = 999;
-- 1

ADMIN CHECK TABLE c;
-- Query OK
```

## User-Visible Symptom

A user can recover a child table whose `SHOW CREATE TABLE` still declares a foreign key to a parent table that no longer exists. During that state, `foreign_key_checks=ON` does not protect child inserts: orphan rows can be written successfully. If the parent table is later recreated, new invalid rows are rejected, but rows written during the missing-parent window stay in the table.

This is visible from SQL only:

- `SHOW CREATE TABLE c` shows `CONSTRAINT fk_pid ... REFERENCES p(id)`.
- `information_schema.key_column_usage` reports `referenced_table_name = p`.
- `INSERT INTO c VALUES (2,999)` succeeds while `p` is absent.
- `EXPLAIN INSERT INTO c VALUES (91,999)` contains no `Foreign_Key_Check` while `p` is absent.
- After `CREATE TABLE p(id INT PRIMARY KEY)`, `EXPLAIN INSERT INTO c VALUES (92,999)` contains `Foreign_Key_Check`, and new orphan inserts fail.

## Probe Result

Probe: `/Users/bba/pc/ai_native_ddl_fk_flashback_missing_parent_probe.py`

```text
FINGERPRINT 8.0.11-TiDB-v8.4.0-this-is-a-placeholder  1  1
CASE fk_flashback_child_without_parent
baseline_invalid_insert_blocked=True
active_tables_after_drop=0
flashback_rc=0 err=
kcu=c p
orphan_insert_while_parent_missing_rc=0 err=
invalid_insert_after_parent_recreate_fk_error=True
valid_insert_after_parent_recreate_rc=0
orphan_rows_after_parent_recreate=1
admin_check_after_orphan_rc=0 err=
EXPLAIN_MISSING_PARENT
Insert_1 N/A root  N/A
EXPLAIN_AFTER_PARENT
Insert_1 N/A root  N/A
└─Foreign_Key_Check_3 0.00 root table:p, index:PRIMARY foreign_key:fk_pid, check_exist
RED flashback_child_without_parent_red
GREEN(triggered) flashback_database_control_green
GREEN(triggered) create_missing_parent_control_green
SUMMARY total=3 findings=1 controls_ok=1
```

The two controls matter:

- `FLASHBACK DATABASE` restores both parent and child, and FK enforcement stays active.
- Ordinary `CREATE TABLE c ... REFERENCES p(id)` with missing `p` fails with `ERROR 1824`, proving the create path has the safe validation that recover skips.

## Source Chain

- `pkg/ddl/create_table.go:81`: normal create-table flow calls `checkTableForeignKeyValidInOwner`.
- `pkg/ddl/foreign_key.go:214-257`: owner-side FK validation resolves referenced parent tables and validates referred-child edges.
- `pkg/ddl/executor.go:1459-1504`: `RecoverTable` checks only target schema existence and same-name table absence before enqueuing `ActionRecoverTable`; it does not run FK validity checks.
- `pkg/ddl/table.go:159-245`: `onRecoverTable` checks GC/safe point, name, and table ID.
- `pkg/ddl/table.go:296-299`: `recoverTable` clones the old `TableInfo` and calls `CreateTableAndSetAutoID`, publishing old `ForeignKeys` verbatim.
- `pkg/planner/core/operator/physicalop/foreign_key.go:453-457`: DML FK check construction returns `nil` when the referenced parent table cannot be found, so the insert plan contains no `Foreign_Key_Check` while the parent is absent.

## Root Cause

`FLASHBACK TABLE` reuses historical `TableInfo` as schema metadata without replaying the create-time FK proof obligation:

```text
P_check:  target DB exists; no same-name table; old table ID is free; GC safe point allows recovery
Q_claim:  old TableInfo can be published as a valid current table
D_dims:   FK referenced parent table still exists; supporting index/columns still valid; FK checks setting
F_effect: recoverTable clones old TableInfo and publishes ForeignKeys directly
```

The missing dimension is parent-reference liveness. When that dimension is false, DML planning silently omits the FK check because the parent table lookup fails.

## Expected Behavior

With `foreign_key_checks=ON`, `FLASHBACK TABLE c` should not publish an FK that cannot be enforced. Acceptable fix contracts:

- fail `FLASHBACK TABLE c` if any referenced parent table is absent;
- or recover the child table with the invalid FK removed or disabled, if product semantics explicitly choose that behavior;
- and in either case avoid a state where SQL-visible FK metadata exists but DML skips enforcement.

## Fix Direction

The direct fix is to run `checkTableForeignKeyValidInOwner` or an equivalent validation in `RecoverTable`/`onRecoverTable` before publishing the recovered table. The validation needs to respect `foreign_key_checks`, match ordinary create-table semantics, and produce a user-facing DDL error when the referenced parent table is gone.

Regression should cover:

1. `DROP child; DROP parent; FLASHBACK child` with FK checks on must fail or not publish the FK.
2. `DROP DATABASE; FLASHBACK DATABASE` with both parent and child must remain green.
3. If the implementation chooses to strip/disable invalid FKs, `SHOW CREATE TABLE`, `information_schema.key_column_usage`, and DML enforcement must agree.

## Methodology Note

This is a high-quality S6 hit, not a broad FK fuzzing accident. The selector was:

```text
restore path re-materializes historical object metadata
+ ordinary create path has explicit reference validation
+ sibling restore path has green controls
+ strong oracle can observe metadata, enforcement, and plan check
```

The same screen also produced useful boundaries:

- TTL recovery is green: recovered table and recovered database disable TTL scheduling.
- Table-cache recovery is blocked: cached tables cannot be dropped, so recovery is not reachable.
- TiFlash replica recovery is a static high-signal candidate, but this testbed lacks a TiFlash store/PD placement target, so runtime proof is environment-blocked.

The practical improvement to S6 is: after finding a restore-path container bypass, do not enumerate every restored field. First ask whether the field has an ordinary create-time validator and a post-restore behavioral oracle. FK passes both filters; TTL/table-cache/TiFlash show the boundary cases.
