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
db="${TEST_DB:-ai_week_default_format_diff}"
mysql_cmd=("$mysql_bin" -h"$mysql_host" -P"$mysql_port" -uroot --batch --raw --skip-column-names)

cleanup() {
  "${mysql_cmd[@]}" -e "DROP DATABASE IF EXISTS \`$db\`" >/dev/null 2>&1 || true
}
trap cleanup EXIT

"${mysql_cmd[@]}" <<SQL
DROP DATABASE IF EXISTS \`$db\`;
CREATE DATABASE \`$db\`;
USE \`$db\`;
CREATE TABLE push_t(id INT PRIMARY KEY, d DATE NOT NULL);
CREATE TABLE root_t LIKE push_t;
INSERT INTO push_t VALUES
  (1,'2015-12-31'),(2,'2016-01-01'),(3,'2016-01-03'),(4,'2016-01-04'),
  (5,'2019-12-29'),(6,'2019-12-30'),(7,'2020-01-01'),(8,'2020-01-05'),
  (9,'2020-12-31'),(10,'2021-01-01'),(11,'2021-01-03'),(12,'2021-01-04');
INSERT INTO root_t SELECT * FROM push_t;
SQL

settings="$("${mysql_cmd[@]}" -e "
SET @@session.default_week_format=3;
SELECT CONCAT(@@default_week_format,':',@@tidb_enable_metadata_lock,':',@@sql_mode)")"
echo "settings=$settings"
[[ "$settings" == 3:1:* ]]

echo "self-equivalence plan:"
"${mysql_cmd[@]}" -e "
USE \`$db\`;
SET @@session.default_week_format=3;
EXPLAIN FORMAT='brief'
SELECT id FROM push_t
WHERE WEEK(d)<>WEEK(d,@@default_week_format)"

push_impossible_ids="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET @@session.default_week_format=3;
SELECT GROUP_CONCAT(id ORDER BY id)
FROM push_t
WHERE WEEK(d)<>WEEK(d,@@default_week_format)")"
root_impossible_ids="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET @@session.default_week_format=3;
SELECT GROUP_CONCAT(id ORDER BY id)
FROM root_t
WHERE IF(SLEEP(0)=0,WEEK(d)<>WEEK(d,@@default_week_format),NULL)")"
echo "push_impossible_ids=$push_impossible_ids"
echo "root_impossible_ids=$root_impossible_ids"
[[ "$push_impossible_ids" == "1,2,3,6,7,9,10,11" ]]
[[ "$root_impossible_ids" == "NULL" ]]

echo "self-contradictory pushed result:"
"${mysql_cmd[@]}" -e "
USE \`$db\`;
SET @@session.default_week_format=3;
SELECT id,d,WEEK(d) AS implicit_mode,
       WEEK(d,@@default_week_format) AS explicit_mode,
       WEEK(d)<>WEEK(d,@@default_week_format) AS predicate_holds
FROM push_t
WHERE WEEK(d)<>WEEK(d,@@default_week_format)
ORDER BY id"

push_week52_ids="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET @@session.default_week_format=3;
SELECT GROUP_CONCAT(id ORDER BY id) FROM push_t WHERE WEEK(d)=52")"
explicit_week52_ids="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SELECT GROUP_CONCAT(id ORDER BY id) FROM root_t WHERE WEEK(d,3)=52")"
echo "push_week52_ids=$push_week52_ids"
echo "explicit_week52_ids=$explicit_week52_ids"
[[ "$push_week52_ids" == "1,5,6,9" ]]
[[ "$explicit_week52_ids" == "5" ]]

echo "DELETE plan:"
"${mysql_cmd[@]}" -e "
USE \`$db\`;
SET @@session.default_week_format=3;
EXPLAIN FORMAT='brief' DELETE FROM push_t WHERE WEEK(d)=52"

push_delete="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET @@session.default_week_format=3;
DELETE FROM push_t WHERE WEEK(d)=52;
SELECT CONCAT(ROW_COUNT(),':',GROUP_CONCAT(id ORDER BY id)) FROM push_t")"
root_delete="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET @@session.default_week_format=3;
DELETE FROM root_t WHERE IF(SLEEP(0)=0,WEEK(d)=52,NULL);
SELECT CONCAT(ROW_COUNT(),':',GROUP_CONCAT(id ORDER BY id)) FROM root_t")"
echo "pushed_delete=$push_delete"
echo "root_delete=$root_delete"
[[ "$push_delete" == "4:2,3,4,7,8,10,11,12" ]]
[[ "$root_delete" == "1:1,2,3,4,6,7,8,9,10,11,12" ]]

"${mysql_cmd[@]}" -e "
USE \`$db\`;
ADMIN CHECK TABLE push_t;
ADMIN CHECK TABLE root_t"

echo "matrix=RED_CONFIRMED"
