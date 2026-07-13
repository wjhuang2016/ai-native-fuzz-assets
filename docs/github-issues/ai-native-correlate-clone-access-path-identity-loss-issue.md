## Bug Report

### 1. Minimal reproduce step

Run the following SQL on current master. No fault injection or concurrency is needed.

```sql
DROP DATABASE IF EXISTS ai_native_correlate;
CREATE DATABASE ai_native_correlate;
USE ai_native_correlate;

CREATE TABLE o(id INT PRIMARY KEY, a INT NOT NULL);
CREATE TABLE i(a INT NOT NULL, b INT NOT NULL, KEY ia(a));
INSERT INTO o VALUES (1,1),(2,2),(3,3);
INSERT INTO i VALUES (1,10),(1,11),(2,20),(3,30),(5,50);
ANALYZE TABLE o, i;

SET SESSION tidb_opt_hash_join_cost_factor = 1;
SET SESSION tidb_opt_merge_join_cost_factor = 1;

SET SESSION tidb_opt_enable_alternative_logical_plans = OFF;
SELECT id
FROM o
WHERE id <= 3
  AND a IN (SELECT MAX(a) FROM i GROUP BY b)
ORDER BY id;
-- 1, 2, 3

SET SESSION tidb_opt_enable_alternative_logical_plans = ON;
EXPLAIN FORMAT = 'brief'
SELECT id
FROM o
WHERE id <= 3
  AND a IN (SELECT MAX(a) FROM i GROUP BY b)
ORDER BY id;

SELECT id
FROM o
WHERE id <= 3
  AND a IN (SELECT MAX(a) FROM i GROUP BY b)
ORDER BY id;
-- empty
```

The relevant part of the feature-ON plan is:

```text
Apply
  ...
  Limit(Probe)
    Selection(eq(Column#9, o.a))
      HashAgg(group by:i.b, funcs:max(i.a))
        TableDual(rows:0)
```

As an adjacent control, removing the aggregation returns `1,2,3` in both modes. The feature-ON plan
then uses a real correlated `IndexRangeScan`:

```sql
SET SESSION tidb_opt_enable_alternative_logical_plans = OFF;
SELECT id FROM o WHERE id <= 3 AND a IN (SELECT a FROM i) ORDER BY id;
-- 1, 2, 3

SET SESSION tidb_opt_enable_alternative_logical_plans = ON;
SELECT id FROM o WHERE id <= 3 AND a IN (SELECT a FROM i) ORDER BY id;
-- 1, 2, 3
```

### 2. What did you expect to see?

Enabling alternative logical plans must not change the query result. Both modes should return ids
`1,2,3`, and the nonempty inner table must not be replaced by `TableDual`.

### 3. What did you see instead?

With the feature enabled, the optimizer selects an Apply alternative whose aggregate inner side is
planned over `TableDual(rows:0)`. The query silently returns an empty result.

`cloneDataSource` deep-clones `AllPossibleAccessPaths` and `PossibleAccessPaths` independently.
Stats derivation fills ranges through the canonical `AllPossibleAccessPaths` objects, while physical
planning consumes different active-path clones from `PossibleAccessPaths`. In this aggregate shape,
the correlated predicate remains above `HashAgg`, so `resetStatsForCorrelatedDS` does not rebuild
the leaf DataSource paths. The active clones retain empty ranges and are converted to `TableDual`.

Cloning the canonical paths once and mapping every active path to the corresponding canonical clone
keeps the same Apply alternative, restores the real table scan, and returns `1,2,3`.

The feature is currently disabled by default, but enabling a supported optimizer feature should not
cause a silent wrong result.

### 4. What is your TiDB version?

- Current master: `13282a8bd06bd33324a4dbfd3c1c03685f3cd9aa`
- Also reproduced with real TiKV on
  `8.0.11-TiDB-v9.0.0-beta.2.pre-1895-g5c9198e948`
