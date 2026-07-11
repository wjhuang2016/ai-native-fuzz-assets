# id630021 Method Case — masking-policy expression validation before IF NOT EXISTS classifier

## Bug

`CREATE MASKING POLICY IF NOT EXISTS` can fail on an already-existing masking policy when the
unused candidate expression references a non-target column.

Remote `found_bug`: id630021, confirmed.

## Selector

S15: DDL idempotence precheck ordering.

```text
P_check:
  The candidate masking-policy expression references only the target column.

Q_claim:
  TiDB may proceed to create the masking policy.

Missing D:
  With IF NOT EXISTS and the same policy already present on the same table column, the candidate
  expression is discarded.

F_effect:
  CreateMaskingPolicy calls buildMaskingPolicyInfo before selecting OnExistIgnore.
  buildMaskingPolicyInfo validates the expression and returns ERROR 8275 before the duplicate
  classifier in createMaskingPolicyWithInfo can append a note.
```

## Matrix

```text
target policy exists + valid candidate:
  CREATE MASKING POLICY IF NOT EXISTS p_mp ON t(a) AS concat(a, '_x') DISABLE
  -> GREEN, Note 1105 and existing expression/status unchanged

target policy exists + invalid unused candidate:
  CREATE MASKING POLICY IF NOT EXISTS p_mp ON t(a) AS b DISABLE
  -> RED, ERROR 8275

target policy absent + same invalid candidate:
  CREATE MASKING POLICY IF NOT EXISTS p_absent ON t(a) AS b DISABLE
  -> GREEN control, ERROR 8275 and no p_absent row
```

## Why This Worked

The target identity was pinned down before testing:

```text
same policy name: p_mp
same table:       t
same column:      a
only candidate expression changes
```

That removes the ambiguity where `IF NOT EXISTS` might not apply because the statement targets a
different policy or column.

The source order then predicted the red cell:

```text
getSchemaAndTableByIdent
buildMaskingPolicyInfo
  check table
  find target column
  validate candidate expression
select OnExistIgnore
createMaskingPolicyWithInfo
  duplicate-policy classifier
```

## Quality Assessment

- User impact: rerunnable masking policy DDL can fail despite the target policy already existing.
- Data risk: low; existing policy metadata is unchanged.
- Signal quality: good methodology confirmation. It validates the builder/validator-before-target
  classifier pattern on a security/side-metadata DDL owner.
- Fix shape: classify existing same-table/same-column policy before validating candidate
  expression that would be discarded; still reject invalid expressions when the policy is absent.

## Method Refinement

id630020 showed that option setters inside builders can be validators. id630021 adds expression
validation inside a metadata builder.

The S15 audit checklist is now:

1. Confirm the grammar accepts `IF NOT EXISTS`.
2. Define target identity precisely enough to avoid ambiguity.
3. Locate the first duplicate/target-exists classifier.
4. Mark every earlier resolver, builder, option setter, and expression validator.
5. Build only the three-cell matrix: existing+valid, existing+invalid, absent+same-invalid.

Do not enumerate masking expressions. A second expression form would be blast radius, not a new
method result.
