#!/usr/bin/env python3
"""Masking-policy DDL reference-owner probe.

The target owner is mysql.tidb_masking_policy. DDL that changes a table,
database, table ID, column ID/name, or masking expression must rewrite, clean
up, or block the policy metadata instead of leaving a stale reference.
"""

from __future__ import annotations

import argparse
import dataclasses
import subprocess
import sys
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
    alter_rc: int = 0
    alter_err: str = ""


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


def policy_rows(args: argparse.Namespace, where: str) -> list[list[str]]:
    res = run_mysql(
        args,
        "SELECT policy_name, db_name, table_name, table_id, column_name, "
        "column_id, expression, status, masking_type, restrict_on "
        f"FROM mysql.tidb_masking_policy WHERE {where} ORDER BY policy_name",
    )
    if res.rc != 0:
        raise RuntimeError("failed to query masking policy sys table: " + combined(res))
    return [line.split("\t") for line in rows(res)]


def one_policy(args: argparse.Namespace, policy_name: str) -> tuple[bool, list[str] | str]:
    result = policy_rows(args, f"policy_name = {quote_str(policy_name)}")
    if len(result) != 1:
        return False, f"expected one policy named {policy_name}, got {len(result)} rows: {result}"
    return True, result[0]


def policy_count(args: argparse.Namespace, where: str) -> int:
    res = run_mysql(args, f"SELECT COUNT(*) FROM mysql.tidb_masking_policy WHERE {where}")
    if res.rc != 0:
        raise RuntimeError("failed to count masking policies: " + combined(res))
    return int(res.out.strip())


def expression_mentions(expr: str, expected: str, forbidden: str | None = None) -> tuple[bool, str]:
    if f"`{expected}`" not in expr:
        return False, f"expression did not mention rewritten column {expected}: {expr}"
    if forbidden is not None and f"`{forbidden}`" in expr:
        return False, f"expression still mentioned old column {forbidden}: {expr}"
    return True, expr


def setup_db(args: argparse.Namespace, db: str) -> None:
    exec_ok(args, f"DROP DATABASE IF EXISTS {quote_ident(db)}")
    exec_ok(args, f"CREATE DATABASE {quote_ident(db)}")


def cleanup_dbs(args: argparse.Namespace, *dbs: str) -> None:
    for db in dbs:
        exec_ok(args, f"DROP DATABASE IF EXISTS {quote_ident(db)}")


def preflight(args: argparse.Namespace) -> tuple[bool, str]:
    res = run_mysql(args, "SHOW TABLES FROM mysql LIKE 'tidb_masking_policy'")
    if res.rc != 0:
        return False, "cannot query mysql.tidb_masking_policy: " + combined(res)
    if "tidb_masking_policy" not in res.out:
        return False, "mysql.tidb_masking_policy does not exist"

    db = "ai_native_mp_preflight"
    try:
        setup_db(args, db)
        exec_ok(args, "CREATE TABLE t(c VARCHAR(20))", db)
        res = run_mysql(args, "CREATE MASKING POLICY p_preflight ON t(c) AS c ENABLE", db)
        if res.rc != 0:
            return False, "CREATE MASKING POLICY failed: " + combined(res)
    finally:
        cleanup_dbs(args, db)
    return True, "masking policy DDL is available"


def run_simple_case(
    args: argparse.Namespace,
    name: str,
    setup: list[str],
    alter: str,
    expect: str,
    oracle: Callable[[argparse.Namespace, str], tuple[bool, str]],
    block_error_contains: tuple[str, ...] = (),
) -> CaseOutcome:
    db = "ai_native_mp_" + name[:42]
    try:
        setup_db(args, db)
        for sql in setup:
            exec_ok(args, sql, db)
        alter_res = run_mysql(args, alter, db)
        if expect == "block":
            if alter_res.rc == 0:
                ok, detail = oracle(args, db)
                if not ok:
                    detail = "also failed post-oracle: " + detail
                return CaseOutcome(name, "finding", "DDL unexpectedly succeeded; " + detail, alter_res.rc, alter_res.err)
            text = combined(alter_res)
            if block_error_contains and not any(piece.lower() in text.lower() for piece in block_error_contains):
                return CaseOutcome(name, "finding", "blocked with wrong error family: " + text, alter_res.rc, text)
            ok, detail = oracle(args, db)
            if not ok:
                return CaseOutcome(name, "finding", detail, alter_res.rc, text)
            return CaseOutcome(name, "ok", "blocked and policy stayed intact: " + detail, alter_res.rc, text)

        if alter_res.rc != 0:
            return CaseOutcome(name, "finding", "DDL failed unexpectedly: " + combined(alter_res), alter_res.rc, alter_res.err)
        ok, detail = oracle(args, db)
        if not ok:
            return CaseOutcome(name, "finding", detail, alter_res.rc, alter_res.err)
        return CaseOutcome(name, "ok", detail, alter_res.rc, alter_res.err)
    finally:
        cleanup_dbs(args, db)


def case_table_rename(args: argparse.Namespace) -> CaseOutcome:
    def oracle(a: argparse.Namespace, _db: str) -> tuple[bool, str]:
        ok, row = one_policy(a, "p_table_rename")
        if not ok:
            return False, str(row)
        if row[1] != _db or row[2] != "t_new":
            return False, f"policy table reference not rewritten: {row}"
        return True, "table rename rewrote db/table names"

    return run_simple_case(
        args,
        "table_rename_rewrites_policy_table_name",
        [
            "CREATE TABLE t(id INT PRIMARY KEY, c VARCHAR(20))",
            "CREATE MASKING POLICY p_table_rename ON t(c) AS c ENABLE",
        ],
        "RENAME TABLE t TO t_new",
        "rewrite",
        oracle,
    )


def case_multitable_rename(args: argparse.Namespace) -> CaseOutcome:
    def oracle(a: argparse.Namespace, _db: str) -> tuple[bool, str]:
        by_name: dict[str, list[str]] = {}
        for row in policy_rows(a, f"db_name = {quote_str(_db)} AND policy_name IN ('p_a', 'p_b')"):
            by_name[row[0]] = row
        if by_name.get("p_a", [None, None, None])[2] != "a_new":
            return False, f"p_a did not follow table a -> a_new: {by_name}"
        if by_name.get("p_b", [None, None, None])[2] != "b_new":
            return False, f"p_b did not follow table b -> b_new: {by_name}"
        return True, "multi-table rename rewrote both policy references by table ID"

    return run_simple_case(
        args,
        "multitable_rename_rewrites_each_policy",
        [
            "CREATE TABLE a(c VARCHAR(20))",
            "CREATE TABLE b(c VARCHAR(20))",
            "CREATE MASKING POLICY p_a ON a(c) AS c ENABLE",
            "CREATE MASKING POLICY p_b ON b(c) AS c ENABLE",
        ],
        "RENAME TABLE a TO a_new, b TO b_new",
        "rewrite",
        oracle,
    )


def case_cross_db_rename(args: argparse.Namespace) -> CaseOutcome:
    db1 = "ai_native_mp_cross_db_1"
    db2 = "ai_native_mp_cross_db_2"
    try:
        cleanup_dbs(args, db1, db2)
        exec_ok(args, f"CREATE DATABASE {quote_ident(db1)}")
        exec_ok(args, f"CREATE DATABASE {quote_ident(db2)}")
        exec_ok(args, "CREATE TABLE t(c VARCHAR(20))", db1)
        exec_ok(args, "CREATE MASKING POLICY p_cross_db ON t(c) AS c ENABLE", db1)
        alter_res = run_mysql(args, f"RENAME TABLE {quote_ident(db1)}.t TO {quote_ident(db2)}.t")
        if alter_res.rc != 0:
            return CaseOutcome("cross_db_rename_rewrites_policy_db_name", "finding", combined(alter_res), alter_res.rc, alter_res.err)
        ok, row = one_policy(args, "p_cross_db")
        if not ok:
            return CaseOutcome("cross_db_rename_rewrites_policy_db_name", "finding", str(row), alter_res.rc, alter_res.err)
        if row[1] != db2 or row[2] != "t":
            return CaseOutcome("cross_db_rename_rewrites_policy_db_name", "finding", f"policy did not move to {db2}.t: {row}", alter_res.rc, alter_res.err)
        return CaseOutcome("cross_db_rename_rewrites_policy_db_name", "ok", "cross-DB rename rewrote db/table names", alter_res.rc, alter_res.err)
    finally:
        cleanup_dbs(args, db1, db2)


def case_column_rename(args: argparse.Namespace) -> CaseOutcome:
    def oracle(a: argparse.Namespace, _db: str) -> tuple[bool, str]:
        ok, row = one_policy(a, "p_col_rename")
        if not ok:
            return False, str(row)
        if row[4] != "c_new":
            return False, f"policy column_name not rewritten: {row}"
        expr_ok, expr_detail = expression_mentions(row[6], "c_new", "c")
        if not expr_ok:
            return False, expr_detail
        return True, "column rename rewrote column_name and expression"

    return run_simple_case(
        args,
        "column_rename_rewrites_name_and_expression",
        [
            "CREATE TABLE t(id INT PRIMARY KEY, c VARCHAR(20))",
            "CREATE MASKING POLICY p_col_rename ON t(c) AS concat(c, '_x') ENABLE",
        ],
        "ALTER TABLE t RENAME COLUMN c TO c_new",
        "rewrite",
        oracle,
    )


def case_change_column_multischema(args: argparse.Namespace) -> CaseOutcome:
    def oracle(a: argparse.Namespace, _db: str) -> tuple[bool, str]:
        ok, row = one_policy(a, "p_change")
        if not ok:
            return False, str(row)
        if row[4] != "c_new":
            return False, f"policy column_name not rewritten after CHANGE COLUMN: {row}"
        expr_ok, expr_detail = expression_mentions(row[6], "c_new", "c")
        if not expr_ok:
            return False, expr_detail
        return True, "multi-schema CHANGE COLUMN rewrote column_name and expression"

    return run_simple_case(
        args,
        "change_column_multischema_rewrites_expression",
        [
            "CREATE TABLE t(id INT PRIMARY KEY, c VARCHAR(20))",
            "CREATE MASKING POLICY p_change ON t(c) AS concat(c, '_x') ENABLE",
        ],
        "ALTER TABLE t CHANGE COLUMN c c_new VARCHAR(40), ADD COLUMN d INT",
        "rewrite",
        oracle,
    )


def case_modify_supported(args: argparse.Namespace) -> CaseOutcome:
    def oracle(a: argparse.Namespace, _db: str) -> tuple[bool, str]:
        ok, row = one_policy(a, "p_modify_ok")
        if not ok:
            return False, str(row)
        if row[4] != "c" or "`c`" not in row[6]:
            return False, f"policy changed unexpectedly after supported MODIFY: {row}"
        return True, "supported MODIFY COLUMN preserved policy binding"

    return run_simple_case(
        args,
        "modify_supported_type_preserves_policy",
        [
            "CREATE TABLE t(id INT PRIMARY KEY, c VARCHAR(20))",
            "CREATE MASKING POLICY p_modify_ok ON t(c) AS concat(c, '_x') ENABLE",
        ],
        "ALTER TABLE t MODIFY COLUMN c VARCHAR(40)",
        "rewrite",
        oracle,
    )


def case_modify_unsupported_block(args: argparse.Namespace) -> CaseOutcome:
    def oracle(a: argparse.Namespace, _db: str) -> tuple[bool, str]:
        ok, row = one_policy(a, "p_modify_block")
        if not ok:
            return False, str(row)
        if row[4] != "c" or row[6] != "`c`":
            return False, f"policy was changed by failed MODIFY: {row}"
        return True, "policy still points at c"

    return run_simple_case(
        args,
        "modify_unsupported_type_blocks",
        [
            "CREATE TABLE t(id INT PRIMARY KEY, c VARCHAR(20))",
            "CREATE MASKING POLICY p_modify_block ON t(c) AS c ENABLE",
        ],
        "ALTER TABLE t MODIFY COLUMN c JSON",
        "block",
        oracle,
        ("unsupported", "8200"),
    )


def case_drop_column_cleanup(args: argparse.Namespace) -> CaseOutcome:
    def oracle(a: argparse.Namespace, _db: str) -> tuple[bool, str]:
        count = policy_count(a, f"db_name = {quote_str(_db)}")
        if count != 0:
            return False, f"drop column left {count} masking policies behind"
        return True, "drop column cleaned the policy row"

    return run_simple_case(
        args,
        "drop_column_cleans_policy",
        [
            "CREATE TABLE t(id INT PRIMARY KEY, c VARCHAR(20), keep_col INT)",
            "CREATE MASKING POLICY p_drop_col ON t(c) AS c ENABLE",
        ],
        "ALTER TABLE t DROP COLUMN c",
        "cleanup",
        oracle,
    )


def case_drop_column_multischema_cleanup(args: argparse.Namespace) -> CaseOutcome:
    def oracle(a: argparse.Namespace, _db: str) -> tuple[bool, str]:
        count = policy_count(a, f"db_name = {quote_str(_db)}")
        if count != 0:
            return False, f"multi-schema drop column left {count} masking policies behind"
        return True, "multi-schema drop column cleaned the policy row"

    return run_simple_case(
        args,
        "drop_column_multischema_cleans_policy",
        [
            "CREATE TABLE t(id INT PRIMARY KEY, c VARCHAR(20), keep_col INT)",
            "CREATE MASKING POLICY p_drop_col_multi ON t(c) AS c ENABLE",
        ],
        "ALTER TABLE t DROP COLUMN c, ADD COLUMN d INT",
        "cleanup",
        oracle,
    )


def case_truncate(args: argparse.Namespace) -> CaseOutcome:
    old_table_id = ""

    def oracle(a: argparse.Namespace, db: str) -> tuple[bool, str]:
        ok, row = one_policy(a, "p_truncate")
        if not ok:
            return False, str(row)
        new_table_id = row[3]
        if new_table_id == old_table_id:
            return False, f"truncate did not rewrite table_id: old={old_table_id} row={row}"
        if row[1] != db or row[2] != "t" or row[4] != "c" or row[6] != "`c`":
            return False, f"truncate changed non-table-id policy fields: {row}"
        disable = run_mysql(a, "ALTER TABLE t DISABLE MASKING POLICY p_truncate", db)
        if disable.rc != 0:
            return False, "policy could not be operated after truncate: " + combined(disable)
        return True, "truncate rewrote table_id and policy stayed operable"

    db = "ai_native_mp_truncate_rewrites_table_id"
    try:
        setup_db(args, db)
        exec_ok(args, "CREATE TABLE t(id INT PRIMARY KEY, c VARCHAR(20))", db)
        exec_ok(args, "CREATE MASKING POLICY p_truncate ON t(c) AS c ENABLE", db)
        ok, row = one_policy(args, "p_truncate")
        if not ok:
            return CaseOutcome("truncate_rewrites_table_id", "finding", str(row))
        old_table_id = row[3]
        alter_res = run_mysql(args, "TRUNCATE TABLE t", db)
        if alter_res.rc != 0:
            return CaseOutcome("truncate_rewrites_table_id", "finding", combined(alter_res), alter_res.rc, alter_res.err)
        ok, detail = oracle(args, db)
        if not ok:
            return CaseOutcome("truncate_rewrites_table_id", "finding", detail, alter_res.rc, alter_res.err)
        return CaseOutcome("truncate_rewrites_table_id", "ok", detail, alter_res.rc, alter_res.err)
    finally:
        cleanup_dbs(args, db)


def case_drop_table_cleanup(args: argparse.Namespace) -> CaseOutcome:
    def oracle(a: argparse.Namespace, _db: str) -> tuple[bool, str]:
        count = policy_count(a, f"policy_name = 'p_drop_table'")
        if count != 0:
            return False, f"drop table left {count} policy rows"
        return True, "drop table cleaned policy rows"

    return run_simple_case(
        args,
        "drop_table_cleans_policy",
        [
            "CREATE TABLE t(c VARCHAR(20))",
            "CREATE MASKING POLICY p_drop_table ON t(c) AS c ENABLE",
        ],
        "DROP TABLE t",
        "cleanup",
        oracle,
    )


def case_drop_database_cleanup(args: argparse.Namespace) -> CaseOutcome:
    db = "ai_native_mp_drop_database_cleanup"
    try:
        setup_db(args, db)
        exec_ok(args, "CREATE TABLE t1(c VARCHAR(20))", db)
        exec_ok(args, "CREATE TABLE t2(c VARCHAR(20))", db)
        exec_ok(args, "CREATE MASKING POLICY p_drop_db_1 ON t1(c) AS c ENABLE", db)
        exec_ok(args, "CREATE MASKING POLICY p_drop_db_2 ON t2(c) AS c ENABLE", db)
        alter_res = run_mysql(args, f"DROP DATABASE {quote_ident(db)}")
        if alter_res.rc != 0:
            return CaseOutcome("drop_database_cleans_policies", "finding", combined(alter_res), alter_res.rc, alter_res.err)
        count = policy_count(args, "policy_name IN ('p_drop_db_1', 'p_drop_db_2')")
        if count != 0:
            return CaseOutcome("drop_database_cleans_policies", "finding", f"drop database left {count} policy rows", alter_res.rc, alter_res.err)
        return CaseOutcome("drop_database_cleans_policies", "ok", "drop database cleaned all policy rows", alter_res.rc, alter_res.err)
    finally:
        cleanup_dbs(args, db)


def case_alter_policy_expr_block(args: argparse.Namespace) -> CaseOutcome:
    def oracle(a: argparse.Namespace, _db: str) -> tuple[bool, str]:
        ok, row = one_policy(a, "p_expr_block")
        if not ok:
            return False, str(row)
        if row[4] != "a" or row[6] != "`a`":
            return False, f"failed MODIFY MASKING POLICY changed the row: {row}"
        return True, "policy expression stayed on target column"

    return run_simple_case(
        args,
        "alter_policy_expr_non_target_blocks",
        [
            "CREATE TABLE t(a VARCHAR(20), b VARCHAR(20))",
            "CREATE MASKING POLICY p_expr_block ON t(a) AS a ENABLE",
        ],
        "ALTER TABLE t MODIFY MASKING POLICY p_expr_block SET EXPRESSION = b",
        "block",
        oracle,
        ("masking policy", "invalid column", "8247"),
    )


def cases() -> list[Callable[[argparse.Namespace], CaseOutcome]]:
    return [
        case_table_rename,
        case_multitable_rename,
        case_cross_db_rename,
        case_column_rename,
        case_change_column_multischema,
        case_modify_supported,
        case_modify_unsupported_block,
        case_drop_column_cleanup,
        case_drop_column_multischema_cleanup,
        case_truncate,
        case_drop_table_cleanup,
        case_drop_database_cleanup,
        case_alter_policy_expr_block,
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
        print(f"SKIP masking-policy object-reference preflight: {detail}")
        print("SUMMARY total=0 findings=0 skipped=1")
        return 0

    outcomes: list[CaseOutcome] = []
    for case in cases():
        try:
            outcome = case(args)
        except Exception as exc:  # noqa: BLE001 - probe should keep reporting all cases.
            outcome = CaseOutcome(case.__name__, "finding", f"unhandled probe error: {exc}")
        outcomes.append(outcome)
        prefix = "OK" if outcome.status == "ok" else "FINDING"
        print(f"{prefix} masking-policy object-reference {outcome.name} {outcome.detail}")
        sys.stdout.flush()

    findings = sum(1 for item in outcomes if item.status == "finding")
    print(f"SUMMARY total={len(outcomes)} findings={findings} skipped=0")
    return 1 if findings else 0


if __name__ == "__main__":
    raise SystemExit(main())
