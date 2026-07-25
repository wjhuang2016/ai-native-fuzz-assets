#!/usr/bin/env bash
set -euo pipefail

# Dedicated testbed only. This script accelerates the GC wait after proving that
# the retired delete range exactly matches the live resumed TableID.

MYSQL_BIN="${MYSQL_BIN:-/opt/homebrew/opt/mysql-client@8.0/bin/mysql}"
BR_BIN="${BR_BIN:-/Users/bba/.tiup/components/br/v9.0.0-beta.2.pre-nightly/br}"
PD="${PD:-127.0.0.1:2379}"
HOST="${HOST:-127.0.0.1}"
PORT="${PORT:-4000}"
USER="${USER:-root}"
BACKUP="${BACKUP:-/Users/bba/pc/agent-test/runs/critical-br-generation-20260725/backup-big}"
DB="${DB:-brgenbig}"
TABLE="${TABLE:-t}"
WORK_ROOT="${WORK_ROOT:-/tmp/ai-br-checkpoint-retired-id-gc}"
EXPECTED_ROWS="${EXPECTED_ROWS:-128000}"

WORK="$WORK_ROOT/$(date +%Y%m%d-%H%M%S)-$$"
mkdir -p "$WORK"

mysql_exec() {
  "$MYSQL_BIN" -h "$HOST" -P "$PORT" -u "$USER" "$@"
}

br_restore() {
  local log_file="$1"
  "$BR_BIN" restore table \
    --pd "$PD" \
    --storage "local://$BACKUP" \
    --db "$DB" \
    --table "$TABLE" \
    --ratelimit 1 \
    --tikv-max-restore-concurrency 1 \
    --merge-region-size-bytes 1 \
    --merge-region-key-count 1 \
    --log-file "$log_file"
}

original_gc_interval="$(
  mysql_exec -N -e \
    "SELECT VARIABLE_VALUE FROM mysql.tidb WHERE VARIABLE_NAME='tikv_gc_run_interval'"
)"

cleanup() {
  mysql_exec -e \
    "UPDATE mysql.tidb SET VARIABLE_VALUE='${original_gc_interval}' WHERE VARIABLE_NAME='tikv_gc_run_interval'" \
    >/dev/null 2>&1 || true
  mysql_exec -e "DROP DATABASE IF EXISTS \`$DB\`" >/dev/null 2>&1 || true
}
trap cleanup EXIT

mysql_exec -e "DROP DATABASE IF EXISTS \`$DB\`"

first_log="$WORK/interrupt.log"
set +e
br_restore "$first_log" >"$WORK/interrupt.out" 2>&1 &
br_pid=$!
fired=0
for _ in {1..900}; do
  if rg -q '\["import files done"\] \[sn=1\]' "$first_log" 2>/dev/null; then
    kill -INT "$br_pid"
    fired=1
    break
  fi
  if ! kill -0 "$br_pid" 2>/dev/null; then
    break
  fi
  sleep 0.2
done
wait "$br_pid"
first_rc=$?
set -e

if [[ "$fired" != 1 || "$first_rc" == 0 ]]; then
  echo "INVALID: did not interrupt a partial restore" >&2
  exit 2
fi

checkpoint_db="$(
  mysql_exec -N -e \
    "SELECT SCHEMA_NAME FROM INFORMATION_SCHEMA.SCHEMATA
     WHERE SCHEMA_NAME LIKE '__TiDB_BR_Temporary_Snapshot_Restore_Checkpoint_%'
     ORDER BY SCHEMA_NAME DESC LIMIT 1"
)"
old_id="$(
  mysql_exec -N -e \
    "SELECT TIDB_TABLE_ID FROM INFORMATION_SCHEMA.TABLES
     WHERE TABLE_SCHEMA='$DB' AND TABLE_NAME='$TABLE'"
)"
checkpoint_rows="$(
  mysql_exec -N -e "SELECT COUNT(*) FROM \`$checkpoint_db\`.cpt_data"
)"

if [[ -z "$old_id" || "$checkpoint_rows" -lt 1 ]]; then
  echo "INVALID: checkpoint progress was not durable" >&2
  exit 2
fi

restore_id="${checkpoint_db##*_}"
mysql_exec -e "DROP DATABASE \`$DB\`"

# Production waits for stale-task detection. A dedicated testbed can advance
# only this interrupted registration to paused to avoid the five-minute wait.
mysql_exec -e \
  "UPDATE mysql.tidb_restore_registry SET status='paused'
   WHERE id=$restore_id AND status='running'"

resume_log="$WORK/resume.log"
br_restore "$resume_log" >"$WORK/resume.out" 2>&1

new_id="$(
  mysql_exec -N -e \
    "SELECT TIDB_TABLE_ID FROM INFORMATION_SCHEMA.TABLES
     WHERE TABLE_SCHEMA='$DB' AND TABLE_NAME='$TABLE'"
)"
before_count="$(
  mysql_exec -N -e \
    "SELECT COUNT(id+17) FROM \`$DB\`.\`$TABLE\` WHERE id>=0"
)"
table_prefix="$(
  mysql_exec -N -e \
    "SELECT CONCAT('74', HEX(9223372036854775808 + $new_id))"
)"
delete_jobs="$(
  mysql_exec -N -e \
    "SELECT COUNT(*) FROM mysql.gc_delete_range WHERE start_key='$table_prefix'"
)"

if [[ "$old_id" != "$new_id" || "$before_count" != "$EXPECTED_ROWS" || "$delete_jobs" -lt 1 ]]; then
  echo "INVALID: failed to establish reused live ID plus retired cleanup owner" >&2
  exit 2
fi

if ! rg -q "Table Restore success summary" "$resume_log"; then
  echo "INVALID: BR did not publish success" >&2
  exit 2
fi

# Acceleration only: production reaches the same eligible queue row after the
# default ten-minute GC lifetime.
mysql_exec -e \
  "UPDATE mysql.gc_delete_range SET ts=1 WHERE start_key='$table_prefix';
   UPDATE mysql.tidb SET VARIABLE_VALUE='10s'
   WHERE VARIABLE_NAME='tikv_gc_run_interval';"

after_count="$before_count"
for i in {1..60}; do
  after_count="$(
    mysql_exec -N -e \
      "SELECT COUNT(id+$i) FROM \`$DB\`.\`$TABLE\` WHERE id>=0"
  )"
  if [[ "$after_count" == 0 ]]; then
    break
  fi
  sleep 5
done

mysql_exec -e \
  "UPDATE mysql.tidb SET VARIABLE_VALUE='${original_gc_interval}'
   WHERE VARIABLE_NAME='tikv_gc_run_interval'"

if [[ "$after_count" != 0 ]]; then
  echo "INVALID: cleanup owner did not reach the live ID in time" >&2
  exit 2
fi

echo "CRITICAL_RED_CONFIRMED"
echo "old_id=$old_id resumed_id=$new_id checkpoint_rows=$checkpoint_rows"
echo "before_gc=$before_count after_gc=$after_count delete_range_prefix=$table_prefix"
echo "issue=https://github.com/pingcap/tidb/issues/68709 known_duplicate=true"
