#!/usr/bin/env bash
set -euo pipefail

mysql_bin="${MYSQL_BIN:-/opt/homebrew/opt/mysql-client@8.0/bin/mysql}"
mysql_host="${MYSQL_HOST:-127.0.0.1}"
mysql_port="${MYSQL_PORT:-4000}"
db="${TEST_DB:-ai_float_uint_half_tie}"
mysql_cmd=("$mysql_bin" -h"$mysql_host" -P"$mysql_port" -uroot --batch --raw --skip-column-names)

cleanup() {
  "${mysql_cmd[@]}" -e "DROP DATABASE IF EXISTS \`$db\`" >/dev/null 2>&1 || true
}
trap cleanup EXIT

"${mysql_cmd[@]}" <<SQL
DROP DATABASE IF EXISTS \`$db\`;
CREATE DATABASE \`$db\`;
CREATE TABLE \`$db\`.push_red(id INT PRIMARY KEY,x DOUBLE NOT NULL);
CREATE TABLE \`$db\`.root_red LIKE \`$db\`.push_red;
CREATE TABLE \`$db\`.push_green LIKE \`$db\`.push_red;
CREATE TABLE \`$db\`.root_green LIKE \`$db\`.push_red;
INSERT INTO \`$db\`.push_red VALUES (1,0.5),(2,0.4),(3,1.4);
INSERT INTO \`$db\`.root_red SELECT * FROM \`$db\`.push_red;
INSERT INTO \`$db\`.push_green VALUES (1,1.5),(2,1.4),(3,1.6);
INSERT INTO \`$db\`.root_green SELECT * FROM \`$db\`.push_green;
SQL

echo "VERSIONS_AND_SETTINGS"
"${mysql_cmd[@]}" -e "
SELECT tidb_version();
SELECT TYPE,VERSION,GIT_HASH
FROM information_schema.cluster_info
ORDER BY TYPE;
SELECT CONCAT('mdl=',@@tidb_enable_metadata_lock,',sql_mode=',@@sql_mode);"

red='CAST(x AS UNSIGNED)=1'
green='CAST(x AS UNSIGNED)=2'

echo "RED_PLAN"
"${mysql_cmd[@]}" -e "EXPLAIN FORMAT='brief' DELETE FROM \`$db\`.push_red WHERE $red"

push_red="$("${mysql_cmd[@]}" -e \
  "SELECT COALESCE(GROUP_CONCAT(id ORDER BY id),'EMPTY') FROM \`$db\`.push_red WHERE $red")"
root_red="$("${mysql_cmd[@]}" -e \
  "SELECT COALESCE(GROUP_CONCAT(id ORDER BY id),'EMPTY') FROM \`$db\`.root_red WHERE IF(SLEEP(0)=0,$red,NULL)")"
echo "RED_ROWSET push=$push_red root=$root_red"
[[ "$push_red" == "1,3" ]]
[[ "$root_red" == "3" ]]

echo "RED_SELF_ORACLE"
self_red="$("${mysql_cmd[@]}" -e "
SELECT CONCAT_WS(':',id,x,CAST(x AS UNSIGNED),$red)
FROM \`$db\`.push_red
WHERE $red
ORDER BY id")"
echo "$self_red"
[[ "$self_red" == $'1:0.5:0:0\n3:1.4:1:1' ]]

echo "RED_DELETE"
push_affected="$("${mysql_cmd[@]}" -e "
DELETE FROM \`$db\`.push_red WHERE $red;
SELECT ROW_COUNT();")"
root_affected="$("${mysql_cmd[@]}" -e "
DELETE FROM \`$db\`.root_red WHERE IF(SLEEP(0)=0,$red,NULL);
SELECT ROW_COUNT();")"
push_remaining="$("${mysql_cmd[@]}" -e \
  "SELECT GROUP_CONCAT(id ORDER BY id) FROM \`$db\`.push_red")"
root_remaining="$("${mysql_cmd[@]}" -e \
  "SELECT GROUP_CONCAT(id ORDER BY id) FROM \`$db\`.root_red")"
echo "push_affected=$push_affected root_affected=$root_affected"
echo "push_remaining=$push_remaining root_remaining=$root_remaining"
[[ "$push_affected" == "2" ]]
[[ "$root_affected" == "1" ]]
[[ "$push_remaining" == "2" ]]
[[ "$root_remaining" == "1,2" ]]

echo "GREEN_ROWSET"
push_green="$("${mysql_cmd[@]}" -e \
  "SELECT GROUP_CONCAT(id ORDER BY id) FROM \`$db\`.push_green WHERE $green")"
root_green="$("${mysql_cmd[@]}" -e \
  "SELECT GROUP_CONCAT(id ORDER BY id) FROM \`$db\`.root_green WHERE IF(SLEEP(0)=0,$green,NULL)")"
echo "push=$push_green root=$root_green"
[[ "$push_green" == "1,3" ]]
[[ "$root_green" == "1,3" ]]

"${mysql_cmd[@]}" -e "
DELETE FROM \`$db\`.push_green WHERE $green;
DELETE FROM \`$db\`.root_green WHERE IF(SLEEP(0)=0,$green,NULL);
ADMIN CHECK TABLE \`$db\`.push_red;
ADMIN CHECK TABLE \`$db\`.root_red;
ADMIN CHECK TABLE \`$db\`.push_green;
ADMIN CHECK TABLE \`$db\`.root_green;"

echo "matrix=RED_CONFIRMED"
