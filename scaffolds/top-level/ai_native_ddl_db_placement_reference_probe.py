#!/usr/bin/env python3
"""DB-level placement-policy DDL reference-owner probe.

The proof obligation is:

    if a database or a table metadata reference points at a placement policy,
    DDL that drops, rewrites, or releases that policy reference must either
    block or update the owner-visible metadata consistently.

This is intentionally a small matrix. Ordinary table/partition placement paths
are already covered elsewhere; this probe checks the database-level owner and
the boundary where newly-created tables inherit the database default.
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


def rows(res: Result) -> list[str]:
    if not res.out:
        return []
    return res.out.splitlines()


def show_create_database(args: argparse.Namespace, db: str) -> str:
    res = run_mysql(args, f"SHOW CREATE DATABASE {quote_ident(db)}")
    if res.rc != 0:
        raise RuntimeError("SHOW CREATE DATABASE failed: " + combined(res))
    line = res.out
    if "\t" not in line:
        raise RuntimeError("unexpected SHOW CREATE DATABASE output: " + line)
    return line.split("\t", 1)[1]


def show_create_table(args: argparse.Namespace, db: str, table: str) -> str:
    res = run_mysql(args, f"SHOW CREATE TABLE {quote_ident(table)}", db)
    if res.rc != 0:
        raise RuntimeError("SHOW CREATE TABLE failed: " + combined(res))
    line = res.out
    if "\t" not in line:
        raise RuntimeError("unexpected SHOW CREATE TABLE output: " + line)
    return line.split("\t", 1)[1]


def setup_clean(args: argparse.Namespace, dbs: list[str], policies: list[str]) -> None:
    for db in dbs:
        run_mysql(args, f"DROP DATABASE IF EXISTS {quote_ident(db)}")
    for policy in policies:
        run_mysql(args, f"DROP PLACEMENT POLICY IF EXISTS {quote_ident(policy)}")


def cleanup(args: argparse.Namespace, dbs: list[str], policies: list[str]) -> None:
    for db in dbs:
        run_mysql(args, f"DROP DATABASE IF EXISTS {quote_ident(db)}")
    for policy in policies:
        run_mysql(args, f"DROP PLACEMENT POLICY IF EXISTS {quote_ident(policy)}")


def create_policy(args: argparse.Namespace, policy: str, followers: int) -> None:
    exec_ok(args, f"CREATE PLACEMENT POLICY {quote_ident(policy)} FOLLOWERS={followers}")


def drop_policy(args: argparse.Namespace, policy: str) -> Result:
    return run_mysql(args, f"DROP PLACEMENT POLICY {quote_ident(policy)}")


def expect_policy_in_use(res: Result) -> bool:
    text = combined(res).lower()
    return res.rc != 0 and ("8241" in text or "still in use" in text or "placement policy" in text)


def expect_contains_policy(ddl: str, policy: str) -> bool:
    return f"PLACEMENT POLICY=`{policy}`" in ddl or f"PLACEMENT POLICY={quote_ident(policy)}" in ddl


def no_placement_policy(ddl: str) -> bool:
    return "PLACEMENT POLICY" not in ddl


def run_case(args: argparse.Namespace, name: str, fn: Callable[[argparse.Namespace], Outcome]) -> Outcome:
    try:
        return fn(args)
    except Exception as exc:
        return Outcome(name, "finding", f"probe crashed: {exc}")


def case_db_placement_visible_control(args: argparse.Namespace) -> Outcome:
    name = "db_placement_visible_control"
    db = "ai_native_dbpl_visible"
    pp1 = "ai_native_dbpl_pp_visible"
    try:
        setup_clean(args, [db], [pp1])
        create_policy(args, pp1, 1)
        exec_ok(args, f"CREATE DATABASE {quote_ident(db)} PLACEMENT POLICY {quote_ident(pp1)}")
        ddl = show_create_database(args, db)
        if not expect_contains_policy(ddl, pp1):
            return Outcome(name, "finding", "SHOW CREATE DATABASE did not expose DB placement policy: " + ddl)
        return Outcome(name, "ok", "SHOW CREATE DATABASE exposes DB placement policy")
    finally:
        cleanup(args, [db], [pp1])


def case_drop_policy_referenced_by_database_blocks(args: argparse.Namespace) -> Outcome:
    name = "drop_policy_referenced_by_database_blocks"
    db = "ai_native_dbpl_drop_block"
    pp1 = "ai_native_dbpl_pp_drop_block"
    try:
        setup_clean(args, [db], [pp1])
        create_policy(args, pp1, 1)
        exec_ok(args, f"CREATE DATABASE {quote_ident(db)} PLACEMENT POLICY {quote_ident(pp1)}")
        drop = drop_policy(args, pp1)
        if not expect_policy_in_use(drop):
            return Outcome(name, "finding", "DROP PLACEMENT POLICY was not blocked by DB ref: " + combined(drop))
        ddl = show_create_database(args, db)
        if not expect_contains_policy(ddl, pp1):
            return Outcome(name, "finding", "blocked drop did not preserve DB policy ref: " + ddl)
        return Outcome(name, "ok", "DROP PLACEMENT POLICY blocked with DB-level ref still visible")
    finally:
        cleanup(args, [db], [pp1])


def case_alter_database_rewrites_policy_ref(args: argparse.Namespace) -> Outcome:
    name = "alter_database_rewrites_policy_ref"
    db = "ai_native_dbpl_rewrite"
    pp1 = "ai_native_dbpl_pp_old"
    pp2 = "ai_native_dbpl_pp_new"
    try:
        setup_clean(args, [db], [pp1, pp2])
        create_policy(args, pp1, 1)
        create_policy(args, pp2, 2)
        exec_ok(args, f"CREATE DATABASE {quote_ident(db)} PLACEMENT POLICY {quote_ident(pp1)}")
        exec_ok(args, f"ALTER DATABASE {quote_ident(db)} PLACEMENT POLICY {quote_ident(pp2)}")
        ddl = show_create_database(args, db)
        if not expect_contains_policy(ddl, pp2) or expect_contains_policy(ddl, pp1):
            return Outcome(name, "finding", "ALTER DATABASE did not rewrite DB policy ref cleanly: " + ddl)
        old_drop = drop_policy(args, pp1)
        if old_drop.rc != 0:
            return Outcome(name, "finding", "old DB policy remained in-use after rewrite: " + combined(old_drop))
        new_drop = drop_policy(args, pp2)
        if not expect_policy_in_use(new_drop):
            return Outcome(name, "finding", "new DB policy was not protected after rewrite: " + combined(new_drop))
        return Outcome(name, "ok", "ALTER DATABASE rewrote DB ref; old policy droppable, new policy in-use")
    finally:
        cleanup(args, [db], [pp1, pp2])


def case_alter_database_default_releases_policy_ref(args: argparse.Namespace) -> Outcome:
    name = "alter_database_default_releases_policy_ref"
    db = "ai_native_dbpl_release"
    pp1 = "ai_native_dbpl_pp_release"
    try:
        setup_clean(args, [db], [pp1])
        create_policy(args, pp1, 1)
        exec_ok(args, f"CREATE DATABASE {quote_ident(db)} PLACEMENT POLICY {quote_ident(pp1)}")
        exec_ok(args, f"ALTER DATABASE {quote_ident(db)} PLACEMENT POLICY DEFAULT")
        ddl = show_create_database(args, db)
        if not no_placement_policy(ddl):
            return Outcome(name, "finding", "ALTER DATABASE ... DEFAULT left DB policy ref: " + ddl)
        drop = drop_policy(args, pp1)
        if drop.rc != 0:
            return Outcome(name, "finding", "released DB policy still in-use: " + combined(drop))
        return Outcome(name, "ok", "ALTER DATABASE ... DEFAULT released DB policy ref")
    finally:
        cleanup(args, [db], [pp1])


def case_drop_database_releases_policy_ref(args: argparse.Namespace) -> Outcome:
    name = "drop_database_releases_policy_ref"
    db = "ai_native_dbpl_drop_db"
    pp1 = "ai_native_dbpl_pp_drop_db"
    try:
        setup_clean(args, [db], [pp1])
        create_policy(args, pp1, 1)
        exec_ok(args, f"CREATE DATABASE {quote_ident(db)} PLACEMENT POLICY {quote_ident(pp1)}")
        exec_ok(args, f"DROP DATABASE {quote_ident(db)}")
        drop = drop_policy(args, pp1)
        if drop.rc != 0:
            return Outcome(name, "finding", "DROP DATABASE did not release DB policy ref: " + combined(drop))
        return Outcome(name, "ok", "DROP DATABASE released the DB-level policy reference")
    finally:
        cleanup(args, [db], [pp1])


def case_database_default_inheritance_boundary(args: argparse.Namespace) -> Outcome:
    name = "database_default_inheritance_boundary"
    db = "ai_native_dbpl_inherit"
    pp1 = "ai_native_dbpl_pp_inherit_old"
    pp2 = "ai_native_dbpl_pp_inherit_new"
    try:
        setup_clean(args, [db], [pp1, pp2])
        create_policy(args, pp1, 1)
        create_policy(args, pp2, 2)
        exec_ok(args, f"CREATE DATABASE {quote_ident(db)} PLACEMENT POLICY {quote_ident(pp1)}")
        exec_ok(args, "CREATE TABLE t_old(a INT)", db)
        exec_ok(args, f"ALTER DATABASE {quote_ident(db)} PLACEMENT POLICY {quote_ident(pp2)}")
        exec_ok(args, "CREATE TABLE t_new(a INT)", db)

        db_ddl = show_create_database(args, db)
        old_table_ddl = show_create_table(args, db, "t_old")
        new_table_ddl = show_create_table(args, db, "t_new")
        if not expect_contains_policy(db_ddl, pp2):
            return Outcome(name, "finding", "DB did not show new default policy: " + db_ddl)
        if not expect_contains_policy(old_table_ddl, pp1):
            return Outcome(name, "finding", "old table lost inherited original policy: " + old_table_ddl)
        if not expect_contains_policy(new_table_ddl, pp2):
            return Outcome(name, "finding", "new table did not inherit new DB policy: " + new_table_ddl)

        old_drop = drop_policy(args, pp1)
        new_drop = drop_policy(args, pp2)
        if not expect_policy_in_use(old_drop):
            return Outcome(name, "finding", "old inherited table policy was not protected: " + combined(old_drop))
        if not expect_policy_in_use(new_drop):
            return Outcome(name, "finding", "new DB/table policy was not protected: " + combined(new_drop))
        return Outcome(name, "ok", "old table kept old policy; new table inherited new DB policy; both refs protected")
    finally:
        cleanup(args, [db], [pp1, pp2])


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default="mysql")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", default="14000")
    parser.add_argument("--user", default="root")
    args = parser.parse_args()

    cases: list[tuple[str, Callable[[argparse.Namespace], Outcome]]] = [
        ("db_placement_visible_control", case_db_placement_visible_control),
        ("drop_policy_referenced_by_database_blocks", case_drop_policy_referenced_by_database_blocks),
        ("alter_database_rewrites_policy_ref", case_alter_database_rewrites_policy_ref),
        ("alter_database_default_releases_policy_ref", case_alter_database_default_releases_policy_ref),
        ("drop_database_releases_policy_ref", case_drop_database_releases_policy_ref),
        ("database_default_inheritance_boundary", case_database_default_inheritance_boundary),
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
