#!/usr/bin/env python3
"""DDL delete-range object-reference probe.

This probe checks whether DDLs that remove global-index or partition-owned
objects enqueue the right delete-range records. It stays in the DDL lane: the
oracle is mysql.gc_delete_range metadata, not query-planner behavior.
"""

from __future__ import annotations

import argparse
import dataclasses
import os
import subprocess
import sys
import time


INDEX_MARKER_HEX = "5f69"  # "_i" in tablecodec.EncodeTableIndexPrefix.


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


@dataclasses.dataclass
class DeleteRangeRecord:
    job_id: int
    element_id: int
    start_key: str
    end_key: str

    @property
    def is_index_range(self) -> bool:
        return len(self.start_key) > 22 and self.start_key[18:22].lower() == INDEX_MARKER_HEX


@dataclasses.dataclass
class Case:
    name: str
    setup: list[str]
    alter: str
    min_index_ranges: int
    min_table_ranges: int
    max_table_ranges: int | None = None


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


def current_gc_enable(args: argparse.Namespace) -> str:
    res = run_mysql(args, "SHOW VARIABLES LIKE 'tidb_gc_enable'")
    if res.rc != 0 or not res.out:
        return "ON"
    parts = res.out.split()
    return parts[-1].upper() if parts else "ON"


def max_delete_range_job(args: argparse.Namespace) -> int:
    res = run_mysql(
        args,
        """
        SELECT GREATEST(
          COALESCE((SELECT MAX(job_id) FROM mysql.gc_delete_range), 0),
          COALESCE((SELECT MAX(job_id) FROM mysql.gc_delete_range_done), 0)
        )
        """,
    )
    if res.rc != 0:
        raise RuntimeError("failed to read delete-range high-water mark: " + res.err)
    return int(res.out.strip() or "0")


def read_new_records(args: argparse.Namespace, base_job: int) -> list[DeleteRangeRecord]:
    res = run_mysql(
        args,
        f"""
        SELECT job_id, element_id, start_key, end_key
        FROM mysql.gc_delete_range
        WHERE job_id > {base_job}
        ORDER BY job_id, element_id
        """,
    )
    if res.rc != 0:
        raise RuntimeError("failed to read delete-range records: " + res.err)
    records: list[DeleteRangeRecord] = []
    for line in res.out.splitlines():
        job_id, element_id, start_key, end_key = line.split("\t")
        records.append(DeleteRangeRecord(int(job_id), int(element_id), start_key, end_key))
    return records


def cases() -> list[Case]:
    return [
        Case(
            name="remove_partitioning_records_old_global_index_range",
            setup=[
                "CREATE TABLE t(a INT UNSIGNED NOT NULL, b INT NOT NULL, UNIQUE KEY idx_b(b), UNIQUE KEY idx_a(a) GLOBAL) PARTITION BY HASH(b) (PARTITION p0, PARTITION p1, PARTITION p2)",
                "INSERT INTO t VALUES (1,10),(2,20),(3,30)",
            ],
            alter="ALTER TABLE t REMOVE PARTITIONING",
            min_index_ranges=1,
            min_table_ranges=3,
        ),
        Case(
            name="drop_global_index_records_logical_index_range_only",
            setup=[
                "CREATE TABLE t(a INT NOT NULL, b INT NOT NULL, UNIQUE KEY idx_b(b) GLOBAL) PARTITION BY RANGE(a) (PARTITION p0 VALUES LESS THAN (10), PARTITION p1 VALUES LESS THAN (MAXVALUE))",
                "INSERT INTO t VALUES (1,100),(20,200),(30,300)",
            ],
            alter="ALTER TABLE t DROP INDEX idx_b",
            min_index_ranges=1,
            min_table_ranges=0,
            max_table_ranges=0,
        ),
    ]


def run_case(args: argparse.Namespace, db: str, case: Case) -> CaseOutcome:
    exec_ok(args, "DROP TABLE IF EXISTS t", db)
    for sql in case.setup:
        exec_ok(args, sql, db)
    base_job = max_delete_range_job(args)
    alter = run_mysql(args, case.alter, db)
    if alter.rc != 0:
        return CaseOutcome(case.name, "FINDING", "DDL failed before delete-range oracle: " + alter.err)
    records = read_new_records(args, base_job)
    index_ranges = [r for r in records if r.is_index_range]
    table_ranges = [r for r in records if not r.is_index_range]
    if len(index_ranges) < case.min_index_ranges:
        return CaseOutcome(
            case.name,
            "FINDING",
            f"missing index delete-range records: index={len(index_ranges)} table={len(table_ranges)} records={records}",
        )
    if len(table_ranges) < case.min_table_ranges:
        return CaseOutcome(
            case.name,
            "FINDING",
            f"missing table delete-range records: index={len(index_ranges)} table={len(table_ranges)} records={records}",
        )
    if case.max_table_ranges is not None and len(table_ranges) > case.max_table_ranges:
        return CaseOutcome(
            case.name,
            "FINDING",
            f"unexpected table delete-range records: index={len(index_ranges)} table={len(table_ranges)} records={records}",
        )
    jobs = sorted({r.job_id for r in records})
    return CaseOutcome(
        case.name,
        "OK",
        f"jobs={jobs} index_ranges={len(index_ranges)} table_ranges={len(table_ranges)}",
    )


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default=os.environ.get("MYSQL", "mysql"))
    parser.add_argument("--host", default=os.environ.get("MYSQL_HOST", "127.0.0.1"))
    parser.add_argument("--port", default=os.environ.get("MYSQL_PORT", "14000"))
    parser.add_argument("--user", default=os.environ.get("MYSQL_USER", "root"))
    parser.add_argument("--database-prefix", default="ai_native_ddl_delrange")
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

    original_gc = current_gc_enable(args)
    exec_ok(args, "SET GLOBAL tidb_gc_enable=OFF")
    exec_ok(args, f"DROP DATABASE IF EXISTS {quote_db(db)}")
    exec_ok(args, f"CREATE DATABASE {quote_db(db)}")

    outcomes: list[CaseOutcome] = []
    try:
        for case in cases():
            outcome = run_case(args, db, case)
            outcomes.append(outcome)
            print("\t".join([outcome.status, "delete-range object-reference", case.name, outcome.detail.replace("\n", " ")[:500]]))
    finally:
        run_mysql(args, "DROP TABLE IF EXISTS t", db)
        if not args.keep_db:
            run_mysql(args, f"DROP DATABASE IF EXISTS {quote_db(db)}")
        exec_ok(args, f"SET GLOBAL tidb_gc_enable={original_gc}")

    unexpected = [out for out in outcomes if out.status == "FINDING"]
    print(f"SUMMARY total={len(outcomes)} findings={len(unexpected)} skipped=0")
    if unexpected:
        print("UNEXPECTED_FINDINGS")
        for out in unexpected:
            print(f"- {out.name}: {out.detail}")
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
