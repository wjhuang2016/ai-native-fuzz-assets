#!/usr/bin/env bash
set -euo pipefail

mysql_bin="${MYSQL_BIN:-mysql}"
host="${MYSQL_HOST:-127.0.0.1}"
port="${MYSQL_PORT:-4000}"
user="${MYSQL_USER:-root}"
db="${TEST_DB:-ai_native_autoid_to_autorandom_probe}"
attempts="${ATTEMPTS:-64}"
keep_db="${KEEP_DB:-0}"

if [[ ! "$db" =~ ^[A-Za-z0-9_]+$ ]]; then
  printf 'TEST_DB must contain only letters, digits, or underscore\n' >&2
  exit 2
fi
if [[ ! "$attempts" =~ ^[1-9][0-9]*$ ]]; then
  printf 'ATTEMPTS must be a positive integer\n' >&2
  exit 2
fi

mysql_cmd=(
  "$mysql_bin"
  --protocol=tcp
  "-h${host}"
  "-P${port}"
  "-u${user}"
  --batch
  --raw
  --skip-column-names
)

cleanup() {
  if [[ "$keep_db" != "1" ]]; then
    "${mysql_cmd[@]}" -e "DROP DATABASE IF EXISTS \`${db}\`;" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

values=()
for i in $(seq 1 64); do
  printf -v label 'order-%03d' "$i"
  values+=("('${label}')")
done
old_ifs="$IFS"
IFS=,
insert_values="${values[*]}"
IFS="$old_ifs"

"${mysql_cmd[@]}" -e "
  DROP DATABASE IF EXISTS \`${db}\`;
  CREATE DATABASE \`${db}\`;

  CREATE TABLE \`${db}\`.red (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    v VARCHAR(64) NOT NULL
  ) AUTO_ID_CACHE=1;

  CREATE TABLE \`${db}\`.control (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    v VARCHAR(64) NOT NULL
  );

  INSERT INTO \`${db}\`.red(v) VALUES ${insert_values};
  INSERT INTO \`${db}\`.control(v) VALUES ${insert_values};

  SET SESSION tidb_allow_remove_auto_inc=1;
  ALTER TABLE \`${db}\`.red
    MODIFY COLUMN id BIGINT AUTO_RANDOM(1);
  ALTER TABLE \`${db}\`.control
    MODIFY COLUMN id BIGINT AUTO_RANDOM(1);
"

printf 'server_version=%s\n' "$("${mysql_cmd[@]}" -e 'SELECT VERSION();')"
printf 'ddl_owner:\n'
"${mysql_cmd[@]}" -e "ADMIN SHOW DDL;"

red_collisions=0
control_collisions=0

for i in $(seq 1 "$attempts"); do
  red_result="$("${mysql_cmd[@]}" -e "
    REPLACE INTO \`${db}\`.red(v) VALUES (CONCAT('replacement-', ${i}));
    SELECT ROW_COUNT(), LAST_INSERT_ID();
  ")"
  red_affected="$(printf '%s\n' "$red_result" | awk 'NF >= 2 { print $1; exit }')"
  red_id="$(printf '%s\n' "$red_result" | awk 'NF >= 2 { print $2; exit }')"
  if [[ "$red_affected" == "2" ]]; then
    red_collisions=$((red_collisions + 1))
    printf 'red collision attempt=%d affected_rows=%s generated_id=%s\n' \
      "$i" "$red_affected" "$red_id"
  fi

  control_result="$("${mysql_cmd[@]}" -e "
    REPLACE INTO \`${db}\`.control(v) VALUES (CONCAT('replacement-', ${i}));
    SELECT ROW_COUNT(), LAST_INSERT_ID();
  ")"
  control_affected="$(printf '%s\n' "$control_result" | awk 'NF >= 2 { print $1; exit }')"
  control_id="$(printf '%s\n' "$control_result" | awk 'NF >= 2 { print $2; exit }')"
  if [[ "$control_affected" == "2" ]]; then
    control_collisions=$((control_collisions + 1))
    printf 'control collision attempt=%d affected_rows=%s generated_id=%s\n' \
      "$i" "$control_affected" "$control_id"
  fi
done

read -r red_total red_original red_replacement <<<"$(
  "${mysql_cmd[@]}" -e "
    SELECT COUNT(*), SUM(v LIKE 'order-%'), SUM(v LIKE 'replacement-%')
    FROM \`${db}\`.red;
  "
)"
read -r control_total control_original control_replacement <<<"$(
  "${mysql_cmd[@]}" -e "
    SELECT COUNT(*), SUM(v LIKE 'order-%'), SUM(v LIKE 'replacement-%')
    FROM \`${db}\`.control;
  "
)"

printf 'red total=%s original=%s replacement=%s affected_rows_2=%s\n' \
  "$red_total" "$red_original" "$red_replacement" "$red_collisions"
printf 'control total=%s original=%s replacement=%s affected_rows_2=%s\n' \
  "$control_total" "$control_original" "$control_replacement" "$control_collisions"
printf 'red overwritten old IDs: '
"${mysql_cmd[@]}" -e "
  SELECT COALESCE(GROUP_CONCAT(id ORDER BY id), 'none')
  FROM \`${db}\`.red
  WHERE id BETWEEN 1 AND 64 AND v LIKE 'replacement-%';
"

"${mysql_cmd[@]}" -e "
  ADMIN CHECK TABLE \`${db}\`.red;
  ADMIN CHECK TABLE \`${db}\`.control;
"

if (( red_original < 64 )) &&
   [[ "$red_replacement" == "$attempts" ]] &&
   [[ "$control_original" == "64" ]] &&
   [[ "$control_replacement" == "$attempts" ]] &&
   [[ "$control_collisions" == "0" ]]; then
  printf 'VERDICT=RED\n'
  exit 0
fi

printf 'VERDICT=GREEN_OR_INCONCLUSIVE\n'
exit 1
