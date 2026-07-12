#!/usr/bin/env bash
set -euo pipefail

MYSQL_HOST="${MYSQL_HOST:-127.0.0.1}"
MYSQL_PORT="${MYSQL_PORT:-14003}"
MYSQL_USER="${MYSQL_USER:-root}"
STATUS_URL="${STATUS_URL:-http://127.0.0.1:18083}"
DB="${DB:-ai_import_partial_probe}"
FP="github.com/pingcap/tidb/pkg/executor/importer/mockIndexEngineCloseErrAfterDataImport"

mysql_cmd=(mysql --protocol=tcp -h"$MYSQL_HOST" -P"$MYSQL_PORT" -u"$MYSQL_USER" --comments)

"${mysql_cmd[@]}" -e "
SET GLOBAL tidb_enable_dist_task=OFF;
DROP DATABASE IF EXISTS ${DB};
CREATE DATABASE ${DB};
USE ${DB};
CREATE TABLE src(a INT PRIMARY KEY, b INT);
INSERT INTO src VALUES (1,10),(2,20),(3,30);
CREATE TABLE dst(a INT PRIMARY KEY, b INT, INDEX ib(b));
"

curl -fsS -X PUT -d 'return(true)' "${STATUS_URL}/fail/${FP}"
set +e
"${mysql_cmd[@]}" -D"$DB" -e "IMPORT INTO dst FROM SELECT a,b FROM src;"
import_rc=$?
set -e
curl -fsS -X DELETE "${STATUS_URL}/fail/${FP}"

"${mysql_cmd[@]}" -D"$DB" -e "
SELECT 'table_scan' AS arm,COUNT(*) AS rows_seen FROM dst IGNORE INDEX(ib);
SELECT 'forced_index' AS arm,COUNT(*) AS rows_seen FROM dst USE INDEX(ib) WHERE b>=0;
"

set +e
"${mysql_cmd[@]}" -D"$DB" -e "ADMIN CHECK TABLE dst;"
check_rc=$?
set -e

printf 'import_rc=%s admin_check_rc=%s\n' "$import_rc" "$check_rc"
test "$import_rc" -ne 0
test "$check_rc" -ne 0
