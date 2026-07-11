# CREATE TABLE LIKE copies READ ONLY table lock to the new table

Status: confirmed on testbed `8192975`; inserted into remote `found_bug` as id1200001.
Remote state after insert: `COUNT(*)=70`, `COUNT(DISTINCT root_cause_id)=48`.

## User-Visible Symptom

If a source table is marked `READ ONLY`, `CREATE TABLE dst LIKE src` succeeds, but the newly
created empty `dst` table is also `READ ONLY`. A user can create the table and immediately fail to
insert into it with `ERROR 8020`, even though they never locked `dst`.

This is not a partition case; the repro uses ordinary non-partition tables.

## Minimal Repro

```sql
DROP DATABASE IF EXISTS ai_like_readonly2;
CREATE DATABASE ai_like_readonly2;
USE ai_like_readonly2;

CREATE TABLE src(a INT);
INSERT INTO src VALUES (1);

ALTER TABLE src READ ONLY;
CREATE TABLE dst LIKE src;

INSERT INTO src VALUES (2);
-- ERROR 8020: Table 'src' was locked in READ ONLY ...

INSERT INTO dst VALUES (2);
-- ERROR 8020: Table 'dst' was locked in READ ONLY ...
```

The error for `dst` carried the same lock session as `src` on testbed `8192975`.

## Isolation Control

Cleaning only `dst` makes `dst` writable while `src` remains read-only:

```sql
ADMIN CLEANUP TABLE LOCK dst;
INSERT INTO dst VALUES (3);
-- succeeds

INSERT INTO src VALUES (3);
-- ERROR 8020: Table 'src' was locked in READ ONLY ...
```

The user-level repair is also independent:

```sql
ALTER TABLE dst READ WRITE;
INSERT INTO dst VALUES (3);
-- succeeds

INSERT INTO src VALUES (3);
-- still ERROR 8020
```

## Expected

`CREATE TABLE LIKE` should clone the schema definition, not runtime/table-lock state. The new
table should be writable unless the user explicitly marks the new table read-only.

## Actual

`dst` is published with copied `TableInfo.Lock` metadata and rejects writes until the user cleans
or clears `dst`'s lock.

## Source Anchors

- `/Users/bba/pc/tidb/pkg/ddl/create_table.go:1249`: `BuildTableInfoWithLike` starts with
  `tblInfo := *referTblInfo`.
- `/Users/bba/pc/tidb/pkg/ddl/create_table.go:1263-1298`: the LIKE path resets several target-only
  fields (`Name`, `AutoIncID`, `ForeignKeys`, cache status, TiFlash availability, TTL, affinity)
  but never resets `tblInfo.Lock`.
- `/Users/bba/pc/tidb/pkg/ddl/executor.go:1786-1803`: `ALTER TABLE ... READ ONLY` is implemented
  by creating a `TableLockReadOnly` lock.
- `/Users/bba/pc/tidb/pkg/ddl/table_lock.go:145-167`: locked tables reject incompatible writes
  with `ErrTableLocked`.

## Root Cause

The proof obligation is source/target runtime-state isolation:

```text
P_check:  BuildTableInfoWithLike shallow-copies source TableInfo and selectively resets fields.
Q_claim:  every copied field left on the target is schema definition, not runtime object state.
D_dim:    TableInfo.Lock is runtime table-lock state, not schema definition.
F_effect: checkTableLocked trusts the copied Lock on the new table and rejects writes to dst.
```

## Fix Direction

Set `tblInfo.Lock = nil` while building the LIKE target. Then audit other non-schema runtime fields
in `TableInfo` that should not survive object reconstruction.

## Quality

Medium. It is not data corruption, but it creates an unusable table from a successful DDL and the
error points at a lock the user did not place on that table. The oracle is strong because clearing
only `dst` fixes `dst` while `src` remains read-only.

## Method Lesson

S13 should not be limited to pointer-backed source mutation. The higher-level selector is:

```text
source object -> target object reconstruction
+ shallow/top-level copy
+ selective resets for some target-only fields
+ at least one remaining field is runtime state
+ user-visible behavior oracle on the target
= high-value clone-state candidate
```

The efficient step was not enumerating `CREATE TABLE LIKE` options. It was scanning the shallow
copy against `TableInfo` fields and asking which fields are schema definition versus runtime state.
