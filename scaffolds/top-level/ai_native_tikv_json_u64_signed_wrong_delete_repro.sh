#!/usr/bin/env bash
set -euo pipefail

mysql_bin="${MYSQL_BIN:-mysql}"
mysql_host="${MYSQL_HOST:-127.0.0.1}"
mysql_port="${MYSQL_PORT:-4000}"
mysql_user="${MYSQL_USER:-root}"
db="${DB_NAME:-ai_json_u64_signed_repro}"

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

settings="$("${mysql_base[@]}" -e "
  SELECT VERSION(), @@tidb_enable_metadata_lock, @@sql_mode;
")"

"${mysql_base[@]}" -e "
  DROP DATABASE IF EXISTS \`$db\`;
  CREATE DATABASE \`$db\`;
  CREATE TABLE \`$db\`.events_push(
    event_id BIGINT PRIMARY KEY,
    payload JSON NOT NULL,
    preimage VARCHAR(64) NOT NULL
  );
  INSERT INTO \`$db\`.events_push VALUES
    (101, '{\"account_id\": 42, \"kind\": \"normal\"}', 'original-normal'),
    (102, '{\"account_id\": 9223372036854775808, \"kind\": \"external\"}',
          'original-large-id'),
    (103, '{\"account_id\": 18446744073709551615, \"kind\": \"external\"}',
          'original-max-id');
  CREATE TABLE \`$db\`.events_root LIKE \`$db\`.events_push;
  INSERT INTO \`$db\`.events_root SELECT * FROM \`$db\`.events_push;
"

push_plan="$("${mysql_base[@]}" "$db" -e "
  EXPLAIN FORMAT='brief'
  DELETE FROM events_push
  WHERE CAST(JSON_EXTRACT(payload, '$.account_id') AS SIGNED) < 0;
")"
root_plan="$("${mysql_base[@]}" "$db" -e "
  EXPLAIN FORMAT='brief'
  DELETE FROM events_root
  WHERE CAST(JSON_EXTRACT(payload, '$.account_id') AS SIGNED) < 0
     OR RAND() < 0;
")"

push_ids="$("${mysql_base[@]}" "$db" -e "
  SELECT COALESCE(GROUP_CONCAT(event_id ORDER BY event_id), 'NULL')
  FROM events_push
  WHERE CAST(JSON_EXTRACT(payload, '$.account_id') AS SIGNED) < 0;
")"
root_ids="$("${mysql_base[@]}" "$db" -e "
  SELECT COALESCE(GROUP_CONCAT(event_id ORDER BY event_id), 'NULL')
  FROM events_root
  WHERE CAST(JSON_EXTRACT(payload, '$.account_id') AS SIGNED) < 0
     OR RAND() < 0;
")"

push_affected="$("${mysql_base[@]}" "$db" -e "
  DELETE FROM events_push
  WHERE CAST(JSON_EXTRACT(payload, '$.account_id') AS SIGNED) < 0;
  SELECT ROW_COUNT();
")"

set +e
root_error="$("${mysql_base[@]}" "$db" -e "
  DELETE FROM events_root
  WHERE CAST(JSON_EXTRACT(payload, '$.account_id') AS SIGNED) < 0
     OR RAND() < 0;
" 2>&1)"
root_status=$?
set -e

push_remaining="$("${mysql_base[@]}" "$db" -e "
  SELECT GROUP_CONCAT(event_id ORDER BY event_id) FROM events_push;
")"
root_remaining="$("${mysql_base[@]}" "$db" -e "
  SELECT GROUP_CONCAT(event_id ORDER BY event_id) FROM events_root;
")"
"${mysql_base[@]}" "$db" -e "ADMIN CHECK TABLE events_push;"

printf '%s\n' "SETTINGS" "$settings"
printf '%s\n' "PUSH PLAN" "$push_plan"
printf '%s\n' "ROOT PLAN" "$root_plan"
printf 'push_ids=%s\n' "$push_ids"
printf 'root_ids=%s\n' "$root_ids"
printf 'push_affected=%s\n' "$push_affected"
printf 'root_status=%s\n' "$root_status"
printf 'root_error=%s\n' "$root_error"
printf 'push_remaining=%s\n' "$push_remaining"
printf 'root_remaining=%s\n' "$root_remaining"

grep -q $'Selection.*cop\\[tikv\\]' <<<"$push_plan"
grep -q $'Selection.*root' <<<"$root_plan"
grep -q $'\t1\t' <<<"$settings"
grep -q 'STRICT_TRANS_TABLES' <<<"$settings"
test "$push_ids" = "102,103"
test "$root_ids" = "NULL"
test "$push_affected" = "2"
test "$root_status" -ne 0
grep -q 'ERROR 1690' <<<"$root_error"
test "$push_remaining" = "101"
test "$root_remaining" = "101,102,103"

printf '%s\n' "VERDICT=RED"
