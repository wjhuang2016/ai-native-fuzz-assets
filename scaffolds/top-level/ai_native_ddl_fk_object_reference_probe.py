#!/usr/bin/env python3
"""Foreign-key table/index object-reference probe.

This extends the DDL reference-owner matrix beyond FK column rename. The owner
is FK metadata that references a parent table and supporting indexes.
"""

from __future__ import annotations

import argparse
import dataclasses
import os
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
    alter_rc: int
    alter_err: str


@dataclasses.dataclass
class Case:
    name: str
    setup: list[str]
    alter: str
    expected: str
    oracle: Callable[[argparse.Namespace, str], tuple[bool, str]] | None = None
    block_error_contains: tuple[str, ...] = ()


TABLES = ("c_new", "p_new", "c", "p")


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


def quote_db(name: str) -> str:
    return "`" + name.replace("`", "``") + "`"


def rows(res: Result) -> list[str]:
    if res.out == "":
        return []
    return res.out.splitlines()


def combined(res: Result) -> str:
    return (res.err + "\n" + res.out).strip()


def contains_any(text: str, needles: tuple[str, ...]) -> bool:
    lowered = text.lower()
    return any(needle.lower() in lowered for needle in needles)


def show_create(args: argparse.Namespace, db: str, table_name: str) -> str:
    res = run_mysql(args, f"SHOW CREATE TABLE `{table_name}`", db)
    if res.rc != 0:
        return res.err
    return "\n".join(rows(res))


def fk_enforced(args: argparse.Namespace, db: str, child: str) -> tuple[bool, str]:
    bad = run_mysql(args, f"INSERT INTO `{child}` VALUES (999, 999)", db)
    if bad.rc == 0:
        return False, "FK did not reject orphan child row"
    text = combined(bad)
    if "1452" not in text and "foreign key constraint fails" not in text.lower():
        return False, "orphan child row failed with wrong error family: " + text
    good = run_mysql(args, f"INSERT INTO `{child}` VALUES (1000, 1)", db)
    if good.rc != 0:
        return False, "FK rejected valid child row: " + combined(good)
    return True, text


def require_fk_ref(child: str, parent: str, index_name: str | None = None) -> Callable[[argparse.Namespace, str], tuple[bool, str]]:
    def oracle(args: argparse.Namespace, db: str) -> tuple[bool, str]:
        ddl = show_create(args, db, child)
        if f"REFERENCES `{parent}` (`id`)" not in ddl:
            return False, f"FK reference was not rewritten to {parent}: {ddl}"
        if index_name is not None and f"KEY `{index_name}` (`pid`)" not in ddl:
            return False, f"supporting index {index_name} missing after DDL: {ddl}"
        ok, detail = fk_enforced(args, db, child)
        if not ok:
            return False, detail + "; SHOW CREATE=" + ddl
        return True, f"FK references {parent} and enforcement stayed active"

    return oracle


def setup_parent_child() -> list[str]:
    return [
        "CREATE TABLE p(id INT PRIMARY KEY)",
        "CREATE TABLE c(id INT PRIMARY KEY, pid INT, INDEX idx_pid(pid), CONSTRAINT fk_pid FOREIGN KEY(pid) REFERENCES p(id))",
        "INSERT INTO p VALUES (1)",
        "INSERT INTO c VALUES (1,1)",
    ]


def cases() -> list[Case]:
    return [
        Case(
            name="fk_parent_rename_rewrites_child_ref",
            setup=setup_parent_child(),
            alter="RENAME TABLE p TO p_new",
            expected="rewrite",
            oracle=require_fk_ref("c", "p_new", "idx_pid"),
        ),
        Case(
            name="fk_child_rename_preserves_parent_ref",
            setup=setup_parent_child(),
            alter="RENAME TABLE c TO c_new",
            expected="rewrite",
            oracle=require_fk_ref("c_new", "p", "idx_pid"),
        ),
        Case(
            name="fk_multirename_child_then_parent_rewrites",
            setup=setup_parent_child(),
            alter="RENAME TABLE c TO c_new, p TO p_new",
            expected="rewrite",
            oracle=require_fk_ref("c_new", "p_new", "idx_pid"),
        ),
        Case(
            name="fk_multirename_parent_then_child_rewrites",
            setup=setup_parent_child(),
            alter="RENAME TABLE p TO p_new, c TO c_new",
            expected="rewrite",
            oracle=require_fk_ref("c_new", "p_new", "idx_pid"),
        ),
        Case(
            name="fk_drop_parent_table_block",
            setup=setup_parent_child(),
            alter="DROP TABLE p",
            expected="block",
            block_error_contains=("3730", "foreign key", "referenced"),
            oracle=require_fk_ref("c", "p", "idx_pid"),
        ),
        Case(
            name="fk_truncate_parent_table_block",
            setup=setup_parent_child(),
            alter="TRUNCATE TABLE p",
            expected="block",
            block_error_contains=("1701", "foreign key", "referenced"),
            oracle=require_fk_ref("c", "p", "idx_pid"),
        ),
        Case(
            name="fk_drop_parent_supporting_index_block",
            setup=[
                "CREATE TABLE p(id INT PRIMARY KEY, b INT NOT NULL, INDEX idx_b(b))",
                "CREATE TABLE c(id INT PRIMARY KEY, pid INT, INDEX idx_pid(pid), CONSTRAINT fk_pid FOREIGN KEY(pid) REFERENCES p(b))",
                "INSERT INTO p VALUES (1,1)",
                "INSERT INTO c VALUES (1,1)",
            ],
            alter="ALTER TABLE p DROP INDEX idx_b",
            expected="block",
            block_error_contains=("1553", "needed in a foreign key"),
            oracle=lambda args, db: fk_enforced(args, db, "c"),
        ),
        Case(
            name="fk_drop_child_supporting_index_block",
            setup=setup_parent_child(),
            alter="ALTER TABLE c DROP INDEX idx_pid",
            expected="block",
            block_error_contains=("1553", "needed in a foreign key"),
            oracle=require_fk_ref("c", "p", "idx_pid"),
        ),
        Case(
            name="fk_rename_child_supporting_index_preserves_fk",
            setup=setup_parent_child(),
            alter="ALTER TABLE c RENAME INDEX idx_pid TO idx_pid_new",
            expected="rewrite",
            oracle=require_fk_ref("c", "p", "idx_pid_new"),
        ),
        Case(
            name="fk_drop_redundant_child_index_allowed",
            setup=[
                "CREATE TABLE p(id INT PRIMARY KEY)",
                "CREATE TABLE c(id INT PRIMARY KEY, pid INT, INDEX idx_pid(pid), INDEX idx_pid2(pid), CONSTRAINT fk_pid FOREIGN KEY(pid) REFERENCES p(id))",
                "INSERT INTO p VALUES (1)",
                "INSERT INTO c VALUES (1,1)",
            ],
            alter="ALTER TABLE c DROP INDEX idx_pid",
            expected="rewrite",
            oracle=require_fk_ref("c", "p", "idx_pid2"),
        ),
    ]


def cleanup_case(args: argparse.Namespace, db: str) -> None:
    for table_name in TABLES:
        run_mysql(args, f"DROP TABLE IF EXISTS `{table_name}`", db)


def setup_case(args: argparse.Namespace, db: str, case: Case) -> None:
    cleanup_case(args, db)
    for sql in case.setup:
        exec_ok(args, sql, db)


def run_case(args: argparse.Namespace, db: str, case: Case) -> CaseOutcome:
    setup_case(args, db, case)
    alter_res = run_mysql(args, case.alter, db)
    try:
        if case.expected == "block":
            if alter_res.rc == 0:
                return CaseOutcome(case.name, "FINDING", "DDL unexpectedly succeeded; FK object reference may be stale", alter_res.rc, alter_res.err)
            if case.block_error_contains and not contains_any(combined(alter_res), case.block_error_contains):
                return CaseOutcome(case.name, "FINDING", "blocked with unexpected error family: " + combined(alter_res), alter_res.rc, alter_res.err)
            if case.oracle is not None:
                ok, detail = case.oracle(args, db)
                if not ok:
                    return CaseOutcome(case.name, "FINDING", "post-block oracle failed: " + detail, alter_res.rc, alter_res.err)
            return CaseOutcome(case.name, "OK", "blocked by FK owner and original reference still works", alter_res.rc, alter_res.err)
        if case.expected == "rewrite":
            if alter_res.rc != 0:
                return CaseOutcome(case.name, "FINDING", "DDL should rewrite/preserve FK object references but failed: " + combined(alter_res), alter_res.rc, alter_res.err)
            if case.oracle is not None:
                ok, detail = case.oracle(args, db)
                if not ok:
                    return CaseOutcome(case.name, "FINDING", detail, alter_res.rc, alter_res.err)
                return CaseOutcome(case.name, "OK", detail, alter_res.rc, alter_res.err)
            return CaseOutcome(case.name, "OK", "DDL succeeded", alter_res.rc, alter_res.err)
    finally:
        cleanup_case(args, db)
    raise AssertionError(f"unknown expectation {case.expected}")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default=os.environ.get("MYSQL", "mysql"))
    parser.add_argument("--host", default=os.environ.get("MYSQL_HOST", "127.0.0.1"))
    parser.add_argument("--port", default=os.environ.get("MYSQL_PORT", "14000"))
    parser.add_argument("--user", default=os.environ.get("MYSQL_USER", "root"))
    parser.add_argument("--database-prefix", default="ai_native_ddl_fk_obj")
    parser.add_argument("--keep-db", action="store_true")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    suffix = time.strftime("%Y%m%d_%H%M%S")
    db = f"{args.database_prefix}_{suffix}"

    health = run_mysql(args, "SELECT 1")
    if health.rc != 0:
        print(f"cannot connect to TiDB/MySQL at {args.host}:{args.port}: {health.err}", file=sys.stderr)
        return 2

    exec_ok(args, "SET GLOBAL tidb_enable_foreign_key=ON")
    exec_ok(args, f"DROP DATABASE IF EXISTS {quote_db(db)}")
    exec_ok(args, f"CREATE DATABASE {quote_db(db)}")

    outcomes: list[CaseOutcome] = []
    try:
        for case in cases():
            outcome = run_case(args, db, case)
            outcomes.append(outcome)
            print("\t".join([outcome.status, "FK object-reference", case.name, outcome.detail.replace("\n", " ")[:500]]))
    finally:
        cleanup_case(args, db)
        if not args.keep_db:
            run_mysql(args, f"DROP DATABASE IF EXISTS {quote_db(db)}")

    unexpected = [out for out in outcomes if out.status == "FINDING"]
    print(f"SUMMARY total={len(outcomes)} findings={len(unexpected)} skipped=0")
    if unexpected:
        print("UNEXPECTED_FINDINGS")
        for out in unexpected:
            print(f"- {out.name}: {out.detail}; alter_err={out.alter_err}")
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
