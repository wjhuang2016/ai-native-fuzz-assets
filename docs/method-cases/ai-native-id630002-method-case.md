# id630002 Method Case: FK Modify Validation Metric Mismatch

## Selector

```text
S10_DDL_VALIDATION_METRIC_MISMATCH
```

The code validates whether a DDL target state is safe by checking a simplified inequality over metadata. If that inequality is stronger than the product's own target-state contract, DDL can falsely reject a valid transition.

This is a high-signal selector whenever:

```text
P_check:  validator checks new metadata against old metadata or related metadata
Q_claim:  target state is valid only if that inequality holds
effect:   reject DDL before a target-state validator or data-fit oracle can run
D_dim:    sibling create/add path accepts target states outside the inequality
```

## Matrix

| Cell | Initial state | Target state | Oracle | Result |
| --- | --- | --- | --- | --- |
| Direct target A | none | parent `varchar(10)`, child `varchar(10)` FK | create succeeds | GREEN |
| Direct target B | none | parent `varchar(10)`, child `varchar(15)` FK | create succeeds | GREEN |
| Direct target C | none | parent `varchar(15)`, child `varchar(20)` FK | create succeeds | GREEN |
| Red child exact | child `varchar(20)` -> `varchar(10)`, parent `varchar(10)` | target A + data max length 10 | RED |
| Red child wider | child `varchar(20)` -> `varchar(15)`, parent `varchar(10)` | target B | RED |
| Red parent | parent `varchar(10)` -> `varchar(15)`, child `varchar(20)` | target C | RED |
| Control child widen | child `varchar(20)` -> `varchar(25)` | current checker allows | GREEN |
| Control parent widen | parent `varchar(10)` -> `varchar(20)`, child `varchar(20)` | current checker allows | GREEN |

## Oracle

```text
O14_TARGET_TYPE_ACCEPTANCE_REFERENCE
```

For DDL target-state validation, direct creation of the same target schema is a strong reference. If `CREATE TABLE` / `ADD FOREIGN KEY` accepts the target parent-child column pair, and existing data fits the target column, `MODIFY COLUMN` should not reject solely because of the transition path.

## Why The Method Worked

The source contained two sibling contracts:

```text
FK creation: type + unsigned + charset + collation must match.
FK modification: newFlen must also be >= oldFlen and >= relatedFlen.
```

That asymmetry immediately produced a target-state oracle:

```text
Can we create the exact target schema directly?
If yes, can ALTER reach the same target from a valid old schema?
```

The red cells appeared without generating random FK shapes. The matrix only needed to vary which length inequality was violated:

- `new < old`, but target pair valid;
- `new < related`, but target pair valid;
- both controls where the current checker's inequalities hold.

## Quality

Medium.

- User-visible symptom: valid DDL rejected with `ERROR 1832` or `ERROR 1833`.
- Strong oracle: direct target schema is accepted by TiDB and existing data fits.
- Root cause localized: `/Users/bba/pc/tidb/pkg/ddl/foreign_key.go:356`.
- It is not data loss or wrong result, so lower severity than id600001.
- It has high methodology value because it proves S10 generalizes beyond the `LENGTH` bug.

## Pause Gate

Do not enumerate all FK type pairs. Reopen only if:

- another target-state validator imposes a different hidden inequality than create/add path;
- the consequence changes from false rejection to silent invalid metadata;
- a fix needs validation for char/varchar, decimal, binary strings, parent/child direction, and data-fit boundaries.
