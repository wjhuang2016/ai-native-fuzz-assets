# Sequence Default Reference DDL Bug Draft

Date: 2026-07-02

Status: DDL-only candidate, id30005 draft.

## Summary

A table column can store a sequence call as its default expression:

```sql
CREATE SEQUENCE seq;
CREATE TABLE t(a INT DEFAULT NEXT VALUE FOR seq);
```

The create/alter path validates that `seq` exists and is a sequence. However, later DDL can remove or rename the referenced sequence while `t.a` still points at the old sequence name. The DDL succeeds, but future inserts using the default fail with `ERROR 1146 Table '<db>.seq' doesn't exist`.

This violates the current proof obligation:

```text
DDL changes/removes an object
-> every metadata reference to that object must rewrite or block
```

## Minimal Repro 1: DROP SEQUENCE

```sql
DROP DATABASE IF EXISTS seq_ref;
CREATE DATABASE seq_ref;
USE seq_ref;

CREATE SEQUENCE seq START WITH 1 INCREMENT BY 1 NOCACHE;
CREATE TABLE t(a INT DEFAULT NEXT VALUE FOR seq, b INT);

INSERT INTO t(b) VALUES (10);
SELECT a, b FROM t;
-- 1, 10

DROP SEQUENCE seq;
-- Succeeds.

SHOW CREATE TABLE t;
-- `a` int DEFAULT (nextval(`seq_ref`.`seq`))

INSERT INTO t(b) VALUES (20);
-- ERROR 1146 (42S02): Table 'seq_ref.seq' doesn't exist
```

Expected: `DROP SEQUENCE seq` should either block while a live table default references it, or the table default must be changed in a user-visible, consistent way. Silently leaving a broken default is not recoverable from the DDL result alone.

## Minimal Repro 2: RENAME TABLE on Sequence

```sql
DROP DATABASE IF EXISTS seq_ref;
CREATE DATABASE seq_ref;
USE seq_ref;

CREATE SEQUENCE seq START WITH 1 INCREMENT BY 1 NOCACHE;
CREATE TABLE t(a INT DEFAULT NEXT VALUE FOR seq, b INT);

RENAME TABLE seq TO seq2;
-- Succeeds.

SHOW CREATE TABLE t;
-- `a` int DEFAULT (nextval(`seq_ref`.`seq`))

INSERT INTO t(b) VALUES (20);
-- ERROR 1146 (42S02): Table 'seq_ref.seq' doesn't exist
```

Expected: renaming the sequence should either block or rewrite dependent table defaults from `seq` to `seq2`.

## Minimal Repro 3: Cross-DB DROP DATABASE

```sql
DROP DATABASE IF EXISTS seqdb;
DROP DATABASE IF EXISTS tabdb;
CREATE DATABASE seqdb;
CREATE DATABASE tabdb;

CREATE SEQUENCE seqdb.seq START WITH 10 INCREMENT BY 1 NOCACHE;
CREATE TABLE tabdb.t(a INT DEFAULT NEXT VALUE FOR seqdb.seq, b INT);

INSERT INTO tabdb.t(b) VALUES (10);
SELECT a, b FROM tabdb.t;
-- 10, 10

DROP DATABASE seqdb;
-- Succeeds.

SHOW CREATE TABLE tabdb.t;
-- `a` int DEFAULT (nextval(`seqdb`.`seq`))

INSERT INTO tabdb.t(b) VALUES (20);
-- ERROR 1146 (42S02): Table 'seqdb.seq' doesn't exist
```

Expected: dropping a database that contains a sequence should block if that sequence is referenced by a live table default outside the dropped database.

## Probe

```bash
python3 /Users/bba/pc/ai_native_ddl_sequence_default_reference_probe.py
```

Result on the current `fp-tidb` testbed:

```text
SUMMARY total=5 findings=3 skipped=0
```

Findings:

| Cell | Observed behavior |
|---|---|
| table default references sequence + `DROP SEQUENCE` | DDL succeeds, `SHOW CREATE TABLE` still points at old sequence, default insert fails with `1146` |
| table default references sequence + `RENAME TABLE seq TO seq2` | DDL succeeds, default is not rewritten, default insert fails with `1146` |
| cross-DB table default references sequence + `DROP DATABASE` for sequence DB | DDL succeeds, external table default is not rewritten/blocked, default insert fails with `1146` |

Green controls:

| Cell | Observed behavior |
|---|---|
| live sequence default | `INSERT` without explicit column value consumes sequence successfully |
| `CHANGE COLUMN a aa INT DEFAULT NEXT VALUE FOR seq` | default remains live after column rename when the sequence itself is unchanged |

## Source Chain

Sequence defaults are stored as restored SQL text:

- `pkg/ddl/add_column.go:667` handles `ast.NextVal` as a default expression.
- `pkg/ddl/add_column.go:908` restores the sequence default expression into a string.
- `pkg/ddl/add_column.go:1269` only checks that the target column type supports sequence defaults.

The sequence name is resolved by name at runtime:

- `pkg/expression/builtin_info.go:1531` extracts schema and sequence name from the default expression argument.
- `pkg/expression/sessionexpr/sessionctx.go:404` provides a sequence operator by name.
- `pkg/expression/sessionexpr/sessionctx.go:406` calls `util.GetSequenceByName`.

The DDL paths that remove or rename the sequence do not scan table defaults:

- `pkg/ddl/executor.go:4264` selects `ActionDropSequence`.
- `pkg/ddl/executor.go:4317` only verifies that the target object is a sequence.
- `pkg/ddl/executor.go:4516` / `:4569` allow `RENAME TABLE` for the sequence object without sequence-specific dependency handling.
- `pkg/ddl/table.go:824` rewrites FK metadata after table rename.
- `pkg/ddl/table.go:837` rewrites masking policy names after table rename.
- No equivalent sequence-default rewrite/block helper appears in the rename path.
- `pkg/ddl/schema.go:158` handles `DROP DATABASE`; it checks FK references and cleans affinity/masking metadata, but does not check sequence-default references in other schemas.

## Root Cause Model

```text
column default stores sequence call as SQL text
+ create/alter path validates referenced sequence exists
+ sequence lookup at insert time resolves by name
+ DROP/RENAME/DROP DATABASE path has no reverse dependency scan
= DDL succeeds, but live table default points at a missing sequence
```

This is not the same as privilege grants:

- grants are name policy and may intentionally reattach to same-name objects;
- sequence defaults are executable schema expressions validated at DDL time and used by future writes.

## Fix Semantics

Recommended behavior:

1. `DROP SEQUENCE`: block if any live column default references the sequence.
2. `RENAME TABLE old_seq TO new_seq` where the object is a sequence: either block, or rewrite all dependent defaults to the new qualified name. Rewrite is more consistent with FK/masking rename behavior, but block is simpler and safer.
3. `DROP DATABASE seq_db`: block if it would remove a sequence referenced by a live table outside `seq_db`. If both the sequence and dependent table are dropped in the same schema drop, no external live reference remains.

## Method Lesson

This validates a new high-signal selector:

```text
DDL object is referenced by executable schema expression
+ create/alter validates the referenced object
+ runtime resolves by name
+ remove/rename path lacks reverse dependency scan
= dangling schema expression after DDL
```

The useful AI move was not to fuzz all sequence SQL. It was to notice that sequence defaults sit between earlier column-expression owners and object-reference owners: the reference is inside table metadata, but the target is a separate DDL object.

Full method case:

```text
/Users/bba/pc/ai-native-id30005-method-case.md
```
