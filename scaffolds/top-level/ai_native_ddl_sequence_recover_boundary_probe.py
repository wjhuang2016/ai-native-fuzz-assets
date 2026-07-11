#!/usr/bin/env python3
"""Sequence-default recover boundary probe for S6 validator-gap screening.

This is a selector-calibration probe, not a new finding claim.

S6 should target restore paths that bypass a validator present on the normal
create/alter path. Sequence defaults look tempting because FLASHBACK TABLE can
restore a default that points at a dropped sequence. However, current TiDB also
allows CREATE TABLE with a default that points at a missing sequence, so this
owner is not a clean S6 "validator gap" target. It belongs to the existing
sequence-default lazy-name-resolution family (id30005), not a new recover-only
bug family.
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
        raise RuntimeError(f"SQL failed rc={res.rc}: {sql}\n{res.err}\n{res.out}")


def qident(name: str) -> str:
    return "`" + name.replace("`", "``") + "`"


def combined(res: Result) -> str:
    return (res.err + "\n" + res.out).strip()


def show_create(args: argparse.Namespace, db: str, table: str = "t") -> str:
    res = run_mysql(args, f"SHOW CREATE TABLE {qident(table)}", db)
    if res.rc != 0:
        return combined(res)
    return res.out


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default="mysql")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", default="14000")
    parser.add_argument("--user", default="root")
    args = parser.parse_args()

    version = run_mysql(args, "SELECT VERSION()")
    if version.rc != 0:
        print(f"ERROR fingerprint failed: {combined(version)}")
        return 2
    print(f"FINGERPRINT version={version.out}")

    db_missing = "ai_seq_missing_ctl"
    db_recover = "ai_seq_recover_boundary"
    db_flashback = "ai_seq_recover_boundary_db"
    for db in (db_missing, db_recover, db_flashback):
        run_mysql(args, f"DROP DATABASE IF EXISTS {qident(db)}")

    try:
        exec_ok(args, f"CREATE DATABASE {qident(db_missing)}")
        create_missing = run_mysql(
            args,
            "CREATE TABLE t(a INT DEFAULT NEXT VALUE FOR seq, b INT)",
            db_missing,
        )
        insert_missing = run_mysql(args, "INSERT INTO t(b) VALUES (1)", db_missing)
        print("CASE create_missing_sequence_control")
        print(f"create_rc={create_missing.rc} err={create_missing.err}")
        print(f"insert_rc={insert_missing.rc} err={insert_missing.err}")
        print(f"show_create={show_create(args, db_missing)}")

        exec_ok(args, f"CREATE DATABASE {qident(db_recover)}")
        exec_ok(args, "CREATE SEQUENCE seq START WITH 1 INCREMENT BY 1 NOCACHE", db_recover)
        exec_ok(args, "CREATE TABLE t(a INT DEFAULT NEXT VALUE FOR seq, b INT)", db_recover)
        exec_ok(args, "INSERT INTO t(b) VALUES (10)", db_recover)
        exec_ok(args, "DROP TABLE t", db_recover)
        exec_ok(args, "DROP SEQUENCE seq", db_recover)
        flashback_table = run_mysql(args, "FLASHBACK TABLE t", db_recover)
        insert_recovered = run_mysql(args, "INSERT INTO t(b) VALUES (20)", db_recover)
        print("CASE flashback_table_without_sequence")
        print(f"flashback_rc={flashback_table.rc} err={flashback_table.err}")
        print(f"insert_rc={insert_recovered.rc} err={insert_recovered.err}")
        print(f"show_create={show_create(args, db_recover)}")

        exec_ok(args, f"CREATE DATABASE {qident(db_flashback)}")
        exec_ok(args, "CREATE SEQUENCE seq START WITH 1 INCREMENT BY 1 NOCACHE", db_flashback)
        exec_ok(args, "CREATE TABLE t(a INT DEFAULT NEXT VALUE FOR seq, b INT)", db_flashback)
        exec_ok(args, "INSERT INTO t(b) VALUES (10)", db_flashback)
        exec_ok(args, f"DROP DATABASE {qident(db_flashback)}")
        flashback_db = run_mysql(args, f"FLASHBACK DATABASE {qident(db_flashback)}")
        insert_flashback_db = run_mysql(args, "INSERT INTO t(b) VALUES (20)", db_flashback)
        rows = run_mysql(args, "SELECT GROUP_CONCAT(CONCAT(a, ':', b) ORDER BY b) FROM t", db_flashback)
        print("CASE flashback_database_with_sequence")
        print(f"flashback_rc={flashback_db.rc} err={flashback_db.err}")
        print(f"insert_rc={insert_flashback_db.rc} err={insert_flashback_db.err}")
        print(f"rows={rows.out}")

        validator_gap = create_missing.rc != 0 and flashback_table.rc == 0 and insert_recovered.rc != 0
        boundary = create_missing.rc == 0 and insert_missing.rc != 0 and flashback_table.rc == 0 and insert_recovered.rc != 0
        if validator_gap:
            print("RED\tsequence_recover_validator_gap\tcreate blocked missing sequence but recover restored it")
            print("SUMMARY total=3 findings=1 boundary=0")
            return 1
        if boundary:
            print("INFO\tsequence_recover_boundary\tordinary create also allows missing sequence; not S6 validator-gap")
            print("SUMMARY total=3 findings=0 boundary=1")
            return 0
        print("GREEN(triggered)\tsequence_recover_boundary\tno recover-specific violation observed")
        print("SUMMARY total=3 findings=0 boundary=0")
        return 0
    finally:
        for db in (db_missing, db_recover, db_flashback):
            run_mysql(args, f"DROP DATABASE IF EXISTS {qident(db)}")


if __name__ == "__main__":
    raise SystemExit(main())
