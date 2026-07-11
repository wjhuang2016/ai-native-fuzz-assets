# id630009 Method Case: Partial-Index Dependency Gate Overbroad

## Selector

```text
S11_DDL_DEPENDENCY_GATE_OVERBROAD
```

This case starts from a dependency checker that answers only "is this column referenced?", while
the DDL path uses that answer as if it proved "every MODIFY on this column is unsafe".

```text
P_check:  partial index condition references column b
Q_claim:  any MODIFY COLUMN b can invalidate the partial-index condition
effect:   MODIFY COLUMN rejects before classifying the requested change
D_dim:    metadata-only COMMENT/DEFAULT changes do not alter the condition semantics
```

## Matrix

| Cell | SQL shape | Oracle | Result |
| --- | --- | --- | --- |
| Direct comment target | `CREATE TABLE ... b INT COMMENT ..., INDEX idx_a(a) WHERE b > 0` | target schema + `ADMIN CHECK` | GREEN |
| Existing table comment ALTER | `ALTER TABLE ... MODIFY b INT COMMENT ...` | should reach same target schema | RED, ERROR 8272 |
| Direct default target | `CREATE TABLE ... b INT DEFAULT 5, INDEX idx_a(a) WHERE b > 0` | default insert + `ADMIN CHECK` | GREEN |
| Existing table default ALTER | `ALTER TABLE ... MODIFY b INT DEFAULT 5` | should reach same target schema | RED, ERROR 8272 |
| Non-condition column metadata | `MODIFY c INT COMMENT ...` | unrelated column should work | GREEN |
| Drop index then metadata | `DROP INDEX idx_a; MODIFY b INT COMMENT ...` | dependency removed | GREEN |

## Oracle

```text
O14_TARGET_TYPE_ACCEPTANCE_REFERENCE
```

The direct target schema is the safe path: if TiDB can create and check the final schema directly,
an equivalent metadata-only transition should not be rejected by a dependency gate unless the
product intentionally imposes stricter ALTER semantics.

## Why The Method Worked

The source shape was crisp:

```text
checkColumnReferencedByPartialCondition:
  if colName appears in idx.AffectColumn -> ErrModifyColumnReferencedByPartialCondition

MODIFY COLUMN:
  call checker before distinguishing COMMENT/DEFAULT from rename/type/collation/nullability
```

That immediately yields the tiny matrix:

```text
same final partial-index schema via CREATE TABLE -> GREEN
same final partial-index schema via ALTER COMMENT/DEFAULT -> RED
unrelated column / dropped dependency -> GREEN
```

## Quality

Medium method value, low-to-medium product severity.

- Product symptom: routine metadata-only schema changes are blocked on partial-index condition
  columns.
- Oracle strength: direct target schema, behavior query, and `ADMIN CHECK TABLE` all agree the
  target state is valid.
- Root-cause novelty: this is still S11, but it is a different dependency gate from generated
  columns / expression indexes. Count it as a new owner and stronger selector validation, not a
  wholly new class.

## Pause Gate

Do not enumerate partial-index predicate syntax. Reopen this family only for:

- a silent wrong-acceptance where a semantic change is allowed and corrupts partial-index behavior;
- fix validation across COMMENT, DEFAULT, rename, drop, type, collation, and nullability;
- another dependency gate with a different checker and a strong target-schema oracle.
