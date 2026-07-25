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

mysql_host="${MYSQL_HOST:-127.0.0.1}"
mysql_port="${MYSQL_PORT:-4000}"
db="${TEST_DB:-ai_virtual_generated_week_format}"
mysql_cmd=("$mysql_bin" -h"$mysql_host" -P"$mysql_port" -uroot --batch --raw --skip-column-names)

cleanup() {
  "${mysql_cmd[@]}" -e "DROP DATABASE IF EXISTS \`$db\`" >/dev/null 2>&1 || true
  "${mysql_cmd[@]}" -e "DROP DATABASE IF EXISTS ai_week_gen_gate" >/dev/null 2>&1 || true
  "${mysql_cmd[@]}" -e "DROP DATABASE IF EXISTS ai_week_gen_red" >/dev/null 2>&1 || true
}
trap cleanup EXIT

settings="$("${mysql_cmd[@]}" -e "
SELECT CONCAT(@@tidb_enable_metadata_lock,':',@@sql_mode)")"
echo "settings=$settings"
[[ "$settings" == 1:* ]]

"${mysql_cmd[@]}" <<SQL
DROP DATABASE IF EXISTS \`$db\`;
CREATE DATABASE \`$db\`;
USE \`$db\`;

CREATE TABLE red_fast(
  id INT PRIMARY KEY,
  d DATE NOT NULL,
  g INT AS (WEEK(d)) VIRTUAL,
  UNIQUE KEY ug(g)
);
CREATE TABLE red_root LIKE red_fast;
CREATE TABLE green_explicit(
  id INT PRIMARY KEY,
  d DATE NOT NULL,
  g INT AS (WEEK(d, 3)) VIRTUAL,
  UNIQUE KEY ug(g)
);
CREATE TABLE direct_expr(id INT PRIMARY KEY, d DATE NOT NULL);

SET @@session.default_week_format=0;
INSERT INTO red_fast(id,d) VALUES (1,'2021-01-01');
INSERT INTO red_root(id,d) VALUES (1,'2021-01-01');

SET @@session.default_week_format=3;
INSERT INTO red_fast(id,d) VALUES (2,'2021-01-01');
INSERT INTO red_root(id,d) VALUES (2,'2021-01-01');
SQL

if direct_out="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
CREATE INDEX direct_week ON direct_expr((WEEK(d)))" 2>&1)"; then
  echo "expected the direct expression-index safety gate to reject WEEK(d)" >&2
  exit 1
fi
echo "direct_expression_index=$direct_out"
[[ "$direct_out" == *"ERROR 8200"* ]]
[[ "$direct_out" == *"unsafe functions"* ]]

base_before="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET @@session.default_week_format=3;
SELECT GROUP_CONCAT(CONCAT(id,':',g,':',WEEK(d),':',g=0) ORDER BY id)
FROM red_fast IGNORE INDEX(ug)")"
index_before="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET @@session.default_week_format=3;
SELECT GROUP_CONCAT(CONCAT(id,':',g) ORDER BY id)
FROM red_fast FORCE INDEX(ug)")"
echo "red_base_before=$base_before"
echo "red_index_before=$index_before"
[[ "$base_before" == "1:53:53:0,2:53:53:0" ]]
[[ "$index_before" == "1:0,2:53" ]]

root_g0="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET @@session.default_week_format=3;
SELECT GROUP_CONCAT(id ORDER BY id)
FROM red_root IGNORE INDEX(ug)
WHERE g=0")"
fast_g0="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET @@session.default_week_format=3;
SELECT GROUP_CONCAT(CONCAT(id,':',g,':',WEEK(d),':',g=0) ORDER BY id)
FROM red_fast
WHERE g=0")"
logical_duplicate="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET @@session.default_week_format=3;
SELECT CONCAT(g,':',COUNT(*))
FROM red_fast IGNORE INDEX(ug)
GROUP BY g
HAVING COUNT(*) > 1")"
echo "red_root_g0=$root_g0"
echo "red_fast_g0=$fast_g0"
echo "red_logical_duplicate=$logical_duplicate"
[[ "$root_g0" == "NULL" ]]
[[ "$fast_g0" == "1:53:53:0" ]]
[[ "$logical_duplicate" == "53:2" ]]

if admin_before="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET @@session.default_week_format=3;
ADMIN CHECK TABLE red_fast" 2>&1)"; then
  echo "expected ADMIN CHECK TABLE to detect the pre-delete inconsistency" >&2
  exit 1
fi
echo "red_admin_before=$admin_before"
[[ "$admin_before" == *"ERROR 8223"* ]]

echo "red DELETE plan:"
"${mysql_cmd[@]}" -e "
USE \`$db\`;
SET @@session.default_week_format=3;
EXPLAIN FORMAT='brief' DELETE FROM red_fast WHERE g=0"

delete_result="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET @@session.default_week_format=3;
DELETE FROM red_fast WHERE g=0;
SELECT ROW_COUNT();
DELETE FROM red_root IGNORE INDEX(ug) WHERE g=0;
SELECT ROW_COUNT()")"
echo "red_delete=$delete_result"
[[ "$delete_result" == $'1\n0' ]]

base_after="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET @@session.default_week_format=3;
SELECT GROUP_CONCAT(CONCAT(id,':',g) ORDER BY id)
FROM red_fast IGNORE INDEX(ug)")"
index_after="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET @@session.default_week_format=3;
SELECT GROUP_CONCAT(CONCAT(id,':',g) ORDER BY id)
FROM red_fast FORCE INDEX(ug)")"
root_after="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET @@session.default_week_format=3;
SELECT GROUP_CONCAT(CONCAT(id,':',g) ORDER BY id)
FROM red_root IGNORE INDEX(ug)")"
echo "red_base_after=$base_after"
echo "red_index_after=$index_after"
echo "red_root_after=$root_after"
[[ "$base_after" == "2:53" ]]
[[ "$index_after" == "1:0" ]]
[[ "$root_after" == "1:53,2:53" ]]

if admin_after="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET @@session.default_week_format=3;
ADMIN CHECK TABLE red_fast" 2>&1)"; then
  echo "expected ADMIN CHECK TABLE to detect the post-delete corruption" >&2
  exit 1
fi
echo "red_admin_after=$admin_after"
[[ "$admin_after" == *"ERROR 8223"* ]]

"${mysql_cmd[@]}" -e "
USE \`$db\`;
SET @@session.default_week_format=0;
INSERT INTO green_explicit(id,d) VALUES (1,'2021-01-01')"
if green_insert="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET @@session.default_week_format=3;
INSERT INTO green_explicit(id,d) VALUES (2,'2021-01-01')" 2>&1)"; then
  echo "expected explicit WEEK mode to enforce the unique key" >&2
  exit 1
fi
echo "green_insert=$green_insert"
[[ "$green_insert" == *"ERROR 1062"* ]]

green_rows="$("${mysql_cmd[@]}" -e "
USE \`$db\`;
SET @@session.default_week_format=3;
SELECT GROUP_CONCAT(CONCAT(id,':',g) ORDER BY id)
FROM green_explicit IGNORE INDEX(ug)")"
echo "green_rows=$green_rows"
[[ "$green_rows" == "1:53" ]]
"${mysql_cmd[@]}" -e "
USE \`$db\`;
SET @@session.default_week_format=3;
ADMIN CHECK TABLE green_explicit"

echo "matrix=CRITICAL_RED_CONFIRMED"
