#!/usr/bin/env python3
"""Plan-cache parameter-drift proof-obligation probe.

The probe checks whether a proof made for one parameter remains safe after the
same prepared statement is executed with another parameter. The oracle compares:

* prepared execution with plan cache enabled
* prepared execution with plan cache disabled
* direct literal execution
"""

from __future__ import annotations

import argparse
import dataclasses
import os
import shlex
import subprocess
import sys
import time


@dataclasses.dataclass
class Result:
    rc: int
    out: str
    err: str


@dataclasses.dataclass
class ExecBlock:
    rows: list[str]
    last_plan_from_cache: str


@dataclasses.dataclass
class DriftCase:
    name: str
    setup: list[str]
    prepare_sql: str
    direct_sql_template: str
    params: list[tuple[str, ...]]


@dataclasses.dataclass
class Finding:
    case_name: str
    params: tuple[str, ...]
    cached_rows: list[str]
    nocache_rows: list[str]
    direct_rows: list[str]
    cached_last_plan: str
    nocache_last_plan: str


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


def rows(res: Result) -> list[str]:
    if res.out == "":
        return []
    return res.out.splitlines()


def quote_db(name: str) -> str:
    return "`" + name.replace("`", "``") + "`"


def sql_quote(value: str) -> str:
    if value.upper() == "NULL":
        return "NULL"
    return "'" + value.replace("'", "''") + "'"


def value_expr(value: str) -> str:
    if value.upper() == "NULL":
        return "NULL"
    try:
        int(value)
        return value
    except ValueError:
        return sql_quote(value)


def parse_marked_rows(out_rows: list[str]) -> dict[str, list[str]]:
    blocks: dict[str, list[str]] = {}
    current = ""
    for row in out_rows:
        if row.startswith("MARK\t"):
            current = row.split("\t", 1)[1]
            blocks[current] = []
            continue
        if row.startswith("LAST\t"):
            parts = row.split("\t")
            if len(parts) >= 3:
                blocks[f"{parts[1]}_last_plan"] = [parts[2]]
            current = ""
            continue
        if current:
            blocks[current].append(row)
    return blocks


def format_param_set(params: tuple[str, ...]) -> str:
    return "(" + ", ".join(params) + ")"


def direct_sql(case: DriftCase, params: tuple[str, ...]) -> str:
    values = {f"p{i}": value_expr(param) for i, param in enumerate(params)}
    if len(params) == 1:
        values["param"] = values["p0"]
    return case.direct_sql_template.format(**values)


def run_prepared_sequence(args: argparse.Namespace, db: str, case: DriftCase, cache_enabled: bool) -> dict[str, ExecBlock]:
    stmts = [
        f"SET tidb_enable_prepared_plan_cache = {1 if cache_enabled else 0}",
        "SET tidb_enable_collect_execution_info = 0",
        "PREPARE stmt FROM " + sql_quote(case.prepare_sql),
    ]
    for i, params in enumerate(case.params):
        param_vars = []
        for j, param in enumerate(params):
            var_name = f"@p{j}"
            param_vars.append(var_name)
            stmts.append(f"SET {var_name} = {value_expr(param)}")
        stmts.extend(
            [
                f"SELECT 'MARK', 'p{i}'",
                "EXECUTE stmt USING " + ", ".join(param_vars),
                f"SELECT 'LAST', 'p{i}', @@last_plan_from_cache",
            ]
        )
    stmts.append("DEALLOCATE PREPARE stmt")
    res = run_mysql(args, ";\n".join(stmts) + ";", db)
    if res.rc != 0:
        raise RuntimeError(f"prepared sequence failed case={case.name} cache={cache_enabled}: {res.err}")
    blocks = parse_marked_rows(rows(res))
    out: dict[str, ExecBlock] = {}
    for i, _ in enumerate(case.params):
        last = blocks.get(f"p{i}_last_plan", [""])[0] if blocks.get(f"p{i}_last_plan") else ""
        out[f"p{i}"] = ExecBlock(rows=blocks.get(f"p{i}", []), last_plan_from_cache=last)
    return out


def direct_rows(args: argparse.Namespace, db: str, case: DriftCase, params: tuple[str, ...]) -> list[str]:
    sql = direct_sql(case, params)
    res = run_mysql(args, sql, db)
    if res.rc != 0:
        raise RuntimeError(f"direct query failed case={case.name} params={format_param_set(params)}: {res.err}")
    return rows(res)


def cases() -> list[DriftCase]:
    base_rows = "VALUES (1,NULL,1,'n'),(2,-1,2,'neg'),(3,0,3,'zero'),(4,1,4,'one'),(5,2,5,'two'),(6,3,6,'three'),(7,10,7,'ten'),(8,20,8,'twenty')"
    return [
        DriftCase(
            name="point_get_cache_baseline",
            setup=[
                "DROP TABLE IF EXISTS t",
                "CREATE TABLE t(id INT PRIMARY KEY, a INT, tag VARCHAR(20))",
                "INSERT INTO t VALUES (1,10,'one'),(2,20,'two'),(3,30,'three')",
            ],
            prepare_sql="SELECT CONCAT_WS(',', id, a, tag) FROM t WHERE id = ? ORDER BY id",
            direct_sql_template="SELECT CONCAT_WS(',', id, a, tag) FROM t WHERE id = {param} ORDER BY id",
            params=[("1",), ("2",), ("3",)],
        ),
        DriftCase(
            name="normal_index_range_cache_baseline",
            setup=[
                "DROP TABLE IF EXISTS t",
                "CREATE TABLE t(id INT PRIMARY KEY, a INT, tag VARCHAR(20), KEY ia(a))",
                "INSERT INTO t VALUES (1,-1,'neg'),(2,0,'zero'),(3,1,'one'),(4,3,'three'),(5,10,'ten')",
            ],
            prepare_sql="SELECT CONCAT_WS(',', id, a, tag) FROM t WHERE a < ? ORDER BY id",
            direct_sql_template="SELECT CONCAT_WS(',', id, a, tag) FROM t WHERE a < {param} ORDER BY id",
            params=[("0",), ("3",), ("11",)],
        ),
        DriftCase(
            name="limit_param_cache_key_guard",
            setup=[
                "DROP TABLE IF EXISTS t",
                "CREATE TABLE t(id INT PRIMARY KEY, a INT, tag VARCHAR(20))",
                "INSERT INTO t VALUES (1,10,'one'),(2,20,'two'),(3,30,'three'),(4,40,'four')",
            ],
            prepare_sql="SELECT CONCAT_WS(',', id, a, tag) FROM t ORDER BY id LIMIT ?",
            direct_sql_template="SELECT CONCAT_WS(',', id, a, tag) FROM t ORDER BY id LIMIT {param}",
            params=[("1",), ("3",), ("1",)],
        ),
        DriftCase(
            name="partial_index_is_not_null_nulleq",
            setup=[
                "DROP TABLE IF EXISTS t",
                "CREATE TABLE t(id INT PRIMARY KEY, a INT NULL, b INT, tag VARCHAR(20))",
                "INSERT INTO t " + base_rows,
                "ALTER TABLE t ADD INDEX pi(b) WHERE a IS NOT NULL",
            ],
            prepare_sql="SELECT CONCAT_WS(',', id, IFNULL(a, 'NULL'), b, tag) FROM t USE INDEX(pi) WHERE a <=> ? ORDER BY id",
            direct_sql_template="SELECT CONCAT_WS(',', id, IFNULL(a, 'NULL'), b, tag) FROM t IGNORE INDEX(pi) WHERE a <=> {param} ORDER BY id",
            params=[("10",), ("NULL",), ("-1",)],
        ),
        DriftCase(
            name="partial_index_is_not_null_eq",
            setup=[
                "DROP TABLE IF EXISTS t",
                "CREATE TABLE t(id INT PRIMARY KEY, a INT NULL, b INT, tag VARCHAR(20))",
                "INSERT INTO t " + base_rows,
                "ALTER TABLE t ADD INDEX pi(b) WHERE a IS NOT NULL",
            ],
            prepare_sql="SELECT CONCAT_WS(',', id, IFNULL(a, 'NULL'), b, tag) FROM t USE INDEX(pi) WHERE a = ? ORDER BY id",
            direct_sql_template="SELECT CONCAT_WS(',', id, IFNULL(a, 'NULL'), b, tag) FROM t IGNORE INDEX(pi) WHERE a = {param} ORDER BY id",
            params=[("1",), ("NULL",), ("-1",), ("20",)],
        ),
        DriftCase(
            name="partial_index_gt_threshold",
            setup=[
                "DROP TABLE IF EXISTS t",
                "CREATE TABLE t(id INT PRIMARY KEY, a INT NULL, b INT, tag VARCHAR(20))",
                "INSERT INTO t " + base_rows,
                "ALTER TABLE t ADD INDEX pi(b) WHERE a > 10",
            ],
            prepare_sql="SELECT CONCAT_WS(',', id, IFNULL(a, 'NULL'), b, tag) FROM t USE INDEX(pi) WHERE a > ? ORDER BY id",
            direct_sql_template="SELECT CONCAT_WS(',', id, IFNULL(a, 'NULL'), b, tag) FROM t IGNORE INDEX(pi) WHERE a > {param} ORDER BY id",
            params=[("15",), ("0",), ("10",)],
        ),
        DriftCase(
            name="partition_range_boundary",
            setup=[
                "DROP TABLE IF EXISTS t",
                """
CREATE TABLE t(
  id INT,
  a INT NULL,
  tag VARCHAR(20),
  KEY(id)
) PARTITION BY RANGE COLUMNS(a) (
  PARTITION p0 VALUES LESS THAN (0),
  PARTITION p1 VALUES LESS THAN (3),
  PARTITION pmax VALUES LESS THAN (MAXVALUE)
)""",
                "INSERT INTO t VALUES (1,NULL,'n'),(2,-1,'neg'),(3,0,'zero'),(4,1,'one'),(5,2,'two'),(6,3,'three'),(7,10,'ten')",
            ],
            prepare_sql="SELECT CONCAT_WS(',', id, IFNULL(a, 'NULL'), tag) FROM t WHERE a < ? ORDER BY id",
            direct_sql_template="SELECT CONCAT_WS(',', id, IFNULL(a, 'NULL'), tag) FROM t WHERE a < {param} ORDER BY id",
            params=[("0",), ("3",), ("11",)],
        ),
        DriftCase(
            name="partition_dynamic_between_boundary",
            setup=[
                "SET SESSION tidb_partition_prune_mode = 'dynamic'",
                "DROP TABLE IF EXISTS t",
                """
CREATE TABLE t(
  id INT,
  a INT NULL,
  tag VARCHAR(20),
  KEY(a)
) PARTITION BY RANGE COLUMNS(a) (
  PARTITION p0 VALUES LESS THAN (0),
  PARTITION p1 VALUES LESS THAN (3),
  PARTITION p2 VALUES LESS THAN (8),
  PARTITION pmax VALUES LESS THAN (MAXVALUE)
)""",
                "INSERT INTO t VALUES (1,NULL,'n'),(2,-1,'neg'),(3,0,'zero'),(4,1,'one'),(5,2,'two'),(6,3,'three'),(7,7,'seven'),(8,8,'eight'),(9,10,'ten')",
            ],
            prepare_sql="SELECT CONCAT_WS(',', id, IFNULL(a, 'NULL'), tag) FROM t WHERE a >= ? AND a < ? ORDER BY id",
            direct_sql_template="SELECT CONCAT_WS(',', id, IFNULL(a, 'NULL'), tag) FROM t WHERE a >= {p0} AND a < {p1} ORDER BY id",
            params=[("0", "3"), ("3", "8"), ("-10", "0"), ("0", "100"), ("8", "2")],
        ),
        DriftCase(
            name="partition_dynamic_list_default_range",
            setup=[
                "SET SESSION tidb_partition_prune_mode = 'dynamic'",
                "DROP TABLE IF EXISTS t",
                """
CREATE TABLE t(
  id INT,
  a INT NULL,
  tag VARCHAR(20),
  KEY(id)
) PARTITION BY LIST COLUMNS(a) (
  PARTITION pnull VALUES IN (NULL),
  PARTITION p0 VALUES IN (-1,0,1),
  PARTITION p1 VALUES IN (3,10),
  PARTITION pdef DEFAULT
)""",
                "INSERT INTO t VALUES (1,NULL,'null'),(2,-1,'neg'),(3,0,'zero'),(4,1,'one'),(5,2,'two-default'),(6,3,'three'),(7,4,'four-default'),(8,10,'ten'),(9,11,'eleven-default')",
            ],
            prepare_sql="SELECT CONCAT_WS(',', id, IFNULL(a, 'NULL'), tag) FROM t WHERE a > ? ORDER BY id",
            direct_sql_template="SELECT CONCAT_WS(',', id, IFNULL(a, 'NULL'), tag) FROM t WHERE a > {param} ORDER BY id",
            params=[("0",), ("2",), ("9",), ("NULL",), ("-2",)],
        ),
        DriftCase(
            name="partition_dynamic_list_multi_default_point",
            setup=[
                "SET SESSION tidb_partition_prune_mode = 'dynamic'",
                "DROP TABLE IF EXISTS t",
                """
CREATE TABLE t(
  id INT,
  a INT NULL,
  b INT NULL,
  tag VARCHAR(20),
  KEY(id)
) PARTITION BY LIST COLUMNS(a,b) (
  PARTITION p0 VALUES IN ((1,1),(1,2),(2,1)),
  PARTITION p1 VALUES IN ((3,3),(NULL,3)),
  PARTITION pdef DEFAULT
)""",
                "INSERT INTO t VALUES (1,1,1,'p0-11'),(2,1,2,'p0-12'),(3,1,3,'def-13'),(4,2,1,'p0-21'),(5,2,2,'def-22'),(6,3,3,'p1-33'),(7,NULL,3,'p1-null3'),(8,NULL,4,'def-null4'),(9,4,NULL,'def-4null')",
            ],
            prepare_sql="SELECT CONCAT_WS(',', id, IFNULL(a, 'NULL'), IFNULL(b, 'NULL'), tag) FROM t WHERE a = ? AND b = ? ORDER BY id",
            direct_sql_template="SELECT CONCAT_WS(',', id, IFNULL(a, 'NULL'), IFNULL(b, 'NULL'), tag) FROM t WHERE a = {p0} AND b = {p1} ORDER BY id",
            params=[("1", "1"), ("1", "3"), ("2", "2"), ("3", "3"), ("4", "4")],
        ),
        DriftCase(
            name="predicate_simplification_null_in",
            setup=[
                "DROP TABLE IF EXISTS t",
                "CREATE TABLE t(id INT PRIMARY KEY, a INT NULL, tag VARCHAR(20))",
                "INSERT INTO t VALUES (1,NULL,'n'),(2,1,'one'),(3,2,'two'),(4,3,'three')",
            ],
            prepare_sql="SELECT CONCAT_WS(',', id, IFNULL(a, 'NULL'), tag) FROM t WHERE a IS NULL AND a IN (?, 1, 2) ORDER BY id",
            direct_sql_template="SELECT CONCAT_WS(',', id, IFNULL(a, 'NULL'), tag) FROM t WHERE a IS NULL AND a IN ({param}, 1, 2) ORDER BY id",
            params=[("3",), ("NULL",)],
        ),
        DriftCase(
            name="index_merge_or_point_range",
            setup=[
                "DROP TABLE IF EXISTS t",
                "CREATE TABLE t(a INT PRIMARY KEY, b INT, c INT, tag VARCHAR(20), KEY ib(b), KEY ic(c))",
                "INSERT INTO t VALUES (1,1,9,'one'),(2,2,8,'two'),(3,3,7,'three'),(4,4,6,'four'),(5,5,5,'five'),(6,6,4,'six'),(7,7,3,'seven'),(8,8,2,'eight'),(9,9,1,'nine'),(10,10,10,'ten'),(11,10,11,'eleven'),(12,11,10,'twelve')",
            ],
            prepare_sql="SELECT /*+ use_index_merge(t) */ CONCAT_WS(',', a, b, c, tag) FROM t WHERE c = ? OR (b = ? AND a >= ? AND a <= ?) ORDER BY a",
            direct_sql_template="SELECT /*+ use_index_merge(t) */ CONCAT_WS(',', a, b, c, tag) FROM t WHERE c = {p0} OR (b = {p1} AND a >= {p2} AND a <= {p3}) ORDER BY a",
            params=[("10", "10", "10", "10"), ("11", "10", "10", "12"), ("10", "10", "9", "11"), ("10", "10", "11", "9")],
        ),
    ]


def run_case(args: argparse.Namespace, db: str, case: DriftCase) -> list[Finding]:
    for sql in case.setup:
        exec_ok(args, sql, db)
    cached = run_prepared_sequence(args, db, case, True)
    nocache = run_prepared_sequence(args, db, case, False)
    findings: list[Finding] = []
    for i, params in enumerate(case.params):
        key = f"p{i}"
        direct = direct_rows(args, db, case, params)
        cached_block = cached[key]
        nocache_block = nocache[key]
        if args.trace:
            print(
                "TRACE "
                f"case={case.name} params={format_param_set(params)} "
                f"cached_last_plan={cached_block.last_plan_from_cache} "
                f"nocache_last_plan={nocache_block.last_plan_from_cache} "
                f"rows={len(direct)}",
                flush=True,
            )
        if cached_block.rows != direct or nocache_block.rows != direct:
            findings.append(
                Finding(
                    case_name=case.name,
                    params=params,
                    cached_rows=cached_block.rows,
                    nocache_rows=nocache_block.rows,
                    direct_rows=direct,
                    cached_last_plan=cached_block.last_plan_from_cache,
                    nocache_last_plan=nocache_block.last_plan_from_cache,
                )
            )
    exec_ok(args, "DROP TABLE IF EXISTS t", db)
    return findings


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default=os.environ.get("MYSQL", "mysql"))
    parser.add_argument("--host", default=os.environ.get("TIDB_HOST", "127.0.0.1"))
    parser.add_argument("--port", type=int, default=int(os.environ.get("TIDB_PORT", "14000")))
    parser.add_argument("--user", default=os.environ.get("TIDB_USER", "root"))
    parser.add_argument("--db-prefix", default="ai_native_pc_drift")
    parser.add_argument("--case", default="", help="run only cases whose name contains this substring")
    parser.add_argument("--trace", action="store_true", help="print cache-hit evidence for every parameter")
    args = parser.parse_args()

    db = f"{args.db_prefix}_{int(time.time())}"
    print(f"mysql={shlex.join(mysql_args(args))}")
    print(f"database={db}")
    exec_ok(args, f"DROP DATABASE IF EXISTS {quote_db(db)}")
    exec_ok(args, f"CREATE DATABASE {quote_db(db)}")

    findings: list[Finding] = []
    try:
        for case in cases():
            if args.case and args.case not in case.name:
                continue
            print(f"CASE {case.name}", flush=True)
            got = run_case(args, db, case)
            findings.extend(got)
            for item in got:
                print(
                    "\n".join(
                        [
                            f"HIT case={item.case_name} params={format_param_set(item.params)}",
                            f"cached_last_plan={item.cached_last_plan} nocache_last_plan={item.nocache_last_plan}",
                            f"direct_rows={item.direct_rows}",
                            f"cached_rows={item.cached_rows}",
                            f"nocache_rows={item.nocache_rows}",
                        ]
                    ),
                    flush=True,
                )
    finally:
        exec_ok(args, f"DROP DATABASE IF EXISTS {quote_db(db)}")

    print(f"SUMMARY findings={len(findings)}")
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main())
