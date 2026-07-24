#!/usr/bin/env bash
set -euo pipefail

mysql_bin="${MYSQL_BIN:-mysql}"
host="${MYSQL_HOST:-127.0.0.1}"
port="${MYSQL_PORT:-4000}"
user="${MYSQL_USER:-root}"
db="${TEST_DB:-ai_native_import_owner_race}"
file_a="${FILE_A:?set FILE_A to the first absolute CSV path}"
file_b="${FILE_B:?set FILE_B to the second absolute CSV path}"
timeout_s="${TIMEOUT_S:-180}"

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

"${mysql_cmd[@]}" -e "
  DROP DATABASE IF EXISTS \`${db}\`;
  CREATE DATABASE \`${db}\`;
  CREATE TABLE \`${db}\`.t (
    v VARCHAR(64) NOT NULL,
    UNIQUE KEY uk(v)
  );
"

out_dir="$(mktemp -d)"
trap 'find "$out_dir" -depth -delete' EXIT

"${mysql_cmd[@]}" -e \
  "IMPORT INTO \`${db}\`.t FROM '${file_a}' WITH DETACHED, thread=1;" \
  >"$out_dir/a.out" 2>"$out_dir/a.err" &
pid_a=$!
"${mysql_cmd[@]}" -e \
  "IMPORT INTO \`${db}\`.t FROM '${file_b}' WITH DETACHED, thread=1;" \
  >"$out_dir/b.out" 2>"$out_dir/b.err" &
pid_b=$!

wait "$pid_a"
wait "$pid_b"

job_a="$(awk 'NF { print $1; exit }' "$out_dir/a.out")"
job_b="$(awk 'NF { print $1; exit }' "$out_dir/b.out")"
printf 'accepted jobs: A=%s B=%s\n' "$job_a" "$job_b"

deadline=$((SECONDS + timeout_s))
while (( SECONDS < deadline )); do
  states="$("${mysql_cmd[@]}" -e "
    SELECT id, status
    FROM mysql.tidb_import_jobs
    WHERE id IN (${job_a}, ${job_b})
    ORDER BY id;
  ")"
  nonterminal="$(printf '%s\n' "$states" | awk '$2 == "pending" || $2 == "running" { n++ } END { print n + 0 }')"
  if [[ "$nonterminal" == "0" ]]; then
    break
  fi
  sleep 1
done

"${mysql_cmd[@]}" -e "
  SELECT id, status, error_message
  FROM mysql.tidb_import_jobs
  WHERE id IN (${job_a}, ${job_b})
  ORDER BY id;
  SELECT _tidb_rowid, v FROM \`${db}\`.t USE INDEX() ORDER BY _tidb_rowid LIMIT 3;
  SELECT _tidb_rowid, v FROM \`${db}\`.t FORCE INDEX(uk) ORDER BY v LIMIT 3;
"

record_count="$("${mysql_cmd[@]}" -e "SELECT COUNT(*) FROM \`${db}\`.t USE INDEX();")"
index_count="$("${mysql_cmd[@]}" -e "SELECT COUNT(*) FROM \`${db}\`.t FORCE INDEX(uk);")"

set +e
admin_output="$("${mysql_cmd[@]}" -e "ADMIN CHECK TABLE \`${db}\`.t;" 2>&1)"
admin_rc=$?
set -e

printf 'record_count=%s\nunique_index_count=%s\n' "$record_count" "$index_count"
printf 'admin_check_rc=%s\n%s\n' "$admin_rc" "$admin_output"

if [[ "$record_count" != "$index_count" && "$admin_rc" != "0" ]]; then
  printf 'VERDICT=RED\n'
  exit 0
fi

printf 'VERDICT=GREEN_OR_INCONCLUSIVE\n'
exit 1
