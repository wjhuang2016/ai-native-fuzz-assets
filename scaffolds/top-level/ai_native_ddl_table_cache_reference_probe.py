#!/usr/bin/env python3
"""Table-cache side-metadata DDL reference-owner probe.

mysql.table_cache_meta is keyed by table ID. A cached table is represented in
both TableInfo.TableCacheStatusType and mysql.table_cache_meta. DDL that removes
or changes the table object must either block while the table is cached or
remove the table-cache side metadata.
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
class CaseOutcome:
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


def exec_ok(args: argparse.Namespace, sql: str, database: str | None = None) -> None:
    res = run_mysql(args, sql, database)
    if res.rc != 0:
        raise RuntimeError(f"SQL failed rc={res.rc}: {sql}\n{combined(res)}")


def quote_ident(name: str) -> str:
    return "`" + name.replace("`", "``") + "`"


def quote_str(value: str) -> str:
    return "'" + value.replace("\\", "\\\\").replace("'", "''") + "'"


def combined(res: Result) -> str:
    return (res.err + "\n" + res.out).strip()


def rows(res: Result) -> list[str]:
    if not res.out:
        return []
    return res.out.splitlines()


def setup_db(args: argparse.Namespace, db: str) -> None:
    exec_ok(args, f"DROP DATABASE IF EXISTS {quote_ident(db)}")
    exec_ok(args, f"CREATE DATABASE {quote_ident(db)}")


def cleanup_db(args: argparse.Namespace, db: str, tids: list[int] | None = None) -> None:
    # If a cached table still exists, DROP DATABASE may be the behavior under
    # test. Disable table cache first for ordinary cleanup.
    for tbl in ("t", "t2"):
        run_mysql(args, f"ALTER TABLE {quote_ident(tbl)} NOCACHE", db)
    run_mysql(args, f"DROP DATABASE IF EXISTS {quote_ident(db)}")
    for tid in tids or []:
        run_mysql(args, f"DELETE FROM mysql.table_cache_meta WHERE tid = {tid}")


def table_id(args: argparse.Namespace, db: str, table: str = "t") -> int:
    res = run_mysql(
        args,
        "SELECT tidb_table_id FROM information_schema.tables "
        f"WHERE table_schema={quote_str(db)} AND table_name={quote_str(table)}",
    )
    if res.rc != 0:
        raise RuntimeError("failed to query table ID: " + combined(res))
    if not res.out.strip():
        raise RuntimeError(f"table {db}.{table} not found")
    return int(res.out.strip())


def cache_meta_rows(args: argparse.Namespace, tid: int) -> list[str]:
    res = run_mysql(args, f"SELECT tid, lock_type, lease, oldReadLease FROM mysql.table_cache_meta WHERE tid={tid}")
    if res.rc != 0:
        raise RuntimeError("failed to query mysql.table_cache_meta: " + combined(res))
    return rows(res)


def show_create(args: argparse.Namespace, db: str, table: str = "t") -> str:
    res = run_mysql(args, f"SHOW CREATE TABLE {quote_ident(table)}", db)
    if res.rc != 0:
        raise RuntimeError("failed to SHOW CREATE TABLE: " + combined(res))
    return res.out


def expect_cache_meta(args: argparse.Namespace, tid: int) -> tuple[bool, str]:
    result = cache_meta_rows(args, tid)
    if len(result) != 1:
        return False, f"expected one table_cache_meta row for tid={tid}, got {result}"
    return True, result[0]


def expect_no_cache_meta(args: argparse.Namespace, tid: int) -> tuple[bool, str]:
    result = cache_meta_rows(args, tid)
    if result:
        return False, f"expected no table_cache_meta row for tid={tid}, got {result}"
    return True, f"no row for tid={tid}"


def run_case(
    args: argparse.Namespace,
    name: str,
    fn: Callable[[argparse.Namespace], CaseOutcome],
) -> CaseOutcome:
    try:
        return fn(args)
    except Exception as exc:
        return CaseOutcome(name, "finding", f"probe crashed: {exc}")


def case_cache_nocache_lifecycle(args: argparse.Namespace) -> CaseOutcome:
    name = "cache_nocache_metadata_lifecycle"
    db = "ai_native_cache_lifecycle"
    tids: list[int] = []
    try:
        setup_db(args, db)
        exec_ok(args, "CREATE TABLE t(id INT PRIMARY KEY, v INT)", db)
        tid = table_id(args, db)
        tids.append(tid)
        exec_ok(args, "ALTER TABLE t CACHE", db)

        ok, detail = expect_cache_meta(args, tid)
        if not ok:
            return CaseOutcome(name, "finding", detail)
        if "CACHED ON" not in show_create(args, db):
            return CaseOutcome(name, "finding", "SHOW CREATE TABLE did not expose cached state")

        exec_ok(args, "ALTER TABLE t NOCACHE", db)
        ok, detail = expect_no_cache_meta(args, tid)
        if not ok:
            return CaseOutcome(name, "finding", detail)
        if "CACHED ON" in show_create(args, db):
            return CaseOutcome(name, "finding", "SHOW CREATE TABLE still exposed cached state after NOCACHE")
        return CaseOutcome(name, "ok", "CACHE creates table-id row and NOCACHE removes it")
    finally:
        cleanup_db(args, db, tids)


def case_cached_table_blocks_direct_ddl(args: argparse.Namespace) -> CaseOutcome:
    name = "cached_table_blocks_direct_table_and_index_ddl"
    db = "ai_native_cache_block"
    tids: list[int] = []
    try:
        setup_db(args, db)
        exec_ok(args, "CREATE TABLE t(id INT PRIMARY KEY, v INT, KEY k_v(v))", db)
        tid = table_id(args, db)
        tids.append(tid)
        exec_ok(args, "ALTER TABLE t CACHE", db)

        checks = [
            "RENAME TABLE t TO t2",
            "DROP TABLE t",
            "TRUNCATE TABLE t",
            "ALTER TABLE t ADD INDEX k2(v)",
            "ALTER TABLE t RENAME INDEX k_v TO k_v2",
            "ALTER TABLE t PARTITION BY RANGE(v) (PARTITION p0 VALUES LESS THAN (100))",
        ]
        failures: list[str] = []
        for sql in checks:
            res = run_mysql(args, sql, db)
            text = combined(res).lower()
            if res.rc == 0:
                failures.append(f"{sql}: unexpectedly succeeded")
            elif "cache table" not in text and "cache tables" not in text:
                failures.append(f"{sql}: wrong error family: {combined(res)}")
            ok, detail = expect_cache_meta(args, tid)
            if not ok:
                failures.append(f"{sql}: side metadata changed after failed DDL: {detail}")
        if failures:
            return CaseOutcome(name, "finding", "; ".join(failures))
        return CaseOutcome(name, "ok", "cached table blocked rename/drop/truncate/index/partition DDL and preserved side metadata")
    finally:
        cleanup_db(args, db, tids)


def case_drop_database_with_cached_table(args: argparse.Namespace) -> CaseOutcome:
    name = "drop_database_with_cached_table_cleans_or_blocks_cache_meta"
    db = "ai_native_cache_drop_schema"
    tids: list[int] = []
    try:
        setup_db(args, db)
        exec_ok(args, "CREATE TABLE t(id INT PRIMARY KEY, v INT)", db)
        tid = table_id(args, db)
        tids.append(tid)
        exec_ok(args, "ALTER TABLE t CACHE", db)
        ok, detail = expect_cache_meta(args, tid)
        if not ok:
            return CaseOutcome(name, "finding", "precondition failed: " + detail)

        drop_res = run_mysql(args, f"DROP DATABASE {quote_ident(db)}")
        if drop_res.rc != 0:
            text = combined(drop_res).lower()
            if "cache table" in text or "cache tables" in text:
                return CaseOutcome(name, "ok", "DROP DATABASE blocked cached table: " + combined(drop_res))
            return CaseOutcome(name, "finding", "DROP DATABASE failed with wrong error family: " + combined(drop_res))

        remaining = cache_meta_rows(args, tid)
        if remaining:
            return CaseOutcome(
                name,
                "finding",
                f"DROP DATABASE succeeded but left mysql.table_cache_meta row for dropped table_id={tid}: {remaining}",
            )
        return CaseOutcome(name, "ok", "DROP DATABASE succeeded and cleaned table-cache metadata")
    finally:
        cleanup_db(args, db, tids)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default="mysql")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", default="14000")
    parser.add_argument("--user", default="root")
    args = parser.parse_args()

    cases = [
        ("cache_nocache_metadata_lifecycle", case_cache_nocache_lifecycle),
        ("cached_table_blocks_direct_table_and_index_ddl", case_cached_table_blocks_direct_ddl),
        ("drop_database_with_cached_table_cleans_or_blocks_cache_meta", case_drop_database_with_cached_table),
    ]
    outcomes: list[CaseOutcome] = []
    for name, fn in cases:
        outcome = run_case(args, name, fn)
        outcomes.append(outcome)
        print(f"{outcome.status.upper()}\t{outcome.name}\t{outcome.detail}")

    findings = sum(1 for outcome in outcomes if outcome.status == "finding")
    print(f"SUMMARY total={len(outcomes)} findings={findings} skipped=0")
    return 1 if findings else 0


if __name__ == "__main__":
    raise SystemExit(main())
