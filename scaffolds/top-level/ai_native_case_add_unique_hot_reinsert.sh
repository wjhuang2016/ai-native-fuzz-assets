# Case file for ai_native_concurrency_harness.sh
# Target: fast-reorg ADD UNIQUE INDEX under hot delete/reinsert on the indexed key.
CASE_NAME="add-unique-hot-reinsert"
DB="ainh_unique_reinsert"
TARGET_ROWS=200000
SETUP_SQL="CREATE TABLE t(
  id BIGINT PRIMARY KEY,
  c VARCHAR(96) NOT NULL,
  d BIGINT NOT NULL,
  pad VARCHAR(192) NOT NULL
);
SET SESSION cte_max_recursion_depth=250000;
INSERT INTO t
WITH RECURSIVE seq(n) AS (
  SELECT 1
  UNION ALL
  SELECT n+1 FROM seq WHERE n<${TARGET_ROWS}
)
SELECT
  n,
  CONCAT('c-', LPAD(CAST(n AS CHAR), 12, '0'), '-', LPAD(CAST(n * 17 AS CHAR), 18, '0')),
  MOD(n * 29, 100003),
  REPEAT('x', 192)
FROM seq;
UPDATE t SET c='hot-0001', d=1 WHERE id=190001;
UPDATE t SET c='hot-0002', d=2 WHERE id=190101;
UPDATE t SET c='hot-0003', d=3 WHERE id=190201;
UPDATE t SET c='hot-0004', d=4 WHERE id=190301;
UPDATE t SET c='hot-0005', d=5 WHERE id=190401;
UPDATE t SET c='hot-0006', d=6 WHERE id=190501;
UPDATE t SET c='hot-0007', d=7 WHERE id=190601;
UPDATE t SET c='hot-0008', d=8 WHERE id=190701;
SPLIT TABLE t BETWEEN (0) AND (300001) REGIONS 120;"
DDL_SQL="ALTER TABLE t ADD UNIQUE INDEX uk_c(c);"
dml_for_iter() {
  local wid=${2:-1}
  local base alt hot dval
  case "$wid" in
    1) base=190001; alt=290001; hot='hot-0001'; dval=1 ;;
    2) base=190101; alt=290101; hot='hot-0002'; dval=2 ;;
    3) base=190201; alt=290201; hot='hot-0003'; dval=3 ;;
    4) base=190301; alt=290301; hot='hot-0004'; dval=4 ;;
    5) base=190401; alt=290401; hot='hot-0005'; dval=5 ;;
    6) base=190501; alt=290501; hot='hot-0006'; dval=6 ;;
    7) base=190601; alt=290601; hot='hot-0007'; dval=7 ;;
    8) base=190701; alt=290701; hot='hot-0008'; dval=8 ;;
    *) base=190001; alt=290001; hot='hot-0001'; dval=1 ;;
  esac
  cat <<SQL
BEGIN;
DELETE FROM t WHERE id=${base};
INSERT INTO t VALUES (${alt}, '${hot}', ${dval}, REPEAT('x', 192));
COMMIT;
BEGIN;
DELETE FROM t WHERE id=${alt};
INSERT INTO t VALUES (${base}, '${hot}', ${dval}, REPEAT('x', 192));
COMMIT;
SQL
}
SILENT_SQL="SELECT msg FROM (
  SELECT CONCAT('rowcount=', cnt) AS msg
  FROM (SELECT COUNT(*) AS cnt FROM t) rc
  WHERE cnt <> ${TARGET_ROWS}
  UNION ALL
  SELECT CONCAT(c, ':', COUNT(*)) AS msg
  FROM t
  WHERE c IN ('hot-0001', 'hot-0002', 'hot-0003', 'hot-0004', 'hot-0005', 'hot-0006', 'hot-0007', 'hot-0008')
  GROUP BY c
  HAVING COUNT(*) <> 1
) bad
LIMIT 1;"
