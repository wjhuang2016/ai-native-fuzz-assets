#!/usr/bin/env bash

set -euo pipefail

mode="${1:-1pc}"
ddl_shape="${2:-truncate}"
case "${mode}" in
1pc)
	enable_one_pc=1
	;;
2pc)
	enable_one_pc=0
	;;
*)
	echo "usage: $0 [1pc|2pc]" >&2
	exit 2
	;;
esac
case "${ddl_shape}" in
truncate|add-index)
	;;
*)
	echo "usage: $0 [1pc|2pc] [truncate|add-index]" >&2
	exit 2
	;;
esac

mysql_host="${MYSQL_HOST:-127.0.0.1}"
mysql_port="${MYSQL_PORT:-4005}"
status_url="${STATUS_URL:-http://127.0.0.1:10085}"
enable_async_commit="${ENABLE_ASYNC_COMMIT:-0}"
db="ai_onepc_schema_horizon_${mode}_${ddl_shape//-/_}"
writer_log="/tmp/${db}_writer.log"
failpoint_url="${status_url}/fail/tikvclient/beforePrewrite"
mysql_cmd=(mysql -h "${mysql_host}" -P "${mysql_port}" -u root --batch --skip-column-names --raw)
old_mdl=""

sql() {
	"${mysql_cmd[@]}" -e "$1"
}

cleanup() {
	curl -fsS -X DELETE "${failpoint_url}" >/dev/null 2>&1 || true
	if [[ -n "${old_mdl}" ]]; then
		sql "set global tidb_enable_metadata_lock=${old_mdl}" >/dev/null 2>&1 || true
	fi
	sql "drop database if exists ${db}" >/dev/null 2>&1 || true
	rm -f "${writer_log}"
}
trap cleanup EXIT

old_mdl="$(sql 'select @@global.tidb_enable_metadata_lock')"
sql "set global tidb_enable_metadata_lock=0"
sql "drop database if exists ${db}; create database ${db}; create table ${db}.t (id int primary key, v int)"

old_table_id="$(sql "select tidb_table_id from information_schema.tables where table_schema='${db}' and table_name='t'")"
job_floor="$(sql 'select ifnull(max(job_id), 0) from mysql.tidb_ddl_history')"

curl -fsS -X PUT -d '1*pause' "${failpoint_url}" >/dev/null

set +e
(
	"${mysql_cmd[@]}" <<SQL
use ${db};
set @@tidb_enable_async_commit=${enable_async_commit};
set @@tidb_enable_1pc=${enable_one_pc};
insert into t values (1, 10);
select concat('TXN_INFO=', @@tidb_last_txn_info);
SQL
) >"${writer_log}" 2>&1 &
writer_pid=$!
set -e

sleep 0.3
if ! kill -0 "${writer_pid}" >/dev/null 2>&1; then
	echo "INVALID: writer reached a terminal result before the hold point"
	cat "${writer_log}"
	exit 3
fi

process_state="$(sql "select concat(command, ':', time, ':', ifnull(info, '')) from information_schema.processlist where db='${db}' order by id" || true)"
case "${ddl_shape}" in
truncate)
	ddl_sql="truncate table ${db}.t"
	;;
add-index)
	ddl_sql="alter table ${db}.t add index idx_v(v)"
	;;
esac
timeout 15 "${mysql_cmd[@]}" -e "${ddl_sql}"

ddl_job_id="$(sql "select job_id from mysql.tidb_ddl_history where job_id > ${job_floor} and db_name='${db}' and table_name='t' order by job_id desc limit 1")"
ddl_finished_ts="$(sql "select json_unquote(json_extract(cast(cast(job_meta as char) as json), '$.binlog.FinishedTS')) from mysql.tidb_ddl_history where job_id=${ddl_job_id}")"
new_table_id="$(sql "select tidb_table_id from information_schema.tables where table_schema='${db}' and table_name='t'")"

curl -fsS -X DELETE "${failpoint_url}" >/dev/null
set +e
wait "${writer_pid}"
writer_rc=$?
set -e

writer_output="$(cat "${writer_log}")"
current_rows="$(sql "select concat(id, ':', v) from ${db}.t order by id")"
index_rows="not-applicable"
admin_check_rc="not-applicable"
if [[ "${ddl_shape}" == "add-index" ]]; then
	index_rows="$(sql "select concat(id, ':', v) from ${db}.t force index(idx_v) where v=10 order by id")"
	set +e
	admin_check_output="$(sql "admin check table ${db}.t" 2>&1)"
	admin_check_rc=$?
	set -e
fi
txn_info="$(printf '%s\n' "${writer_output}" | sed -n 's/^TXN_INFO=//p' | tail -n 1)"
commit_ts="$(printf '%s\n' "${txn_info}" | sed -n 's/.*\"commit_ts\":\([0-9][0-9]*\).*/\1/p')"

printf 'mode=%s\n' "${mode}"
printf 'ddl_shape=%s\n' "${ddl_shape}"
printf 'enable_async_commit=%s\n' "${enable_async_commit}"
printf 'writer_rc=%s\n' "${writer_rc}"
printf 'process_state=%s\n' "${process_state}"
printf 'old_table_id=%s\n' "${old_table_id}"
printf 'new_table_id=%s\n' "${new_table_id}"
printf 'ddl_job_id=%s\n' "${ddl_job_id}"
printf 'ddl_finished_ts=%s\n' "${ddl_finished_ts}"
printf 'commit_ts=%s\n' "${commit_ts:-unknown}"
printf 'current_rows=%s\n' "${current_rows:-<empty>}"
printf 'index_rows=%s\n' "${index_rows:-<empty>}"
printf 'admin_check_rc=%s\n' "${admin_check_rc}"
printf 'txn_info=%s\n' "${txn_info:-<missing>}"
if [[ ${writer_rc} -ne 0 ]]; then
	printf 'writer_output=%s\n' "${writer_output}"
fi

verdict="INVALID"
if [[ ${writer_rc} -eq 0 && -n "${commit_ts}" && ${commit_ts} -gt ${ddl_finished_ts} ]]; then
	if [[ "${ddl_shape}" == "truncate" ]]; then
		if [[ "${mode}" == "1pc" && -z "${current_rows}" ]]; then
			verdict="RED"
		elif [[ "${mode}" == "2pc" && "${current_rows}" == "1:10" ]]; then
			verdict="GREEN"
		fi
	elif [[ "${mode}" == "1pc" && "${current_rows}" == "1:10" && -z "${index_rows}" && ${admin_check_rc} -ne 0 ]]; then
		verdict="RED"
	elif [[ "${mode}" == "2pc" && "${current_rows}" == "1:10" && "${index_rows}" == "1:10" && ${admin_check_rc} -eq 0 ]]; then
		verdict="GREEN"
	fi
fi
printf 'verdict=%s\n' "${verdict}"
