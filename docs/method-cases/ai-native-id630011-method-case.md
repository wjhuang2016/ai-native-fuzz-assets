# id630011 Method Case: Validator Runs Before Target Nullability Exists

## Selector

```text
S16_DDL_VALIDATOR_ORDERING_GAP
```

This case adds a new DDL proof shape:

```text
P_check:  FK MODIFY validator sees unchanged type, flen, and decimal, then returns nil
Q_claim:  the modified child column remains compatible with all existing FK actions
effect:   column options are processed after the FK validator, so NOT NULL is added later
D_dim:    target nullability is a referential-action compatibility dimension
```

The fast path is not a special executor shortcut. It is an ordering shortcut: the validator runs
before the target column state is complete, then later code publishes a stronger target state.

## Matrix

| Cell | SQL shape | Oracle | Result |
| --- | --- | --- | --- |
| Direct DELETE target | `pid INT NOT NULL ... ON DELETE SET NULL` | target-state validator rejects | GREEN, ERROR 1830 |
| Direct UPDATE target | `pid INT NOT NULL ... ON UPDATE SET NULL` | target-state validator rejects | GREEN, ERROR 1830 |
| Transition DELETE | nullable child FK, then `MODIFY pid INT NOT NULL` | should match direct target rejection | RED, ALTER succeeds |
| Transition UPDATE | nullable child FK, then `MODIFY pid INT NOT NULL` | should match direct target rejection | RED, ALTER succeeds |
| Runtime DELETE consequence | parent delete triggers SET NULL | should not reach impossible schema | RED, ERROR 1048 |
| Runtime UPDATE consequence | parent update triggers SET NULL | should not reach impossible schema | RED, ERROR 1048 |
| RESTRICT control | nullable child FK with `ON DELETE RESTRICT`, then NOT NULL | no SET NULL write needed | GREEN, ALTER succeeds |

## Oracle

```text
O19_TARGET_STATE_REJECTION_REFERENCE
```

This oracle compares a transition path against the sibling direct target-state validator:

```text
direct target schema is rejected as invalid
but transition path reaches the same invalid target schema
then a later behavior oracle exercises the invalid state
```

For id630011, the behavior oracle is parent `DELETE`/`UPDATE`: the published FK action needs to set
the child column to NULL, but the child column is now NOT NULL.

## Why The Method Worked

The source clue was the distance between the check and the state mutation:

```text
checkModifyColumnWithForeignKeyConstraint(...)
ProcessModifyColumnOptions(...)
```

The first function receives a `newCol`, but `newCol` is not the final target column yet. Its
`NOT NULL` flag is added by option processing later. That made the small matrix obvious:

```text
direct target with NOT NULL + SET NULL -> should reject
transition nullable -> NOT NULL under same FK -> candidate red
runtime parent DELETE/UPDATE -> consequence oracle
RESTRICT action -> green control
```

This was faster than broad FK fuzzing because the AI did not enumerate FK syntax. It asked only:

```text
Which dimensions are consumed by the validator?
Which dimensions are applied after the validator?
Does a sibling target-state path validate the missing dimension?
```

## Quality

Medium severity, high method value.

- The DDL accepts an invalid schema.
- The schema later blocks ordinary parent-side DML with `ERROR 1048`.
- The repro has both target-state controls and behavior consequence controls.
- The observed consequence is fail-stop rather than silent data corruption.

## Pause Gate

Do not enumerate FK actions, type pairs, or multi-column FK shapes now. Reopen S16 only for:

- another DDL validator that runs before target options/defaults/collation/nullability are applied;
- a wrong-acceptance consequence stronger than runtime error, such as silent orphan rows or
  corrupted metadata;
- fix validation that proves CREATE/ADD FK and MODIFY COLUMN now share the same target-state
  validator.
