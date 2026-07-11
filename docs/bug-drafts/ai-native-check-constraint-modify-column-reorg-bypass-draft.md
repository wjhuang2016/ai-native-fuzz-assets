# id630013 Draft: MODIFY COLUMN Can Leave Rows Violating Existing CHECK Constraints

Remote `found_bug` row:

```text
id:        630013
status:    issue-filed
severity:  high
title:     MODIFY COLUMN can leave rows violating existing CHECK constraints
issue:     https://github.com/pingcap/tidb/issues/69649
oracle:    O20_POST_CONVERSION_CHECK_ORACLE
method:    S17_DDL_REORG_CONSTRAINT_BYPASS
```

## User-Visible Symptom

`ALTER TABLE ... MODIFY COLUMN` can convert existing rows into values that violate an already
enforced `CHECK` constraint. The DDL succeeds with no warnings under `STRICT_TRANS_TABLES`; the
final table still publishes the CHECK constraint, but `SELECT` can find rows where the predicate is
false.

Minimal repro confirmed on testbed `8192975` / `fp-tidb`:

```sql
DROP DATABASE IF EXISTS ai_chk_reorg_0703;
CREATE DATABASE ai_chk_reorg_0703;
USE ai_chk_reorg_0703;

CREATE TABLE t(
  a DECIMAL(10,2),
  CONSTRAINT c CHECK (a > 0)
);
INSERT INTO t VALUES (0.4),(1.2);

SELECT a, a > 0 AS ok FROM t;
ALTER TABLE t MODIFY a INT;
SHOW WARNINGS;
SELECT a, a > 0 AS ok FROM t;
SHOW CREATE TABLE t;
```

Observed:

```text
before ALTER: 0.40 -> ok=1, 1.20 -> ok=1
ALTER TABLE t MODIFY a INT: Query OK, no warnings
after ALTER:  0    -> ok=0, 1    -> ok=1
SHOW CREATE:  CONSTRAINT `c` CHECK ((`a` > 0))
```

The same shape reproduced with `VARCHAR(10)` and `DOUBLE` source columns containing `0.4`.

## Strong Oracles

Reference add-check oracle:

```sql
CREATE TABLE ref(a INT);
INSERT INTO ref VALUES (0),(1);
ALTER TABLE ref ADD CONSTRAINT c_ref CHECK (a > 0);
```

Observed:

```text
ERROR 3819 (HY000): Check constraint 'c_ref' is violated.
```

DML oracle:

```sql
INSERT INTO t VALUES (0);
```

Observed:

```text
ERROR 3819 (HY000): Check constraint 'c' is violated.
```

So CHECK itself is enforced on ordinary writes and on `ADD CHECK` validation; the gap is specific to
the `MODIFY COLUMN` data-rewrite path.

`ADMIN CHECK TABLE t` returned success, which means this class is not caught by TiDB's normal
record/index consistency checker.

## Source Chain

- `pkg/ddl/constraint.go:354-389`: `ADD CHECK` has `verifyRemainRecordsForCheckConstraint`, which
  scans existing rows and rejects violations.
- `pkg/table/tables/tables.go:508-510`: ordinary `UpdateRecord` evaluates writable CHECK
  constraints before writing.
- `pkg/table/tables/tables.go:888`: ordinary `AddRecord` evaluates writable CHECK constraints
  before writing.
- `pkg/ddl/column.go:592-604`: `modifyTableColumn` dispatches `MODIFY COLUMN` data rewrite to
  `updatePhysicalTableRow`.
- `pkg/ddl/column.go:754-815`: `updateColumnWorker.getRowRecord` decodes the old row, casts the
  old column value to the new column type, and encodes the new row.
- `pkg/ddl/column.go:847-863`: the backfill transaction writes the converted row with `txn.Set`
  directly. No `table.CheckRowConstraint` equivalent appears on this path.

## P / Q / D / F Card

```text
P_check:  existing rows satisfied CHECK under the old column type, and CastColumnValue succeeds
Q_claim:  converted rows still satisfy all writable CHECK constraints under the new column type
D_dim:    type conversion can change predicate truth value, e.g. DECIMAL 0.4 -> INT 0
F_effect: MODIFY COLUMN reorg writes converted rows directly to KV and publishes the new schema
O_oracle: direct ADD CHECK rejects the same final data; DML rejects future inserts of the bad value
```

## Quality

High-quality DDL correctness bug:

- It violates a published data-integrity constraint.
- It is silent: the DDL succeeds and `SHOW WARNINGS` is empty.
- The final table is self-inconsistent: `SHOW CREATE` declares `CHECK (a > 0)` while rows exist
  with `a > 0 = 0`.
- The reference oracle is strong: `ADD CHECK` and ordinary DML both reject the same final value.

## Fix Direction

During `MODIFY COLUMN` backfill, evaluate writable CHECK constraints on the post-conversion row
before `txn.Set`, or run a `verifyRemainRecordsForCheckConstraint`-style scan after conversion and
before the new column reaches public state. Prefer reusing the DML `table.CheckRowConstraint`
semantics to avoid a second CHECK evaluator drifting over time.

## Duplicate Notes

During verification, `CREATE TABLE clone LIKE t` also showed source CHECK name mutation. That is
already known and inserted as `found_bug id630005`; it is not part of id630013.
