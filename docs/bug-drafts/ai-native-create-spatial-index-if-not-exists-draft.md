# CREATE SPATIAL INDEX IF NOT EXISTS fails before duplicate index no-op

## Summary

`CREATE SPATIAL INDEX IF NOT EXISTS` can fail even when the target table already has an index with
the requested name. TiDB rejects the unsupported `SPATIAL` index type before it reaches the
duplicate-name `IF NOT EXISTS` no-op path.

Remote `found_bug`: id630022, confirmed.

## User-visible Symptom

A rerunnable index DDL can abort even though the requested index name already exists:

```sql
CREATE SPATIAL INDEX IF NOT EXISTS idx_a ON t(a);
```

TiDB returns:

```text
ERROR 8200 (HY000): SPATIAL index is not supported
```

The existing index is unchanged, but the idempotent statement fails.

## Minimal Repro

Environment: testbed `8192975`, TiDB `8.0.11-TiDB-v8.4.0-this-is-a-placeholder`.

```sql
DROP DATABASE IF EXISTS ai_s15_idx;
CREATE DATABASE ai_s15_idx;
USE ai_s15_idx;

CREATE TABLE t(a INT, b INT);
CREATE INDEX idx_a ON t(a);
SHOW INDEX FROM t;

-- Green duplicate control. Name identity dominates candidate column list.
CREATE INDEX IF NOT EXISTS idx_a ON t(b);
SHOW WARNINGS;
SHOW INDEX FROM t;

-- Red cell: same index name exists, but candidate SPATIAL type is rejected first.
CREATE SPATIAL INDEX IF NOT EXISTS idx_a ON t(a);
SHOW WARNINGS;
SHOW INDEX FROM t;

-- Target-absent control.
CREATE SPATIAL INDEX IF NOT EXISTS idx_sp_absent ON t(a);
SHOW WARNINGS;
SHOW INDEX FROM t;
```

Observed:

```text
CREATE INDEX IF NOT EXISTS idx_a ON t(b)
  -> Query OK, Note 1061 Duplicate key name 'idx_a'
  -> SHOW INDEX still has only idx_a on column a

CREATE SPATIAL INDEX IF NOT EXISTS idx_a ON t(a)
  -> ERROR 8200 SPATIAL index is not supported
  -> SHOW INDEX still has only idx_a on column a

CREATE SPATIAL INDEX IF NOT EXISTS idx_sp_absent ON t(a)
  -> ERROR 8200
  -> no idx_sp_absent is created
```

## Expected

When the target table already has an index with the requested name and `IF NOT EXISTS` is present,
TiDB should classify the statement as a duplicate no-op before rejecting candidate-only properties
that will not be used.

When the index name is absent, the same unsupported `SPATIAL` type should still hard-error.

## Root Cause

Source anchors:

- `/Users/bba/pc/tidb/pkg/parser/parser_test.go:3469`: parser accepts
  `CREATE SPATIAL INDEX IF NOT EXISTS`.
- `/Users/bba/pc/tidb/pkg/ddl/executor.go:5065`: `createIndex`
- `/Users/bba/pc/tidb/pkg/ddl/executor.go:5068`: checks `keyType`.
- `/Users/bba/pc/tidb/pkg/ddl/executor.go:5069`: `IndexKeyTypeSpatial`.
- `/Users/bba/pc/tidb/pkg/ddl/executor.go:5070`: returns unsupported-index error.
- `/Users/bba/pc/tidb/pkg/ddl/executor.go:5085`: duplicate-name classifier
  `checkIndexNameAndColumns` is reached only later.

The order is:

```text
CREATE SPATIAL INDEX IF NOT EXISTS idx_a ON t(a)
  -> createIndex
  -> keyType == Spatial
  -> return ERROR 8200
  -> never reaches FindIndexByName / IF NOT EXISTS note path
```

## Quality

Severity: low.

This is a wrong-error for rerunnable DDL. It does not corrupt index metadata. It is still a useful
methodology hit because it applies S15 to a common top-level DDL owner and a very compact
capability gate.

## Method Lesson

This validates the S15 sub-shape:

```text
capability gate before duplicate classifier
```

The important control is `CREATE INDEX IF NOT EXISTS idx_a ON t(b)`: TiDB already treats same-name
index creation as a no-op even when the candidate column list differs. Therefore the red `SPATIAL`
cell is not a different-object ambiguity; it is a candidate type gate dominating the same-name
classifier.

Negative calibration from the same scan:

- `CREATE DATABASE IF NOT EXISTS` looks green statically because `CreateSchemaWithInfo` checks
  schema existence before charset/collation and placement validation.

Stop rule: do not enumerate index types or index options. Reopen only for a different DDL owner, a
stronger consequence than wrong-error, or fix validation.
