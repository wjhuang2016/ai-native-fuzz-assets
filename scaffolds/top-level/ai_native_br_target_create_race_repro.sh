#!/usr/bin/env bash
set -euo pipefail

MYSQL_BIN="${MYSQL_BIN:-/opt/homebrew/opt/mysql-client/bin/mysql}"
BR_BIN="${BR_BIN:-$HOME/.tiup/components/br/v9.0.0-beta.2.pre-nightly/br}"
PD_ADDR="${PD_ADDR:-127.0.0.1:2379}"
MYSQL_HOST="${MYSQL_HOST:-127.0.0.1}"
MYSQL_PORT="${MYSQL_PORT:-4000}"
DB="${DB:-ai_br_schema_race_repro}"
STORAGE_DIR="${STORAGE_DIR:-/tmp/ai-br-schema-race-repro}"
LOG_DIR="${LOG_DIR:-/tmp}"
KEEP="${KEEP:-0}"

RUN_LOG="$LOG_DIR/ai-br-schema-race-repro.log"
BACKUP_LOG="$LOG_DIR/ai-br-schema-race-backup.log"
RED_LOG="$LOG_DIR/ai-br-schema-race-restore-red.log"
PREEXIST_LOG="$LOG_DIR/ai-br-schema-race-preexisting-green.log"
NO_RACE_LOG="$LOG_DIR/ai-br-schema-race-no-race-green.log"

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
    /tmp/ai-br-schema-race-*)
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

checkpoint_schema_count() {
  "${mysql_cmd[@]}" --batch --raw --skip-column-names -e \
    "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME LIKE '__TiDB_BR_Temporary_Snapshot_Restore_Checkpoint%';" \
    2>/dev/null
}

echo "== versions and defaults =="
"$BR_BIN" --version
sql "SELECT TIDB_VERSION(); SELECT @@tidb_enable_metadata_lock;"

echo "== prepare backup schema uk(a) =="
sql "DROP DATABASE IF EXISTS \`$DB\`; CREATE DATABASE \`$DB\`; USE \`$DB\`; CREATE TABLE t(id INT PRIMARY KEY, a INT NOT NULL, b INT NOT NULL, UNIQUE KEY uk(a)); INSERT INTO t VALUES (1,10,100),(2,20,200);"
clean_storage
"$BR_BIN" backup db \
  --pd "$PD_ADDR" \
  --db "$DB" \
  --storage "local://$STORAGE_DIR" \
  --log-file "$BACKUP_LOG"

echo "== RED: create incompatible uk(b) after precheck =="
sql "DROP TABLE \`$DB\`.t;"
"$BR_BIN" restore table \
  --pd "$PD_ADDR" \
  --db "$DB" \
  --table t \
  --storage "local://$STORAGE_DIR" \
  --checksum \
  --log-file "$RED_LOG" &
br_pid=$!

created=0
for _ in $(seq 1 200); do
  cp_count="$(checkpoint_schema_count || true)"
  if [[ -n "$cp_count" && "$cp_count" != "0" ]]; then
    sql "USE \`$DB\`; CREATE TABLE t(id INT PRIMARY KEY, a INT NOT NULL, b INT NOT NULL, UNIQUE KEY uk(b));"
    created=1
    break
  fi
  sleep 0.05
done

if [[ "$created" != "1" ]]; then
  echo "failed to observe checkpoint window" >&2
  exit 1
fi
wait "$br_pid"

sql "USE \`$DB\`; SHOW CREATE TABLE t; SELECT * FROM t ORDER BY id; EXPLAIN SELECT /*+ USE_INDEX(t,uk) */ * FROM t WHERE b=10; SELECT /*+ USE_INDEX(t,uk) */ id,a,b,(b=10) AS predicate_holds FROM t WHERE b=10; SELECT /*+ USE_INDEX(t,uk) */ id,a,b,(b=100) AS predicate_holds FROM t WHERE b=100;"

affected="$("${mysql_cmd[@]}" --batch --raw --skip-column-names -e \
  "USE \`$DB\`; UPDATE /*+ USE_INDEX(t,uk) */ t SET a=999 WHERE b=10; SELECT ROW_COUNT();")"
echo "red_wrong_update_affected=$affected"
if [[ "$affected" != "1" ]]; then
  echo "expected the corrupted point path to update one wrong row" >&2
  exit 1
fi
sql "USE \`$DB\`; SELECT * FROM t ORDER BY id;"
if sql "USE \`$DB\`; ADMIN CHECK TABLE t;"; then
  echo "unexpected: corrupted target passed ADMIN CHECK" >&2
  exit 1
else
  echo "expected: corrupted target failed ADMIN CHECK"
fi

echo "== GREEN 1: target exists before precheck =="
sql "USE \`$DB\`; DROP TABLE t; CREATE TABLE t(id INT PRIMARY KEY, a INT NOT NULL, b INT NOT NULL, UNIQUE KEY uk(b));"
if "$BR_BIN" restore table \
  --pd "$PD_ADDR" \
  --db "$DB" \
  --table t \
  --storage "local://$STORAGE_DIR" \
  --checksum \
  --log-file "$PREEXIST_LOG"; then
  echo "unexpected: preexisting target was accepted" >&2
  exit 1
else
  echo "expected: preexisting target rejected before ingest"
fi

echo "== GREEN 2: no competing CREATE =="
sql "DROP TABLE \`$DB\`.t;"
"$BR_BIN" restore table \
  --pd "$PD_ADDR" \
  --db "$DB" \
  --table t \
  --storage "local://$STORAGE_DIR" \
  --checksum \
  --log-file "$NO_RACE_LOG"

index_column="$("${mysql_cmd[@]}" --batch --raw --skip-column-names -e \
  "SELECT COLUMN_NAME FROM information_schema.STATISTICS WHERE TABLE_SCHEMA='$DB' AND TABLE_NAME='t' AND INDEX_NAME='uk';")"
echo "green_index_column=$index_column"
if [[ "$index_column" != "a" ]]; then
  echo "expected no-race restore to create uk(a)" >&2
  exit 1
fi

affected="$("${mysql_cmd[@]}" --batch --raw --skip-column-names -e \
  "USE \`$DB\`; UPDATE t SET a=999 WHERE b=10; SELECT ROW_COUNT();")"
echo "green_update_affected=$affected"
if [[ "$affected" != "0" ]]; then
  echo "expected healthy table to update zero rows" >&2
  exit 1
fi
sql "USE \`$DB\`; ADMIN CHECK TABLE t;"

if [[ "$KEEP" != "1" ]]; then
  sql "DROP DATABASE IF EXISTS \`$DB\`;"
  clean_storage
fi

echo "RED/GREEN matrix completed; main transcript: $RUN_LOG"
