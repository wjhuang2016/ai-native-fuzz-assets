#!/usr/bin/env python3
"""Functional-index DDL reference-owner screening probe.

The proof obligation is:

    if a DDL changes a column referenced by a functional index expression, the
    dependency must be blocked unless the functional index owner is removed.

Functional indexes are implemented through hidden generated columns. This probe
keeps the matrix small: it checks the block owner, the cleanup owner, and the
multi-schema behavior where "DROP INDEX + change referenced column" is written
in one statement.
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


def create_func_index_table(args: argparse.Namespace, db: str) -> None:
    exec_ok(args, "CREATE TABLE t(a INT, b INT, INDEX idx_expr ((a + 1)))", db)


def show_create(args: argparse.Namespace, db: str, table: str = "t") -> str:
    res = run_mysql(args, f"SHOW CREATE TABLE {quote_ident(table)}", db)
    if res.rc != 0:
        raise RuntimeError("SHOW CREATE failed: " + combined(res))
    if "\t" not in res.out:
        raise RuntimeError("unexpected SHOW CREATE output: " + res.out)
    return res.out.split("\t", 1)[1]


def expect_functional_dependency_error(res: Result) -> bool:
    text = combined(res)
    return res.rc != 0 and "3837" in text and "expression index dependency" in text


def schema_has_original_func_index(args: argparse.Namespace, db: str) -> bool:
    ddl = show_create(args, db)
    return "`a` int" in ddl and "KEY `idx_expr` ((`a` + 1))" in ddl and "`aa` int" not in ddl


def run_case(args: argparse.Namespace, name: str, fn: Callable[[argparse.Namespace], Outcome]) -> Outcome:
    try:
        return fn(args)
    except Exception as exc:
        return Outcome(name, "finding", f"probe crashed: {exc}")


def case_visible_control(args: argparse.Namespace) -> Outcome:
    name = "functional_index_visible_control"
    db = "ai_native_ddl_funcidx_control"
    try:
        setup_db(args, db)
        create_func_index_table(args, db)
        ddl = show_create(args, db)
        if "KEY `idx_expr` ((`a` + 1))" not in ddl:
            return Outcome(name, "finding", "SHOW CREATE did not expose functional index: " + ddl)
        return Outcome(name, "ok", "functional index expression is visible in SHOW CREATE")
    finally:
        cleanup_db(args, db)


def case_column_alter_blocks(args: argparse.Namespace) -> Outcome:
    name = "functional_index_blocks_column_alter"
    db = "ai_native_ddl_funcidx_block"
    checks = [
        ("rename", "ALTER TABLE t RENAME COLUMN a TO aa"),
        ("change", "ALTER TABLE t CHANGE COLUMN a aa INT"),
        ("modify", "ALTER TABLE t MODIFY COLUMN a BIGINT"),
        ("drop", "ALTER TABLE t DROP COLUMN a"),
    ]
    try:
        setup_db(args, db)
        for label, sql in checks:
            exec_ok(args, "DROP TABLE IF EXISTS t", db)
            create_func_index_table(args, db)
            res = run_mysql(args, sql, db)
            if not expect_functional_dependency_error(res):
                return Outcome(name, "finding", f"{label} was not blocked by functional-index owner: {combined(res)}")
            if not schema_has_original_func_index(args, db):
                return Outcome(name, "finding", f"{label} changed schema despite block: {show_create(args, db)}")
        return Outcome(name, "ok", "rename/change/modify/drop referenced column all block with 3837 and preserve schema")
    finally:
        cleanup_db(args, db)


def case_drop_index_releases_dependency(args: argparse.Namespace) -> Outcome:
    name = "drop_functional_index_releases_column_dependency"
    db = "ai_native_ddl_funcidx_drop_index"
    try:
        setup_db(args, db)
        create_func_index_table(args, db)
        exec_ok(args, "ALTER TABLE t DROP INDEX idx_expr", db)
        ddl_after_drop = show_create(args, db)
        if "idx_expr" in ddl_after_drop or "(`a` + 1)" in ddl_after_drop:
            return Outcome(name, "finding", "DROP INDEX left expression in SHOW CREATE: " + ddl_after_drop)
        exec_ok(args, "ALTER TABLE t RENAME COLUMN a TO aa", db)
        ddl_after_rename = show_create(args, db)
        if "`aa` int" not in ddl_after_rename or "`a` int" in ddl_after_rename or "idx_expr" in ddl_after_rename:
            return Outcome(name, "finding", "column rename after DROP INDEX did not produce clean schema: " + ddl_after_rename)
        return Outcome(name, "ok", "DROP INDEX removed functional dependency; subsequent rename succeeded")
    finally:
        cleanup_db(args, db)


def case_multi_schema_drop_index_and_rename_blocks(args: argparse.Namespace) -> Outcome:
    name = "multi_schema_drop_index_and_rename_blocks"
    db = "ai_native_ddl_funcidx_multi_rename"
    checks = [
        ("drop_then_rename", "ALTER TABLE t DROP INDEX idx_expr, RENAME COLUMN a TO aa"),
        ("rename_then_drop", "ALTER TABLE t RENAME COLUMN a TO aa, DROP INDEX idx_expr"),
    ]
    try:
        setup_db(args, db)
        for label, sql in checks:
            exec_ok(args, "DROP TABLE IF EXISTS t", db)
            create_func_index_table(args, db)
            res = run_mysql(args, sql, db)
            if not expect_functional_dependency_error(res):
                return Outcome(name, "finding", f"{label} did not block with functional-index error: {combined(res)}")
            if not schema_has_original_func_index(args, db):
                return Outcome(name, "finding", f"{label} changed schema despite block: {show_create(args, db)}")
        return Outcome(name, "ok", "both multi-schema orders block against original functional-index dependency")
    finally:
        cleanup_db(args, db)


def case_multi_schema_drop_index_and_drop_column_blocks(args: argparse.Namespace) -> Outcome:
    name = "multi_schema_drop_index_and_drop_column_blocks"
    db = "ai_native_ddl_funcidx_multi_drop"
    checks = [
        ("drop_index_then_drop_col", "ALTER TABLE t DROP INDEX idx_expr, DROP COLUMN a"),
        ("drop_col_then_drop_index", "ALTER TABLE t DROP COLUMN a, DROP INDEX idx_expr"),
    ]
    try:
        setup_db(args, db)
        for label, sql in checks:
            exec_ok(args, "DROP TABLE IF EXISTS t", db)
            create_func_index_table(args, db)
            res = run_mysql(args, sql, db)
            if not expect_functional_dependency_error(res):
                return Outcome(name, "finding", f"{label} did not block with functional-index error: {combined(res)}")
            if not schema_has_original_func_index(args, db):
                return Outcome(name, "finding", f"{label} changed schema despite block: {show_create(args, db)}")
        return Outcome(name, "ok", "drop-index+drop-column multi-schema orders block and preserve schema")
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
        ("functional_index_visible_control", case_visible_control),
        ("functional_index_blocks_column_alter", case_column_alter_blocks),
        ("drop_functional_index_releases_column_dependency", case_drop_index_releases_dependency),
        ("multi_schema_drop_index_and_rename_blocks", case_multi_schema_drop_index_and_rename_blocks),
        ("multi_schema_drop_index_and_drop_column_blocks", case_multi_schema_drop_index_and_drop_column_blocks),
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
