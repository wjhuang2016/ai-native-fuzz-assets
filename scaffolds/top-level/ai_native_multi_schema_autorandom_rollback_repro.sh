#!/usr/bin/env bash
set -euo pipefail

mysql_bin="${MYSQL_BIN:-mysql}"
tidb_bin="${TIDB_BIN:?set TIDB_BIN to the exact tidb-server binary used by the warm node}"
host="${MYSQL_HOST:-127.0.0.1}"
warm_port="${WARM_PORT:-4000}"
cold_port="${COLD_PORT:-4400}"
cold_status_port="${COLD_STATUS_PORT:-10480}"
pd_addr="${PD_ADDR:-127.0.0.1:2379}"
user="${MYSQL_USER:-root}"
db="${TEST_DB:-ai_native_multischema_autorandom_rollback}"
tmp_dir="${TMPDIR:-/tmp}/ai-native-id3570003-$$"
cold_pid=""

if [[ ! "$db" =~ ^[A-Za-z0-9_]+$ ]]; then
  printf 'TEST_DB must contain only letters, digits, or underscore\n' >&2
  exit 2
fi

mkdir -p "$tmp_dir"

mysql_at() {
  local port="$1"
  shift
  "$mysql_bin" --protocol=tcp "-h${host}" "-P${port}" "-u${user}" \
    --batch --raw --skip-column-names "$@"
}

cleanup() {
  if [[ -n "$cold_pid" ]]; then
    kill "$cold_pid" >/dev/null 2>&1 || true
    wait "$cold_pid" >/dev/null 2>&1 || true
  fi
  mysql_at "$warm_port" -e "DROP DATABASE IF EXISTS \`${db}\`;" >/dev/null 2>&1 || true
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

values=()
for i in $(seq 1 64); do
  printf -v label 'original-%03d' "$i"
  values+=("($((i % 8)),'${label}')")
done
old_ifs="$IFS"
IFS=,
insert_values="${values[*]}"
IFS="$old_ifs"

mysql_at "$warm_port" -e "
  DROP DATABASE IF EXISTS \`${db}\`;
  CREATE DATABASE \`${db}\`;
  CREATE TABLE \`${db}\`.t (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    v INT NOT NULL,
    payload VARCHAR(64) NOT NULL
  );
  INSERT INTO \`${db}\`.t(v,payload) VALUES ${insert_values};
  SELECT COUNT(*),MIN(id),MAX(id) FROM \`${db}\`.t;
  SHOW TABLE \`${db}\`.t NEXT_ROW_ID;
"

set +e
alter_output="$(
  mysql_at "$warm_port" -e "
    USE \`${db}\`;
    SET SESSION tidb_allow_remove_auto_inc=1;
    ALTER TABLE t
      MODIFY COLUMN id BIGINT AUTO_RANDOM(1),
      ADD UNIQUE INDEX ux_v(v);
  " 2>&1
)"
alter_rc=$?
set -e
printf '%s\n' "$alter_output"

if [[ "$alter_rc" -eq 0 ]] || [[ "$alter_output" != *"Duplicate entry"* ]]; then
  printf 'expected the unique-index subjob to fail with a duplicate\n' >&2
  exit 1
fi

post_schema="$(mysql_at "$warm_port" -e "SHOW CREATE TABLE \`${db}\`.t;")"
printf '%s\n' "$post_schema"
mysql_at "$warm_port" -e "SHOW TABLE \`${db}\`.t NEXT_ROW_ID; ADMIN SHOW DDL JOBS 5;"

if [[ "$post_schema" != *"AUTO_INCREMENT"* ]] || [[ "$post_schema" != *"AUTO_RANDOM"* ]]; then
  printf 'hybrid post-rollback schema was not observed\n' >&2
  exit 1
fi

"$tidb_bin" \
  -P "$cold_port" \
  --store=tikv \
  --host="$host" \
  --status="$cold_status_port" \
  --path="$pd_addr" \
  --log-file="$tmp_dir/cold-tidb.log" \
  >"$tmp_dir/cold-stdout.log" 2>&1 &
cold_pid=$!

for _ in $(seq 1 60); do
  if mysql_at "$cold_port" -e "SELECT 1;" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done
mysql_at "$cold_port" -e "SELECT VERSION(); SHOW TABLE \`${db}\`.t NEXT_ROW_ID;"

preimage="$(mysql_at "$cold_port" -e "SELECT payload FROM \`${db}\`.t WHERE id=2;")"
set +e
insert_output="$(
  mysql_at "$cold_port" -e \
    "INSERT INTO \`${db}\`.t(v,payload) VALUES (9001,'cold-insert');" 2>&1
)"
insert_rc=$?
set -e
printf '%s\n' "$insert_output"

if [[ "$insert_rc" -eq 0 ]] || [[ "$insert_output" != *"Duplicate entry '1'"* ]]; then
  printf 'cold INSERT did not reuse primary key 1\n' >&2
  exit 1
fi

read -r generated_id affected_rows <<<"$(
  mysql_at "$cold_port" -e "
    REPLACE INTO \`${db}\`.t(v,payload) VALUES (9002,'cold-replace');
    SELECT LAST_INSERT_ID(),ROW_COUNT();
  "
)"
postimage="$(mysql_at "$warm_port" -e "SELECT payload FROM \`${db}\`.t WHERE id=2;")"
preimage_left="$(mysql_at "$warm_port" -e \
  "SELECT COUNT(*) FROM \`${db}\`.t WHERE payload='${preimage}';")"

printf 'preimage=%s generated_id=%s affected_rows=%s postimage=%s preimage_left=%s\n' \
  "$preimage" "$generated_id" "$affected_rows" "$postimage" "$preimage_left"
mysql_at "$warm_port" -e "
  SELECT COUNT(*),SUM(payload LIKE 'original-%'),SUM(payload='cold-replace')
  FROM \`${db}\`.t;
  ADMIN CHECK TABLE \`${db}\`.t;
"

if [[ "$generated_id" == "2" ]] &&
   [[ "$affected_rows" == "2" ]] &&
   [[ "$postimage" == "cold-replace" ]] &&
   [[ "$preimage_left" == "0" ]]; then
  printf 'VERDICT=RED\n'
  exit 0
fi

printf 'VERDICT=GREEN_OR_INCONCLUSIVE\n'
exit 1

