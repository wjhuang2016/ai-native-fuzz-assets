# ADD FOREIGN KEY can publish historical orphans when the referenced key contains NULL

Status: confirmed on official nightly; current master retains the source root; no exact upstream
issue found.

## Summary

`ALTER TABLE ... ADD FOREIGN KEY` validates existing child rows with a `NOT IN` subquery. If the
referenced nullable unique key contains one `NULL`, SQL three-valued logic can hide absent non-NULL
child keys. The DDL succeeds and publishes the constraint over historical orphan rows.

Future orphan writes are rejected, so the table presents a misleading split state: the constraint
is active for new writes while invalid historical data remains durable.

## Production trigger

A common migration starts with a nullable unique business key, such as an external customer ID.
Some parent rows keep that key unset as `NULL`. After child data has accumulated, an operator adds
a foreign key to harden the schema.

The bug needs only:

1. one `NULL` in the referenced unique key;
2. one non-NULL historical child key without a parent;
3. ordinary `ADD FOREIGN KEY` with existing-row checks enabled.

It does not need concurrency, a large table, a failpoint, retry, or an infrastructure fault.

## Environment

```text
TiDB nightly: ed2376acc6e0feeff9f3e2c38db489727933aa80
TiKV nightly: 730be34f959185c934b7d3db730ca1dbeb3949f8
PD nightly:   f7db42521223b92fa30d68352b15e6962b699b7e
TiDB master:  05b396fb6636f73b3bc06b09107cf43f2c725c35
Topology:     one TiDB, one PD, one real TiKV
MDL:          enabled
FK checks:    enabled
sql_mode:     default strict mode
```

## Minimal reproduction

```sql
CREATE DATABASE fk_null_repro;
USE fk_null_repro;

CREATE TABLE parent(
  id INT PRIMARY KEY,
  business_key INT NULL,
  UNIQUE KEY uk_business_key(business_key)
);
CREATE TABLE child(
  id INT PRIMARY KEY,
  parent_key INT NULL,
  KEY ik_parent_key(parent_key)
);

INSERT INTO parent VALUES (1,1),(2,NULL);
INSERT INTO child VALUES (1,1),(2,2);

ALTER TABLE child
  ADD CONSTRAINT fk_child_parent
  FOREIGN KEY(parent_key) REFERENCES parent(business_key);
```

The `ALTER` unexpectedly succeeds. The constraint is public:

```sql
SELECT CONSTRAINT_NAME
FROM information_schema.referential_constraints
WHERE constraint_schema='fk_null_repro';
-- fk_child_parent
```

The existing orphan is still present:

```sql
SELECT c.id,c.parent_key
FROM child AS c
LEFT JOIN parent AS p ON p.business_key=c.parent_key
WHERE c.parent_key IS NOT NULL AND p.id IS NULL;
-- 2, 2
```

New writes are enforced:

```sql
INSERT INTO child VALUES (3,3);
-- ERROR 1452: Cannot add or update a child row
```

`ADMIN CHECK TABLE child` reports no error because it does not validate referential closure.

## Strong oracle

The validator-shaped query and the correct anti-join disagree on the same snapshot:

```sql
SELECT COUNT(*)
FROM child
WHERE parent_key IS NOT NULL
  AND parent_key NOT IN (SELECT business_key FROM parent);
-- 0

SELECT COUNT(*)
FROM child AS c
WHERE c.parent_key IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM parent AS p
    WHERE p.business_key=c.parent_key
  );
-- 1
```

For the matched GREEN, remove only parent row `(2,NULL)`. The same `ALTER` then fails with error
1452 and does not publish a constraint.

## Root cause

`pkg/ddl/foreign_key.go:checkForeignKeyConstrain` builds:

```sql
child_columns IS NOT NULL
AND (child_columns) NOT IN (
  SELECT referenced_columns FROM referenced_table
)
```

For an absent child key, comparison with the referenced `NULL` yields `UNKNOWN`. `NOT IN` therefore
does not return the violating child row. The code treats the empty result as proof that all
historical rows satisfy the foreign key and advances the DDL.

## Counterfactual

A temporary current-master regression test expected the `ALTER` to fail. It failed on the original
source with `expected error, got nil`. Replacing the validator with a correlated `NOT EXISTS`
anti-join made the focused test pass. The temporary test and source patch were then removed.

## Expected behavior

Existing-row validation must reject every non-NULL child key without a matching referenced key.
A correlated `NOT EXISTS` anti-join provides the required NULL-safe absence proof.

## Impact

One referenced `NULL` can mask many historical orphans. Schema migration reports success and
publishes metadata that claims referential integrity, while downstream reads, cascades, exports,
and application assumptions operate on invalid persistent data. The current evidence supports
high severity; upstream may weigh the potentially unbounded integrity scope during triage.
