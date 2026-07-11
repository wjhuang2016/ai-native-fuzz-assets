# id630010 Method Case: Spec-Level Idempotence Flag Lost During Split

## Selector

```text
S15_DDL_IDEMPOTENCE_FLAG_DROPPED
```

This case improves S15 from "one executor branch forgot a flag" to a more general audit shape:
the parser stores a flag on a parent DDL spec, then resolution splits that spec into child jobs and
the child owner reads a different flag slot.

```text
P_check:  parser accepts ADD IF NOT EXISTS (...) and records spec.IfNotExists
Q_claim:  duplicate table elements from the accepted list should use idempotent semantics
effect:   ResolveAlterTableSpec splits constraints into AlterTableAddConstraint specs, but the
          index/check branches read only constraint-local flags or no flag at all
D_dim:    flag ownership must survive AST/spec splitting, not only direct parser-to-executor paths
```

## Matrix

| Cell | SQL shape | Oracle | Result |
| --- | --- | --- | --- |
| Outer column IFNE | `ADD IF NOT EXISTS (b INT)` twice | note/no-op, one column | GREEN, Note 1060 |
| Outer KEY IFNE | `ADD IF NOT EXISTS (KEY idx_a(a))` twice | should be idempotent if accepted | RED, ERROR 1061 |
| Inner KEY IFNE | `ADD IF NOT EXISTS (KEY IF NOT EXISTS idx_a(a))` twice | note/no-op, one index | GREEN, Note 1061 |
| Outer CHECK IFNE | `ADD IF NOT EXISTS (CONSTRAINT ck_a CHECK(...))` twice | should be idempotent or rejected up front | RED, ERROR 3822 |
| Schema count | index/check metadata after red | exactly one object | GREEN, no duplicate write |

## Oracle

```text
O18_IDEMPOTENT_DDL_FLAG_ORACLE
```

This oracle compares flagged duplicate behavior against:

- a same-grammar element that honors the outer flag;
- a sibling or inner spelling that honors the object-level flag;
- a schema-count guard that separates wrong-error from duplicate-write.

## Why The Method Worked

The source proof obligation was visible before running SQL:

```text
parser:
  ADD IfNotExists (TableElementList) -> spec.IfNotExists

split:
  NewColumns stay AlterTableAddColumns
  NewConstraints become AlterTableAddConstraint

execute:
  column duplicate checks spec.IfNotExists
  index duplicate checks constr.IfNotExists
  CHECK duplicate has no spec.IfNotExists path
```

That gave a tiny matrix with one red arm and two strong controls. The useful trick was to compare
two spellings of the same intended idempotence:

```text
outer flag only:  ADD IF NOT EXISTS (KEY idx_a(a))        -> RED
inner flag too:   ADD IF NOT EXISTS (KEY IF NOT EXISTS...) -> GREEN
```

## Quality

Low to medium severity, high method value.

- It is a wrong-error/idempotence bug.
- It is user-visible in migration scripts that use table-element-list DDL.
- It does not corrupt metadata; counts stay at one.
- It teaches a new S15 sub-selector: flags can be lost during parser/spec splitting or AST rewrite,
  not only in the final executor branch.

## Pause Gate

Do not enumerate every table-element syntax. Reopen this S15 sub-shape only for:

- a different spec-splitting or AST-rewrite path that loses a parser flag;
- silent duplicate-write or wrong-acceptance instead of wrong-error;
- fix validation for columns, indexes, CHECK constraints, mixed column+constraint lists, and
  unsupported constraint forms.
