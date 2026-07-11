# id630020 Method Case — resource group candidate builder before IF NOT EXISTS classifier

## Bug

`CREATE RESOURCE GROUP IF NOT EXISTS` can fail on an already-existing resource group when the
unused candidate definition contains a `BACKGROUND` option.

Remote `found_bug`: id630020, confirmed.

## Selector

S15: DDL idempotence precheck ordering.

```text
P_check:
  The candidate resource-group definition is built successfully before submitting the DDL job.

Q_claim:
  TiDB may proceed to create the resource group.

Missing D:
  With IF NOT EXISTS and an existing target resource group, the candidate definition will be
  discarded and should not need to satisfy creation-time option gates.

F_effect:
  AddResourceGroup calls buildResourceGroup before ResourceGroupByName. buildResourceGroup rejects
  BACKGROUND for non-default groups, so the duplicate no-op classifier is unreachable.
```

## Matrix

```text
target exists + valid candidate:
  CREATE RESOURCE GROUP IF NOT EXISTS ai_rg_s15 RU_PER_SEC=2000
  -> GREEN, Note 8248, existing RU_PER_SEC remains 1000

target exists + invalid unused candidate:
  CREATE RESOURCE GROUP IF NOT EXISTS ai_rg_s15 BACKGROUND=()
  -> RED, ERROR 1105

target absent + same invalid candidate:
  CREATE RESOURCE GROUP IF NOT EXISTS ai_rg_s15_absent BACKGROUND=()
  -> GREEN control, ERROR 1105, no group created
```

## Why This Worked

The selector started from a proof obligation instead of SQL enumeration:

```text
If IF NOT EXISTS is present, target existence dominates candidate validity.
```

The source made the risk visible:

```text
buildResourceGroup(...)       -- candidate builder and option gate
ResourceGroupByName(...)      -- target-exists classifier
AppendNote(ErrResourceGroupExists)
```

That order means any error in `buildResourceGroup` can bypass the idempotent no-op path.

## Quality Assessment

- User impact: rerunnable resource-group DDL can abort even though the target already exists.
- Data risk: low; the existing group remains unchanged.
- Signal quality: good for methodology, because it proves the create-like selector is not limited
  to the shared `CreateTableWithInfo` path used by table and sequence.
- Fix shape: move the target-exists `IF NOT EXISTS` classifier before candidate build-time option
  gates, while preserving hard errors when the target is absent.

## Method Refinement

Previous create-like S15 hits focused on explicit validators:

- id630018: `CREATE TABLE IF NOT EXISTS` validates source/table metadata before target existence.
- id630019: `CREATE SEQUENCE IF NOT EXISTS` validates sequence options before target existence.

id630020 adds a builder-internal gate:

```text
candidate builder != harmless construction
```

For future scans, the checklist becomes:

1. Confirm grammar/AST really carries an idempotence promise.
2. Locate the first target-exists or missing-object classifier.
3. List every operation before it: resolver, builder, setter, helper, validator, capability gate.
4. For each operation, ask whether it is still required when the requested object already exists
   or is missing under an idempotence flag.
5. Run only the three-cell matrix: existing+valid, existing+invalid, absent+same-invalid.

Negative calibration from this pass:

- `CREATE VIEW IF NOT EXISTS` is parser-unsupported, so it is not an executable promise.
- `CREATE PLACEMENT POLICY IF NOT EXISTS` checks existence before semantic policy validation, so
  the obvious invalid-option cells are green controls for this sub-shape.
