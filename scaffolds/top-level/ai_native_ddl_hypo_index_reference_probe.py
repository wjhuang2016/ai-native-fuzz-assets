#!/usr/bin/env python3
"""Hypothetical-index DDL reference-owner probe.

Hypo indexes are session-local metadata created by DDL syntax:

    ALTER TABLE t ADD INDEX idx(a) USING HYPO

The creation path validates the table and column, and `SHOW CREATE TABLE` merges
session-local hypo indexes into the visible table definition. This probe checks
whether later DDL that renames/removes the referenced table or column also
rewrites or removes the session-local hypo index.
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


def mysql_args(args: argparse.Namespace) -> list[str]:
    return [
        args.mysql,
        f"-h{args.host}",
        f"-P{args.port}",
        f"-u{args.user}",
        "--batch",
        "--raw",
        "--skip-column-names",
        "--connect-timeout=5",
    ]


def run_session(args: argparse.Namespace, sql: str) -> Result:
    proc = subprocess.run(
        mysql_args(args),
        input=sql,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    return Result(proc.returncode, proc.stdout.rstrip("\n"), proc.stderr.rstrip("\n"))


def run_sql(args: argparse.Namespace, sql: str) -> Result:
    proc = subprocess.run(
        mysql_args(args) + ["-e", sql],
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    return Result(proc.returncode, proc.stdout.rstrip("\n"), proc.stderr.rstrip("\n"))


def combined(res: Result) -> str:
    return (res.err + "\n" + res.out).strip()


def cleanup_db(args: argparse.Namespace, db: str) -> None:
    run_sql(args, f"DROP DATABASE IF EXISTS `{db}`")


def segment_after(out: str, marker: str) -> str:
    needle = marker + "\n"
    if needle not in out:
        return ""
    part = out.split(needle, 1)[1]
    next_marker = "\n__"
    if next_marker in part:
        part = part.split(next_marker, 1)[0]
    return part


def has_hypo_on_column(text: str, column: str = "a") -> bool:
    return "HYPO INDEX" in text and f"`idx_a` (`{column}`)" in text


def run_case(args: argparse.Namespace, name: str, fn: Callable[[argparse.Namespace], Outcome]) -> Outcome:
    try:
        return fn(args)
    except Exception as exc:
        return Outcome(name, "finding", f"probe crashed: {exc}")


def case_live_hypo_index_control(args: argparse.Namespace) -> Outcome:
    name = "live_hypo_index_control"
    db = "ai_native_hypo_control"
    try:
        cleanup_db(args, db)
        res = run_session(
            args,
            f"""
CREATE DATABASE `{db}`;
USE `{db}`;
CREATE TABLE t(a INT, b INT);
ALTER TABLE t ADD INDEX idx_a(a) USING HYPO;
SELECT '__SHOW__';
SHOW CREATE TABLE t;
DROP DATABASE `{db}`;
""",
        )
        if res.rc != 0:
            return Outcome(name, "finding", "setup failed: " + combined(res))
        shown = segment_after(res.out, "__SHOW__")
        if not has_hypo_on_column(shown, "a"):
            return Outcome(name, "finding", "hypo index not visible in SHOW CREATE: " + shown)
        return Outcome(name, "ok", "SHOW CREATE exposes session-local hypo index on existing column")
    finally:
        cleanup_db(args, db)


def case_rename_column_leaves_stale_hypo(args: argparse.Namespace) -> Outcome:
    name = "rename_column_leaves_stale_hypo_index"
    db = "ai_native_hypo_rename_col"
    try:
        cleanup_db(args, db)
        res = run_session(
            args,
            f"""
CREATE DATABASE `{db}`;
USE `{db}`;
CREATE TABLE t(a INT, b INT);
ALTER TABLE t ADD INDEX idx_a(a) USING HYPO;
SELECT '__BEFORE_ALTER__';
ALTER TABLE t RENAME COLUMN a TO aa;
SELECT '__SHOW__';
SHOW CREATE TABLE t;
DROP DATABASE `{db}`;
""",
        )
        if res.rc != 0:
            if "__BEFORE_ALTER__" in res.out:
                return Outcome(name, "ok", "column rename was blocked: " + combined(res))
            return Outcome(name, "finding", "setup failed before rename: " + combined(res))
        shown = segment_after(res.out, "__SHOW__")
        if has_hypo_on_column(shown, "a") and "`aa` int" in shown:
            return Outcome(name, "finding", "column rename succeeded but hypo index still references old column: " + shown)
        return Outcome(name, "ok", "hypo index was rewritten, removed, or not visible after column rename: " + shown)
    finally:
        cleanup_db(args, db)


def case_change_column_leaves_stale_hypo(args: argparse.Namespace) -> Outcome:
    name = "change_column_rename_leaves_stale_hypo_index"
    db = "ai_native_hypo_change_col"
    try:
        cleanup_db(args, db)
        res = run_session(
            args,
            f"""
CREATE DATABASE `{db}`;
USE `{db}`;
CREATE TABLE t(a INT, b INT);
ALTER TABLE t ADD INDEX idx_a(a) USING HYPO;
SELECT '__BEFORE_ALTER__';
ALTER TABLE t CHANGE COLUMN a aa INT;
SELECT '__SHOW__';
SHOW CREATE TABLE t;
DROP DATABASE `{db}`;
""",
        )
        if res.rc != 0:
            if "__BEFORE_ALTER__" in res.out:
                return Outcome(name, "ok", "change-column rename was blocked: " + combined(res))
            return Outcome(name, "finding", "setup failed before change-column rename: " + combined(res))
        shown = segment_after(res.out, "__SHOW__")
        if has_hypo_on_column(shown, "a") and "`aa` int" in shown:
            return Outcome(name, "finding", "CHANGE COLUMN succeeded but hypo index still references old column: " + shown)
        return Outcome(name, "ok", "hypo index was rewritten, removed, or not visible after CHANGE COLUMN: " + shown)
    finally:
        cleanup_db(args, db)


def case_drop_column_leaves_stale_hypo(args: argparse.Namespace) -> Outcome:
    name = "drop_column_leaves_stale_hypo_index"
    db = "ai_native_hypo_drop_col"
    try:
        cleanup_db(args, db)
        res = run_session(
            args,
            f"""
CREATE DATABASE `{db}`;
USE `{db}`;
CREATE TABLE t(a INT, b INT);
ALTER TABLE t ADD INDEX idx_a(a) USING HYPO;
SELECT '__BEFORE_ALTER__';
ALTER TABLE t DROP COLUMN a;
SELECT '__SHOW__';
SHOW CREATE TABLE t;
DROP DATABASE `{db}`;
""",
        )
        if res.rc != 0:
            if "__BEFORE_ALTER__" in res.out:
                return Outcome(name, "ok", "drop column was blocked: " + combined(res))
            return Outcome(name, "finding", "setup failed before drop column: " + combined(res))
        shown = segment_after(res.out, "__SHOW__")
        if has_hypo_on_column(shown, "a") and "`a` int" not in shown:
            return Outcome(name, "finding", "DROP COLUMN succeeded but hypo index still references dropped column: " + shown)
        return Outcome(name, "ok", "hypo index was removed or not visible after DROP COLUMN: " + shown)
    finally:
        cleanup_db(args, db)


def case_drop_table_recreate_resurrects_hypo(args: argparse.Namespace) -> Outcome:
    name = "drop_table_recreate_resurrects_hypo_index"
    db = "ai_native_hypo_drop_table"
    try:
        cleanup_db(args, db)
        res = run_session(
            args,
            f"""
CREATE DATABASE `{db}`;
USE `{db}`;
CREATE TABLE t(a INT, b INT);
ALTER TABLE t ADD INDEX idx_a(a) USING HYPO;
DROP TABLE t;
CREATE TABLE t(a INT, b INT);
SELECT '__SHOW__';
SHOW CREATE TABLE t;
DROP DATABASE `{db}`;
""",
        )
        if res.rc != 0:
            return Outcome(name, "finding", "drop/recreate setup failed: " + combined(res))
        shown = segment_after(res.out, "__SHOW__")
        if has_hypo_on_column(shown, "a"):
            return Outcome(name, "finding", "DROP TABLE + recreate attached old hypo index to the new table: " + shown)
        return Outcome(name, "ok", "DROP TABLE removed or hid the old hypo index: " + shown)
    finally:
        cleanup_db(args, db)


def case_rename_table_recreate_resurrects_hypo(args: argparse.Namespace) -> Outcome:
    name = "rename_table_recreate_old_name_resurrects_hypo_index"
    db = "ai_native_hypo_rename_table"
    try:
        cleanup_db(args, db)
        res = run_session(
            args,
            f"""
CREATE DATABASE `{db}`;
USE `{db}`;
CREATE TABLE t(a INT, b INT);
ALTER TABLE t ADD INDEX idx_a(a) USING HYPO;
RENAME TABLE t TO t2;
CREATE TABLE t(a INT, b INT);
SELECT '__SHOW_T2__';
SHOW CREATE TABLE t2;
SELECT '__SHOW_T__';
SHOW CREATE TABLE t;
DROP DATABASE `{db}`;
""",
        )
        if res.rc != 0:
            return Outcome(name, "finding", "rename/recreate setup failed: " + combined(res))
        old_name = segment_after(res.out, "__SHOW_T__")
        new_name = segment_after(res.out, "__SHOW_T2__")
        if has_hypo_on_column(old_name, "a"):
            return Outcome(
                name,
                "finding",
                "RENAME TABLE left old-name hypo index, and recreating old name attached it to the new table; "
                "renamed table=" + new_name + "; new old-name table=" + old_name,
            )
        return Outcome(name, "ok", "RENAME TABLE did not leak old-name hypo index to recreated table")
    finally:
        cleanup_db(args, db)


def case_drop_database_recreate_resurrects_hypo(args: argparse.Namespace) -> Outcome:
    name = "drop_database_recreate_resurrects_hypo_index"
    db = "ai_native_hypo_drop_db"
    try:
        cleanup_db(args, db)
        res = run_session(
            args,
            f"""
CREATE DATABASE `{db}`;
USE `{db}`;
CREATE TABLE t(a INT, b INT);
ALTER TABLE t ADD INDEX idx_a(a) USING HYPO;
DROP DATABASE `{db}`;
CREATE DATABASE `{db}`;
USE `{db}`;
CREATE TABLE t(a INT, b INT);
SELECT '__SHOW__';
SHOW CREATE TABLE t;
DROP DATABASE `{db}`;
""",
        )
        if res.rc != 0:
            return Outcome(name, "finding", "drop-db/recreate setup failed: " + combined(res))
        shown = segment_after(res.out, "__SHOW__")
        if has_hypo_on_column(shown, "a"):
            return Outcome(name, "finding", "DROP DATABASE + recreate attached old hypo index to the new table: " + shown)
        return Outcome(name, "ok", "DROP DATABASE removed or hid old hypo index metadata: " + shown)
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
        ("live_hypo_index_control", case_live_hypo_index_control),
        ("rename_column_leaves_stale_hypo_index", case_rename_column_leaves_stale_hypo),
        ("change_column_rename_leaves_stale_hypo_index", case_change_column_leaves_stale_hypo),
        ("drop_column_leaves_stale_hypo_index", case_drop_column_leaves_stale_hypo),
        ("drop_table_recreate_resurrects_hypo_index", case_drop_table_recreate_resurrects_hypo),
        ("rename_table_recreate_old_name_resurrects_hypo_index", case_rename_table_recreate_resurrects_hypo),
        ("drop_database_recreate_resurrects_hypo_index", case_drop_database_recreate_resurrects_hypo),
    ]

    outcomes = [run_case(args, name, fn) for name, fn in cases]
    findings = 0
    skipped = 0
    for outcome in outcomes:
        print(f"{outcome.status} {outcome.name}")
        if outcome.detail:
            print(f"  {outcome.detail}")
        if outcome.status == "finding":
            findings += 1
        if outcome.status == "skipped":
            skipped += 1
    print(f"SUMMARY total={len(outcomes)} findings={findings} skipped={skipped}")
    return 1 if findings else 0


if __name__ == "__main__":
    raise SystemExit(main())
