#!/usr/bin/env python3
"""FK + FLASHBACK TABLE missing-parent probe (selector S6).

Proof obligation:
    A recover/flashback path that re-materializes a table with foreign keys must
    preserve the same validity contract as CREATE TABLE: referenced parent
    tables must exist when foreign_key_checks is ON, or the FK must not be
    published as an enforceable schema object.

Bug shape:
    DROP child, DROP parent, then FLASHBACK child. The recovered child keeps
    its FK metadata referencing the now-missing parent. While the parent is
    absent, inserts that violate the FK are accepted. Recreating the parent
    makes new bad inserts fail again, leaving the already accepted orphan rows.
"""

from __future__ import annotations

import argparse
import dataclasses
import subprocess
import sys


@dataclasses.dataclass
class Result:
    rc: int
    out: str
    err: str


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


def run_session(args: argparse.Namespace, sql: str, database: str | None = None) -> Result:
    return run_mysql(args, "SET SESSION foreign_key_checks=1; " + sql, database)


def exec_ok(args: argparse.Namespace, sql: str, database: str | None = None) -> None:
    res = run_session(args, sql, database)
    if res.rc != 0:
        raise RuntimeError(f"SQL failed rc={res.rc}: {sql}\n{res.err}")


def sql_name(name: str) -> str:
    return "`" + name.replace("`", "``") + "`"


def combined(res: Result) -> str:
    return (res.err + "\n" + res.out).strip()


def is_fk_error(res: Result) -> bool:
    text = combined(res).lower()
    return res.rc != 0 and ("1452" in text or "foreign key constraint fails" in text)


def print_block(name: str, value: str, max_lines: int = 12) -> None:
    print(name)
    for line in value.splitlines()[:max_lines]:
        print(line)


def create_parent_child(args: argparse.Namespace, db: str) -> None:
    exec_ok(args, f"DROP DATABASE IF EXISTS {sql_name(db)}")
    exec_ok(args, f"CREATE DATABASE {sql_name(db)}")
    exec_ok(
        args,
        "CREATE TABLE p(id INT PRIMARY KEY); "
        "CREATE TABLE c("
        "id INT PRIMARY KEY, "
        "pid INT, "
        "INDEX idx_pid(pid), "
        "CONSTRAINT fk_pid FOREIGN KEY(pid) REFERENCES p(id)"
        "); "
        "INSERT INTO p VALUES (1); "
        "INSERT INTO c VALUES (1,1);",
        db,
    )


def case_flashback_child_without_parent(args: argparse.Namespace) -> bool:
    db = "ai_fk_flashback_child"
    create_parent_child(args, db)

    baseline_bad = run_session(args, "INSERT INTO c VALUES (90,999)", db)
    exec_ok(args, "DROP TABLE c; DROP TABLE p", db)
    active_after_drop = run_session(
        args,
        "SELECT COUNT(*) FROM information_schema.tables "
        f"WHERE table_schema='{db}'",
    )
    flashback = run_session(args, "FLASHBACK TABLE c", db)
    kcu = run_session(
        args,
        "SELECT table_name, referenced_table_name "
        "FROM information_schema.key_column_usage "
        f"WHERE table_schema='{db}' "
        "AND table_name='c' AND referenced_table_name IS NOT NULL",
    )
    ddl = run_session(args, "SHOW CREATE TABLE c", db)
    explain_missing = run_session(args, "EXPLAIN INSERT INTO c VALUES (91,999)", db)
    orphan_insert = run_session(args, "INSERT INTO c VALUES (2,999)", db)

    exec_ok(args, "CREATE TABLE p(id INT PRIMARY KEY); INSERT INTO p VALUES (1)", db)
    explain_after_parent = run_session(args, "EXPLAIN INSERT INTO c VALUES (92,999)", db)
    invalid_after_parent = run_session(args, "INSERT INTO c VALUES (3,999)", db)
    valid_after_parent = run_session(args, "INSERT INTO c VALUES (4,1)", db)
    orphan_count = run_session(args, "SELECT COUNT(*) FROM c WHERE pid=999", db)
    admin_check = run_session(args, "ADMIN CHECK TABLE c", db)

    print("CASE fk_flashback_child_without_parent")
    print(f"baseline_invalid_insert_blocked={is_fk_error(baseline_bad)}")
    print(f"active_tables_after_drop={active_after_drop.out}")
    print(f"flashback_rc={flashback.rc} err={flashback.err}")
    print(f"kcu={kcu.out}")
    print(f"orphan_insert_while_parent_missing_rc={orphan_insert.rc} err={orphan_insert.err}")
    print(f"invalid_insert_after_parent_recreate_fk_error={is_fk_error(invalid_after_parent)}")
    print(f"valid_insert_after_parent_recreate_rc={valid_after_parent.rc}")
    print(f"orphan_rows_after_parent_recreate={orphan_count.out}")
    print(f"admin_check_after_orphan_rc={admin_check.rc} err={admin_check.err}")
    print_block("SHOW_CREATE_C", ddl.out, 10)
    print_block("EXPLAIN_MISSING_PARENT", explain_missing.out, 8)
    print_block("EXPLAIN_AFTER_PARENT", explain_after_parent.out, 8)

    finding = (
        is_fk_error(baseline_bad)
        and flashback.rc == 0
        and "c\tp" in kcu.out
        and orphan_insert.rc == 0
        and is_fk_error(invalid_after_parent)
        and valid_after_parent.rc == 0
        and orphan_count.out.strip() == "1"
    )

    cleanup = run_session(args, f"DROP DATABASE IF EXISTS {sql_name(db)}")
    if cleanup.rc != 0:
        print(f"WARN cleanup failed for {db}: {cleanup.err}", file=sys.stderr)
    return finding


def case_flashback_database_control(args: argparse.Namespace) -> bool:
    db = "ai_fk_flashback_db"
    create_parent_child(args, db)
    exec_ok(args, f"DROP DATABASE {sql_name(db)}")
    flashback = run_session(args, f"FLASHBACK DATABASE {sql_name(db)}")
    invalid = run_session(args, "INSERT INTO c VALUES (2,999)", db)
    valid = run_session(args, "INSERT INTO c VALUES (3,1)", db)
    tables = run_session(
        args,
        "SELECT GROUP_CONCAT(table_name ORDER BY table_name) "
        "FROM information_schema.tables "
        f"WHERE table_schema='{db}'",
    )
    print("CASE fk_flashback_database_control")
    print(f"flashback_rc={flashback.rc} err={flashback.err}")
    print(f"tables={tables.out}")
    print(f"invalid_insert_fk_error={is_fk_error(invalid)}")
    print(f"valid_insert_rc={valid.rc}")
    cleanup = run_session(args, f"DROP DATABASE IF EXISTS {sql_name(db)}")
    if cleanup.rc != 0:
        print(f"WARN cleanup failed for {db}: {cleanup.err}", file=sys.stderr)
    return flashback.rc == 0 and tables.out == "c,p" and is_fk_error(invalid) and valid.rc == 0


def case_create_missing_parent_control(args: argparse.Namespace) -> bool:
    db = "ai_fk_create_missing_parent"
    exec_ok(args, f"DROP DATABASE IF EXISTS {sql_name(db)}")
    exec_ok(args, f"CREATE DATABASE {sql_name(db)}")
    create_child = run_session(
        args,
        "CREATE TABLE c("
        "id INT PRIMARY KEY, "
        "pid INT, "
        "INDEX idx_pid(pid), "
        "CONSTRAINT fk_pid FOREIGN KEY(pid) REFERENCES p(id)"
        ")",
        db,
    )
    print("CASE fk_create_missing_parent_control")
    print(f"create_child_rc={create_child.rc} err={create_child.err}")
    cleanup = run_session(args, f"DROP DATABASE IF EXISTS {sql_name(db)}")
    if cleanup.rc != 0:
        print(f"WARN cleanup failed for {db}: {cleanup.err}", file=sys.stderr)
    text = combined(create_child).lower()
    return create_child.rc != 0 and ("referenced table" in text or "open parent" in text)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--mysql", default="mysql")
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", default="14000")
    ap.add_argument("--user", default="root")
    args = ap.parse_args()

    version = run_mysql(args, "SELECT VERSION(), @@global.tidb_enable_foreign_key, @@foreign_key_checks")
    if version.rc != 0:
        print(f"ERROR fingerprint failed: {version.err}", file=sys.stderr)
        return 2
    print(f"FINGERPRINT {version.out}")

    cases = {
        "flashback_child_without_parent_red": case_flashback_child_without_parent(args),
        "flashback_database_control_green": case_flashback_database_control(args),
        "create_missing_parent_control_green": case_create_missing_parent_control(args),
    }

    for name, ok in cases.items():
        status = "RED" if name.endswith("_red") and ok else "GREEN(triggered)" if ok else "FAIL"
        print(f"{status}\t{name}")

    finding = cases["flashback_child_without_parent_red"]
    controls_ok = (
        cases["flashback_database_control_green"]
        and cases["create_missing_parent_control_green"]
    )
    print(f"SUMMARY total=3 findings={1 if finding else 0} controls_ok={1 if controls_ok else 0}")
    return 1 if finding and controls_ok else 0


if __name__ == "__main__":
    raise SystemExit(main())
