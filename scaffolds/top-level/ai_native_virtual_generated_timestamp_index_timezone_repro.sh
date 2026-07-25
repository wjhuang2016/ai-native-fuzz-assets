#!/usr/bin/env bash
set -euo pipefail

mysql_bin="${MYSQL_BIN:-}"
if [[ -z "$mysql_bin" ]]; then
  for candidate in \
    /opt/homebrew/Cellar/mysql-client@8.0/8.0.42/bin/mysql \
    /usr/local/mysql-8.3.0-macos14-arm64/bin/mysql \
    mysql
  do
    if command -v "$candidate" >/dev/null 2>&1 || [[ -x "$candidate" ]]; then
      mysql_bin="$candidate"
      break
    fi
  done
fi

mysql_host="${MYSQL_HOST:-127.0.0.1}"
mysql_port="${MYSQL_PORT:-4000}"
db="${TEST_DB:-ai_virtual_generated_timestamp_timezone}"
mysql_cmd=("$mysql_bin" -h"$mysql_host" -P"$mysql_port" -uroot --batch --raw --skip-column-names)

cleanup() {
  "${mysql_cmd[@]}" -e "DROP DATABASE IF EXISTS \`$db\`" >/dev/null 2>&1 || true
}
trap cleanup EXIT

settings="$("${mysql_cmd[@]}" -e "
SELECT CONCAT(@@tidb_enable_metadata_lock,':',@@tidb_enable_fast_table_check,':',@@sql_mode)")"
echo "settings=$settings"
[[ "$settings" == 1:1:* ]]

"${mysql_cmd[@]}" <<SQL
DROP DATABASE IF EXISTS \`$db\`;
CREATE DATABASE \`$db\`;
USE \`$db\`;
CREATE TABLE red_index(
  id INT PRIMARY KEY,
  ts TIMESTAMP,
  d DATE AS (DATE(ts)) VIRTUAL,
  INDEX idx_d(d)
);
CREATE TABLE red_root LIKE red_index;
CREATE TABLE green_same LIKE red_index;
CREATE TABLE green_datetime(
  id INT PRIMARY KEY,
  dt DATETIME,
  d DATE AS (DATE(dt)) VIRTUAL,
  INDEX idx_d(d)
);
CREATE TABLE direct_expr(id INT PRIMARY KEY, ts TIMESTAMP);
SQL

if direct_out="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
CREATE INDEX direct_idx ON direct_expr((DATE(ts)))" 2>&1)"; then
  echo "expected the default direct expression-index safety gate to reject DATE(TIMESTAMP)" >&2
  exit 1
fi
echo "direct_expression_index=$direct_out"
[[ "$direct_out" == *"ERROR 8200"* ]]
[[ "$direct_out" == *"unsafe functions"* ]]

"${mysql_cmd[@]}" -e "
USE \`$db\`;
SET time_zone='+08:00';
INSERT INTO red_index(id,ts) VALUES (1,'2025-01-01 04:00:00');
INSERT INTO red_root(id,ts) VALUES (1,'2025-01-01 04:00:00');
INSERT INTO green_same(id,ts) VALUES (1,'2025-01-01 04:00:00');
INSERT INTO green_datetime(id,dt) VALUES (1,'2025-01-01 04:00:00')"

root_ids="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET time_zone='-08:00';
SELECT GROUP_CONCAT(id ORDER BY id)
FROM red_index IGNORE INDEX(idx_d)
WHERE d='2025-01-01'")"
index_ids="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET time_zone='-08:00';
SELECT GROUP_CONCAT(id ORDER BY id)
FROM red_index USE INDEX(idx_d)
WHERE d='2025-01-01'")"
echo "red_root_ids=$root_ids"
echo "red_index_ids=$index_ids"
[[ "$root_ids" == "NULL" ]]
[[ "$index_ids" == "1" ]]

contradiction="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET time_zone='-08:00';
SELECT CONCAT(id,':',ts,':',d,':',DATE(ts),':',d='2025-01-01')
FROM red_index USE INDEX(idx_d)
WHERE d='2025-01-01'")"
echo "red_self_contradiction=$contradiction"
[[ "$contradiction" == "1:2024-12-31 12:00:00:2024-12-31:2024-12-31:0" ]]

echo "red SELECT plan:"
"${mysql_cmd[@]}" -e "
USE \`$db\`;
SET time_zone='-08:00';
EXPLAIN FORMAT='brief'
SELECT id FROM red_index WHERE d='2025-01-01'"

"${mysql_cmd[@]}" -e "
USE \`$db\`;
SET time_zone='-08:00';
ADMIN CHECK TABLE red_index"
echo "red_admin_check=PASSED_WITH_STALE_KEY"

echo "red DELETE plan:"
"${mysql_cmd[@]}" -e "
USE \`$db\`;
SET time_zone='-08:00';
EXPLAIN FORMAT='brief'
DELETE FROM red_index WHERE d='2025-01-01'"

delete_result="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET time_zone='-08:00';
DELETE FROM red_index WHERE d='2025-01-01';
SELECT CONCAT(ROW_COUNT(),':',(SELECT COUNT(*) FROM red_index));
DELETE FROM red_root IGNORE INDEX(idx_d) WHERE d='2025-01-01';
SELECT CONCAT(ROW_COUNT(),':',(SELECT COUNT(*) FROM red_root),':',
              (SELECT d='2025-01-01' FROM red_root WHERE id=1))")"
echo "red_delete=$delete_result"
[[ "$delete_result" == $'1:0\n0:1:0' ]]

same_root="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET time_zone='+08:00';
SELECT GROUP_CONCAT(id ORDER BY id)
FROM green_same IGNORE INDEX(idx_d)
WHERE d='2025-01-01'")"
same_index="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET time_zone='+08:00';
SELECT GROUP_CONCAT(id ORDER BY id)
FROM green_same USE INDEX(idx_d)
WHERE d='2025-01-01'")"
echo "green_same_root=$same_root"
echo "green_same_index=$same_index"
[[ "$same_root" == "1" ]]
[[ "$same_index" == "1" ]]

datetime_root="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET time_zone='-08:00';
SELECT GROUP_CONCAT(id ORDER BY id)
FROM green_datetime IGNORE INDEX(idx_d)
WHERE d='2025-01-01'")"
datetime_index="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET time_zone='-08:00';
SELECT GROUP_CONCAT(id ORDER BY id)
FROM green_datetime USE INDEX(idx_d)
WHERE d='2025-01-01'")"
echo "green_datetime_root=$datetime_root"
echo "green_datetime_index=$datetime_index"
[[ "$datetime_root" == "1" ]]
[[ "$datetime_index" == "1" ]]

"${mysql_cmd[@]}" -e "
USE \`$db\`;
SET time_zone='+08:00';
ADMIN CHECK TABLE green_same;
SET time_zone='-08:00';
ADMIN CHECK TABLE green_datetime"

echo "matrix=CRITICAL_RED_CONFIRMED"
