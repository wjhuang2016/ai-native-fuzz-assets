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

SET SESSION tidb_opt_enable_alternative_logical_plans = OFF;
SELECT id FROM o WHERE id <= 3 AND a IN (SELECT a FROM i) ORDER BY id;
SET SESSION tidb_opt_enable_alternative_logical_plans = ON;
EXPLAIN FORMAT = 'brief'
SELECT id FROM o WHERE id <= 3 AND a IN (SELECT a FROM i) ORDER BY id;
SELECT id FROM o WHERE id <= 3 AND a IN (SELECT a FROM i) ORDER BY id;

DROP DATABASE ai_native_correlate;
