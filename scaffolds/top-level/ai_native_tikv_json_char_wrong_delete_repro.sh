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
db="${TEST_DB:-ai_json_char_diff}"
mysql_cmd=("$mysql_bin" -h"$mysql_host" -P"$mysql_port" -uroot --batch --raw --skip-column-names)

cleanup() {
  "${mysql_cmd[@]}" -e "DROP DATABASE IF EXISTS \`$db\`" >/dev/null 2>&1 || true
}
trap cleanup EXIT

"${mysql_cmd[@]}" <<SQL
DROP DATABASE IF EXISTS \`$db\`;
CREATE DATABASE \`$db\`;
USE \`$db\`;
CREATE TABLE push_t(id INT PRIMARY KEY,j JSON);
CREATE TABLE root_t LIKE push_t;
INSERT INTO push_t VALUES
  (1,CAST('1234.5' AS JSON)),
  (2,CAST('1234' AS JSON)),
  (3,CAST('12' AS JSON));
INSERT INTO root_t SELECT * FROM push_t;
SQL

push_ids="$("${mysql_cmd[@]}" -e \
  "SELECT GROUP_CONCAT(id ORDER BY id) FROM \`$db\`.push_t WHERE CAST(j AS CHAR(4))<>'1234'")"
root_ids="$("${mysql_cmd[@]}" -e \
  "SELECT GROUP_CONCAT(id ORDER BY id) FROM \`$db\`.root_t WHERE IF(SLEEP(0)=0,CAST(j AS CHAR(4))<>'1234',NULL)")"
echo "push_ids=$push_ids"
echo "root_ids=$root_ids"
[[ "$push_ids" == "1,3" ]]
[[ "$root_ids" == "3" ]]

echo "self-contradictory pushed result:"
"${mysql_cmd[@]}" -e "
SELECT id,j,CAST(j AS CHAR(4)) AS cast_value,
       CAST(j AS CHAR(4))<>'1234' AS predicate_holds
FROM \`$db\`.push_t
WHERE CAST(j AS CHAR(4))<>'1234'
ORDER BY id"

echo "DELETE plan:"
"${mysql_cmd[@]}" -e \
  "EXPLAIN FORMAT='brief' DELETE FROM \`$db\`.push_t WHERE CAST(j AS CHAR(4))<>'1234'"

red="$("${mysql_cmd[@]}" -e "
DELETE FROM \`$db\`.push_t WHERE CAST(j AS CHAR(4))<>'1234';
SELECT CONCAT(ROW_COUNT(),':',GROUP_CONCAT(id ORDER BY id)) FROM \`$db\`.push_t")"
echo "pushed_delete=$red"
[[ "$red" == "2:2" ]]

set +e
root_error="$("${mysql_cmd[@]}" -e "
DELETE FROM \`$db\`.root_t
WHERE IF(SLEEP(0)=0,CAST(j AS CHAR(4))<>'1234',NULL)" 2>&1)"
root_status=$?
set -e
echo "root_delete_status=$root_status"
echo "root_delete_error=$root_error"
[[ "$root_status" -ne 0 ]]
[[ "$root_error" == *"ERROR 1406"* ]]

root_after="$("${mysql_cmd[@]}" -e \
  "SELECT CONCAT(COUNT(*),':',GROUP_CONCAT(id ORDER BY id)) FROM \`$db\`.root_t")"
echo "root_after=$root_after"
[[ "$root_after" == "3:1,2,3" ]]

echo "matrix=RED_CONFIRMED"
