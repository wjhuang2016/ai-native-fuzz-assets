# id630022 Method Case — CREATE INDEX capability gate before duplicate classifier

## Bug

`CREATE SPATIAL INDEX IF NOT EXISTS` can fail on an already-existing index name because the
unsupported `SPATIAL` type gate runs before duplicate-name classification.

Remote `found_bug`: id630022, confirmed.

## Selector

S15: DDL idempotence precheck ordering.

```text
P_check:
  The requested index type is supported.

Q_claim:
  TiDB may proceed to create the index.

Missing D:
  With IF NOT EXISTS and an already-existing same-name index, the candidate index type is discarded.

F_effect:
  createIndex returns ERROR 8200 for SPATIAL before checkIndexNameAndColumns can append the
  duplicate-key note and return the no-op sentinel.
```

## Matrix

```text
target index exists + valid candidate:
  CREATE INDEX IF NOT EXISTS idx_a ON t(b)
  -> GREEN, Note 1061 and idx_a remains on column a

target index exists + unsupported candidate type:
  CREATE SPATIAL INDEX IF NOT EXISTS idx_a ON t(a)
  -> RED, ERROR 8200

target index absent + same unsupported type:
  CREATE SPATIAL INDEX IF NOT EXISTS idx_sp_absent ON t(a)
  -> GREEN control, ERROR 8200 and no new index
```

## Why This Worked

The source order is unusually direct:

```text
createIndex
  switch keyType
    SPATIAL -> unsupported
  get table
  checkIndexNameAndColumns
    FindIndexByName
    if IF NOT EXISTS append note and no-op
```

The ordinary duplicate control proves TiDB's identity rule for `IF NOT EXISTS` is index name on the
same table, not candidate column list. That makes the `SPATIAL` red cell a clean ordering bug.

## Quality Assessment

- User impact: rerunnable `CREATE INDEX IF NOT EXISTS` can abort if the candidate statement uses
  an unsupported index type while the index name already exists.
- Data risk: low; existing index metadata remains unchanged.
- Signal quality: good S15 confirmation on a common top-level DDL owner.
- Fix shape: check same-table same-name index existence before candidate-only type gates, while
  preserving unsupported-type errors when the index name is absent.

## Method Refinement

id630022 reinforces that capability gates should be audited before duplicate classifiers even when
the gate is a short `switch` at the top of an executor function.

Negative calibration from the same source pass:

```text
CREATE DATABASE IF NOT EXISTS
  -> CreateSchemaWithInfo checks existing DB before charset/collation/placement validation
  -> green control for this selector
```

Do not enumerate `FULLTEXT`, `VECTOR`, columnar, or index option variants from this hit. The method
result is the owner/path ordering, not the list of index types.
