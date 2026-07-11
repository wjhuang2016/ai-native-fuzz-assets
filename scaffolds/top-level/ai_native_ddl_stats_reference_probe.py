#!/usr/bin/env python3
"""Stats side-metadata DDL reference-owner probe.

The target owner is the statistics metadata stored in mysql.stats_* tables.
Those rows are keyed by table, partition, column, and index IDs while SHOW
STATS* exposes schema names. DDL that changes object IDs or names must keep the
visible stats attached to the live object, or explicitly rely on delayed stats
GC for old physical IDs.
"""

from __future__ import annotations

import argparse
import dataclasses
import subprocess
import sys
import time
from collections.abc import Callable


@dataclasses.dataclass
class Result:
    rc: int
    out: str
    err: str


@dataclasses.dataclass
class CaseOutcome:
    name: str
    status: str
    detail: str


def mysql_args(args: argparse.Namespace, database: str | None = None) -> list[str]:
    cmd = [
        args.mysql,
        f"-h{args.host}",
        f"-P{args.port}",
        f"-u{args.user}",
        "--batch",
        "--raw",
        "--skip-column-names",
        "--connect-timeout=5",
    ]
    if database:
        cmd.append(database)
    return cmd


def run_mysql(args: argparse.Namespace, sql: str, database: str | None = None) -> Result:
    proc = subprocess.run(
        mysql_args(args, database) + ["-e", sql],
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    return Result(proc.returncode, proc.stdout.rstrip("\n"), proc.stderr.rstrip("\n"))


def exec_ok(args: argparse.Namespace, sql: str, database: str | None = None) -> None:
    res = run_mysql(args, sql, database)
    if res.rc != 0:
        raise RuntimeError(f"SQL failed rc={res.rc}: {sql}\n{res.err}")


def quote_ident(name: str) -> str:
    return "`" + name.replace("`", "``") + "`"


def quote_str(value: str) -> str:
    return "'" + value.replace("\\", "\\\\").replace("'", "''") + "'"


def rows(res: Result) -> list[str]:
    if res.out == "":
        return []
    return res.out.splitlines()


def combined(res: Result) -> str:
    return (res.err + "\n" + res.out).strip()


def setup_db(args: argparse.Namespace, db: str) -> None:
    exec_ok(args, f"DROP DATABASE IF EXISTS {quote_ident(db)}")
    exec_ok(args, f"CREATE DATABASE {quote_ident(db)}")


def cleanup_db(args: argparse.Namespace, db: str) -> None:
    exec_ok(args, f"DROP DATABASE IF EXISTS {quote_ident(db)}")


def table_id(args: argparse.Namespace, db: str, table: str = "t") -> int:
    res = run_mysql(
        args,
        "SELECT tidb_table_id FROM information_schema.tables "
        f"WHERE table_schema={quote_str(db)} AND table_name={quote_str(table)}",
    )
    if res.rc != 0:
        raise RuntimeError("failed to query table ID: " + combined(res))
    if not res.out.strip():
        raise RuntimeError(f"table {db}.{table} not found")
    return int(res.out.strip())


def raw_stats_meta(args: argparse.Namespace, tid: int) -> list[list[str]]:
    res = run_mysql(args, f"SELECT table_id, modify_count, count FROM mysql.stats_meta WHERE table_id={tid}")
    if res.rc != 0:
        raise RuntimeError("failed to query mysql.stats_meta: " + combined(res))
    return [line.split("\t") for line in rows(res)]


def show_stats_meta(args: argparse.Namespace, db: str, table: str = "t") -> list[list[str]]:
    res = run_mysql(
        args,
        f"SHOW STATS_META WHERE db_name={quote_str(db)} AND table_name={quote_str(table)}",
    )
    if res.rc != 0:
        raise RuntimeError("failed to SHOW STATS_META: " + combined(res))
    return [line.split("\t") for line in rows(res)]


def show_stats_histograms(args: argparse.Namespace, db: str, table: str = "t") -> list[list[str]]:
    res = run_mysql(
        args,
        f"SHOW STATS_HISTOGRAMS WHERE db_name={quote_str(db)} AND table_name={quote_str(table)}",
    )
    if res.rc != 0:
        raise RuntimeError("failed to SHOW STATS_HISTOGRAMS: " + combined(res))
    return [line.split("\t") for line in rows(res)]


def wait_for(
    predicate: Callable[[], tuple[bool, str]],
    timeout: float = 20.0,
    interval: float = 0.5,
) -> tuple[bool, str]:
    deadline = time.monotonic() + timeout
    last_detail = ""
    while time.monotonic() < deadline:
        ok, detail = predicate()
        if ok:
            return True, detail
        last_detail = detail
        time.sleep(interval)
    ok, detail = predicate()
    if ok:
        return True, detail
    return False, detail or last_detail


def analyze(args: argparse.Namespace, db: str, table: str = "t") -> None:
    exec_ok(args, f"ANALYZE TABLE `{table}`", db)


def counts_by_partition(meta_rows: list[list[str]]) -> dict[str, int]:
    # SHOW STATS_META columns: db, table, partition, update_time, modify_count, count, ...
    result: dict[str, int] = {}
    for row in meta_rows:
        if len(row) >= 6:
            result[row[2]] = int(row[5])
    return result


def case_table_rename_stats_follow_name(args: argparse.Namespace) -> CaseOutcome:
    name = "table_rename_stats_follow_new_name"
    db = "ai_native_stats_rename"
    try:
        setup_db(args, db)
        exec_ok(args, "CREATE TABLE t(id INT PRIMARY KEY, a INT, KEY idx_a(a))", db)
        exec_ok(args, "INSERT INTO t VALUES (1,10),(2,20),(3,30),(4,40)", db)
        analyze(args, db)
        exec_ok(args, "RENAME TABLE t TO t_new", db)

        def predicate() -> tuple[bool, str]:
            new_rows = show_stats_meta(args, db, "t_new")
            old_rows = show_stats_meta(args, db, "t")
            if old_rows:
                return False, f"old table name still has visible stats: {old_rows}"
            counts = counts_by_partition(new_rows)
            if counts.get("") != 4:
                return False, f"new table name stats not visible with count=4: {new_rows}"
            return True, "stats followed table rename through table ID"

        ok, detail = wait_for(predicate)
        return CaseOutcome(name, "ok" if ok else "finding", detail)
    finally:
        cleanup_db(args, db)


def case_column_rename_stats_follow_name(args: argparse.Namespace) -> CaseOutcome:
    name = "column_rename_stats_follow_new_name"
    db = "ai_native_stats_col_rename"
    try:
        setup_db(args, db)
        exec_ok(args, "CREATE TABLE t(id INT PRIMARY KEY, a INT, b INT, KEY idx_a(a))", db)
        exec_ok(args, "INSERT INTO t VALUES (1,10,100),(2,20,200),(3,30,300),(4,40,400)", db)
        analyze(args, db)
        exec_ok(args, "ALTER TABLE t RENAME COLUMN a TO aa", db)

        def predicate() -> tuple[bool, str]:
            hist = show_stats_histograms(args, db)
            col_names = {row[3] for row in hist if len(row) > 4 and row[4] == "0"}
            if "aa" not in col_names:
                return False, f"renamed column stats not visible as aa: {hist}"
            if "a" in col_names:
                return False, f"old column name still visible in column stats: {hist}"
            return True, "column stats followed column rename through column ID"

        ok, detail = wait_for(predicate)
        return CaseOutcome(name, "ok" if ok else "finding", detail)
    finally:
        cleanup_db(args, db)


def case_column_change_stats_follow_name(args: argparse.Namespace) -> CaseOutcome:
    name = "column_change_stats_follow_new_name"
    db = "ai_native_stats_col_change"
    try:
        setup_db(args, db)
        exec_ok(args, "CREATE TABLE t(id INT PRIMARY KEY, a INT, b INT, KEY idx_a(a))", db)
        exec_ok(args, "INSERT INTO t VALUES (1,10,100),(2,20,200),(3,30,300),(4,40,400)", db)
        analyze(args, db)
        exec_ok(args, "ALTER TABLE t CHANGE COLUMN a aa INT", db)

        def predicate() -> tuple[bool, str]:
            hist = show_stats_histograms(args, db)
            col_names = {row[3] for row in hist if len(row) > 4 and row[4] == "0"}
            if "aa" not in col_names:
                return False, f"changed column stats not visible as aa: {hist}"
            if "a" in col_names:
                return False, f"old column name still visible in column stats after CHANGE: {hist}"
            return True, "column stats followed CHANGE COLUMN through column ID"

        ok, detail = wait_for(predicate)
        return CaseOutcome(name, "ok" if ok else "finding", detail)
    finally:
        cleanup_db(args, db)


def case_add_partitioning_rewrites_global_stats_id(args: argparse.Namespace) -> CaseOutcome:
    name = "add_partitioning_rewrites_global_stats_id"
    db = "ai_native_stats_add_part"
    try:
        setup_db(args, db)
        exec_ok(args, "CREATE TABLE t(id INT PRIMARY KEY, a INT, KEY idx_a(a))", db)
        exec_ok(args, "INSERT INTO t VALUES (1,10),(2,20),(3,30),(4,40)", db)
        analyze(args, db)
        old_tid = table_id(args, db)
        exec_ok(
            args,
            "ALTER TABLE t PARTITION BY RANGE(id) "
            "(PARTITION p0 VALUES LESS THAN (3), PARTITION p1 VALUES LESS THAN (10))",
            db,
        )
        new_tid = table_id(args, db)

        def predicate() -> tuple[bool, str]:
            meta = show_stats_meta(args, db)
            counts = counts_by_partition(meta)
            raw_new = raw_stats_meta(args, new_tid)
            raw_old = raw_stats_meta(args, old_tid) if old_tid != new_tid else []
            if counts.get("global") != 4:
                return False, f"global stats not visible after add partitioning: {meta}"
            if not raw_new or int(raw_new[0][2]) != 4:
                return False, f"new global table ID {new_tid} has no count=4 stats: {raw_new}"
            if old_tid != new_tid and raw_old:
                return False, f"old single-table stats ID {old_tid} still has raw stats after rewrite: {raw_old}"
            return True, "single-table stats ID rewrote to partitioned global table ID"

        ok, detail = wait_for(predicate)
        return CaseOutcome(name, "ok" if ok else "finding", detail)
    finally:
        cleanup_db(args, db)


def case_remove_partitioning_rewrites_global_stats_id(args: argparse.Namespace) -> CaseOutcome:
    name = "remove_partitioning_rewrites_global_stats_id"
    db = "ai_native_stats_remove_part"
    try:
        setup_db(args, db)
        exec_ok(
            args,
            "CREATE TABLE t(id INT PRIMARY KEY, a INT, KEY idx_a(a)) "
            "PARTITION BY RANGE(id) (PARTITION p0 VALUES LESS THAN (3), PARTITION p1 VALUES LESS THAN (10))",
            db,
        )
        exec_ok(args, "INSERT INTO t VALUES (1,10),(2,20),(3,30),(4,40)", db)
        analyze(args, db)
        old_tid = table_id(args, db)
        exec_ok(args, "ALTER TABLE t REMOVE PARTITIONING", db)
        new_tid = table_id(args, db)

        def predicate() -> tuple[bool, str]:
            meta = show_stats_meta(args, db)
            counts = counts_by_partition(meta)
            raw_new = raw_stats_meta(args, new_tid)
            raw_old = raw_stats_meta(args, old_tid) if old_tid != new_tid else []
            if counts != {"": 4}:
                return False, f"visible stats did not collapse to one single-table row: {meta}"
            if not raw_new or int(raw_new[0][2]) != 4:
                return False, f"new single table ID {new_tid} has no count=4 stats: {raw_new}"
            if old_tid != new_tid and raw_old:
                return False, f"old global table ID {old_tid} still has raw stats after rewrite: {raw_old}"
            return True, "partitioned global stats ID rewrote to new single-table ID"

        ok, detail = wait_for(predicate)
        return CaseOutcome(name, "ok" if ok else "finding", detail)
    finally:
        cleanup_db(args, db)


def case_truncate_table_stats_new_table_id(args: argparse.Namespace) -> CaseOutcome:
    name = "truncate_table_stats_new_table_id"
    db = "ai_native_stats_truncate"
    try:
        setup_db(args, db)
        exec_ok(args, "CREATE TABLE t(id INT PRIMARY KEY, a INT, KEY idx_a(a))", db)
        exec_ok(args, "INSERT INTO t VALUES (1,10),(2,20),(3,30),(4,40)", db)
        analyze(args, db)
        old_tid = table_id(args, db)
        exec_ok(args, "TRUNCATE TABLE t", db)
        new_tid = table_id(args, db)
        if new_tid == old_tid:
            return CaseOutcome(name, "finding", f"truncate did not allocate a new table ID: {old_tid}")

        def predicate() -> tuple[bool, str]:
            meta = show_stats_meta(args, db)
            counts = counts_by_partition(meta)
            raw_new = raw_stats_meta(args, new_tid)
            if counts.get("") != 0:
                return False, f"visible stats for truncated table should be count=0: {meta}"
            if not raw_new or int(raw_new[0][2]) != 0:
                return False, f"new table ID {new_tid} has no empty stats row: {raw_new}"
            return True, "truncate created empty stats for the new table ID"

        ok, detail = wait_for(predicate)
        return CaseOutcome(name, "ok" if ok else "finding", detail)
    finally:
        cleanup_db(args, db)


def case_truncate_partition_updates_visible_stats(args: argparse.Namespace) -> CaseOutcome:
    name = "truncate_partition_updates_visible_stats"
    db = "ai_native_stats_trunc_part"
    try:
        setup_db(args, db)
        exec_ok(
            args,
            "CREATE TABLE t(id INT PRIMARY KEY, a INT, KEY idx_a(a)) "
            "PARTITION BY RANGE(id) (PARTITION p0 VALUES LESS THAN (3), PARTITION p1 VALUES LESS THAN (10))",
            db,
        )
        exec_ok(args, "INSERT INTO t VALUES (1,10),(2,20),(3,30),(4,40)", db)
        analyze(args, db)
        exec_ok(args, "ALTER TABLE t TRUNCATE PARTITION p0", db)

        def predicate() -> tuple[bool, str]:
            meta = show_stats_meta(args, db)
            counts = counts_by_partition(meta)
            if counts.get("global") != 2 or counts.get("p0") != 0 or counts.get("p1") != 2:
                return False, f"truncate partition stats should be global=2,p0=0,p1=2: {meta}"
            return True, "truncate partition moved visible stats to the new partition ID and updated global count"

        ok, detail = wait_for(predicate)
        return CaseOutcome(name, "ok" if ok else "finding", detail)
    finally:
        cleanup_db(args, db)


def preflight(args: argparse.Namespace) -> tuple[bool, str]:
    res = run_mysql(args, "SHOW TABLES FROM mysql LIKE 'stats_meta'")
    if res.rc != 0:
        return False, "cannot query mysql.stats_meta: " + combined(res)
    if "stats_meta" not in res.out:
        return False, "mysql.stats_meta does not exist"
    return True, "stats tables are available"


def cases() -> list[Callable[[argparse.Namespace], CaseOutcome]]:
    return [
        case_table_rename_stats_follow_name,
        case_column_rename_stats_follow_name,
        case_column_change_stats_follow_name,
        case_add_partitioning_rewrites_global_stats_id,
        case_remove_partitioning_rewrites_global_stats_id,
        case_truncate_table_stats_new_table_id,
        case_truncate_partition_updates_visible_stats,
    ]


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default="mysql")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", default="14000")
    parser.add_argument("--user", default="root")
    args = parser.parse_args()

    ok, detail = preflight(args)
    if not ok:
        print(f"SKIP stats side-metadata preflight: {detail}")
        print("SUMMARY total=0 findings=0 skipped=1")
        return 0

    original_prune = run_mysql(args, "SELECT @@global.tidb_partition_prune_mode").out.strip()
    if original_prune:
        exec_ok(args, "SET GLOBAL tidb_partition_prune_mode='dynamic'")

    outcomes: list[CaseOutcome] = []
    try:
        for case in cases():
            try:
                outcome = case(args)
            except Exception as exc:  # noqa: BLE001 - keep reporting the matrix.
                outcome = CaseOutcome(case.__name__, "finding", f"unhandled probe error: {exc}")
            outcomes.append(outcome)
            prefix = "OK" if outcome.status == "ok" else "FINDING"
            print(f"{prefix} stats side-metadata {outcome.name} {outcome.detail}")
            sys.stdout.flush()
    finally:
        if original_prune:
            exec_ok(args, f"SET GLOBAL tidb_partition_prune_mode={quote_str(original_prune)}")

    findings = sum(1 for item in outcomes if item.status == "finding")
    print(f"SUMMARY total={len(outcomes)} findings={findings} skipped=0")
    return 1 if findings else 0


if __name__ == "__main__":
    raise SystemExit(main())
