# id630012 Draft: MODIFY COLUMN Can Make FK Child Signedness Incompatible

## Summary

`ALTER TABLE ... MODIFY COLUMN` can change a child foreign-key column from signed `INT` to
`INT UNSIGNED` while it still references a signed parent `INT` column.

The final schema cannot be created directly: TiDB rejects the signed/unsigned mismatch with
`ERROR 3780`. But the transition path succeeds with no warning and leaves the FK published. Later
parent `ON UPDATE CASCADE` to a negative key fails with `ERROR 1264 Out of range value`, while the
valid signed/signed control cascades successfully.

Remote `found_bug` row:

```text
id:        630012
title:     MODIFY COLUMN can make FK child signedness incompatible with parent
severity:  medium
category:  DDL correctness
oracle:    O19_TARGET_STATE_REJECTION_REFERENCE
method:    S16_DDL_VALIDATOR_ORDERING_GAP
status:    confirmed
```

## Minimal Repro

```sql
DROP DATABASE IF EXISTS ai_fk_unsigned_proof;
CREATE DATABASE ai_fk_unsigned_proof;
USE ai_fk_unsigned_proof;
SET foreign_key_checks=1;

CREATE TABLE p_red(a INT PRIMARY KEY);
CREATE TABLE c_red(
  a INT,
  INDEX(a),
  CONSTRAINT fk_red FOREIGN KEY (a) REFERENCES p_red(a) ON UPDATE CASCADE
);

INSERT INTO p_red VALUES (1);
INSERT INTO c_red VALUES (1);

ALTER TABLE c_red MODIFY COLUMN a INT UNSIGNED;

SHOW CREATE TABLE c_red;
SELECT table_name, column_name, column_type
FROM information_schema.columns
WHERE table_schema = DATABASE()
  AND table_name IN ('p_red', 'c_red')
ORDER BY table_name, column_name;

UPDATE p_red SET a = -1 WHERE a = 1;
```

Actual:

```text
ALTER TABLE c_red MODIFY COLUMN a INT UNSIGNED
Query OK, 0 rows affected

SHOW CREATE TABLE c_red
`a` int unsigned DEFAULT NULL,
CONSTRAINT `fk_red` FOREIGN KEY (`a`) REFERENCES `p_red` (`a`) ON UPDATE CASCADE

information_schema.columns
c_red.a: int unsigned
p_red.a: int

UPDATE p_red SET a = -1 WHERE a = 1
ERROR 1264 (22003): Out of range value for column 'a' at row 1
```

The parent and child rows remain unchanged after the failed cascade.

## Controls

Direct target-state creation is rejected:

```sql
CREATE TABLE p_direct(a INT PRIMARY KEY);
CREATE TABLE c_direct(
  a INT UNSIGNED,
  INDEX(a),
  CONSTRAINT fk_direct FOREIGN KEY (a) REFERENCES p_direct(a) ON UPDATE CASCADE
);
```

Actual:

```text
ERROR 3780 (HY000): Referencing column 'a' and referenced column 'a'
in foreign key constraint 'fk_direct' are incompatible.
```

The valid signed/signed control behaves correctly:

```sql
CREATE TABLE p_ok(a INT PRIMARY KEY);
CREATE TABLE c_ok(
  a INT,
  INDEX(a),
  CONSTRAINT fk_ok FOREIGN KEY (a) REFERENCES p_ok(a) ON UPDATE CASCADE
);
INSERT INTO p_ok VALUES (1);
INSERT INTO c_ok VALUES (1);
UPDATE p_ok SET a = -1 WHERE a = 1;
SELECT a FROM p_ok; -- -1
SELECT a FROM c_ok; -- -1
```

Round-trip revalidation rejects the same FK after the transition:

```sql
ALTER TABLE c_red DROP FOREIGN KEY fk_red;
ALTER TABLE c_red ADD CONSTRAINT fk_red2 FOREIGN KEY (a) REFERENCES p_red(a) ON UPDATE CASCADE;
```

Actual:

```text
ERROR 3780 (HY000): Referencing column 'a' and referenced column 'a'
in foreign key constraint 'fk_red2' are incompatible.
```

## Source Anchor

The create/add FK path checks signedness:

```text
pkg/ddl/foreign_key.go:284-288
  if col.GetType() != refCol.GetType() ||
     mysql.HasUnsignedFlag(col.GetFlag()) != mysql.HasUnsignedFlag(refCol.GetFlag()) ||
     col.GetCharset() != refCol.GetCharset() ||
     col.GetCollate() != refCol.GetCollate() {
    return ErrFKIncompatibleColumns
  }
```

The MODIFY path can skip that comparison:

```text
pkg/ddl/foreign_key.go:301-304
  if newCol.GetType() == originalCol.GetType() &&
     newCol.GetFlen() == originalCol.GetFlen() &&
     newCol.GetDecimal() == originalCol.GetDecimal() {
    return nil
  }
```

For `INT` -> `INT UNSIGNED`, type, flen, and decimal can remain equal while the unsigned flag
changes. Later generic checks do not re-run the FK target-state validator for child signedness.

## Fix Direction

Run FK compatibility validation on the fully materialized target column and compare the same
dimensions as CREATE/ADD FK:

```text
type, unsigned flag, charset, collation, and the action-specific nullability requirement
```

At minimum, the early return in `checkModifyColumnWithForeignKeyConstraint` must include the
unsigned flag and every other FK compatibility dimension, or it should be removed in favor of a
complete target-state comparison.

## Quality

Medium.

- This is wrong acceptance of an invalid target FK schema.
- The repro has a direct target-state rejection, a valid signed/signed behavior control, and a
  round-trip ADD FK rejection.
- The user-facing consequence is fail-stop DML: a parent update that cascades in the valid schema
  fails after the transition.
- Method value is high: S16 predicted a second missing dimension from the same incomplete-target
  validator, while collation and primary-key NULL sibling probes stayed green because later
  validators covered them.
