# id630007 Method Case: Functional-Index Dependency Gate

## Selector

```text
S11_DDL_DEPENDENCY_GATE_OVERBROAD
```

This case reuses S11 on a second dependency owner: expression indexes.

```text
P_check:  column A is referenced by hidden generated column B for expression index E
Q_claim:  any MODIFY COLUMN operation on A can invalidate E
effect:   reject before distinguishing semantic changes from metadata-only changes
D_dim:    COMMENT/DEFAULT metadata changes do not rename A, change the expression, or change A's type
```

## Matrix

| Cell | Initial state | Target state | Oracle | Result |
| --- | --- | --- | --- | --- |
| Direct comment target | `a int`, `idx_expr((a+1))` | `a int COMMENT 'new-comment'` | create + query + `ADMIN CHECK` | GREEN |
| ALTER comment | same | same target | should match direct target | RED, ERROR 3106/3837 |
| Direct default target | `a int`, `idx_expr((a+1))` | `a int DEFAULT 5` | default insert returns `5,6` | GREEN |
| ALTER default | same | same target | should match direct target | RED, ERROR 3106/3837 |
| Non-dependent column | expression index depends on `a`, modify `b` | `b COMMENT ...` | dependency absent for `b` | GREEN |
| Drop index first | expression index removed | `a COMMENT ...` | dependency removed | GREEN |
| True type change | expression index depends on `a` | `a BIGINT` | true semantic change reject | GREEN reject |

## Oracle

```text
O14_TARGET_TYPE_ACCEPTANCE_REFERENCE
```

The direct target schema is the reference. If TiDB can directly create a table with the same
expression index and the same metadata-only column attributes, the equivalent metadata-only ALTER
should not fail solely because an expression-index dependency exists.

## Why The Method Worked

id630004 taught the compact rule:

```text
dependency exists
therefore all MODIFY COLUMN operations are unsafe
```

The improvement in this tick was to test a different dependency owner without changing the matrix
shape. Expression indexes are stored through hidden generated columns, so the same code path returns
`ErrDependentByFunctionalIndex`. The red cells proved S11 is not just a generated-column syntax
case; it also affects the expression-index feature surface.

## Quality

Medium, but root-cause novelty is limited.

- User-visible symptom: valid metadata-only DDL fails with `ERROR 3106/3837`.
- Strong oracle: direct target expression-index schemas work and pass `ADMIN CHECK TABLE`.
- It is a companion/blast-radius case for id630004, not a fresh root-cause family.
- The method value is that S11 generalized to a second dependency owner with a tiny matrix.

## Pause Gate

Do not enumerate expression-index syntax. Reopen S11 only for:

- another dependency owner with a distinct code path;
- a silent wrong-acceptance consequence;
- fix validation across generated columns, expression indexes, defaults, comments, rename, type
  change, and nullability.
