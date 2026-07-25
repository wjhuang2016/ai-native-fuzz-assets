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

lightning_bin="${LIGHTNING_BIN:-}"
if [[ -z "$lightning_bin" ]]; then
  for candidate in \
    /Users/bba/.tiup/components/tidb-lightning/v9.0.0-beta.2.pre-nightly/tidb-lightning \
    tidb-lightning
  do
    if command -v "$candidate" >/dev/null 2>&1 || [[ -x "$candidate" ]]; then
      lightning_bin="$candidate"
      break
    fi
  done
fi

mysql_host="${MYSQL_HOST:-127.0.0.1}"
mysql_port="${MYSQL_PORT:-4000}"
tidb_status_port="${TIDB_STATUS_PORT:-10080}"
pd_addr="${PD_ADDR:-127.0.0.1:2379}"
status_base="${LIGHTNING_STATUS_BASE:-18289}"
db="${TEST_DB:-ai_lightning_conflict_skip}"
red_task="${RED_TASK_DB:-ai_lightning_conflict_skip_red_task}"
green_task="${GREEN_TASK_DB:-ai_lightning_conflict_skip_green_task}"
work="$(mktemp -d "${TMPDIR:-/tmp}/ai-lightning-conflict-skip.XXXXXX")"
data_dir="$work/data"
mkdir -p "$data_dir"
mysql_cmd=("$mysql_bin" -h"$mysql_host" -P"$mysql_port" -uroot --batch --raw --skip-column-names)

cleanup() {
  "${mysql_cmd[@]}" -e "
DROP DATABASE IF EXISTS \`$db\`;
DROP DATABASE IF EXISTS \`$red_task\`;
DROP DATABASE IF EXISTS \`$green_task\`" >/dev/null 2>&1 || true
  find "$work" -depth -delete >/dev/null 2>&1 || true
}
trap cleanup EXIT

cat > "$data_dir/$db-schema-create.sql" <<SQL
CREATE DATABASE \`$db\`;
SQL

cat > "$data_dir/$db.t-schema.sql" <<'SQL'
CREATE TABLE t (
  id INT PRIMARY KEY CLUSTERED,
  u INT NOT NULL,
  UNIQUE KEY uu(u)
);
SQL

cat > "$data_dir/$db.t.0.csv" <<'CSV'
id,u
1,7
2,7
CSV

write_config() {
  local path="$1"
  local task_db="$2"
  local sorted_dir="$3"
  local checksum="$4"
  cat > "$path" <<TOML
[lightning]
level = "info"
region-concurrency = 1
task-info-schema-name = "$task_db"

[tidb]
host = "$mysql_host"
port = $mysql_port
user = "root"
status-port = $tidb_status_port
pd-addr = "$pd_addr"

[tikv-importer]
backend = "local"
sorted-kv-dir = "$sorted_dir"
add-index-by-sql = false

[mydumper]
data-source-dir = "$data_dir"

[mydumper.csv]
header = true

[conflict]
strategy = "replace"

[checkpoint]
enable = false

[post-restore]
checksum = "$checksum"
analyze = "off"
TOML
}

write_config "$work/red.toml" "$red_task" "$work/sorted-red" "off"
write_config "$work/green.toml" "$green_task" "$work/sorted-green" "required"

settings="$("${mysql_cmd[@]}" -e "
SELECT CONCAT(@@tidb_enable_metadata_lock,':',@@sql_mode)")"
echo "settings=$settings"
[[ "$settings" == 1:* ]]

"$lightning_bin" -V

"${mysql_cmd[@]}" -e "
DROP DATABASE IF EXISTS \`$db\`;
DROP DATABASE IF EXISTS \`$red_task\`"
"$lightning_bin" \
  -config "$work/red.toml" \
  -status-addr "127.0.0.1:$status_base" \
  -log-file "$work/red.log" > "$work/red.out" 2>&1

grep -q "tidb lightning exit successfully" "$work/red.out"
red_base="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SELECT GROUP_CONCAT(CONCAT(id,':',u) ORDER BY id)
FROM t IGNORE INDEX(uu)")"
red_index="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SELECT GROUP_CONCAT(CONCAT(id,':',u) ORDER BY id)
FROM t FORCE INDEX(uu)")"
echo "red_base=$red_base"
echo "red_index=$red_index"
[[ "$red_base" == "1:7,2:7" ]]
[[ "$red_index" == "1:7" || "$red_index" == "2:7" ]]

if red_admin="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
ADMIN CHECK TABLE t" 2>&1)"; then
  echo "expected ADMIN CHECK TABLE to detect the RED inconsistency" >&2
  exit 1
fi
echo "red_admin=$red_admin"
[[ "$red_admin" == *"ERROR 8223"* ]]
if grep -q "duplicate detection" "$work/red.log"; then
  echo "RED unexpectedly entered duplicate detection" >&2
  exit 1
fi

"${mysql_cmd[@]}" -e "
DROP DATABASE IF EXISTS \`$db\`;
DROP DATABASE IF EXISTS \`$green_task\`"
"$lightning_bin" \
  -config "$work/green.toml" \
  -status-addr "127.0.0.1:$((status_base + 1))" \
  -log-file "$work/green.log" > "$work/green.out" 2>&1

grep -q "tidb lightning exit successfully" "$work/green.out"
green_base="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SELECT GROUP_CONCAT(CONCAT(id,':',u) ORDER BY id)
FROM t IGNORE INDEX(uu)")"
green_index="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SELECT GROUP_CONCAT(CONCAT(id,':',u) ORDER BY id)
FROM t FORCE INDEX(uu)")"
green_conflicts="$("${mysql_cmd[@]}" -e "
SELECT COUNT(*) FROM \`$green_task\`.conflict_view")"
echo "green_base=$green_base"
echo "green_index=$green_index"
echo "green_conflicts=$green_conflicts"
[[ "$green_base" == "$green_index" ]]
[[ "$green_base" == "1:7" || "$green_base" == "2:7" ]]
[[ "$green_conflicts" == "2" ]]
"${mysql_cmd[@]}" -e "
USE \`$db\`;
ADMIN CHECK TABLE t"

echo "matrix=CRITICAL_RED_CONFIRMED"
