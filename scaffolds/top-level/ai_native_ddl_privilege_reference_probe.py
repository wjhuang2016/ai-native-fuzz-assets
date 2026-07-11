#!/usr/bin/env python3
"""Privilege side-metadata DDL owner screening probe.

The question is whether mysql.tables_priv / mysql.columns_priv should be
treated as DDL-owned object references. A true object reference should rewrite
or block when a table/column is renamed. Privileges are suspicious because they
are stored by (DB, Table_name, Column_name) and GRANT is allowed to describe
name-based policy.

This probe keeps the oracle DDL-only: after a DDL changes an object, check
whether the privilege follows object identity or the textual name.
"""

from __future__ import annotations

import argparse
import dataclasses
import subprocess


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


def mysql_args(args: argparse.Namespace, user: str | None = None, database: str | None = None) -> list[str]:
    cmd = [
        args.mysql,
        f"-h{args.host}",
        f"-P{args.port}",
        f"-u{user or args.user}",
        "--batch",
        "--raw",
        "--skip-column-names",
        "--connect-timeout=5",
    ]
    if database:
        cmd.append(database)
    return cmd


def run_mysql(
    args: argparse.Namespace,
    sql: str,
    database: str | None = None,
    user: str | None = None,
) -> Result:
    proc = subprocess.run(
        mysql_args(args, user=user, database=database) + ["-e", sql],
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    return Result(proc.returncode, proc.stdout.rstrip("\n"), proc.stderr.rstrip("\n"))


def exec_ok(args: argparse.Namespace, sql: str, database: str | None = None) -> None:
    res = run_mysql(args, sql, database)
    if res.rc != 0:
        raise RuntimeError(f"SQL failed rc={res.rc}: {sql}\n{combined(res)}")


def quote_ident(name: str) -> str:
    return "`" + name.replace("`", "``") + "`"


def quote_str(value: str) -> str:
    return "'" + value.replace("\\", "\\\\").replace("'", "''") + "'"


def account(user: str, host: str = "%") -> str:
    return f"{quote_str(user)}@{quote_str(host)}"


def combined(res: Result) -> str:
    return (res.err + "\n" + res.out).strip()


def rows(res: Result) -> list[str]:
    if not res.out:
        return []
    return res.out.splitlines()


def setup_db(args: argparse.Namespace, db: str) -> None:
    exec_ok(args, f"DROP DATABASE IF EXISTS {quote_ident(db)}")
    exec_ok(args, f"CREATE DATABASE {quote_ident(db)}")


def cleanup(args: argparse.Namespace, db: str, user: str) -> None:
    exec_ok(args, f"DROP DATABASE IF EXISTS {quote_ident(db)}")
    exec_ok(args, f"DROP USER IF EXISTS {account(user)}")


def reset_user(args: argparse.Namespace, user: str) -> None:
    exec_ok(args, f"DROP USER IF EXISTS {account(user)}")
    exec_ok(args, f"CREATE USER {account(user)} IDENTIFIED BY ''")


def expect_user_success(args: argparse.Namespace, user: str, sql: str) -> tuple[bool, str]:
    res = run_mysql(args, sql, user=user)
    if res.rc != 0:
        return False, "expected user SQL to succeed, got: " + combined(res)
    return True, res.out


def expect_user_denied(args: argparse.Namespace, user: str, sql: str) -> tuple[bool, str]:
    res = run_mysql(args, sql, user=user)
    if res.rc == 0:
        return False, "expected privilege denial, but SQL succeeded with: " + res.out
    text = combined(res).lower()
    if "denied" not in text and "command" not in text:
        return False, "expected privilege denial, got different error: " + combined(res)
    return True, combined(res)


def table_priv_rows(args: argparse.Namespace, db: str, user: str) -> list[str]:
    res = run_mysql(
        args,
        "SELECT DB, Table_name, Table_priv, Column_priv "
        "FROM mysql.tables_priv "
        f"WHERE User={quote_str(user)} AND Host='%' AND DB={quote_str(db)} "
        "ORDER BY Table_name",
    )
    if res.rc != 0:
        raise RuntimeError("failed to query mysql.tables_priv: " + combined(res))
    return rows(res)


def column_priv_rows(args: argparse.Namespace, db: str, user: str) -> list[str]:
    res = run_mysql(
        args,
        "SELECT DB, Table_name, Column_name, Column_priv "
        "FROM mysql.columns_priv "
        f"WHERE User={quote_str(user)} AND Host='%' AND DB={quote_str(db)} "
        "ORDER BY Table_name, Column_name",
    )
    if res.rc != 0:
        raise RuntimeError("failed to query mysql.columns_priv: " + combined(res))
    return rows(res)


def show_grants(args: argparse.Namespace, user: str) -> list[str]:
    res = run_mysql(args, f"SHOW GRANTS FOR {account(user)}")
    if res.rc != 0:
        raise RuntimeError("failed to SHOW GRANTS: " + combined(res))
    return rows(res)


def case_table_rename_name_policy(args: argparse.Namespace) -> CaseOutcome:
    name = "table_privilege_rename_tracks_name_not_object"
    db = "ai_native_priv_table_rename"
    user = "ai_priv_table_ref"
    try:
        cleanup(args, db, user)
        setup_db(args, db)
        reset_user(args, user)
        exec_ok(args, "CREATE TABLE t(id INT PRIMARY KEY, v INT)", db)
        exec_ok(args, "INSERT INTO t VALUES (1, 10)", db)
        exec_ok(args, f"GRANT SELECT ON {quote_ident(db)}.t TO {account(user)}")
        exec_ok(args, "FLUSH PRIVILEGES")
        ok, detail = expect_user_success(args, user, f"SELECT COUNT(*) FROM {quote_ident(db)}.t")
        if not ok:
            return CaseOutcome(name, "finding", detail)

        exec_ok(args, f"RENAME TABLE {quote_ident(db)}.t TO {quote_ident(db)}.t2")
        exec_ok(args, "FLUSH PRIVILEGES")
        priv_rows = table_priv_rows(args, db, user)
        grants = show_grants(args, user)
        if not any("\tt\t" in row for row in priv_rows):
            return CaseOutcome(name, "finding", f"table privilege did not remain on old name: {priv_rows}")
        if any("\tt2\t" in row for row in priv_rows) or any(".t2" in grant for grant in grants):
            return CaseOutcome(name, "finding", f"table privilege followed renamed object: rows={priv_rows}, grants={grants}")
        ok, detail = expect_user_denied(args, user, f"SELECT COUNT(*) FROM {quote_ident(db)}.t2")
        if not ok:
            return CaseOutcome(name, "finding", detail)

        exec_ok(args, f"RENAME TABLE {quote_ident(db)}.t2 TO {quote_ident(db)}.t")
        ok, detail = expect_user_success(args, user, f"SELECT COUNT(*) FROM {quote_ident(db)}.t")
        if not ok:
            return CaseOutcome(name, "finding", "old-name grant did not reattach after renaming back: " + detail)
        return CaseOutcome(name, "ok", "table grant stayed on textual table name and reattached when that name returned")
    finally:
        cleanup(args, db, user)


def case_column_rename_name_policy(args: argparse.Namespace) -> CaseOutcome:
    name = "column_privilege_rename_tracks_name_not_object"
    db = "ai_native_priv_col_rename"
    user = "ai_priv_col_ref"
    try:
        cleanup(args, db, user)
        setup_db(args, db)
        reset_user(args, user)
        exec_ok(args, "CREATE TABLE t(id INT PRIMARY KEY, a INT, b INT)", db)
        exec_ok(args, "INSERT INTO t VALUES (1, 10, 20)", db)
        exec_ok(args, f"GRANT SELECT(a) ON {quote_ident(db)}.t TO {account(user)}")
        exec_ok(args, "FLUSH PRIVILEGES")
        initial_priv_rows = column_priv_rows(args, db, user)
        initial_grants = show_grants(args, user)
        if not any("\ta\t" in row for row in initial_priv_rows):
            return CaseOutcome(name, "finding", f"column privilege was not stored on the granted name: {initial_priv_rows}")
        if not any("(a)" in grant for grant in initial_grants):
            return CaseOutcome(name, "finding", f"SHOW GRANTS did not expose the column grant: {initial_grants}")

        exec_ok(args, f"ALTER TABLE {quote_ident(db)}.t RENAME COLUMN a TO aa")
        exec_ok(args, "FLUSH PRIVILEGES")
        priv_rows = column_priv_rows(args, db, user)
        grants = show_grants(args, user)
        if not any("\ta\t" in row for row in priv_rows):
            return CaseOutcome(name, "finding", f"column privilege did not remain on old name: {priv_rows}")
        if any("\taa\t" in row for row in priv_rows) or any("(aa)" in grant for grant in grants):
            return CaseOutcome(name, "finding", f"column privilege followed renamed column: rows={priv_rows}, grants={grants}")

        exec_ok(args, f"ALTER TABLE {quote_ident(db)}.t ADD COLUMN a INT DEFAULT 99")
        final_priv_rows = column_priv_rows(args, db, user)
        final_grants = show_grants(args, user)
        if not any("\ta\t" in row for row in final_priv_rows):
            return CaseOutcome(name, "finding", f"old-name column grant disappeared after adding replacement column: {final_priv_rows}")
        if any("\taa\t" in row for row in final_priv_rows) or any("(aa)" in grant for grant in final_grants):
            return CaseOutcome(name, "finding", f"old-name column grant leaked to renamed column: rows={final_priv_rows}, grants={final_grants}")
        return CaseOutcome(name, "ok", "column grant metadata stayed on textual column name across rename and replacement")
    finally:
        cleanup(args, db, user)


def case_drop_recreate_table_name_policy(args: argparse.Namespace) -> CaseOutcome:
    name = "table_privilege_drop_recreate_rebinds_same_name"
    db = "ai_native_priv_drop_recreate"
    user = "ai_priv_recreate_ref"
    try:
        cleanup(args, db, user)
        setup_db(args, db)
        reset_user(args, user)
        exec_ok(args, "CREATE TABLE t(id INT PRIMARY KEY, v INT)", db)
        exec_ok(args, f"GRANT SELECT ON {quote_ident(db)}.t TO {account(user)}")
        exec_ok(args, "DROP TABLE t", db)
        exec_ok(args, "CREATE TABLE t(id INT PRIMARY KEY, secret INT)", db)
        exec_ok(args, "INSERT INTO t VALUES (1, 42)", db)
        exec_ok(args, "FLUSH PRIVILEGES")
        ok, detail = expect_user_success(args, user, f"SELECT COUNT(*) FROM {quote_ident(db)}.t")
        if not ok:
            return CaseOutcome(name, "finding", "name grant did not rebind after drop/recreate: " + detail)
        priv_rows = table_priv_rows(args, db, user)
        if not any("\tt\t" in row for row in priv_rows):
            return CaseOutcome(name, "finding", f"table privilege disappeared after drop/recreate: {priv_rows}")
        return CaseOutcome(name, "ok", "table grant survived DROP TABLE and applied to the new object with the same name")
    finally:
        cleanup(args, db, user)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default="mysql")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", default="14000")
    parser.add_argument("--user", default="root")
    args = parser.parse_args()

    cases = [
        case_table_rename_name_policy,
        case_column_rename_name_policy,
        case_drop_recreate_table_name_policy,
    ]
    outcomes: list[CaseOutcome] = []
    for case in cases:
        try:
            outcome = case(args)
        except Exception as exc:
            outcome = CaseOutcome(case.__name__, "finding", f"probe crashed: {exc}")
        outcomes.append(outcome)
        print(f"{outcome.status.upper()}\t{outcome.name}\t{outcome.detail}")

    findings = sum(1 for outcome in outcomes if outcome.status == "finding")
    print(f"SUMMARY total={len(outcomes)} findings={findings} skipped=0")
    return 1 if findings else 0


if __name__ == "__main__":
    raise SystemExit(main())
