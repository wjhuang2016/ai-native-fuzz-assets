# New severe candidate: FLASHBACK TABLE rebinds a historical FK to a recreated same-name parent

## Status

- Status: current testbed candidate; control matrix complete; root accounting now supports an independent high-severity fix-locus candidate
- Severity: high candidate / data-integrity
- Testbed: `8220955`, failpoint owner front via `127.0.0.1:14101`
- Evidence log: `assets/store/logs/flashback-fk-same-name-parent-rebind-red-20260712.log`
- Proposed root cause ID: `flashback-fk-rebinds-recreated-parent`

This is a new trigger and user-visible consequence discovered by extending the existing
`FLASHBACK TABLE` foreign-key oracle. It must not be counted as a separate root from the
missing-parent case until the product/fix boundary is reviewed.

## User-Visible Symptom

`FLASHBACK TABLE` can publish a child table whose historical rows were valid against the old
parent object but are orphaned against a newly created, same-name parent object. The FK metadata
is present and future invalid inserts are rejected, so a superficial FK check looks healthy. The
already recovered rows are not revalidated.

## Minimal Reproduction

```sql
DROP DATABASE IF EXISTS ai_native_fk_rebind;
CREATE DATABASE ai_native_fk_rebind;
USE ai_native_fk_rebind;
SET SESSION foreign_key_checks = 1;

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

-- A different object reuses the old parent name but has no matching row.
CREATE TABLE p(id INT PRIMARY KEY);

FLASHBACK TABLE c;

SELECT c.id, c.pid, p.id AS parent_id
FROM c LEFT JOIN p ON c.pid = p.id;
ADMIN CHECK TABLE c;
```

Observed on the current testbed:

```text
FLASHBACK TABLE c                         -> succeeds
SHOW CREATE TABLE c                       -> still declares fk_pid REFERENCES p(id)
recovered row                              -> id=1, pid=1
current parent rowset                     -> empty
orphan_rows (LEFT JOIN p ... IS NULL)     -> 1
ADMIN CHECK TABLE c                       -> no error
```

The recovered table still produces a `Foreign_Key_Check` for new writes. For example,
`INSERT INTO c VALUES (2, 999)` fails with `ERROR 1452`. That makes the failure especially easy to
miss: future writes are protected while the old row silently violates the current reference.

## Direct-Target Control

The direct path can create the empty parent and child schema, but it cannot load the historical
row after the parent is empty:

```sql
CREATE TABLE direct_p(id INT PRIMARY KEY);
CREATE TABLE direct_c(
  id INT PRIMARY KEY,
  pid INT,
  KEY(pid),
  CONSTRAINT direct_fk FOREIGN KEY(pid) REFERENCES direct_p(id)
);
INSERT INTO direct_c VALUES (1, 1);
-- ERROR 1452
```

Thus `FLASHBACK TABLE` is bypassing a data-validity boundary that ordinary DML preserves. The
existing missing-parent control is also useful: when no parent exists, recovered future inserts
can pass without any FK check at all. This new shape shows that merely recreating the name is not
enough to restore the old reference semantics.

## Matrix Verification

The four-cell follow-up matrix ran on the same testbed:

| Cell | Result | Strong evidence |
| --- | --- | --- |
| same-name parent recreated with original row | GREEN | `orphan_rows=0`; a new `pid=999` insert is rejected; row count remains `1` |
| same-name parent recreated empty | RED | `orphan_rows=1`; recovered `c.id=1,pid=1` has `parent_id=NULL`; `ADMIN CHECK TABLE` is silent |
| same-name parent recreated with `VARCHAR` key | RED | direct FK creation returns `ERROR 3780`, but `FLASHBACK TABLE` succeeds and recovers one row |
| `FLASHBACK DATABASE` restores parent and child together | GREEN | `orphan_rows=0`; invalid future insert is rejected; row count remains `1` |

The raw matrix output is in:
`assets/store/logs/flashback-fk-identity-drift-matrix-20260712.log`.

## Source Proof

Normal create uses a reference validator:

- `pkg/ddl/create_table.go:81` calls `checkTableForeignKeyValidInOwner` before publishing a table.
- `pkg/ddl/foreign_key.go:186-211` resolves the current parent by schema/table name and checks FK
  column compatibility.

Recovery does not call that proof or validate recovered rows:

- `pkg/ddl/executor.go:1459-1472` checks only current schema existence and target table-name
  collision before submitting `ActionRecoverTable`.
- `pkg/ddl/table.go:183-198` repeats table-name and table-ID checks.
- `pkg/ddl/table.go:296-300` clones the historical `TableInfo` and publishes it with
  `CreateTableAndSetAutoID`.
- `pkg/meta/model/table.go:1253-1265` shows `FKInfo` stores `RefSchema`, `RefTable`, and column
  names, but no referenced table identity/version. A same-name replacement is therefore
  indistinguishable from the historical parent at metadata-serialization time.

The hidden proof obligation is:

```text
P_check:  the historical child table and its old parent were valid together
Q_claim:  the historical child rows remain valid after recovery
D_dims:   current parent object identity, schema, columns, indexes, and rowset
F_effect: recover old TableInfo and old rows after only name/ID availability checks
O_oracle: recovered-row FK differential against the current parent plus ADMIN CHECK TABLE
R_redflag: old parent is dropped and a different empty/incompatible object reuses its name
```

## Severity Assessment

This is stronger than a metadata-only stale reference: the operation succeeds while publishing a
table containing an existing orphan row under `foreign_key_checks=ON`. `ADMIN CHECK TABLE` does not
report it, and future writes can be correctly rejected, leaving the system with a mixed historical
state that ordinary FK checks do not repair.

Before external filing, the remaining review is root accounting and product semantics. The
runtime controls are complete:

1. parent absent during recovery (existing id30016 surface);
2. same-name parent recreated with the original row (green);
3. same-name parent recreated empty (red existing orphan);
4. `FLASHBACK DATABASE` restoring parent and child together (green);
5. same-name parent with incompatible type (red invalid-schema publication).

## Root Accounting Conclusion

This is related to id30016 through the broad S6 recovery-validator selector, but it is not merely
the same missing-parent surface:

| Surface | What is missing | Why the other fix is insufficient |
| --- | --- | --- |
| id30016 | Current referenced parent existence/structural validation | A current-parent existence check rejects the absent-parent case, but an empty same-name parent passes it |
| this candidate | Current-parent identity/row-membership validation for already recovered child rows | The recovered child has a visible FK and future `Foreign_Key_Check`, yet the existing row is already orphaned |

The smallest fix that closes id30016 is therefore not enough to close this case. The new case needs
either historical parent identity preservation or a row-level reconciliation proof before publishing
the recovered child. The two cases may share a broader implementation function, but they have
different load-bearing predicates and different user-visible failure modes, so this candidate uses
`root_cause_id=flashback-fk-rebinds-recreated-parent` pending upstream product/fix review.

## Method Asset

The selector extension is:

```text
restore path re-materializes historical references
+ old referenced object is absent or has been replaced under the same name
+ ordinary create/DML path validates current reference plus row membership
+ recovery checks only container/name/table-ID availability
= test identity drift, not only object absence
```

The small matrix reuses the old FK oracle but changes exactly one hidden dimension: parent
`missing` -> parent `same-name replacement`. The stronger oracle checks existing rows, not only the
next DML statement. This is the reusable lesson; enumerating more FK actions would be lower value.
