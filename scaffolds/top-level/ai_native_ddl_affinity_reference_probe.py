#!/usr/bin/env python3
"""Affinity DDL reference-owner screening probe.

The proof obligation is:

    if DDL changes/removes a table or partition with AFFINITY metadata, the
    public affinity reference must either follow the object or disappear/block.

This is a screening probe, not a broad affinity test. Affinity looked similar
to other side owners because it creates PD affinity groups, but source reading
shows group IDs are table/partition-ID based and the main SQL-visible metadata
lives inside TableInfo. The probe checks whether a small DDL matrix contradicts
that negative-selector model.
"""

from __future__ import annotations

import argparse
import dataclasses
import subprocess
import time
from collections.abc import Callable


@dataclasses.dataclass
class Result:
    rc: int
    out: str
    err: str


@dataclasses.dataclass
class Outcome:
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


def combined(res: Result) -> str:
    return (res.err + "\n" + res.out).strip()


def exec_ok(args: argparse.Namespace, sql: str, database: str | None = None) -> None:
    res = run_mysql(args, sql, database)
    if res.rc != 0:
        raise RuntimeError(f"SQL failed rc={res.rc}: {sql}\n{combined(res)}")


def quote_ident(name: str) -> str:
    return "`" + name.replace("`", "``") + "`"


def setup_db(args: argparse.Namespace, db: str) -> None:
    exec_ok(args, f"DROP DATABASE IF EXISTS {quote_ident(db)}")
    exec_ok(args, f"CREATE DATABASE {quote_ident(db)}")


def cleanup_db(args: argparse.Namespace, *dbs: str) -> None:
    for db in dbs:
        run_mysql(args, f"DROP DATABASE IF EXISTS {quote_ident(db)}")


def show_create(args: argparse.Namespace, db: str, table: str) -> str:
    res = run_mysql(args, f"SHOW CREATE TABLE {quote_ident(table)}", db)
    if res.rc != 0:
        raise RuntimeError("SHOW CREATE failed: " + combined(res))
    if "\t" not in res.out:
        raise RuntimeError("unexpected SHOW CREATE output: " + res.out)
    return res.out.split("\t", 1)[1]


def affinity_rows(args: argparse.Namespace, db: str, retries: int = 5) -> list[list[str]]:
    last = ""
    for attempt in range(retries):
        res = run_mysql(args, "SHOW AFFINITY")
        if res.rc == 0:
            rows: list[list[str]] = []
            for line in res.out.splitlines():
                fields = line.split("\t")
                if fields and fields[0] == db:
                    rows.append(fields)
            return rows
        last = combined(res)
        if "Information schema is changed" in last and attempt + 1 < retries:
            time.sleep(0.25)
            continue
        break
    raise RuntimeError("SHOW AFFINITY failed: " + last)


def row_names(rows: list[list[str]]) -> list[tuple[str, str]]:
    names: list[tuple[str, str]] = []
    for row in rows:
        table = row[1] if len(row) > 1 else ""
        part = row[2] if len(row) > 2 and row[2] != "NULL" else ""
        names.append((table, part))
    return sorted(names)


def run_case(args: argparse.Namespace, name: str, fn: Callable[[argparse.Namespace], Outcome]) -> Outcome:
    try:
        return fn(args)
    except Exception as exc:
        return Outcome(name, "finding", f"probe crashed: {exc}")


def case_visible_control(args: argparse.Namespace) -> Outcome:
    name = "table_affinity_visible_control"
    db = "ai_native_ddl_affinity_control"
    try:
        setup_db(args, db)
        exec_ok(args, "CREATE TABLE t(a INT) AFFINITY='table'", db)
        ddl = show_create(args, db, "t")
        rows = affinity_rows(args, db)
        names = row_names(rows)
        if ("t", "") not in names:
            return Outcome(name, "finding", f"SHOW AFFINITY did not expose table affinity; rows={rows}; ddl={ddl}")
        if "AFFINITY='table'" not in ddl and "AFFINITY = 'table'" not in ddl:
            return Outcome(name, "finding", "SHOW CREATE did not expose affinity: " + ddl)
        return Outcome(name, "ok", "AFFINITY is visible in SHOW CREATE and SHOW AFFINITY")
    finally:
        cleanup_db(args, db)


def case_rename_table_follows(args: argparse.Namespace) -> Outcome:
    name = "rename_table_affinity_follows_new_name"
    db = "ai_native_ddl_affinity_rename"
    try:
        setup_db(args, db)
        exec_ok(args, "CREATE TABLE t(a INT) AFFINITY='table'", db)
        exec_ok(args, "RENAME TABLE t TO tt", db)
        rows = affinity_rows(args, db)
        names = row_names(rows)
        if ("tt", "") not in names or ("t", "") in names:
            return Outcome(name, "finding", f"affinity did not follow rename cleanly; names={names}")
        ddl = show_create(args, db, "tt")
        if "AFFINITY='table'" not in ddl and "AFFINITY = 'table'" not in ddl:
            return Outcome(name, "finding", "renamed table lost affinity in SHOW CREATE: " + ddl)
        return Outcome(name, "ok", "rename table moved visible affinity to the new table name")
    finally:
        cleanup_db(args, db)


def case_truncate_table_preserves_visible_affinity(args: argparse.Namespace) -> Outcome:
    name = "truncate_table_affinity_preserved"
    db = "ai_native_ddl_affinity_truncate"
    try:
        setup_db(args, db)
        exec_ok(args, "CREATE TABLE t(a INT) AFFINITY='table'", db)
        exec_ok(args, "TRUNCATE TABLE t", db)
        rows = affinity_rows(args, db)
        names = row_names(rows)
        if names.count(("t", "")) != 1:
            return Outcome(name, "finding", f"truncate did not preserve exactly one visible affinity row; names={names}")
        ddl = show_create(args, db, "t")
        if "AFFINITY='table'" not in ddl and "AFFINITY = 'table'" not in ddl:
            return Outcome(name, "finding", "truncated table lost affinity in SHOW CREATE: " + ddl)
        return Outcome(name, "ok", "truncate table preserved one visible affinity reference")
    finally:
        cleanup_db(args, db)


def case_drop_table_cleans_visible_affinity(args: argparse.Namespace) -> Outcome:
    name = "drop_table_affinity_removed"
    db = "ai_native_ddl_affinity_droptable"
    try:
        setup_db(args, db)
        exec_ok(args, "CREATE TABLE t(a INT) AFFINITY='table'", db)
        exec_ok(args, "DROP TABLE t", db)
        rows = affinity_rows(args, db)
        if rows:
            return Outcome(name, "finding", f"DROP TABLE left visible affinity rows: {rows}")
        return Outcome(name, "ok", "drop table removed visible affinity reference")
    finally:
        cleanup_db(args, db)


def case_partition_affinity_paths(args: argparse.Namespace) -> Outcome:
    name = "partition_affinity_truncate_and_block_controls"
    db = "ai_native_ddl_affinity_partition"
    try:
        setup_db(args, db)
        exec_ok(
            args,
            "CREATE TABLE tp(a INT) AFFINITY='partition' "
            "PARTITION BY RANGE(a) (PARTITION p0 VALUES LESS THAN (10), PARTITION p1 VALUES LESS THAN (20))",
            db,
        )
        exec_ok(args, "ALTER TABLE tp TRUNCATE PARTITION p0", db)
        names = row_names(affinity_rows(args, db))
        if ("tp", "p0") not in names or ("tp", "p1") not in names:
            return Outcome(name, "finding", f"truncate partition did not preserve partition affinity rows; names={names}")
        drop = run_mysql(args, "ALTER TABLE tp DROP PARTITION p1", db)
        if drop.rc == 0 or "AFFINITY" not in combined(drop):
            return Outcome(name, "finding", "DROP PARTITION was not blocked by affinity owner: " + combined(drop))
        remove = run_mysql(args, "ALTER TABLE tp REMOVE PARTITIONING", db)
        if remove.rc == 0 or "AFFINITY" not in combined(remove):
            return Outcome(name, "finding", "REMOVE PARTITIONING was not blocked by affinity owner: " + combined(remove))
        return Outcome(name, "ok", "truncate partition preserved affinity; drop/remove partitioning blocked")
    finally:
        cleanup_db(args, db)


def case_drop_database_cleans_visible_affinity(args: argparse.Namespace) -> Outcome:
    name = "drop_database_affinity_removed"
    db = "ai_native_ddl_affinity_dropdb"
    try:
        setup_db(args, db)
        exec_ok(args, "CREATE TABLE t(a INT) AFFINITY='table'", db)
        exec_ok(
            args,
            "CREATE TABLE tp(a INT) AFFINITY='partition' PARTITION BY HASH(a) PARTITIONS 2",
            db,
        )
        before = row_names(affinity_rows(args, db))
        if len(before) != 3:
            return Outcome(name, "finding", f"setup did not expose expected affinity rows before DROP DATABASE; names={before}")
        exec_ok(args, f"DROP DATABASE {quote_ident(db)}")
        rows = affinity_rows(args, db)
        if rows:
            return Outcome(name, "finding", f"DROP DATABASE left visible affinity rows: {rows}")
        return Outcome(name, "ok", "drop database removed visible table and partition affinity references")
    finally:
        cleanup_db(args, db)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default="mysql")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", default="14000")
    parser.add_argument("--user", default="root")
    args = parser.parse_args()

    cases: list[tuple[str, Callable[[argparse.Namespace], Outcome]]] = [
        ("table_affinity_visible_control", case_visible_control),
        ("rename_table_affinity_follows_new_name", case_rename_table_follows),
        ("truncate_table_affinity_preserved", case_truncate_table_preserves_visible_affinity),
        ("drop_table_affinity_removed", case_drop_table_cleans_visible_affinity),
        ("partition_affinity_truncate_and_block_controls", case_partition_affinity_paths),
        ("drop_database_affinity_removed", case_drop_database_cleans_visible_affinity),
    ]

    outcomes = [run_case(args, name, fn) for name, fn in cases]
    for outcome in outcomes:
        print(f"{outcome.status}\t{outcome.name}\t{outcome.detail}")
    findings = sum(1 for outcome in outcomes if outcome.status == "finding")
    skipped = sum(1 for outcome in outcomes if outcome.status == "skipped")
    print(f"SUMMARY total={len(outcomes)} findings={findings} skipped={skipped}")
    return 1 if findings else 0


if __name__ == "__main__":
    raise SystemExit(main())
