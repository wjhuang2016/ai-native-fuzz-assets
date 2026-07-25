#!/usr/bin/env bash
set -euo pipefail

mysql_bin="${MYSQL_BIN:-mysql}"
mysql_host="${MYSQL_HOST:-127.0.0.1}"
mysql_port="${MYSQL_PORT:-4000}"
mysql_user="${MYSQL_USER:-root}"
db="${DB_NAME:-ai_json_decimal_precision_repro}"

mysql_base=(
  "$mysql_bin"
  --batch
  --raw
  --skip-column-names
  -h "$mysql_host"
  -P "$mysql_port"
  -u "$mysql_user"
)

cleanup() {
  "${mysql_base[@]}" -e "DROP DATABASE IF EXISTS \`$db\`" >/dev/null
}
trap cleanup EXIT

"${mysql_base[@]}" -e "
  DROP DATABASE IF EXISTS \`$db\`;
  CREATE DATABASE \`$db\`;
  CREATE TABLE \`$db\`.events_push(
    id INT PRIMARY KEY,
    payload JSON NOT NULL,
    entity_id DECIMAL(65,0) NOT NULL
  );
  CREATE TABLE \`$db\`.events_root LIKE \`$db\`.events_push;
  INSERT INTO \`$db\`.events_push VALUES
    (1, JSON_OBJECT('entity_id', 9007199254740991), 9007199254740991),
    (2, JSON_OBJECT('entity_id', 9007199254740993), 9007199254740993),
    (3, JSON_OBJECT('entity_id', 9223372036854775807), 9223372036854775807),
    (4, JSON_OBJECT('entity_id', 18446744073709551615), 18446744073709551615);
  INSERT INTO \`$db\`.events_root SELECT * FROM \`$db\`.events_push;
"

push_plan="$("${mysql_base[@]}" "$db" -e "
  EXPLAIN FORMAT='brief'
  DELETE FROM events_push
  WHERE CAST(payload->'$.entity_id' AS DECIMAL(65,0)) <> entity_id;
")"
root_plan="$("${mysql_base[@]}" "$db" -e "
  EXPLAIN FORMAT='brief'
  DELETE FROM events_root
  WHERE IF(
    SLEEP(0)=0,
    CAST(payload->'$.entity_id' AS DECIMAL(65,0)) <> entity_id,
    NULL
  );
")"

push_preimage="$("${mysql_base[@]}" "$db" -e "
  SELECT COALESCE(GROUP_CONCAT(id ORDER BY id), 'NULL')
  FROM events_push
  WHERE CAST(payload->'$.entity_id' AS DECIMAL(65,0)) <> entity_id;
")"
root_preimage="$("${mysql_base[@]}" "$db" -e "
  SELECT COALESCE(GROUP_CONCAT(id ORDER BY id), 'NULL')
  FROM events_root
  WHERE IF(
    SLEEP(0)=0,
    CAST(payload->'$.entity_id' AS DECIMAL(65,0)) <> entity_id,
    NULL
  );
")"

push_affected="$("${mysql_base[@]}" "$db" -e "
  DELETE FROM events_push
  WHERE CAST(payload->'$.entity_id' AS DECIMAL(65,0)) <> entity_id;
  SELECT ROW_COUNT();
")"
root_affected="$("${mysql_base[@]}" "$db" -e "
  DELETE FROM events_root
  WHERE IF(
    SLEEP(0)=0,
    CAST(payload->'$.entity_id' AS DECIMAL(65,0)) <> entity_id,
    NULL
  );
  SELECT ROW_COUNT();
")"
push_remaining="$("${mysql_base[@]}" "$db" -e "
  SELECT GROUP_CONCAT(id ORDER BY id) FROM events_push;
")"
root_remaining="$("${mysql_base[@]}" "$db" -e "
  SELECT GROUP_CONCAT(id ORDER BY id) FROM events_root;
")"

printf '%s\n' "PUSH PLAN" "$push_plan"
printf '%s\n' "ROOT PLAN" "$root_plan"
printf 'push_preimage=%s\n' "$push_preimage"
printf 'root_preimage=%s\n' "$root_preimage"
printf 'push_affected=%s\n' "$push_affected"
printf 'root_affected=%s\n' "$root_affected"
printf 'push_remaining=%s\n' "$push_remaining"
printf 'root_remaining=%s\n' "$root_remaining"

grep -q $'Selection.*cop\\[tikv\\]' <<<"$push_plan"
grep -q $'Selection.*root' <<<"$root_plan"
test "$push_preimage" = "2,3,4"
test "$root_preimage" = "NULL"
test "$push_affected" = "3"
test "$root_affected" = "0"
test "$push_remaining" = "1"
test "$root_remaining" = "1,2,3,4"
