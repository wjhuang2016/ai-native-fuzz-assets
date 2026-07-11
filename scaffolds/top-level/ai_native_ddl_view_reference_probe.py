#!/usr/bin/env python3
"""View reference screening probe for the DDL reference-owner selector.

Views look like schema objects that reference base tables and columns, but the
metadata shape is different from FK, masking policy, or sequence defaults:
TiDB stores the view SELECT text. This probe verifies the boundary so view
invalidation is not mistaken for a rewrite/block bug.
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


def setup_clean(args: argparse.Namespace, *dbs: str) -> None:
    for db in dbs:
        run_mysql(args, f"DROP DATABASE IF EXISTS {quote_ident(db)}")
    for db in dbs:
        exec_ok(args, f"CREATE DATABASE {quote_ident(db)}")


def cleanup(args: argparse.Namespace, *dbs: str) -> None:
    for db in dbs:
        run_mysql(args, f"DROP DATABASE IF EXISTS {quote_ident(db)}")


def rows(res: Result) -> list[str]:
    if not res.out:
        return []
    return res.out.splitlines()


def show_create_view(args: argparse.Namespace, db: str, view: str = "v") -> str:
    res = run_mysql(args, f"SHOW CREATE VIEW {quote_ident(view)}", db)
    if res.rc != 0:
        raise RuntimeError("SHOW CREATE VIEW failed: " + combined(res))
    line = res.out
    if "\t" not in line:
        raise RuntimeError("unexpected SHOW CREATE VIEW output: " + line)
    return line.split("\t", 1)[1]


def expect_view_invalid(res: Result) -> bool:
    text = combined(res).lower()
    return res.rc != 0 and (
        "1356" in text
        or "1146" in text
        or "1054" in text
        or "invalid" in text
        or "doesn't exist" in text
        or "unknown column" in text
    )


def create_base_and_view(args: argparse.Namespace, db: str) -> None:
    exec_ok(args, "CREATE TABLE t(a INT, b INT)", db)
    exec_ok(args, "INSERT INTO t VALUES (1, 2)", db)
    exec_ok(args, "CREATE VIEW v AS SELECT a, b FROM t WHERE a > 0", db)


def run_case(args: argparse.Namespace, name: str, fn: Callable[[argparse.Namespace], Outcome]) -> Outcome:
    try:
        return fn(args)
    except Exception as exc:
        return Outcome(name, "finding", f"probe crashed: {exc}")


def case_live_view_control(args: argparse.Namespace) -> Outcome:
    name = "live_view_control"
    db = "ai_native_view_control"
    try:
        setup_clean(args, db)
        create_base_and_view(args, db)
        res = run_mysql(args, "SELECT * FROM v", db)
        if res.rc != 0 or rows(res) != ["1\t2"]:
            return Outcome(name, "finding", "live view did not return base rows: " + combined(res))
        ddl = show_create_view(args, db)
        if "SELECT `a`,`b` FROM `t`" not in ddl.replace(" ", "") and "`t`" not in ddl:
            return Outcome(name, "finding", "SHOW CREATE VIEW did not expose stored SELECT text: " + ddl)
        return Outcome(name, "ok", "live view works and stores SELECT text")
    finally:
        cleanup(args, db)


def case_rename_base_table_invalidates_view(args: argparse.Namespace) -> Outcome:
    name = "rename_base_table_invalidates_view"
    db = "ai_native_view_rename_table"
    try:
        setup_clean(args, db)
        create_base_and_view(args, db)
        exec_ok(args, "RENAME TABLE t TO tt", db)
        ddl = show_create_view(args, db)
        res = run_mysql(args, "SELECT * FROM v", db)
        if not expect_view_invalid(res):
            return Outcome(name, "finding", "view stayed valid or failed unexpectedly after base table rename: " + combined(res))
        if "`t`" not in ddl or "`tt`" in ddl:
            return Outcome(name, "finding", "view SELECT text was rewritten unexpectedly after base table rename: " + ddl)
        return Outcome(name, "ok", "base table rename is allowed; view keeps old SELECT text and becomes invalid")
    finally:
        cleanup(args, db)


def case_rename_base_column_invalidates_view(args: argparse.Namespace) -> Outcome:
    name = "rename_base_column_invalidates_view"
    db = "ai_native_view_rename_column"
    try:
        setup_clean(args, db)
        create_base_and_view(args, db)
        exec_ok(args, "ALTER TABLE t RENAME COLUMN a TO aa", db)
        ddl = show_create_view(args, db)
        res = run_mysql(args, "SELECT * FROM v", db)
        if not expect_view_invalid(res):
            return Outcome(name, "finding", "view stayed valid or failed unexpectedly after base column rename: " + combined(res))
        if "`a`" not in ddl or "`aa`" in ddl:
            return Outcome(name, "finding", "view SELECT text was rewritten unexpectedly after base column rename: " + ddl)
        return Outcome(name, "ok", "base column rename is allowed; view keeps old SELECT text and becomes invalid")
    finally:
        cleanup(args, db)


def case_drop_base_table_invalidates_view(args: argparse.Namespace) -> Outcome:
    name = "drop_base_table_invalidates_view"
    db = "ai_native_view_drop_table"
    try:
        setup_clean(args, db)
        create_base_and_view(args, db)
        exec_ok(args, "DROP TABLE t", db)
        ddl = show_create_view(args, db)
        res = run_mysql(args, "SELECT * FROM v", db)
        if not expect_view_invalid(res):
            return Outcome(name, "finding", "view stayed valid or failed unexpectedly after base table drop: " + combined(res))
        if "`t`" not in ddl:
            return Outcome(name, "finding", "view SELECT text lost old base table after drop: " + ddl)
        return Outcome(name, "ok", "base table drop is allowed; view keeps old SELECT text and becomes invalid")
    finally:
        cleanup(args, db)


def case_drop_cross_db_base_database_invalidates_view(args: argparse.Namespace) -> Outcome:
    name = "drop_cross_db_base_database_invalidates_view"
    base_db = "ai_native_view_base_db"
    view_db = "ai_native_view_holder_db"
    try:
        setup_clean(args, base_db, view_db)
        exec_ok(args, f"CREATE TABLE {quote_ident(base_db)}.t(a INT, b INT)")
        exec_ok(args, f"INSERT INTO {quote_ident(base_db)}.t VALUES (1, 2)")
        exec_ok(args, f"CREATE VIEW {quote_ident(view_db)}.v AS SELECT a, b FROM {quote_ident(base_db)}.t WHERE a > 0")
        exec_ok(args, f"DROP DATABASE {quote_ident(base_db)}")
        ddl = show_create_view(args, view_db)
        res = run_mysql(args, "SELECT * FROM v", view_db)
        if not expect_view_invalid(res):
            return Outcome(name, "finding", "view stayed valid or failed unexpectedly after cross-DB base drop: " + combined(res))
        if base_db not in ddl:
            return Outcome(name, "finding", "view SELECT text lost old cross-DB base name after DROP DATABASE: " + ddl)
        return Outcome(name, "ok", "cross-DB base database drop is allowed; external view remains name-bound and invalid")
    finally:
        cleanup(args, view_db, base_db)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default="mysql")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", default="14000")
    parser.add_argument("--user", default="root")
    args = parser.parse_args()

    cases: list[tuple[str, Callable[[argparse.Namespace], Outcome]]] = [
        ("live_view_control", case_live_view_control),
        ("rename_base_table_invalidates_view", case_rename_base_table_invalidates_view),
        ("rename_base_column_invalidates_view", case_rename_base_column_invalidates_view),
        ("drop_base_table_invalidates_view", case_drop_base_table_invalidates_view),
        ("drop_cross_db_base_database_invalidates_view", case_drop_cross_db_base_database_invalidates_view),
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
