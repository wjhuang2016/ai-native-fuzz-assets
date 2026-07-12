#!/usr/bin/env bash
set -euo pipefail

# Requires a test TiDB with a dynamic pause failpoint immediately before
# ttlTableSession.ExecuteSQLWithCheck in ttlDeleteTask.doDelete.
: "${MYSQL_PORT:?set MYSQL_PORT to the test TiDB SQL port}"
: "${STATUS_PORT:?set STATUS_PORT to the test TiDB status port}"

mysql_cmd=(mysql --protocol=tcp -h127.0.0.1 -P"${MYSQL_PORT}" -uroot)
fail_url="http://127.0.0.1:${STATUS_PORT}/fail/github.com/pingcap/tidb/pkg/ttl/ttlworker/beforeTTLDeleteExecute"
trigger_url="http://127.0.0.1:${STATUS_PORT}/test/ttl/trigger/ai_ttl_tz_probe/t"

"${mysql_cmd[@]}" -e "
SET GLOBAL time_zone='+00:00';
SET GLOBAL tidb_ttl_job_enable=ON;
DROP DATABASE IF EXISTS ai_ttl_tz_probe;
CREATE DATABASE ai_ttl_tz_probe;
CREATE TABLE ai_ttl_tz_probe.t (
  id BIGINT PRIMARY KEY,
  ts DATETIME NOT NULL
) TTL=ts + INTERVAL 1 MINUTE TTL_JOB_INTERVAL='1h';
SET time_zone='+00:00';
INSERT INTO ai_ttl_tz_probe.t VALUES (1,NOW()-INTERVAL 1 DAY);"

curl -fsS -X PUT -d pause "${fail_url}"
curl -fsS -X POST "${trigger_url}"

table_id=$("${mysql_cmd[@]}" -Nse \
  "SELECT TIDB_TABLE_ID FROM information_schema.tables WHERE table_schema='ai_ttl_tz_probe' AND table_name='t'")

for _ in $(seq 1 60); do
  cutoff=$("${mysql_cmd[@]}" -Nse \
    "SELECT current_job_ttl_expire FROM mysql.tidb_ttl_table_status WHERE table_id=${table_id} AND current_job_status='running'")
  [[ -n "${cutoff}" ]] && break
  sleep 1
done
: "${cutoff:?TTL job did not reach running state}"

"${mysql_cmd[@]}" --table -e "
SET time_zone='+00:00';
SET @cutoff='${cutoff}';
SET @epoch=UNIX_TIMESTAMP(@cutoff);
UPDATE ai_ttl_tz_probe.t SET ts=@cutoff+INTERVAL 4 HOUR WHERE id=1;
SELECT @epoch AS expire_epoch,@cutoff AS scan_cutoff,id,ts,
       ts<FROM_UNIXTIME(@epoch) AS expired_under_scan_context
FROM ai_ttl_tz_probe.t;
SET GLOBAL time_zone='+08:00';
SET time_zone='+08:00';
SELECT FROM_UNIXTIME(@epoch) AS delete_cutoff,id,ts,
       ts<FROM_UNIXTIME(@epoch) AS delete_matches
FROM ai_ttl_tz_probe.t;"

curl -fsS -X DELETE "${fail_url}"
sleep 2
"${mysql_cmd[@]}" --table -e \
  "SELECT COUNT(*) AS remaining_rows FROM ai_ttl_tz_probe.t"
