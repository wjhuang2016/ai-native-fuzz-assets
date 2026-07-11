# CREATE MASKING POLICY IF NOT EXISTS pre-validates unused expressions

## Summary

`CREATE MASKING POLICY IF NOT EXISTS` can still fail when the same masking policy already exists on
the same table column. The failure happens because TiDB validates the unused candidate expression
before it reaches the duplicate-policy no-op path.

Remote `found_bug`: id630021, confirmed.

## User-visible Symptom

A rerunnable masking-policy DDL can abort even though the requested policy already exists:

```sql
CREATE MASKING POLICY IF NOT EXISTS p_mp ON t(a) AS b DISABLE;
```

If `p_mp` already exists on `t(a)`, TiDB still returns:

```text
ERROR 8275 (HY000): masking policy expression can only reference the target column 'a'
```

The existing policy is unchanged, but the idempotent script fails.

## Minimal Repro

Environment: testbed `8192975`, TiDB `8.0.11-TiDB-v8.4.0-this-is-a-placeholder`.

```sql
DROP DATABASE IF EXISTS ai_s15_mp;
CREATE DATABASE ai_s15_mp;
USE ai_s15_mp;

CREATE TABLE t(a VARCHAR(32), b VARCHAR(32));
CREATE MASKING POLICY p_mp ON t(a) AS a ENABLE;

SELECT policy_name, table_name, column_name, expression, status
FROM mysql.tidb_masking_policy
WHERE db_name='ai_s15_mp' AND policy_name='p_mp';

-- Green duplicate control.
CREATE MASKING POLICY IF NOT EXISTS p_mp ON t(a) AS concat(a, '_x') DISABLE;
SHOW WARNINGS;

SELECT policy_name, table_name, column_name, expression, status
FROM mysql.tidb_masking_policy
WHERE db_name='ai_s15_mp' AND policy_name='p_mp';

-- Red cell: target policy exists, but unused expression is rejected.
CREATE MASKING POLICY IF NOT EXISTS p_mp ON t(a) AS b DISABLE;
SHOW WARNINGS;

SELECT policy_name, table_name, column_name, expression, status
FROM mysql.tidb_masking_policy
WHERE db_name='ai_s15_mp' AND policy_name='p_mp';

-- Target-absent control.
CREATE MASKING POLICY IF NOT EXISTS p_absent ON t(a) AS b DISABLE;
SHOW WARNINGS;
```

Observed:

```text
CREATE MASKING POLICY IF NOT EXISTS p_mp ON t(a) AS concat(a, '_x') DISABLE
  -> Query OK, Note 1105 masking policy p_mp already exists
  -> existing policy stays expression `a`, status ENABLED

CREATE MASKING POLICY IF NOT EXISTS p_mp ON t(a) AS b DISABLE
  -> ERROR 8275 masking policy expression can only reference the target column 'a'
  -> existing policy stays unchanged

CREATE MASKING POLICY IF NOT EXISTS p_absent ON t(a) AS b DISABLE
  -> ERROR 8275
  -> no p_absent policy is created
```

## Expected

When the same policy already exists on the same table column and `IF NOT EXISTS` is present, TiDB
should classify the statement as an idempotent no-op before validating candidate expressions that
will be discarded.

When the policy is absent, the same invalid expression should still hard-error.

## Root Cause

Source anchors:

- `/Users/bba/pc/tidb/pkg/ddl/executor.go:6477`: `CreateMaskingPolicy`
- `/Users/bba/pc/tidb/pkg/ddl/executor.go:6495`: calls `buildMaskingPolicyInfo` before setting
  `OnExistIgnore`.
- `/Users/bba/pc/tidb/pkg/ddl/executor.go:6509`: `OnExistIgnore` is selected only after candidate
  policy info is built.
- `/Users/bba/pc/tidb/pkg/ddl/masking_policy.go:344`: `buildMaskingPolicyInfo`
- `/Users/bba/pc/tidb/pkg/ddl/masking_policy.go:370`: validates candidate expression.
- `/Users/bba/pc/tidb/pkg/ddl/masking_policy.go:409`: `validateMaskingPolicyExpression`
- `/Users/bba/pc/tidb/pkg/ddl/masking_policy.go:416`: extracts referenced columns.
- `/Users/bba/pc/tidb/pkg/ddl/masking_policy.go:419`: returns
  `ErrMaskingPolicyExprInvalidColumn` before duplicate classification.

The order is:

```text
CREATE MASKING POLICY IF NOT EXISTS p_mp ON t(a) AS b
  -> get table t
  -> buildMaskingPolicyInfo(candidate)
  -> validate expression b against target column a
  -> ERROR 8275
  -> never reaches createMaskingPolicyWithInfo duplicate-policy note path
```

## Quality

Severity: low.

This is a user-visible wrong-error for rerunnable DDL. It does not corrupt metadata, and the
existing masking policy remains unchanged. It is still valuable because it extends S15 to a
security/side-metadata owner with expression validation.

## Method Lesson

This is another confirmation of the refined S15 rule:

```text
target exists + IF NOT EXISTS should dominate candidate validity
```

Compared with id630020, the early gate is not an option setter; it is expression validation inside
a metadata builder.

The compact matrix stayed at three cells:

```text
existing target + valid candidate   -> Note/no-op
existing target + invalid candidate -> RED hard error
absent target + same invalid        -> expected hard error
```

The identity ambiguity was controlled by keeping the same policy name, same table, and same target
column in the red cell.

Stop rule: do not enumerate masking expressions or other masking policy options. Reopen only for a
different DDL owner, a stronger consequence than wrong-error, or fix validation.
