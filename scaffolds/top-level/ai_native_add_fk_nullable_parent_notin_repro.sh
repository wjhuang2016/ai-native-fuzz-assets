#!/usr/bin/env bash
set -euo pipefail

mysql_bin="${MYSQL_BIN:-/opt/homebrew/opt/mysql-client@8.0/bin/mysql}"
mysql_host="${MYSQL_HOST:-127.0.0.1}"
mysql_port="${MYSQL_PORT:-4000}"
db="${TEST_DB:-ai_add_fk_nullable_parent}"
mysql_cmd=("$mysql_bin" -h"$mysql_host" -P"$mysql_port" -uroot --batch --raw --skip-column-names)

cleanup() {
  "${mysql_cmd[@]}" -e "DROP DATABASE IF EXISTS \`$db\`" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "VERSIONS_AND_SETTINGS"
"${mysql_cmd[@]}" -e "
SELECT tidb_version();
SELECT TYPE,VERSION,GIT_HASH
FROM information_schema.cluster_info
ORDER BY TYPE;
SELECT CONCAT(
  'mdl=',@@tidb_enable_metadata_lock,
  ',fk_enabled=',@@tidb_enable_foreign_key,
  ',fk_checks=',@@foreign_key_checks,
  ',sql_mode=',@@sql_mode
);"

"${mysql_cmd[@]}" <<SQL
DROP DATABASE IF EXISTS \`$db\`;
CREATE DATABASE \`$db\`;
CREATE TABLE \`$db\`.parent_red(
  id INT PRIMARY KEY,
  business_key INT NULL,
  UNIQUE KEY uk_business_key(business_key)
);
CREATE TABLE \`$db\`.child_red(
  id INT PRIMARY KEY,
  parent_key INT NULL,
  KEY ik_parent_key(parent_key)
);
INSERT INTO \`$db\`.parent_red VALUES (1,1),(2,NULL);
INSERT INTO \`$db\`.child_red VALUES (1,1),(2,2);

CREATE TABLE \`$db\`.parent_green LIKE \`$db\`.parent_red;
CREATE TABLE \`$db\`.child_green LIKE \`$db\`.child_red;
INSERT INTO \`$db\`.parent_green VALUES (1,1);
INSERT INTO \`$db\`.child_green VALUES (1,1),(2,2);
SQL

echo "RED_VALIDATOR_ORACLE"
not_in_count="$("${mysql_cmd[@]}" -e "
SELECT COUNT(*)
FROM \`$db\`.child_red
WHERE parent_key IS NOT NULL
  AND parent_key NOT IN (
    SELECT business_key FROM \`$db\`.parent_red
  );")"
not_exists_count="$("${mysql_cmd[@]}" -e "
SELECT COUNT(*)
FROM \`$db\`.child_red AS c
WHERE c.parent_key IS NOT NULL
  AND NOT EXISTS (
    SELECT 1
    FROM \`$db\`.parent_red AS p
    WHERE p.business_key=c.parent_key
  );")"
echo "validator_not_in=$not_in_count correct_not_exists=$not_exists_count"
[[ "$not_in_count" == "0" ]]
[[ "$not_exists_count" == "1" ]]

echo "RED_ADD_FOREIGN_KEY"
"${mysql_cmd[@]}" -e "
ALTER TABLE \`$db\`.child_red
  ADD CONSTRAINT fk_child_red_parent_red
  FOREIGN KEY(parent_key)
  REFERENCES \`$db\`.parent_red(business_key);"

constraint_count="$("${mysql_cmd[@]}" -e "
SELECT COUNT(*)
FROM information_schema.referential_constraints
WHERE constraint_schema='$db'
  AND constraint_name='fk_child_red_parent_red';")"
orphan_count="$("${mysql_cmd[@]}" -e "
SELECT COUNT(*)
FROM \`$db\`.child_red AS c
LEFT JOIN \`$db\`.parent_red AS p
  ON p.business_key=c.parent_key
WHERE c.parent_key IS NOT NULL
  AND p.id IS NULL;")"
echo "published_constraint=$constraint_count historical_orphans=$orphan_count"
[[ "$constraint_count" == "1" ]]
[[ "$orphan_count" == "1" ]]

echo "RED_POST_PUBLICATION_ENFORCEMENT"
if "${mysql_cmd[@]}" -e "
  INSERT INTO \`$db\`.child_red VALUES (3,3);
" 2>&1; then
  echo "unexpected: post-publication orphan insert succeeded" >&2
  exit 1
else
  echo "new orphan rejected while the historical orphan remains"
fi
"${mysql_cmd[@]}" -e "
ADMIN CHECK TABLE \`$db\`.child_red;
SELECT id,parent_key
FROM \`$db\`.child_red
ORDER BY id;"

echo "GREEN_NO_NULL_IN_REFERENCED_KEY"
if "${mysql_cmd[@]}" -e "
  ALTER TABLE \`$db\`.child_green
    ADD CONSTRAINT fk_child_green_parent_green
    FOREIGN KEY(parent_key)
    REFERENCES \`$db\`.parent_green(business_key);
" 2>&1; then
  echo "unexpected: control ALTER accepted a historical orphan" >&2
  exit 1
else
  echo "control ALTER rejected the historical orphan"
fi

green_constraint_count="$("${mysql_cmd[@]}" -e "
SELECT COUNT(*)
FROM information_schema.referential_constraints
WHERE constraint_schema='$db'
  AND constraint_name='fk_child_green_parent_green';")"
echo "green_published_constraint=$green_constraint_count"
[[ "$green_constraint_count" == "0" ]]

echo "matrix=RED_CONFIRMED"
