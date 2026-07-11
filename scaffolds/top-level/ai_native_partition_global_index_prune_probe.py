#!/usr/bin/env python3
"""Global-index partition-pruning probe.

Oracle:
  ref unpartitioned rows == partitioned table rows forced through a global index.

The selector is intentionally narrow: dynamic partition pruning keeps global
index paths and adds a _tidb_tid filter; static pruning removes global index
paths. Both paths must still produce the same user-visible rows.
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
class PredicateCase:
    name: str
    where: str


@dataclasses.dataclass
class TableCase:
    name: str
    create_sql: str
    create_ref_sql: str
    insert_sql: str
    select_expr: str
    predicates: list[PredicateCase]


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
    return [] if res.out == "" else res.out.splitlines()


def quote_db(name: str) -> str:
    return "`" + name.replace("`", "``") + "`"


def dedupe(predicates: list[PredicateCase]) -> list[PredicateCase]:
    seen: set[str] = set()
    out: list[PredicateCase] = []
    for pred in predicates:
        if pred.where in seen:
            continue
        seen.add(pred.where)
        out.append(pred)
    return out


def numeric_preds(col: str, edges: list[int], nullable: bool = True) -> list[PredicateCase]:
    values = sorted(set(v for edge in edges for v in (edge - 1, edge, edge + 1)))
    out: list[PredicateCase] = []
    if nullable:
        out.extend(
            [
                PredicateCase(f"{col}_is_null", f"{col} IS NULL"),
                PredicateCase(f"{col}_null_safe_null", f"{col} <=> NULL"),
            ]
        )
    for v in values:
        label = str(v).replace("-", "neg")
        out.extend(
            [
                PredicateCase(f"{col}_eq_{label}", f"{col} = {v}"),
                PredicateCase(f"{col}_lt_{label}", f"{col} < {v}"),
                PredicateCase(f"{col}_le_{label}", f"{col} <= {v}"),
                PredicateCase(f"{col}_gt_{label}", f"{col} > {v}"),
                PredicateCase(f"{col}_ge_{label}", f"{col} >= {v}"),
                PredicateCase(f"{col}_ne_{label}", f"{col} != {v}"),
                PredicateCase(f"{col}_null_safe_{label}", f"{col} <=> {v}"),
            ]
        )
    if values:
        out.extend(
            [
                PredicateCase(f"{col}_between_mid", f"{col} BETWEEN {values[1]} AND {values[-2]}"),
                PredicateCase(f"{col}_in_edges", f"{col} IN ({','.join(str(v) for v in values[::2])})"),
                PredicateCase(f"{col}_not_in_edges", f"{col} NOT IN ({','.join(str(v) for v in values[1::2])})"),
                PredicateCase(
                    f"{col}_or_split",
                    f"({col} BETWEEN {values[0]} AND {values[min(2, len(values)-1)]}) OR "
                    f"({col} BETWEEN {values[max(0, len(values)-3)]} AND {values[-1]})",
                ),
            ]
        )
        if nullable:
            out.append(PredicateCase(f"{col}_or_null", f"({col} < {values[len(values)//2]}) OR ({col} IS NULL)"))
    return dedupe(out)


def table_cases() -> list[TableCase]:
    int_rows = ",".join(
        [
            "(1,NULL,100,'null')",
            "(2,-1,101,'neg')",
            "(3,0,102,'zero')",
            "(4,1,103,'one')",
            "(5,9,104,'nine')",
            "(6,10,105,'ten')",
            "(7,11,106,'eleven')",
            "(8,19,107,'nineteen')",
            "(9,20,108,'twenty')",
            "(10,99,109,'max')",
        ]
    )
    int_preds = dedupe(
        numeric_preds("a", [0, 10, 20])
        + [
            PredicateCase("or_null_and_range", "(a IS NULL) OR (a BETWEEN 9 AND 11)"),
            PredicateCase("not_between", "NOT (a BETWEEN 0 AND 10)"),
            PredicateCase("case_wrapped", "CASE WHEN a < 10 THEN 1 ELSE 0 END = 1"),
        ]
    )

    multi_rows = ",".join(
        [
            "(1,NULL,0,200,'null0')",
            "(2,-1,9,201,'neg9')",
            "(3,0,0,202,'zero0')",
            "(4,0,2,203,'zero2')",
            "(5,1,1,204,'one1')",
            "(6,2,9,205,'two9')",
            "(7,3,0,206,'three0')",
            "(8,3,3,207,'three3')",
            "(9,9,9,208,'nine9')",
            "(10,10,0,209,'ten0')",
        ]
    )
    multi_preds = dedupe(
        numeric_preds("a", [0, 3, 10])
        + numeric_preds("b", [0, 3, 10], nullable=True)
        + [
            PredicateCase("point_prefix", "a = 0 AND b = 2"),
            PredicateCase("prefix_tail_low", "a = 3 AND b < 3"),
            PredicateCase("prefix_tail_high", "a = 3 AND b >= 3"),
            PredicateCase("or_prefixes", "(a = 0 AND b = 2) OR (a = 3 AND b = 0)"),
            PredicateCase("null_or_prefix", "(a IS NULL) OR (a = 0 AND b = 0)"),
            PredicateCase("lex_range", "(a > 0 OR (a = 0 AND b >= 2)) AND (a < 10)"),
        ]
    )

    list_rows = ",".join(
        [
            "(1,NULL,300,'null')",
            "(2,-1,301,'neg')",
            "(3,0,302,'zero')",
            "(4,1,303,'one')",
            "(5,2,304,'two-default')",
            "(6,3,305,'three')",
            "(7,4,306,'four-default')",
            "(8,10,307,'ten')",
            "(9,11,308,'eleven-default')",
        ]
    )
    list_preds = dedupe(
        numeric_preds("a", [0, 1, 3, 10])
        + [
            PredicateCase("not_in_values", "a NOT IN (-1,0,1,3,10)"),
            PredicateCase("in_default_and_listed", "a IN (1,2,3,4,10,11)"),
            PredicateCase("range_hits_default", "a BETWEEN 1 AND 4"),
            PredicateCase("or_null_default", "(a IS NULL) OR (a = 2) OR (a = 10)"),
        ]
    )

    list_multi_rows = ",".join(
        [
            "(1,1,1,400,'p0-11')",
            "(2,1,2,401,'p0-12')",
            "(3,1,3,402,'def-13')",
            "(4,2,1,403,'p0-21')",
            "(5,2,2,404,'def-22')",
            "(6,3,3,405,'p1-33')",
            "(7,NULL,3,406,'p1-null3')",
            "(8,NULL,4,407,'def-null4')",
            "(9,4,NULL,408,'def-4null')",
        ]
    )
    list_multi_preds = dedupe(
        numeric_preds("a", [1, 2, 3], nullable=True)
        + numeric_preds("b", [1, 2, 3], nullable=True)
        + [
            PredicateCase("point_in_p0", "a = 1 AND b = 1"),
            PredicateCase("same_a_default", "a = 1 AND b = 3"),
            PredicateCase("both_default", "a = 2 AND b = 2"),
            PredicateCase("null_tuple_listed", "a IS NULL AND b = 3"),
            PredicateCase("null_tuple_default", "a IS NULL AND b = 4"),
            PredicateCase("b_only", "b = 1"),
            PredicateCase("a_only", "a = 1"),
            PredicateCase("or_default_and_listed", "(a = 1 AND b = 1) OR (a = 2 AND b = 2)"),
            PredicateCase("or_null_tuple", "(a IS NULL AND b = 3) OR (a = 4 AND b IS NULL)"),
        ]
    )

    return [
        TableCase(
            name="range_columns_int_global",
            create_sql=f"""
CREATE TABLE t(
  id INT,
  a INT NULL,
  c INT NOT NULL,
  tag VARCHAR(32),
  KEY g(c) GLOBAL,
  KEY idk(id)
) PARTITION BY RANGE COLUMNS(a) (
  PARTITION pnull VALUES LESS THAN (0),
  PARTITION p0 VALUES LESS THAN (10),
  PARTITION p1 VALUES LESS THAN (20),
  PARTITION pmax VALUES LESS THAN (MAXVALUE)
)""",
            create_ref_sql="""
CREATE TABLE t_ref(
  id INT,
  a INT NULL,
  c INT NOT NULL,
  tag VARCHAR(32),
  KEY g(c),
  KEY idk(id)
)""",
            insert_sql=f"INSERT INTO t VALUES {int_rows}",
            select_expr="CONCAT_WS(',', id, IFNULL(CAST(a AS CHAR), 'NULL'), c, tag)",
            predicates=int_preds,
        ),
        TableCase(
            name="range_columns_multi_global",
            create_sql="""
CREATE TABLE t(
  id INT,
  a INT NULL,
  b INT NULL,
  c INT NOT NULL,
  tag VARCHAR(32),
  KEY g(c) GLOBAL,
  KEY idk(id)
) PARTITION BY RANGE COLUMNS(a,b) (
  PARTITION p0 VALUES LESS THAN (0,0),
  PARTITION p1 VALUES LESS THAN (3,3),
  PARTITION p2 VALUES LESS THAN (10,10),
  PARTITION pmax VALUES LESS THAN (MAXVALUE,MAXVALUE)
)""",
            create_ref_sql="""
CREATE TABLE t_ref(
  id INT,
  a INT NULL,
  b INT NULL,
  c INT NOT NULL,
  tag VARCHAR(32),
  KEY g(c),
  KEY idk(id)
)""",
            insert_sql=f"INSERT INTO t VALUES {multi_rows}",
            select_expr="CONCAT_WS(',', id, IFNULL(CAST(a AS CHAR), 'NULL'), IFNULL(CAST(b AS CHAR), 'NULL'), c, tag)",
            predicates=multi_preds,
        ),
        TableCase(
            name="list_columns_int_default_global",
            create_sql="""
CREATE TABLE t(
  id INT,
  a INT NULL,
  c INT NOT NULL,
  tag VARCHAR(32),
  KEY g(c) GLOBAL,
  KEY idk(id)
) PARTITION BY LIST COLUMNS(a) (
  PARTITION pnull VALUES IN (NULL),
  PARTITION p0 VALUES IN (-1,0,1),
  PARTITION p1 VALUES IN (3,10),
  PARTITION pdef DEFAULT
)""",
            create_ref_sql="""
CREATE TABLE t_ref(
  id INT,
  a INT NULL,
  c INT NOT NULL,
  tag VARCHAR(32),
  KEY g(c),
  KEY idk(id)
)""",
            insert_sql=f"INSERT INTO t VALUES {list_rows}",
            select_expr="CONCAT_WS(',', id, IFNULL(CAST(a AS CHAR), 'NULL'), c, tag)",
            predicates=list_preds,
        ),
        TableCase(
            name="list_columns_multi_default_global",
            create_sql="""
CREATE TABLE t(
  id INT,
  a INT NULL,
  b INT NULL,
  c INT NOT NULL,
  tag VARCHAR(32),
  KEY g(c) GLOBAL,
  KEY idk(id)
) PARTITION BY LIST COLUMNS(a,b) (
  PARTITION p0 VALUES IN ((1,1),(1,2),(2,1)),
  PARTITION p1 VALUES IN ((3,3),(NULL,3)),
  PARTITION pdef DEFAULT
)""",
            create_ref_sql="""
CREATE TABLE t_ref(
  id INT,
  a INT NULL,
  b INT NULL,
  c INT NOT NULL,
  tag VARCHAR(32),
  KEY g(c),
  KEY idk(id)
)""",
            insert_sql=f"INSERT INTO t VALUES {list_multi_rows}",
            select_expr="CONCAT_WS(',', id, IFNULL(CAST(a AS CHAR), 'NULL'), IFNULL(CAST(b AS CHAR), 'NULL'), c, tag)",
            predicates=list_multi_preds,
        ),
    ]


def run_mode(args: argparse.Namespace, db: str, mode: str, sql: str) -> Result:
    return run_mysql(args, f"SET @@session.tidb_partition_prune_mode = '{mode}';\n{sql}", db)


def explain(args: argparse.Namespace, db: str, mode: str, sql: str) -> str:
    res = run_mode(args, db, mode, "EXPLAIN FORMAT='brief' " + sql)
    if res.rc != 0:
        return res.err
    return " | ".join(rows(res)[:8])


def select_sql(table: str, select_expr: str, pred: str, use_global: bool) -> str:
    hint = " USE INDEX(g)" if use_global else ""
    return f"SELECT {select_expr} FROM {table}{hint} WHERE {pred} ORDER BY c, id"


def run_case(args: argparse.Namespace, db: str, case: TableCase) -> int:
    findings = 0
    exec_ok(args, "DROP TABLE IF EXISTS t", db)
    exec_ok(args, "DROP TABLE IF EXISTS t_ref", db)
    exec_ok(args, case.create_sql, db)
    exec_ok(args, case.create_ref_sql, db)
    exec_ok(args, case.insert_sql, db)
    exec_ok(args, case.insert_sql.replace("INSERT INTO t VALUES", "INSERT INTO t_ref VALUES", 1), db)

    predicates = case.predicates
    if args.max_predicates > 0:
        predicates = predicates[: args.max_predicates]
    print(f"PREDICATES case={case.name} count={len(predicates)}", flush=True)
    for i, pred in enumerate(predicates, 1):
        if args.progress_every > 0 and (i == 1 or i % args.progress_every == 0 or i == len(predicates)):
            print(f"PROGRESS case={case.name} {i}/{len(predicates)} pred={pred.name}", flush=True)

        ref_sql = select_sql("t_ref", case.select_expr, pred.where, False)
        part_sql = select_sql("t", case.select_expr, pred.where, True)
        ref = run_mysql(args, ref_sql, db)
        dynamic = run_mode(args, db, "dynamic", part_sql)
        static = run_mode(args, db, "static", part_sql)
        if ref.rc != dynamic.rc or ref.rc != static.rc or rows(ref) != rows(dynamic) or rows(ref) != rows(static):
            findings += 1
            print(
                "\n".join(
                    [
                        f"HIT table={case.name} pred={pred.name}",
                        f"where={pred.where}",
                        f"ref_rc={ref.rc} dynamic_rc={dynamic.rc} static_rc={static.rc}",
                        f"ref_rows={rows(ref)}",
                        f"dynamic_rows={rows(dynamic)}",
                        f"static_rows={rows(static)}",
                        f"ref_err={ref.err}",
                        f"dynamic_err={dynamic.err}",
                        f"static_err={static.err}",
                        f"dynamic_plan={explain(args, db, 'dynamic', part_sql)}",
                        f"static_plan={explain(args, db, 'static', part_sql)}",
                    ]
                ),
                flush=True,
            )
    return findings


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default=os.environ.get("MYSQL", "mysql"))
    parser.add_argument("--host", default=os.environ.get("TIDB_HOST", "127.0.0.1"))
    parser.add_argument("--port", type=int, default=int(os.environ.get("TIDB_PORT", "14000")))
    parser.add_argument("--user", default=os.environ.get("TIDB_USER", "root"))
    parser.add_argument("--db-prefix", default="ai_native_global_prune")
    parser.add_argument("--case", default="", help="run only cases whose name contains this substring")
    parser.add_argument("--max-predicates", type=int, default=0, help="per-case cap; 0 means no cap")
    parser.add_argument("--progress-every", type=int, default=50)
    args = parser.parse_args()

    db = f"{args.db_prefix}_{int(time.time())}"
    print(f"mysql={shlex.join(mysql_args(args))}")
    print(f"database={db}")
    exec_ok(args, f"DROP DATABASE IF EXISTS {quote_db(db)}")
    exec_ok(args, f"CREATE DATABASE {quote_db(db)}")

    findings = 0
    try:
        for case in table_cases():
            if args.case and args.case not in case.name:
                continue
            print(f"CASE {case.name}", flush=True)
            findings += run_case(args, db, case)
    finally:
        exec_ok(args, f"DROP DATABASE IF EXISTS {quote_db(db)}")

    print(f"SUMMARY findings={findings}")
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main())
