# Method Case: id630025 EXCHANGE PARTITION DEFAULT validation SQL

## One-line result

`EXCHANGE PARTITION ... WITH VALIDATION` fails on a `LIST DEFAULT` partition with an internal
syntax error, even when the standalone table's rows route to that DEFAULT partition.

## P/Q/F/O Card

```text
P_check:
  EXCHANGE PARTITION validates standalone-table rows by generating a restricted SQL predicate for
  the target partition.

Q_claim:
  The generated predicate is equivalent to the partition locator, including DEFAULT partition
  semantics.

D_dims:
  DEFAULT partitions are complements of all explicit LIST values. They are not represented by the
  current partition's ordinary InValues list.

F_effect:
  The validation safe path builds `not () limit 1`, so valid EXCHANGE PARTITION statements fail
  before data is exchanged.

O_oracle:
  O24 partition-exchange validation oracle:
  prove direct routing into the DEFAULT partition, compare ordinary LIST exchange validation, then
  compare WITH VALIDATION against WITHOUT VALIDATION for the same legal row.
```

## Matrix

```text
direct DEFAULT routing:
  INSERT 3 into pt_direct; SELECT PARTITION(pdef) -> 3
  classification: GREEN target-state oracle

ordinary LIST exchange:
  p1 VALUES IN (1), standalone row 1
  EXCHANGE p1 WITH nt -> success
  classification: GREEN validation-builder control

DEFAULT LIST exchange:
  pdef DEFAULT, standalone row 3
  EXCHANGE pdef WITH nt -> ERROR 1064 near ") limit 1"
  classification: RED

DEFAULT LIST without validation:
  pdef DEFAULT, standalone row 3
  EXCHANGE pdef WITH nt WITHOUT VALIDATION -> success
  classification: GREEN boundary/control

DEFAULT LIST COLUMNS:
  pdef DEFAULT, standalone row (3,3)
  EXCHANGE pdef WITH nt -> ERROR 1064 near ") limit 1"
  classification: RED sibling, same root
```

## Why This Was Fast

This came from the improved P4 rule, not from partition syntax fuzzing:

```text
consequence-first source scan
-> high-risk lane: state-transforming DDL validation
-> source TODO says DEFAULT partition not handled
-> direct target-state oracle proves the row belongs
-> boundary path shows only the validation safe path is broken
```

The useful move was treating internal validation SQL as another "fast path": the code checked
"current partition has InValues" and assumed that proved "we can express partition membership by
iterating current InValues." DEFAULT partitions break that proof because membership is a
complement.

## Quality

Low severity, medium method value.

- User-visible deterministic wrong-error.
- No data corruption: validation fails before exchange.
- Not S15/static-precheck enumeration: the target was selected from the high-risk
  state-transforming DDL lane, and the bug is inside the safety validation builder.
- New root by Reopen test: id630016 is fixed by duplicate/existence ordering; id630025 needs a
  DEFAULT-complement predicate or partition-locator validation.

## Stop Rule

Do not enumerate `LIST` variants. Reopen S19 only for:

- another internal validation SQL builder that omits a different semantic dimension;
- a wrong-acceptance/data-placement consequence;
- fix validation for DEFAULT partition exchange.
