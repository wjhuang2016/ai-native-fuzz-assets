# id630011 Draft: MODIFY COLUMN Allows NOT NULL Child Column for FK SET NULL

## Summary

`ALTER TABLE ... MODIFY COLUMN` can turn a nullable child foreign-key column into `NOT NULL` even
when the foreign key action is `ON DELETE SET NULL` or `ON UPDATE SET NULL`.

The final schema cannot be created directly: TiDB already rejects `NOT NULL` child columns for
`SET NULL` actions with `ERROR 1830`. But the transition path succeeds with no warning. Later parent
`DELETE` or `UPDATE` statements that need to perform the `SET NULL` action fail at runtime with
`ERROR 1048 Column 'pid' cannot be null`.

Remote `found_bug` row:

```text
id:        630011
title:     MODIFY COLUMN allows NOT NULL child column for foreign key SET NULL actions
severity:  medium
category:  DDL
oracle:    O19_TARGET_STATE_REJECTION_REFERENCE
method:    S16_DDL_VALIDATOR_ORDERING_GAP
status:    confirmed
```

## Minimal Repro

```sql
DROP DATABASE IF EXISTS ai_fk_setnull_mod;
CREATE DATABASE ai_fk_setnull_mod;
USE ai_fk_setnull_mod;
SET foreign_key_checks=1;

CREATE TABLE p_del(id INT PRIMARY KEY);
CREATE TABLE c_del(
  id INT PRIMARY KEY,
  pid INT,
  INDEX(pid),
  CONSTRAINT fk_del FOREIGN KEY (pid) REFERENCES p_del(id) ON DELETE SET NULL
);

-- Should be rejected because the resulting FK action cannot set pid to NULL.
ALTER TABLE c_del MODIFY COLUMN pid INT NOT NULL;

SHOW CREATE TABLE c_del;

INSERT INTO p_del VALUES (1);
INSERT INTO c_del VALUES (1,1);

DELETE FROM p_del WHERE id=1;
```

Actual:

```text
ALTER TABLE ... MODIFY COLUMN pid INT NOT NULL
Query OK, 0 rows affected

DELETE FROM p_del WHERE id=1
ERROR 1048 (23000): Column 'pid' cannot be null
```

`SHOW CREATE TABLE c_del` shows the illegal final state:

```text
`pid` int NOT NULL,
CONSTRAINT `fk_del` FOREIGN KEY (`pid`) REFERENCES `p_del` (`id`) ON DELETE SET NULL
```

The same issue appears for `ON UPDATE SET NULL`:

```sql
CREATE TABLE p_upd(id INT PRIMARY KEY);
CREATE TABLE c_upd(
  id INT PRIMARY KEY,
  pid INT,
  INDEX(pid),
  CONSTRAINT fk_upd FOREIGN KEY (pid) REFERENCES p_upd(id) ON UPDATE SET NULL
);

ALTER TABLE c_upd MODIFY COLUMN pid INT NOT NULL;

INSERT INTO p_upd VALUES (1);
INSERT INTO c_upd VALUES (1,1);

UPDATE p_upd SET id=2 WHERE id=1;
```

Actual:

```text
ERROR 1048 (23000): Column 'pid' cannot be null
```

## Controls

Direct target-state creation is rejected, which proves the final schema is not considered valid by
TiDB's own CREATE/ADD FK validator:

```sql
CREATE TABLE p_direct_delete(id INT PRIMARY KEY);
CREATE TABLE c_direct_delete(
  id INT PRIMARY KEY,
  pid INT NOT NULL,
  INDEX(pid),
  CONSTRAINT fk_direct_delete
    FOREIGN KEY (pid) REFERENCES p_direct_delete(id) ON DELETE SET NULL
);
```

Actual:

```text
ERROR 1830 (HY000): Column 'pid' cannot be NOT NULL: needed in a foreign key constraint
'fk_direct_delete' SET NULL
```

Same for `ON UPDATE SET NULL`.

The checker-aligned green control succeeds:

```sql
CREATE TABLE p_res(id INT PRIMARY KEY);
CREATE TABLE c_res(
  id INT PRIMARY KEY,
  pid INT,
  INDEX(pid),
  CONSTRAINT fk_res FOREIGN KEY (pid) REFERENCES p_res(id) ON DELETE RESTRICT
);

ALTER TABLE c_res MODIFY COLUMN pid INT NOT NULL;
```

This is expected because `RESTRICT` does not need to write `NULL` into the child column.

## Source Anchor

The normal CREATE/ADD FK path already has the right target-state validator:

```text
pkg/ddl/executor.go:5329-5330
  if mysql.HasNotNullFlag(col.GetFlag()) &&
     (refer.OnDelete.ReferOpt == ast.ReferOptionSetNull ||
      refer.OnUpdate.ReferOpt == ast.ReferOptionSetNull) {
    return ErrForeignKeyColumnNotNull
  }
```

The MODIFY path checks FK compatibility before column options are applied:

```text
pkg/ddl/modify_column.go:1912
  checkModifyColumnWithForeignKeyConstraint(..., originalCol, newCol)

pkg/ddl/modify_column.go:1924
  ProcessModifyColumnOptions(..., specNewColumn.Options)
```

The FK modify checker returns early if type, length, and decimal are unchanged:

```text
pkg/ddl/foreign_key.go:301-304
  if newCol.GetType() == originalCol.GetType() &&
     newCol.GetFlen() == originalCol.GetFlen() &&
     newCol.GetDecimal() == originalCol.GetDecimal() {
    return nil
  }
```

`NOT NULL` is added later:

```text
pkg/ddl/modify_column.go:2318-2320
  case ast.ColumnOptionNotNull:
    col.AddFlag(mysql.NotNullFlag)
```

So the validator proves the old nullable state, then the path publishes a new non-nullable state
without rechecking referential actions.

## Fix Direction

Build the prospective `ColumnInfo` including column options before checking FK compatibility, or add
a post-option validator that rejects:

```text
child column becomes NOT NULL
and existing FK action is ON DELETE SET NULL or ON UPDATE SET NULL
```

The check should reuse the same `ErrForeignKeyColumnNotNull` semantics already used by CREATE/ADD
FK.

## Quality

Medium.

- This is wrong acceptance of an invalid target schema, not just a wrong error.
- User impact is visible later: parent `DELETE`/`UPDATE` that should enforce the FK action fails.
- It does not silently corrupt rows in the observed repro; the DML fails and leaves rows unchanged.
- Method value is high: it adds a new selector where the validator runs before all target-state
  dimensions have been materialized.
