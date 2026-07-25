#!/usr/bin/env bash
set -euo pipefail

mysql_bin="${MYSQL_BIN:-/opt/homebrew/opt/mysql-client@8.0/bin/mysql}"
mysql_host="${MYSQL_HOST:-127.0.0.1}"
mysql_port="${MYSQL_PORT:-4000}"
db="${TEST_DB:-ai_max_packet_pushdown}"
mysql_cmd=("$mysql_bin" -h"$mysql_host" -P"$mysql_port" -uroot --batch --raw --skip-column-names)

cleanup() {
  "${mysql_cmd[@]}" -e "DROP DATABASE IF EXISTS \`$db\`" >/dev/null 2>&1 || true
}
trap cleanup EXIT

packet="$("${mysql_cmd[@]}" -e "SELECT @@session.max_allowed_packet")"
[[ "$packet" == "67108864" ]]

"${mysql_cmd[@]}" <<SQL
DROP DATABASE IF EXISTS \`$db\`;
CREATE DATABASE \`$db\`;
CREATE TABLE \`$db\`.push_red(id INT PRIMARY KEY,n BIGINT NOT NULL);
CREATE TABLE \`$db\`.root_red LIKE \`$db\`.push_red;
CREATE TABLE \`$db\`.push_green LIKE \`$db\`.push_red;
CREATE TABLE \`$db\`.root_green LIKE \`$db\`.push_red;
INSERT INTO \`$db\`.push_red VALUES (1,16777216),(2,1);
INSERT INTO \`$db\`.root_red SELECT * FROM \`$db\`.push_red;
INSERT INTO \`$db\`.push_green SELECT * FROM \`$db\`.push_red;
INSERT INTO \`$db\`.root_green SELECT * FROM \`$db\`.push_red;
SQL

red="CONCAT(SPACE(n),SPACE(n),SPACE(n),SPACE(n),'x') IS NOT NULL"
green="CONCAT(SPACE(n),SPACE(n),SPACE(n),'x') IS NOT NULL"

echo "SETTINGS max_allowed_packet=$packet"
echo "RED_PLAN"
"${mysql_cmd[@]}" -e "EXPLAIN FORMAT='brief' DELETE FROM \`$db\`.push_red WHERE $red"

push_red="$("${mysql_cmd[@]}" -e \
  "SELECT GROUP_CONCAT(id ORDER BY id) FROM \`$db\`.push_red WHERE $red")"
root_red="$("${mysql_cmd[@]}" -e \
  "SELECT GROUP_CONCAT(id ORDER BY id) FROM \`$db\`.root_red WHERE IF(SLEEP(0)=0,$red,NULL)")"
echo "RED_ROWSET push=$push_red root=$root_red"
[[ "$push_red" == "1,2" ]]
[[ "$root_red" == "2" ]]

self_red="$("${mysql_cmd[@]}" -e "
SELECT CONCAT_WS(':',id,$red)
FROM \`$db\`.push_red
WHERE $red
ORDER BY id")"
echo "RED_SELF_ORACLE"
echo "$self_red"
[[ "$self_red" == $'1:0\n2:1' ]]

push_affected="$("${mysql_cmd[@]}" -e "
DELETE FROM \`$db\`.push_red WHERE $red;
SELECT ROW_COUNT();")"
push_remaining="$("${mysql_cmd[@]}" -e "SELECT COUNT(*) FROM \`$db\`.push_red")"
set +e
root_error="$("${mysql_cmd[@]}" -e \
  "DELETE FROM \`$db\`.root_red WHERE IF(SLEEP(0)=0,$red,NULL)" 2>&1)"
root_status=$?
set -e
root_remaining="$("${mysql_cmd[@]}" -e "SELECT COUNT(*) FROM \`$db\`.root_red")"
echo "RED_DELETE push_affected=$push_affected push_remaining=$push_remaining"
echo "ROOT_DELETE status=$root_status error=$root_error root_remaining=$root_remaining"
[[ "$push_affected" == "2" ]]
[[ "$push_remaining" == "0" ]]
[[ "$root_status" -ne 0 ]]
[[ "$root_error" == *"ERROR 1301"* ]]
[[ "$root_remaining" == "2" ]]

push_green="$("${mysql_cmd[@]}" -e \
  "SELECT GROUP_CONCAT(id ORDER BY id) FROM \`$db\`.push_green WHERE $green")"
root_green="$("${mysql_cmd[@]}" -e \
  "SELECT GROUP_CONCAT(id ORDER BY id) FROM \`$db\`.root_green WHERE IF(SLEEP(0)=0,$green,NULL)")"
echo "GREEN_ROWSET push=$push_green root=$root_green"
[[ "$push_green" == "1,2" ]]
[[ "$root_green" == "1,2" ]]

echo "matrix=KNOWN_ROOT_RED_CONFIRMED"
