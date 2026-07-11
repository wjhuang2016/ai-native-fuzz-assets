#!/usr/bin/env python3
"""Region-split policy DDL reference-owner screen.

This is a negative-sample probe for the DDL reference-owner methodology. Region
split policy is SQL-visible and attached to table/index metadata. The question is
whether table/index/column DDL can leave a stale split-policy reference behind.
"""

from __future__ import annotations

import argparse
import dataclasses
import subprocess
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
        raise RuntimeError(f"SHOW CREATE TABLE failed: {combined(res)}")
    if "\t" not in res.out:
        raise RuntimeError(f"unexpected SHOW CREATE output: {res.out}")
    return res.out.split("\t", 1)[1]


def create_split_index_table(args: argparse.Namespace, db: str, table: str = "t") -> None:
    exec_ok(
        args,
        f"""
        CREATE TABLE {quote_ident(table)} (
            id BIGINT PRIMARY KEY,
            a BIGINT,
            b BIGINT,
            INDEX idx_a(a)
        ) SPLIT INDEX idx_a BETWEEN (0) AND (100) REGIONS 3
        """,
        db,
    )


def run_case(args: argparse.Namespace, name: str, fn: Callable[[argparse.Namespace], Outcome]) -> Outcome:
    try:
        return fn(args)
    except Exception as exc:
        return Outcome(name, "finding", f"probe crashed: {exc}")


def case_rename_index_preserves_policy(args: argparse.Namespace) -> Outcome:
    name = "rename_index_preserves_region_split_policy"
    db = "ai_native_region_split_rename"
    try:
        setup_db(args, db)
        create_split_index_table(args, db)
        exec_ok(args, "ALTER TABLE t RENAME INDEX idx_a TO idx_b", db)
        ddl = show_create(args, db, "t")
        if "SPLIT INDEX `idx_b` BETWEEN (0) AND (100) REGIONS 3" not in ddl:
            return Outcome(name, "finding", "renamed index lost split policy: " + ddl)
        if "SPLIT INDEX `idx_a`" in ddl:
            return Outcome(name, "finding", "old index name still appears in split policy: " + ddl)
        return Outcome(name, "ok", "split policy followed the renamed IndexInfo")
    finally:
        cleanup_db(args, db)


def case_drop_index_removes_policy(args: argparse.Namespace) -> Outcome:
    name = "drop_index_removes_region_split_policy"
    db = "ai_native_region_split_drop"
    try:
        setup_db(args, db)
        create_split_index_table(args, db)
        exec_ok(args, "ALTER TABLE t DROP INDEX idx_a", db)
        ddl = show_create(args, db, "t")
        if "region_split" in ddl or "SPLIT INDEX" in ddl:
            return Outcome(name, "finding", "dropped index left split policy in SHOW CREATE: " + ddl)
        return Outcome(name, "ok", "dropping the IndexInfo removed its attached split policy")
    finally:
        cleanup_db(args, db)


def case_drop_add_index_no_policy_leak(args: argparse.Namespace) -> Outcome:
    name = "drop_add_index_does_not_leak_old_policy"
    db = "ai_native_region_split_drop_add"
    try:
        setup_db(args, db)
        create_split_index_table(args, db)
        exec_ok(args, "ALTER TABLE t DROP INDEX idx_a, ADD INDEX idx_c(b)", db)
        ddl = show_create(args, db, "t")
        if "KEY `idx_c` (`b`)" not in ddl:
            return Outcome(name, "finding", "replacement index missing from SHOW CREATE: " + ddl)
        if "region_split" in ddl or "SPLIT INDEX" in ddl:
            return Outcome(name, "finding", "old split policy leaked onto replacement index: " + ddl)
        return Outcome(name, "ok", "old split policy did not leak across drop+add index")
    finally:
        cleanup_db(args, db)


def case_change_column_type_round_trips(args: argparse.Namespace) -> Outcome:
    name = "change_index_column_type_keeps_replayable_policy"
    db = "ai_native_region_split_change_type"
    try:
        setup_db(args, db)
        create_split_index_table(args, db)
        exec_ok(args, "ALTER TABLE t CHANGE COLUMN a aa DATE", db)
        ddl = show_create(args, db, "t")
        if "KEY `idx_a` (`aa`)" not in ddl:
            return Outcome(name, "finding", "index did not follow changed column: " + ddl)
        if "SPLIT INDEX `idx_a` BETWEEN (0) AND (100) REGIONS 3" not in ddl:
            return Outcome(name, "finding", "split policy disappeared after column type/name change: " + ddl)

        round_trip = ddl.replace("CREATE TABLE `t`", "CREATE TABLE `t_rt`", 1)
        exec_ok(args, "DROP TABLE IF EXISTS t_rt", db)
        exec_ok(args, round_trip, db)
        rt_ddl = show_create(args, db, "t_rt")
        if "SPLIT INDEX `idx_a` BETWEEN (0) AND (100) REGIONS 3" not in rt_ddl:
            return Outcome(name, "finding", "round-tripped table lost split policy: " + rt_ddl)
        return Outcome(name, "ok", "column change kept a replayable literal split policy")
    finally:
        cleanup_db(args, db)


def case_cross_schema_rename_table_preserves_policy(args: argparse.Namespace) -> Outcome:
    name = "cross_schema_rename_table_preserves_policy"
    src = "ai_native_region_split_src"
    dst = "ai_native_region_split_dst"
    try:
        setup_db(args, src)
        setup_db(args, dst)
        exec_ok(
            args,
            """
            CREATE TABLE t (
                id BIGINT PRIMARY KEY,
                a BIGINT,
                INDEX idx_a(a)
            ) SPLIT BETWEEN (0) AND (100) REGIONS 3
              SPLIT INDEX idx_a BETWEEN (0) AND (100) REGIONS 3
            """,
            src,
        )
        exec_ok(args, f"RENAME TABLE {quote_ident(src)}.t TO {quote_ident(dst)}.t")
        ddl = show_create(args, dst, "t")
        if "SPLIT BETWEEN (0) AND (100) REGIONS 3" not in ddl:
            return Outcome(name, "finding", "table split policy lost after cross-schema rename: " + ddl)
        if "SPLIT INDEX `idx_a` BETWEEN (0) AND (100) REGIONS 3" not in ddl:
            return Outcome(name, "finding", "index split policy lost after cross-schema rename: " + ddl)
        return Outcome(name, "ok", "table and index split policies moved with TableInfo")
    finally:
        cleanup_db(args, src, dst)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default="mysql")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", default="14000")
    parser.add_argument("--user", default="root")
    args = parser.parse_args()

    cases: list[tuple[str, Callable[[argparse.Namespace], Outcome]]] = [
        ("rename_index_preserves_region_split_policy", case_rename_index_preserves_policy),
        ("drop_index_removes_region_split_policy", case_drop_index_removes_policy),
        ("drop_add_index_does_not_leak_old_policy", case_drop_add_index_no_policy_leak),
        ("change_index_column_type_keeps_replayable_policy", case_change_column_type_round_trips),
        ("cross_schema_rename_table_preserves_policy", case_cross_schema_rename_table_preserves_policy),
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
