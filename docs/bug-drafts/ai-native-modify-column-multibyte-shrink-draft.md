# id630001: MODIFY COLUMN shrink rejects valid multibyte CHAR/VARCHAR values

> 2026-07-03. Confirmed on testbed `8192975` / `fp-tidb`. Inserted into remote `found_bug` as id630001 (`MAX(id)=630001`, `COUNT(*)=33`).

## Summary

`ALTER TABLE ... MODIFY COLUMN` can reject a valid `CHAR`/`VARCHAR` shrink when existing values contain multibyte characters.

The target type itself accepts the value. For example, `varchar(3)` and `char(3)` both accept `_utf8mb4'中中中'` because its character length is 3. But shrinking an existing `varchar(4)` or `char(4)` column containing that same value to length 3 fails with `ERROR 1265 Data truncated`.

## Minimal Repro

```sql
DROP DATABASE IF EXISTS ai_modcol_len;
CREATE DATABASE ai_modcol_len DEFAULT CHARSET utf8mb4 COLLATE utf8mb4_bin;
USE ai_modcol_len;
SET SESSION sql_mode='STRICT_TRANS_TABLES';

CREATE TABLE ref_v3 (
  a varchar(3) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin
);
INSERT INTO ref_v3 VALUES (_utf8mb4'中中中');
SELECT a, LENGTH(a), CHAR_LENGTH(a) FROM ref_v3;
-- a=中中中, LENGTH=9, CHAR_LENGTH=3

CREATE TABLE t_v4_to_v3 (
  a varchar(4) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin
);
INSERT INTO t_v4_to_v3 VALUES (_utf8mb4'中中中');

ALTER TABLE t_v4_to_v3
  MODIFY COLUMN a varchar(3) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;
-- ERROR 1265 (01000): Data truncated for column 'a', value is '中中中'

SHOW CREATE TABLE t_v4_to_v3;
-- still varchar(4)
```

The same shape reproduces for `char(4) -> char(3)`:

```sql
CREATE TABLE ref_c3 (
  a char(3) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin
);
INSERT INTO ref_c3 VALUES (_utf8mb4'中中中');

CREATE TABLE t_c4_to_c3 (
  a char(4) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin
);
INSERT INTO t_c4_to_c3 VALUES (_utf8mb4'中中中');

ALTER TABLE t_c4_to_c3
  MODIFY COLUMN a char(3) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;
-- ERROR 1265 (01000): Data truncated for column 'a', value is '中中中'
```

## Guard Cells

| Cell | Setup | Expected | Observed |
| --- | --- | --- | --- |
| Target accepts value | direct insert `_utf8mb4'中中中'` into `varchar(3)` | success | GREEN |
| Target accepts value | direct insert `_utf8mb4'中中中'` into `char(3)` | success | GREEN |
| ASCII control | `varchar(4)` value `abc`, shrink to `varchar(3)` | success | GREEN |
| Multibyte varchar | `varchar(4)` value `中中中`, shrink to `varchar(3)` | success | RED: 1265 |
| Multibyte char | `char(4)` value `中中中`, shrink to `char(3)` | success | RED: 1265 |

## Source Chain

- `/Users/bba/pc/tidb/pkg/ddl/modify_column.go:74`: `getModifyColumnType` can choose `ModifyTypeNoReorgWithCheck` for shrinking character string columns under strict SQL mode.
- `/Users/bba/pc/tidb/pkg/ddl/modify_column.go:642`: `doModifyColumnWithCheck` performs a restricted SQL precheck before publishing the metadata change without row reorg.
- `/Users/bba/pc/tidb/pkg/ddl/modify_column.go:851`: `buildCheckSQLFromModifyColumn` builds the validity-check predicate.
- `/Users/bba/pc/tidb/pkg/ddl/modify_column.go:879`: for non-integer range checks it uses `LENGTH(col) > newFlen`.

## Root Cause

The no-reorg validity precheck treats this implication as true:

```text
LENGTH(col) <= target_flen
=> the existing string fits the target CHAR/VARCHAR type
```

That implication is wrong for non-binary character strings. `LENGTH` is byte length, while `CHAR(n)` and `VARCHAR(n)` length limits are character counts. A valid utf8mb4 value can have `CHAR_LENGTH(col) <= n` but `LENGTH(col) > n`.

So the precheck reports a valid row as overflowing, and the DDL fails before publishing the metadata change.

## Expected Behavior

For non-binary character string types, shrinking `CHAR`/`VARCHAR` should reject only values whose character length exceeds the target length. If the target table definition can directly store the existing value, the metadata-change-with-check path should not reject it.

## Fix Direction

Use `CHAR_LENGTH` for non-binary character string length checks in `buildCheckSQLFromModifyColumn`. Keep byte-length checks for binary string types where the type length is byte-based.

Suggested regression coverage:

- `varchar(4) utf8mb4 -> varchar(3) utf8mb4` with `中中中` succeeds.
- `char(4) utf8mb4 -> char(3) utf8mb4` with `中中中` succeeds.
- ASCII overlength still fails.
- Binary/varbinary overlength still uses byte semantics.
- Add indexed-column controls because the same no-reorg-with-check selector can be chosen when index reorg is not needed.

## Method Lesson

This hit is not another equality-as-identity case. It is a new proof-obligation shape:

```text
code checks P: restricted SQL finds no row where LENGTH(col) > target_flen
system believes Q: every existing row fits the new column type
fast path: finish MODIFY COLUMN without row reorg
missing D: column type's unit of measure, bytes vs characters
oracle: direct target-type acceptance reference
```

The key move was to compare the precheck's metric against the target type's real acceptance rule. Once source showed `LENGTH(col) > flen`, the red matrix was just ASCII vs multibyte, and direct insert into the target type became a very strong oracle.
