# FLASHBACK TABLE can rebind historical FK rows to a recreated same-name parent

## Bug Report

### 1. Minimal reproduce step (Required)

The following reproduces the issue without failpoints on testbed `8220955`:

```sql
DROP DATABASE IF EXISTS ai_native_fk_flashback_cascade;
CREATE DATABASE ai_native_fk_flashback_cascade;
USE ai_native_fk_flashback_cascade;
SET SESSION foreign_key_checks = 1;

CREATE TABLE p(id INT PRIMARY KEY);
CREATE TABLE c(
  id INT PRIMARY KEY,
  pid INT NOT NULL,
  CONSTRAINT fk_c_p FOREIGN KEY(pid) REFERENCES p(id) ON DELETE CASCADE
);

INSERT INTO p VALUES (1);
INSERT INTO c VALUES (10, 1);

DROP TABLE c;
DROP TABLE p;

-- This is a different object that only reuses the historical name and key.
CREATE TABLE p(id INT PRIMARY KEY);
INSERT INTO p VALUES (1);

FLASHBACK TABLE c;

SELECT c.id, c.pid, p.id AS current_parent_id
FROM c LEFT JOIN p ON p.id = c.pid;

SELECT COUNT(*) AS child_rows_before_delete FROM c;

DELETE FROM p WHERE id = 1;

SELECT COUNT(*) AS child_rows_after_delete FROM c;
ADMIN CHECK TABLE c;
SHOW CREATE TABLE c;
```

Observed result:

```text
FLASHBACK TABLE c                         -> succeeds
current_parent_id for (10,1)              -> 1
child_rows_before_delete                  -> 1
DELETE FROM p WHERE id = 1                -> succeeds
child_rows_after_delete                   -> 0
ADMIN CHECK TABLE c                       -> succeeds
```

`SHOW CREATE TABLE c` still contains the historical `ON DELETE CASCADE` foreign key.

The same-name empty-parent variant demonstrates the underlying orphan directly:

```sql
DROP DATABASE IF EXISTS ai_native_fk_rebind_empty;
CREATE DATABASE ai_native_fk_rebind_empty;
USE ai_native_fk_rebind_empty;

CREATE TABLE p(id INT PRIMARY KEY);
CREATE TABLE c(
  id INT PRIMARY KEY,
  pid INT,
  KEY(pid),
  CONSTRAINT fk_pid FOREIGN KEY(pid) REFERENCES p(id)
);
INSERT INTO p VALUES (1);
INSERT INTO c VALUES (1, 1);

DROP TABLE c;
DROP TABLE p;
CREATE TABLE p(id INT PRIMARY KEY);

FLASHBACK TABLE c;

SELECT c.id, c.pid, p.id AS parent_id
FROM c LEFT JOIN p ON c.pid = p.id;
ADMIN CHECK TABLE c;
```

The recovered row is `(1,1)` with `parent_id = NULL`, while `ADMIN CHECK TABLE c` still succeeds.

### 2. What did you expect to see? (Required)

`FLASHBACK TABLE` must not publish a child table whose recovered rows violate the foreign-key
relationship against the current referenced object.

Acceptable behavior includes:

- reject the recovery with a clear error; or
- validate the current referenced object and every recovered FK row before publishing; or
- require recovery of the referenced object together with the child when object identity cannot be
  established.

In particular, a newly created same-name parent must not be treated as the historical parent merely
because its schema and key happen to match. A later normal delete of that replacement parent must
not cascade-delete historical child data without an explicit valid relationship.

### 3. What did you see instead (Required)

`FLASHBACK TABLE c` succeeds after the historical parent has been dropped and a different parent
object has reused the same name. TiDB publishes the historical child row and the historical FK
metadata without validating the existing row against the current parent object.

With `ON DELETE CASCADE`, a normal delete against the replacement parent then removes the recovered
historical child row. `ADMIN CHECK TABLE` does not report the orphan or the data loss.

This is not only a future-write validation gap: future invalid inserts can still be rejected by
`Foreign_Key_Check`, while the already recovered row remains silently invalid or can be deleted by
the replacement parent's ordinary cascade action.

### 4. Controls

The following controls were run on the same testbed:

- same-name parent recreated with the historical row: recovered child remains valid;
- same-name parent recreated empty: recovered child becomes an orphan;
- same-name parent recreated with an incompatible key type: recovery succeeds although direct FK
  creation rejects the schema with `ERROR 3780`;
- `FLASHBACK DATABASE` restoring parent and child together: recovered relationship remains valid.

Therefore the trigger is not simply `FLASHBACK TABLE` or a missing parent. The distinguishing
condition is that the historical referenced object was replaced by a different same-name object and
the child recovery does not prove current row membership or object identity.

### 5. What is your TiDB version? (Required)

Reproduced without failpoints on authorized testbed `8220955`:

```text
8.0.11-TiDB-v8.4.0-this-is-a-placeholder
```

The issue is reported from the current DDL recovery behavior; the exact release impact should be
confirmed by maintainers.

### Suspected root cause

The recovery path checks table/schema availability and name/ID collisions, then clones and
publishes historical `TableInfo` and rows. The foreign-key metadata stores referenced schema/table
names and columns, but not the identity or version of the referenced table. The recovery path does
not perform the row-level validation that ordinary create/DML paths perform.

The missing proof obligation is:

```text
historical child and parent were valid together
        does not imply
recovered child rows are valid against the current same-name parent
```

The relevant oracle is an existing-row FK differential (`LEFT JOIN` against the current parent),
followed by the normal destructive action when `ON DELETE CASCADE` is present. `ADMIN CHECK TABLE`
alone is insufficient because it remains green.

Found by AI-assisted testing.
