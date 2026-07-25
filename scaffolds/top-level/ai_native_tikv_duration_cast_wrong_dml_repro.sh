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
db="${TEST_DB:-ai_duration_cast_diff}"

mysql_cmd=(
  "$mysql_bin"
  -h"$mysql_host"
  -P"$mysql_port"
  -uroot
  --batch
  --raw
  --skip-column-names
)

cleanup() {
  "${mysql_cmd[@]}" -e "DROP DATABASE IF EXISTS \`$db\`" >/dev/null 2>&1 || true
}
trap cleanup EXIT

"${mysql_cmd[@]}" <<SQL
DROP DATABASE IF EXISTS \`$db\`;
CREATE DATABASE \`$db\`;
USE \`$db\`;
CREATE TABLE t(
  id INT PRIMARY KEY,
  dur TIME(6) NOT NULL,
  marker INT NOT NULL DEFAULT 0
);
INSERT INTO t(id,dur) VALUES
  (1,'-00:00:00.499999'),
  (2,'-00:00:00.500000'),
  (3,'-00:00:00.500001'),
  (4,'00:00:00.500000');
SQL

mdl="$("${mysql_cmd[@]}" -e "SELECT @@tidb_enable_metadata_lock")"
[[ "$mdl" == "1" ]] || {
  echo "expected MDL enabled, got: $mdl" >&2
  exit 1
}

push_ids="$("${mysql_cmd[@]}" -e \
  "SELECT GROUP_CONCAT(id ORDER BY id) FROM \`$db\`.t WHERE CAST(dur AS SIGNED)<0")"
root_ids="$("${mysql_cmd[@]}" -e \
  "SELECT GROUP_CONCAT(id ORDER BY id) FROM \`$db\`.t WHERE IF(SLEEP(0)=0,CAST(dur AS SIGNED)<0,NULL)")"

echo "push_ids=$push_ids"
echo "root_ids=$root_ids"
[[ "$push_ids" == "2,3" ]]
[[ "$root_ids" == "3" ]]

echo "self-contradictory pushed result:"
"${mysql_cmd[@]}" -e "
SELECT id,dur,CAST(dur AS SIGNED) AS cast_value,
       CAST(dur AS SIGNED)<0 AS predicate_holds
FROM \`$db\`.t
WHERE CAST(dur AS SIGNED)<0
ORDER BY id"

echo "UPDATE plan:"
"${mysql_cmd[@]}" -e \
  "EXPLAIN FORMAT='brief' UPDATE \`$db\`.t SET marker=1 WHERE CAST(dur AS SIGNED)<0"

red="$("${mysql_cmd[@]}" -e "
UPDATE \`$db\`.t SET marker=1 WHERE CAST(dur AS SIGNED)<0;
SELECT CONCAT(ROW_COUNT(),':',GROUP_CONCAT(id ORDER BY id))
FROM \`$db\`.t WHERE marker=1")"
echo "pushed_update=$red"
[[ "$red" == "2:2,3" ]]

wrong="$("${mysql_cmd[@]}" -e "
SELECT GROUP_CONCAT(id ORDER BY id)
FROM \`$db\`.t
WHERE marker=1
  AND IF(SLEEP(0)=0,CAST(dur AS SIGNED)<0,NULL)=0")"
echo "wrongly_updated_ids=$wrong"
[[ "$wrong" == "2" ]]

green="$("${mysql_cmd[@]}" -e "
UPDATE \`$db\`.t SET marker=0;
UPDATE \`$db\`.t
SET marker=2
WHERE IF(SLEEP(0)=0,CAST(dur AS SIGNED)<0,NULL);
SELECT CONCAT(ROW_COUNT(),':',GROUP_CONCAT(id ORDER BY id))
FROM \`$db\`.t WHERE marker=2")"
echo "root_update_control=$green"
[[ "$green" == "1:3" ]]

echo "matrix=RED_CONFIRMED"
