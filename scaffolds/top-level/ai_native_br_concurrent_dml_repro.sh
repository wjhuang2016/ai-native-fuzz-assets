#!/usr/bin/env bash
set -euo pipefail

: "${BR_BIN:?set BR_BIN to an exact br binary}"
: "${STORAGE:?set STORAGE to an empty BR storage URI, for example local:///shared/br-red}"

MYSQL_BIN="${MYSQL_BIN:-/opt/homebrew/opt/mysql-client/bin/mysql}"
MYSQL_HOST="${MYSQL_HOST:-127.0.0.1}"
MYSQL_PORT="${MYSQL_PORT:-4000}"
PD_ADDR="${PD_ADDR:-127.0.0.1:2379}"
DB="${DB:-ai_br_write_fence}"
LOG_FILE="${LOG_FILE:-/tmp/ai-br-concurrent-dml.log}"

sql() {
  "$MYSQL_BIN" -h "$MYSQL_HOST" -P "$MYSQL_PORT" -u root "$@"
}

sql -e "
DROP DATABASE IF EXISTS \`$DB\`;
CREATE DATABASE \`$DB\`;
CREATE TABLE \`$DB\`.t (
  id BIGINT PRIMARY KEY,
  u BIGINT NOT NULL UNIQUE,
  payload VARCHAR(1000)
);
INSERT INTO \`$DB\`.t
WITH RECURSIVE seq AS (
  SELECT 1 AS n
  UNION ALL
  SELECT n + 1 FROM seq WHERE n < 1000
)
SELECT n, n + 100000, RPAD(CONCAT('payload-', n), 512, 'x') FROM seq;
INSERT INTO \`$DB\`.t SELECT id+1000,u+1000,payload FROM \`$DB\`.t;
INSERT INTO \`$DB\`.t SELECT id+2000,u+2000,payload FROM \`$DB\`.t;
INSERT INTO \`$DB\`.t SELECT id+4000,u+4000,payload FROM \`$DB\`.t;
INSERT INTO \`$DB\`.t SELECT id+8000,u+8000,payload FROM \`$DB\`.t;
INSERT INTO \`$DB\`.t SELECT id+16000,u+16000,payload FROM \`$DB\`.t;
INSERT INTO \`$DB\`.t SELECT id+32000,u+32000,payload FROM \`$DB\`.t;
INSERT INTO \`$DB\`.t SELECT id+64000,u+64000,payload FROM \`$DB\`.t;
UPDATE \`$DB\`.t SET payload=CONCAT(
  SHA2(CONCAT(id,'-a'),256),SHA2(CONCAT(id,'-b'),256),
  SHA2(CONCAT(id,'-c'),256),SHA2(CONCAT(id,'-d'),256),
  SHA2(CONCAT(id,'-e'),256),SHA2(CONCAT(id,'-f'),256),
  SHA2(CONCAT(id,'-g'),256),SHA2(CONCAT(id,'-h'),256)
);
"

"$BR_BIN" backup table \
  --db "$DB" --table t --pd "$PD_ADDR" \
  --storage "$STORAGE" --log-file "${LOG_FILE%.log}-backup.log"

sql -e "DROP DATABASE \`$DB\`;"

"$BR_BIN" restore table \
  --db "$DB" --table t --pd "$PD_ADDR" \
  --storage "$STORAGE" --ratelimit 1 --log-file "$LOG_FILE" &
br_pid=$!

for _ in $(seq 1 600); do
  table_mode="$(sql -Nse "
    SELECT TIDB_TABLE_MODE
    FROM information_schema.tables
    WHERE table_schema='$DB' AND table_name='t'
  " 2>/dev/null || true)"
  if [[ -n "$table_mode" ]]; then
    echo "target became visible in mode: $table_mode"
    sql -e "
      INSERT INTO \`$DB\`.t
      VALUES (1,900000000,'app-write-during-restore');
    "
    break
  fi
  sleep 0.05
done

wait "$br_pid"

sql --table -e "
SELECT COUNT(*) AS primary_count,SUM(id) AS primary_sum
FROM \`$DB\`.t IGNORE INDEX(u);
SELECT COUNT(*) AS index_count,SUM(id) AS index_sum
FROM \`$DB\`.t USE INDEX(u);
SELECT id,u,payload,(u=100001) AS predicate_holds
FROM \`$DB\`.t USE INDEX(u)
WHERE u=100001;
"

set +e
sql --table -e "ADMIN CHECK TABLE \`$DB\`.t;"
admin_status=$?
set -e

if [[ "$admin_status" -eq 0 ]]; then
  echo "unexpected GREEN: ADMIN CHECK TABLE succeeded" >&2
  exit 1
fi
echo "RED reproduced: BR succeeded and ADMIN CHECK TABLE found persistent corruption"
