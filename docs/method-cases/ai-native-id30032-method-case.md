# Method Case: id30032 ALTER ADD COLUMN inline CHECK loss

## One-line result

`ALTER TABLE ... ADD COLUMN b INT DEFAULT 1 CHECK (b > 0)` succeeds without warnings but silently
drops the inline CHECK constraint. Direct `CREATE TABLE` and sequential `ADD COLUMN` then `ADD
CHECK` both preserve the constraint and reject `b=0`.

## P/Q/D/F/O card

```text
P_check:
  ADD COLUMN validates and builds a new column from a ColumnDef. The build helper can parse
  column-level CHECK options into ast.Constraint objects.

Q_claim:
  A successful ALTER ADD COLUMN publishes the requested column definition, including inline
  column-level constraints accepted by the parser.

D_dims:
  The column definition contains embedded sub-obligations owned by another DDL owner:
  CHECK needs constraint metadata, remaining-row validation, SHOW CREATE visibility, and DML
  enforcement.

F_effect:
  executor.AddColumn submits only ActionAddColumn/TableColumnArgs. The extracted CHECK constraints
  are discarded, so the schema looks valid but the constraint owner never runs.

O_oracle:
  O23 target-schema constraint reference:
  compare inline ALTER against direct CREATE and sequential ADD CHECK references, then perform a
  violating INSERT.
```

## Matrix

```text
direct CREATE inline CHECK:
  SHOW CREATE includes CHECK
  INSERT b=0 fails with ERROR 3819
  classification: GREEN reference

sequential ALTER ADD COLUMN; ALTER ADD CHECK:
  SHOW CREATE includes CHECK
  INSERT b=0 fails with ERROR 3819
  classification: GREEN reference

inline ALTER ADD COLUMN ... CHECK:
  ALTER succeeds, @@warning_count=0
  SHOW CREATE has no CHECK
  information_schema.check_constraints has no row
  INSERT b=0 succeeds
  classification: RED / confirmed

named inline ALTER ADD COLUMN ... CONSTRAINT ck CHECK:
  ALTER succeeds, @@warning_count=0
  named CHECK is absent
  INSERT b=0 succeeds
  classification: RED sibling, same root
```

## Why this was fast

The source question was not "try more CHECK syntax". It was:

```text
Which DDL path accepts a compound spec but submits only one owner job?
```

ADD COLUMN matched that shape:

1. `buildColumnAndConstraint` already knows how to extract `ColumnOptionCheck`.
2. `CreateNewColumn` deliberately ignores the returned constraints.
3. `AddColumn` submits only `ActionAddColumn`.
4. `CREATE TABLE` and `ADD CHECK` prove that the missing owner is real and externally visible.

That collapsed the SQL matrix to three cells: direct target reference, sequential safe path, and
inline transition path.

## Quality

Medium-quality schema-integrity bug:

- user-visible DDL accepts a constraint and emits no warning;
- final schema does not contain the constraint;
- future violating rows are accepted;
- direct and sequential references reject the same violating row;
- source root cause is narrow.

It is lower severity than id630013 because there is no published CHECK containing bad existing
rows. It is stronger than a wrong-error because it silently removes a requested data-integrity
contract.

This is not a duplicate of id1. id1 covers `CHANGE/MODIFY COLUMN` rename/rebuild dropping an
existing CHECK. id30032 covers `ADD COLUMN` accepting a new inline CHECK and never transferring it
to the CHECK constraint owner.

## Selector lesson

This creates S18: embedded constraint owner loss.

Search for DDL syntax where a parent owner accepts a nested object or semantic obligation but the
true owner is a separate job/path. If the parent path checks only the parent object and never
transfers the child obligation, a tiny direct-vs-transition matrix can expose silent loss.

## Stop rule

Do not enumerate every column option. Reopen only for:

- another embedded sub-obligation with a different owner, such as a constraint/index/reference
  carried inside a column/table spec;
- a same-root fix validation;
- a stronger consequence oracle than constraint absence plus violating write.
