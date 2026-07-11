# Case file for ai_native_concurrency_harness.sh — id30038 MVI owner-mismatch.
# Worked reference: ADD UNIQUE INDEX on an MVI alongside a sibling unique index, under concurrent DML.
CASE_NAME="mvi-owner-mismatch-id30038"
DB="ainh_mvi"
SETUP_SQL="CREATE TABLE t(a INT PRIMARY KEY, b INT, j JSON);
SPLIT TABLE t BETWEEN (0) AND (100000) REGIONS 50;
SET SESSION cte_max_recursion_depth=200000;
INSERT INTO t WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<100000)
  SELECT n,n,CONCAT('[',n,',',n+1000000,']') FROM seq;"
DDL_SQL="ALTER TABLE t ADD UNIQUE INDEX u_mvi((CAST(j AS SIGNED ARRAY))), ADD UNIQUE INDEX u_ab(a,b);"
# concurrent DML on a not-yet-backfilled high row (reversible so data stays clean)
dml_for_iter() { local HI=$((99000 - $1*37)); print "UPDATE t SET b=b+7 WHERE a=$HI;"; print "UPDATE t SET b=b-7 WHERE a=$HI;"; }
# no case-specific SILENT_SQL: JSON_TABLE is unsupported here, and ADMIN CHECK TABLE already
# verifies index-record consistency (a non-unique unique index would fail it). O28 catches the wedge.
