# ADMIN REPAIR TABLE can publish index metadata that does not match reused physical data

## Current classification

- Selector: `REPAIR_INDEX_PHYSICAL_METADATA_RECONCILIATION`
- Module: DDL recovery / `ADMIN REPAIR TABLE`
- Candidate root: `repair-table-index-metadata-not-reconciled`
- Severity: observed high-consequence wrong-result, product-invalid under the documented repair contract
- Status: screened out; retained as a recovery guardrail, not an upstream bug
- Not a new surface of `id1470001` or `id1500002`

## Source proof obligation

`executor.RepairTable` builds a new `TableInfo` from the user-supplied
`CREATE TABLE`, then deliberately preserves the old table ID and index IDs. The
source has an explicit TODO that the new metadata should be verified against the
actual data. The current checks are:

```text
column: name + type
index:  name + column names + index type
```

They do not compare index prefix length, uniqueness, visibility, or other options
that affect physical encoding, constraint enforcement, or planner assumptions.
`getIndexInfoByNameAndColumn` and `indexColumnSliceEqual` confirm this boundary.

The proof obligation is:

```text
P: an existing physical index and its old TableInfo are being reused
Q: the operator-supplied repaired TableInfo describes the same physical index
F: RepairTable publishes Q while preserving the old index ID/data

P + Q must imply that table scans, index scans, planner uniqueness assumptions,
and future writes observe the same rows and constraint semantics.
```

## Live reproduction

Environment:

- testbed `8220955`
- namespace `testbed-tps-8220955-1-213`
- endpoint `127.0.0.1:14003`
- current-master build `13282a8bd06bd33324a4dbfd3c1c03685f3cd9aa`

The test-only `enableTestAPI` and `repairFetchCreateTable` failpoints were used
only to expose a pre-existing public table through the repair harness. No index
data fault was injected.

### Strong control: exact physical definition

Create `t_exact` with `KEY idx_v(v(3))` and rows:

```sql
CREATE TABLE t_exact (
  id INT PRIMARY KEY,
  v VARCHAR(20) NOT NULL,
  KEY idx_v(v(3))
);
INSERT INTO t_exact VALUES
  (1, 'abc-one'), (2, 'abc-two'), (3, 'def-three');
```

Repair it with the exact same index definition:

```sql
ADMIN REPAIR TABLE t_exact CREATE TABLE t_exact (
  id INT PRIMARY KEY,
  v VARCHAR(20) NOT NULL,
  KEY idx_v(v(3))
);
```

Both table scan and forced index lookup return `id=2` for `v='abc-two'`, and
`ADMIN CHECK TABLE t_exact` is silent. This is the GREEN control.

### RED 1: prefix metadata mismatch

Create a physical `KEY idx_v(v(3))`, then repair with `KEY idx_v(v(2))`:

```sql
ADMIN REPAIR TABLE t CREATE TABLE t (
  id INT PRIMARY KEY,
  v VARCHAR(20) NOT NULL,
  KEY idx_v(v(2))
);

SELECT id FROM t IGNORE INDEX(idx_v) WHERE v = 'abc-two';
SELECT id FROM t FORCE INDEX(idx_v) WHERE v = 'abc-two';
ADMIN CHECK TABLE t;
```

Observed:

```text
table scan: 2
forced index: empty
ADMIN CHECK TABLE: silent
```

The physical row is still present. The new metadata makes the equality lookup
construct a different index key from the one stored on disk.

### RED 2: nonunique physical index repaired as UNIQUE

Create a normal index with existing duplicates:

```sql
CREATE TABLE t_nonunique (
  id INT PRIMARY KEY,
  v VARCHAR(20) NOT NULL,
  KEY idx_v(v)
);
INSERT INTO t_nonunique VALUES
  (1, 'abc-one'), (2, 'abc-two'), (3, 'abc-one');
```

Repair it as unique without rebuilding the index:

```sql
ADMIN REPAIR TABLE t_nonunique CREATE TABLE t_nonunique (
  id INT PRIMARY KEY,
  v VARCHAR(20) NOT NULL,
  UNIQUE KEY idx_v(v)
);
INSERT INTO t_nonunique VALUES (4, 'abc-one');
```

The insert succeeds even though `SHOW CREATE TABLE` now advertises a UNIQUE
index. The rowset then splits:

```sql
SELECT id FROM t_nonunique IGNORE INDEX(idx_v)
WHERE v = 'abc-one' ORDER BY id;
-- 1, 3, 4

SELECT id FROM t_nonunique FORCE INDEX(idx_v)
WHERE v = 'abc-one' ORDER BY id;
-- 4

SELECT id FROM t_nonunique WHERE v = 'abc-one';
-- 4; EXPLAIN uses Point_Get on idx_v

ADMIN CHECK TABLE t_nonunique;
-- silent
```

The reverse mismatch, physical `UNIQUE KEY` repaired as ordinary `KEY`, also
allows duplicate values after repair. It is the same root and not a second bug.

## Why this is high quality

This is not a stale metadata display-only issue:

```text
RepairTable reuses physical index ID/data
-> incomplete metadata equivalence check
-> planner trusts a false prefix/uniqueness property
-> default equality query returns the wrong rowset
-> ADMIN CHECK TABLE does not detect it
```

The exact-prefix repair control is important: the repair operation itself is not
the problem. The RED requires a mismatch between the definition supplied to repair
and the physical index being reused.

## Contract gate before upstream filing

`ADMIN REPAIR TABLE` is an operator recovery command, so the upstream report must
state the contract precisely. The candidate is a product bug if the command is
expected to reject or validate an index definition that cannot be proven compatible
with existing physical data. If the contract instead says that the operator is
fully responsible for supplying an exact physical definition, this should remain a
methodology asset and a guardrail/documentation gap rather than a filed severe bug.

The official TiDB documentation resolves this gate in the latter direction: the
repair operation is described as **untrusted**, and the operator must manually ensure
that the original metadata is covered by the supplied `CREATE TABLE` statement.
Therefore the mismatched `PrefixLen` and `Unique` cells above are intentionally
incompatible input, not a confirmed product defect. The observed wrong-result behavior
is still valuable because it proves that this contract must be an explicit admission
gate before any future repair candidate is treated as a bug.

Screen verdict: `INVALID(contract-untrusted-repair-definition)`. Do not file upstream
unless a product-feasible path can supply a definition believed to be exact while the
physical index still differs.

Evidence: `assets/store/logs/admin-repair-index-metadata-red-20260712.log`.
