#!/usr/bin/env bash
set -euo pipefail

MYSQL_BIN="${MYSQL_BIN:-/opt/homebrew/opt/mysql-client/bin/mysql}"
BR_BIN="${BR_BIN:-$HOME/.tiup/components/br/v9.0.0-beta.2.pre-nightly/br}"
PD_ADDR="${PD_ADDR:-127.0.0.1:2379}"
MYSQL_HOST="${MYSQL_HOST:-127.0.0.1}"
MYSQL_PORT="${MYSQL_PORT:-4000}"
DB="${DB:-ai_br_fk_partial_repro}"
STORAGE_DIR="${STORAGE_DIR:-/tmp/ai-br-fk-partial-repro}"
LOG_DIR="${LOG_DIR:-/tmp}"
KEEP="${KEEP:-0}"

RUN_LOG="$LOG_DIR/ai-br-fk-partial-repro.log"
BACKUP_LOG="$LOG_DIR/ai-br-fk-partial-backup.log"
RED_LOG="$LOG_DIR/ai-br-fk-partial-restore-red.log"
GREEN_LOG="$LOG_DIR/ai-br-fk-partial-restore-green.log"

mkdir -p "$LOG_DIR"
exec > >(tee "$RUN_LOG") 2>&1

mysql_cmd=(
  "$MYSQL_BIN"
  -h "$MYSQL_HOST"
  -P "$MYSQL_PORT"
  -u root
  --comments
)

clean_storage() {
  case "$STORAGE_DIR" in
    /tmp/ai-br-fk-partial-*)
      if [[ -e "$STORAGE_DIR" ]]; then
        find "$STORAGE_DIR" -depth -delete
      fi
      ;;
    *)
      echo "refusing to clean unexpected storage path: $STORAGE_DIR" >&2
      exit 2
      ;;
  esac
}

sql() {
  "${mysql_cmd[@]}" -e "$1"
}

echo "== versions and defaults =="
"$BR_BIN" --version
sql "SELECT TIDB_VERSION(); SELECT @@tidb_enable_metadata_lock, @@global.tidb_enable_foreign_key, @@foreign_key_checks;"

echo "== ordinary DDL fail-closed reference =="
sql "DROP DATABASE IF EXISTS \`$DB\`; CREATE DATABASE \`$DB\`;"
if sql "USE \`$DB\`; CREATE TABLE child_ref(id INT PRIMARY KEY, pid INT, INDEX(pid), CONSTRAINT fk_ref FOREIGN KEY(pid) REFERENCES missing_parent(id));"; then
  echo "unexpected: ordinary DDL accepted a missing parent" >&2
  exit 1
else
  echo "expected: ordinary DDL rejected the missing parent"
fi

echo "== prepare a valid parent/child graph =="
sql "USE \`$DB\`; CREATE TABLE p(id INT PRIMARY KEY); CREATE TABLE c(id INT PRIMARY KEY, pid INT, INDEX(pid), CONSTRAINT fk_cp FOREIGN KEY(pid) REFERENCES p(id)); INSERT INTO p VALUES (1); INSERT INTO c VALUES (1,1);"

clean_storage
"$BR_BIN" backup db \
  --pd "$PD_ADDR" \
  --db "$DB" \
  --storage "local://$STORAGE_DIR" \
  --log-file "$BACKUP_LOG"

echo "== RED: restore only the child =="
sql "DROP DATABASE \`$DB\`;"
"$BR_BIN" restore table \
  --pd "$PD_ADDR" \
  --db "$DB" \
  --table c \
  --storage "local://$STORAGE_DIR" \
  --checksum \
  --log-file "$RED_LOG"

sql "SELECT COUNT(*) AS parent_table_count FROM information_schema.TABLES WHERE TABLE_SCHEMA='$DB' AND TABLE_NAME='p'; SHOW CREATE TABLE \`$DB\`.c; SELECT * FROM \`$DB\`.c ORDER BY id;"
sql "INSERT INTO \`$DB\`.c VALUES (2,999); SELECT * FROM \`$DB\`.c ORDER BY id; ADMIN CHECK TABLE \`$DB\`.c;"

echo "== GREEN: restore the dependency-closed database =="
sql "DROP DATABASE \`$DB\`;"
"$BR_BIN" restore db \
  --pd "$PD_ADDR" \
  --db "$DB" \
  --storage "local://$STORAGE_DIR" \
  --checksum \
  --log-file "$GREEN_LOG"

sql "SHOW TABLES FROM \`$DB\`; SELECT COUNT(*) AS orphan_count FROM \`$DB\`.c LEFT JOIN \`$DB\`.p ON c.pid=p.id WHERE p.id IS NULL; ADMIN CHECK TABLE \`$DB\`.p; ADMIN CHECK TABLE \`$DB\`.c;"
if sql "INSERT INTO \`$DB\`.c VALUES (2,999);"; then
  echo "unexpected: dependency-closed restore accepted an orphan" >&2
  exit 1
else
  echo "expected: dependency-closed restore rejected the orphan with FK checks enabled"
fi

if [[ "$KEEP" != "1" ]]; then
  sql "DROP DATABASE IF EXISTS \`$DB\`;"
  clean_storage
fi

echo "RED/GREEN matrix completed; main transcript: $RUN_LOG"
