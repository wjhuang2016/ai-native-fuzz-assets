# id630004 Method Case: Overbroad Dependency Gate

## Selector

```text
S11_DDL_DEPENDENCY_GATE_OVERBROAD
```

This selector applies when code checks dependency existence and then treats it as proof that every
operation touching the object is unsafe.

```text
P_check:  column A is referenced by generated column B
Q_claim:  any MODIFY COLUMN operation on A can invalidate B
effect:   reject before distinguishing semantic changes from metadata-only changes
D_dim:    the requested change does not affect the dependency expression or value type
```

## Matrix

| Cell | Initial state | Target state | Oracle | Result |
| --- | --- | --- | --- | --- |
| Direct comment target | `a int`, `b as (a+1)` | `a int COMMENT 'new-comment'` | create + insert returns `1,2` | GREEN |
| ALTER comment | same | same target | should match direct target | RED, ERROR 3106 |
| Direct default target | `a int`, `b as (a+1)` | `a int DEFAULT 5` | insert default returns `5,6` | GREEN |
| ALTER default | same | same target | should match direct target | RED, ERROR 3106 |
| Non-dependent column | `c int`, generated column depends on `a` | `c int COMMENT 'ok'` | dependency absent for `c` | GREEN |
| Generated column itself | generated `b` | `b ... COMMENT 'gcol-comment'` | expression unchanged | GREEN |
| True type change | base `a int` used by generated `b` | `a bigint` | existing supported-control reject | GREEN reject |

## Oracle

```text
O14_TARGET_TYPE_ACCEPTANCE_REFERENCE
```

The direct target schema is the reference. If the final schema with the same generated expression
can be created directly and the generated value evaluates correctly, a metadata-only transition
should not be rejected solely because a dependency exists.

## Why The Method Worked

The source had a useful contradiction:

```text
rename path:
  dependency error is used only when the column name changes

later common path:
  the same dependency error rejects every MODIFY
```

That means the checker proved a narrow fact, "generated columns mention this base column", but the
DDL path used it as a broad fact, "all changes to this base column are unsafe".

The small matrix did not need random generated expressions. It only had to hold the expression
constant and vary whether the base-column change was semantic:

- comment/default: metadata-only, red;
- non-dependent column: green;
- generated column's own comment: green;
- true type change: green reject.

## Quality

Medium.

- User-visible symptom: valid metadata-only DDL fails with `ERROR 3106/3108`.
- Strong oracle: direct target schema accepts and evaluates generated columns correctly.
- Source is localized to `/Users/bba/pc/tidb/pkg/ddl/modify_column.go`.
- Severity is lower than data loss because it is a false rejection.
- Method value is high because S11 is not a length/metric bug; it is an overbroad dependency proof.

## Pause Gate

Do not enumerate generated expression shapes. Reopen only for:

- another dependency owner where metadata-only changes are rejected as semantic changes;
- a silent wrong-acceptance where dependency existence is under-checked;
- fix validation for virtual/stored generated columns, functional indexes, defaults, comments,
  rename, type change, and nullability.
