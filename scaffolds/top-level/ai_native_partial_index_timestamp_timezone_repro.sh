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
db="${TEST_DB:-ai_partial_timestamp_timezone}"
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
CREATE TABLE red_t(
  id INT PRIMARY KEY,
  k INT,
  ts TIMESTAMP,
  UNIQUE INDEX uk(k) WHERE ts >= '2025-01-01 00:00:00'
);
CREATE TABLE green_t LIKE red_t;
SQL

"${mysql_cmd[@]}" -e "
USE \`$db\`;
SET time_zone='-08:00';
INSERT INTO red_t VALUES (1,7,'2024-12-31 12:00:00')"

"${mysql_cmd[@]}" -e "
USE \`$db\`;
SET time_zone='+08:00';
INSERT INTO red_t VALUES (2,7,'2025-01-01 04:00:00')"

full_ids="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET time_zone='+08:00';
SELECT GROUP_CONCAT(id ORDER BY id)
FROM red_t IGNORE INDEX(uk)
WHERE ts >= '2025-01-01 00:00:00' AND k=7")"
index_ids="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET time_zone='+08:00';
SELECT GROUP_CONCAT(id ORDER BY id)
FROM red_t USE INDEX(uk)
WHERE ts >= '2025-01-01 00:00:00' AND k=7")"
echo "red_full_ids=$full_ids"
echo "red_index_ids=$index_ids"
[[ "$full_ids" == "1,2" ]]
[[ "$index_ids" == "2" ]]

echo "red observer rows:"
"${mysql_cmd[@]}" -e "
USE \`$db\`;
SET time_zone='+08:00';
SELECT id,k,ts,ts >= '2025-01-01 00:00:00' AS predicate_holds
FROM red_t ORDER BY id"

if admin_out="$("${mysql_cmd[@]}" -e "USE \`$db\`; ADMIN CHECK TABLE red_t" 2>&1)"; then
  echo "expected ADMIN CHECK to detect the missing partial-index key" >&2
  exit 1
fi
echo "red_admin=$admin_out"
[[ "$admin_out" == *"ERROR 8223"* ]]

echo "red DELETE plan:"
"${mysql_cmd[@]}" -e "
USE \`$db\`;
SET time_zone='+08:00';
EXPLAIN FORMAT='brief'
DELETE FROM red_t
WHERE ts >= '2025-01-01 00:00:00' AND k=7"

delete_result="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET time_zone='+08:00';
DELETE FROM red_t
WHERE ts >= '2025-01-01 00:00:00' AND k=7;
SELECT CONCAT(ROW_COUNT(),':',GROUP_CONCAT(id ORDER BY id),':',
              MIN(ts >= '2025-01-01 00:00:00'))
FROM red_t")"
echo "red_delete=$delete_result"
[[ "$delete_result" == "1:1:1" ]]

"${mysql_cmd[@]}" -e "
USE \`$db\`;
SET time_zone='+08:00';
INSERT INTO green_t VALUES (1,7,'2025-01-01 04:00:00')"

if duplicate_out="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET time_zone='+08:00';
INSERT INTO green_t VALUES (2,7,'2025-01-01 04:00:00')" 2>&1)"; then
  echo "expected same-time-zone control to reject the duplicate" >&2
  exit 1
fi
echo "green_duplicate=$duplicate_out"
[[ "$duplicate_out" == *"ERROR 1062"* ]]

"${mysql_cmd[@]}" -e "
USE \`$db\`;
SET time_zone='+08:00';
ADMIN CHECK TABLE green_t"

green_ids="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET time_zone='+08:00';
SELECT GROUP_CONCAT(id ORDER BY id)
FROM green_t USE INDEX(uk)
WHERE ts >= '2025-01-01 00:00:00' AND k=7")"
echo "green_index_ids=$green_ids"
[[ "$green_ids" == "1" ]]

echo "matrix=RED_CONFIRMED"
