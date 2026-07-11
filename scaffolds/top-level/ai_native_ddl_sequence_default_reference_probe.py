#!/usr/bin/env python3
"""Sequence-default DDL reference-owner probe.

The proof obligation is:

    if a table column default references a sequence, DDL that removes or renames
    that sequence must either rewrite the default expression or block.

This probe intentionally keeps the matrix small. It is not a broad sequence test;
it checks whether DDL can leave a live table default pointing at a missing
sequence object.
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


def show_create_table(args: argparse.Namespace, db: str, table: str = "t") -> str:
    res = run_mysql(args, f"SHOW CREATE TABLE {quote_ident(table)}", db)
    if res.rc != 0:
        raise RuntimeError("SHOW CREATE TABLE failed: " + combined(res))
    if "\t" not in res.out:
        raise RuntimeError("unexpected SHOW CREATE TABLE output: " + res.out)
    return res.out.split("\t", 1)[1]


def rows(res: Result) -> list[str]:
    if not res.out:
        return []
    return res.out.splitlines()


def create_seq_default_table(args: argparse.Namespace, db: str, seq_name: str = "seq") -> None:
    exec_ok(args, f"CREATE SEQUENCE {quote_ident(seq_name)} START WITH 1 INCREMENT BY 1 NOCACHE", db)
    exec_ok(args, f"CREATE TABLE t(a INT DEFAULT NEXT VALUE FOR {quote_ident(seq_name)}, b INT)", db)


def expect_insert_default_ok(args: argparse.Namespace, db: str, expected: str, b: int) -> tuple[bool, str]:
    ins = run_mysql(args, f"INSERT INTO t(b) VALUES ({b})", db)
    if ins.rc != 0:
        return False, "default insert failed: " + combined(ins)
    res = run_mysql(args, f"SELECT a FROM t WHERE b={b}", db)
    if res.rc != 0:
        return False, "select inserted default failed: " + combined(res)
    got = res.out.strip()
    if got != expected:
        return False, f"expected inserted default {expected}, got {got}"
    return True, f"default inserted {got}"


def run_case(args: argparse.Namespace, name: str, fn: Callable[[argparse.Namespace], Outcome]) -> Outcome:
    try:
        return fn(args)
    except Exception as exc:
        return Outcome(name, "finding", f"probe crashed: {exc}")


def case_live_sequence_default_control(args: argparse.Namespace) -> Outcome:
    name = "live_sequence_default_insert_control"
    db = "ai_native_seq_default_control"
    try:
        setup_db(args, db)
        create_seq_default_table(args, db)
        ok, detail = expect_insert_default_ok(args, db, "1", 10)
        if not ok:
            return Outcome(name, "finding", detail)
        ddl = show_create_table(args, db)
        if "nextval" not in ddl.lower() or f"`{db}`.`seq`" not in ddl:
            return Outcome(name, "finding", "SHOW CREATE did not expose qualified sequence default: " + ddl)
        return Outcome(name, "ok", detail + "; SHOW CREATE exposes qualified sequence default")
    finally:
        cleanup_db(args, db)


def case_drop_sequence_breaks_default(args: argparse.Namespace) -> Outcome:
    name = "drop_sequence_referenced_by_default"
    db = "ai_native_seq_default_drop"
    try:
        setup_db(args, db)
        create_seq_default_table(args, db)
        ok, detail = expect_insert_default_ok(args, db, "1", 10)
        if not ok:
            return Outcome(name, "finding", detail)

        drop = run_mysql(args, "DROP SEQUENCE seq", db)
        if drop.rc != 0:
            return Outcome(name, "ok", "DROP SEQUENCE blocked while referenced: " + combined(drop))

        ddl = show_create_table(args, db)
        insert = run_mysql(args, "INSERT INTO t(b) VALUES (20)", db)
        if insert.rc != 0 and ("doesn't exist" in combined(insert).lower() or "1146" in combined(insert)):
            return Outcome(name, "finding", "DROP SEQUENCE succeeded and left broken default; SHOW CREATE=" + ddl + "; insert error=" + combined(insert))
        return Outcome(name, "ok", "DROP SEQUENCE succeeded but default remained usable or was cleaned: " + combined(insert))
    finally:
        cleanup_db(args, db)


def case_rename_sequence_breaks_default(args: argparse.Namespace) -> Outcome:
    name = "rename_sequence_via_rename_table_breaks_default"
    db = "ai_native_seq_default_rename"
    try:
        setup_db(args, db)
        create_seq_default_table(args, db)
        rename = run_mysql(args, "RENAME TABLE seq TO seq2", db)
        if rename.rc != 0:
            return Outcome(name, "ok", "RENAME TABLE on sequence blocked: " + combined(rename))

        ddl = show_create_table(args, db)
        insert = run_mysql(args, "INSERT INTO t(b) VALUES (20)", db)
        if insert.rc != 0 and ("doesn't exist" in combined(insert).lower() or "1146" in combined(insert)):
            return Outcome(name, "finding", "sequence rename succeeded and left default pointing at old name; SHOW CREATE=" + ddl + "; insert error=" + combined(insert))
        if "`seq2`" in ddl:
            return Outcome(name, "ok", "sequence rename rewrote default to new name")
        return Outcome(name, "ok", "sequence rename succeeded but default stayed usable: " + combined(insert))
    finally:
        cleanup_db(args, db)


def case_drop_sequence_database_breaks_cross_db_default(args: argparse.Namespace) -> Outcome:
    name = "drop_database_with_sequence_breaks_cross_db_default"
    seq_db = "ai_native_seq_default_seqdb"
    table_db = "ai_native_seq_default_tabdb"
    try:
        setup_db(args, seq_db)
        setup_db(args, table_db)
        exec_ok(args, f"CREATE SEQUENCE {quote_ident(seq_db)}.seq START WITH 10 INCREMENT BY 1 NOCACHE")
        exec_ok(args, f"CREATE TABLE {quote_ident(table_db)}.t(a INT DEFAULT NEXT VALUE FOR {quote_ident(seq_db)}.seq, b INT)")
        ok, detail = expect_insert_default_ok(args, table_db, "10", 10)
        if not ok:
            return Outcome(name, "finding", detail)

        drop = run_mysql(args, f"DROP DATABASE {quote_ident(seq_db)}")
        if drop.rc != 0:
            return Outcome(name, "ok", "DROP DATABASE blocked while it contains referenced sequence: " + combined(drop))

        ddl = show_create_table(args, table_db)
        insert = run_mysql(args, "INSERT INTO t(b) VALUES (20)", table_db)
        if insert.rc != 0 and ("doesn't exist" in combined(insert).lower() or "1146" in combined(insert)):
            return Outcome(name, "finding", "DROP DATABASE removed referenced sequence and left cross-db default broken; SHOW CREATE=" + ddl + "; insert error=" + combined(insert))
        return Outcome(name, "ok", "DROP DATABASE succeeded but default remained usable or was cleaned: " + combined(insert))
    finally:
        cleanup_db(args, table_db, seq_db)


def case_change_column_preserves_live_default(args: argparse.Namespace) -> Outcome:
    name = "change_column_preserves_live_sequence_default"
    db = "ai_native_seq_default_change"
    try:
        setup_db(args, db)
        create_seq_default_table(args, db)
        exec_ok(args, "ALTER TABLE t CHANGE COLUMN a aa INT DEFAULT NEXT VALUE FOR seq", db)
        ddl = show_create_table(args, db)
        if "`aa` int DEFAULT (nextval" not in ddl:
            return Outcome(name, "finding", "CHANGE COLUMN lost sequence default: " + ddl)
        ins = run_mysql(args, "INSERT INTO t(b) VALUES (30)", db)
        if ins.rc != 0:
            return Outcome(name, "finding", "default insert failed after CHANGE COLUMN: " + combined(ins))
        res = run_mysql(args, "SELECT aa FROM t WHERE b=30", db)
        if res.out.strip() != "1":
            return Outcome(name, "finding", "unexpected default after CHANGE COLUMN: " + res.out)
        return Outcome(name, "ok", "CHANGE COLUMN preserves live sequence default and behavior")
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
        ("live_sequence_default_insert_control", case_live_sequence_default_control),
        ("drop_sequence_referenced_by_default", case_drop_sequence_breaks_default),
        ("rename_sequence_via_rename_table_breaks_default", case_rename_sequence_breaks_default),
        ("drop_database_with_sequence_breaks_cross_db_default", case_drop_sequence_database_breaks_cross_db_default),
        ("change_column_preserves_live_sequence_default", case_change_column_preserves_live_default),
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
