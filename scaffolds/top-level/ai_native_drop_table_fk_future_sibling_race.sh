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
host="${MYSQL_HOST:-127.0.0.1}"
port="${MYSQL_PORT:-4000}"
user="${MYSQL_USER:-root}"
db_prefix="${TEST_DB:-ai_native_drop_fk_future_sibling}"
fillers="${FILLERS:-80}"
keep_db="${KEEP_DB:-0}"

if [[ ! "$db_prefix" =~ ^[A-Za-z0-9_]+$ ]]; then
  printf 'TEST_DB must contain only letters, digits, or underscore\n' >&2
  exit 2
fi
if [[ ! "$fillers" =~ ^[1-9][0-9]*$ ]]; then
  printf 'FILLERS must be a positive integer\n' >&2
  exit 2
fi

red_db="${db_prefix}_red"
green_db="${db_prefix}_green"
tmp_dir="$(mktemp -d)"
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
    "${mysql_cmd[@]}" -e "
      DROP DATABASE IF EXISTS \`${red_db}\`;
      DROP DATABASE IF EXISTS \`${green_db}\`;
    " >/dev/null 2>&1 || true
  fi
  find "$tmp_dir" -type f -delete >/dev/null 2>&1 || true
  rmdir "$tmp_dir" >/dev/null 2>&1 || true
}
trap cleanup EXIT

setup_case() {
  local db="$1"
  local sql
  sql="
    DROP DATABASE IF EXISTS \`${db}\`;
    CREATE DATABASE \`${db}\`;
    CREATE TABLE \`${db}\`.p (id INT PRIMARY KEY);
    CREATE TABLE \`${db}\`.c (
      id INT PRIMARY KEY,
      pid INT,
      CONSTRAINT fk_c_p FOREIGN KEY (pid) REFERENCES p(id)
    );
    INSERT INTO \`${db}\`.p VALUES (1);
    INSERT INTO \`${db}\`.c VALUES (10, 1);
  "
  for i in $(seq -w 1 "$fillers"); do
    sql+="CREATE TABLE \`${db}\`.f${i} (id INT PRIMARY KEY);"
  done
  "${mysql_cmd[@]}" -e "$sql"
}

drop_list() {
  local db="$1"
  local first="$2"
  local last="$3"
  local out="\`${db}\`.\`${first}\`"
  for i in $(seq -w 1 "$fillers"); do
    out+=",\`${db}\`.\`f${i}\`"
  done
  out+=",\`${db}\`.\`${last}\`"
  printf '%s' "$out"
}

rename_after_parent_disappears() {
  local db="$1"
  local log="$2"
  while "${mysql_cmd[@]}" -e "
    SELECT COUNT(*)
    FROM information_schema.tables
    WHERE table_schema='${db}' AND table_name='p';
  " | grep -qx '1'; do
    sleep 0.005
  done

  set +e
  "${mysql_cmd[@]}" -e "
    RENAME TABLE \`${db}\`.c TO \`${db}\`.c_survivor;
    SELECT 'rename_returned_success';
  " >"$log" 2>&1
  local rc=$?
  set -e
  printf '%s\n' "$rc" >"${log}.rc"
}

printf 'server_version=%s\n' "$("${mysql_cmd[@]}" -e 'SELECT VERSION();')"
printf 'ddl_owner:\n'
"${mysql_cmd[@]}" -e 'ADMIN SHOW DDL;'

printf '\n=== RED: parent first, future child renamed between independent jobs ===\n'
setup_case "$red_db"
red_rename_log="$tmp_dir/red-rename.log"
rename_after_parent_disappears "$red_db" "$red_rename_log" &
red_rename_pid=$!
red_list="$(drop_list "$red_db" p c)"
"${mysql_cmd[@]}" -e "
  DROP TABLE IF EXISTS ${red_list};
  SHOW WARNINGS;
  SELECT 'drop_returned_success';
"
wait "$red_rename_pid"
cat "$red_rename_log"
red_rename_rc="$(cat "${red_rename_log}.rc")"

red_survivor="$("${mysql_cmd[@]}" -e "
  SELECT COUNT(*)
  FROM information_schema.tables
  WHERE table_schema='${red_db}' AND table_name='c_survivor';
")"
red_parent="$("${mysql_cmd[@]}" -e "
  SELECT COUNT(*)
  FROM information_schema.tables
  WHERE table_schema='${red_db}' AND table_name='p';
")"

"${mysql_cmd[@]}" -e "
  SHOW CREATE TABLE \`${red_db}\`.c_survivor;
  INSERT INTO \`${red_db}\`.c_survivor VALUES (11, 1);
  CREATE TABLE \`${red_db}\`.p (id INT PRIMARY KEY);
  INSERT INTO \`${red_db}\`.p VALUES (999);
  INSERT INTO \`${red_db}\`.c_survivor VALUES (12, 999);
"

set +e
invalid_insert_output="$("${mysql_cmd[@]}" -e "
  INSERT INTO \`${red_db}\`.c_survivor VALUES (13, 1);
" 2>&1)"
invalid_insert_rc=$?
set -e
printf 'invalid_insert_rc=%s\n%s\n' "$invalid_insert_rc" "$invalid_insert_output"

orphan_count="$("${mysql_cmd[@]}" -e "
  SELECT COUNT(*)
  FROM \`${red_db}\`.c_survivor AS c
  LEFT JOIN \`${red_db}\`.p AS p ON p.id=c.pid
  WHERE p.id IS NULL;
")"
printf 'red survivor=%s parent_after_drop=%s orphan_count=%s rename_rc=%s\n' \
  "$red_survivor" "$red_parent" "$orphan_count" "$red_rename_rc"
"${mysql_cmd[@]}" -e "
  SELECT c.id,c.pid,p.id AS parent_id
  FROM \`${red_db}\`.c_survivor AS c
  LEFT JOIN \`${red_db}\`.p AS p ON p.id=c.pid
  ORDER BY c.id;
  ADMIN CHECK TABLE \`${red_db}\`.c_survivor;
"

printf '\n=== GREEN: child first, no future-child assumption ===\n'
setup_case "$green_db"
green_rename_log="$tmp_dir/green-rename.log"
rename_after_parent_disappears "$green_db" "$green_rename_log" &
green_rename_pid=$!
green_list="$(drop_list "$green_db" c p)"
"${mysql_cmd[@]}" -e "
  DROP TABLE IF EXISTS ${green_list};
  SHOW WARNINGS;
  SELECT 'drop_returned_success';
"
wait "$green_rename_pid"
cat "$green_rename_log"
green_rename_rc="$(cat "${green_rename_log}.rc")"
green_survivor="$("${mysql_cmd[@]}" -e "
  SELECT COUNT(*)
  FROM information_schema.tables
  WHERE table_schema='${green_db}' AND table_name='c_survivor';
")"
green_child="$("${mysql_cmd[@]}" -e "
  SELECT COUNT(*)
  FROM information_schema.tables
  WHERE table_schema='${green_db}' AND table_name='c';
")"
printf 'green survivor=%s child=%s rename_rc=%s\n' \
  "$green_survivor" "$green_child" "$green_rename_rc"

if [[ "$red_rename_rc" == "0" ]] &&
   [[ "$red_survivor" == "1" ]] &&
   [[ "$red_parent" == "0" ]] &&
   [[ "$orphan_count" == "2" ]] &&
   [[ "$invalid_insert_rc" != "0" ]] &&
   [[ "$green_survivor" == "0" ]] &&
   [[ "$green_child" == "0" ]] &&
   [[ "$green_rename_rc" != "0" ]]; then
  printf 'VERDICT=RED_WITH_MATCHED_GREEN\n'
  exit 0
fi

printf 'VERDICT=GREEN_OR_INCONCLUSIVE\n'
exit 1
