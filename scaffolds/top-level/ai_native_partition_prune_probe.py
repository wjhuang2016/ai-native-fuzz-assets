#!/usr/bin/env python3
"""Partition-pruning proof-obligation probe.

The probe compares three paths on the same stable rows:

* unpartitioned reference table
* partitioned table with static pruning
* partitioned table with dynamic pruning

Each case places rows on both sides of partition boundaries, then derives
predicates from those boundaries to attack the pruning proof.
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


@dataclasses.dataclass
class Finding:
    table_case: str
    predicate_case: str
    where: str
    ref_rows: list[str]
    static_rows: list[str]
    dynamic_rows: list[str]
    static_plan: str
    dynamic_plan: str


def dedupe_predicates(predicates: list[PredicateCase]) -> list[PredicateCase]:
    seen: set[str] = set()
    out: list[PredicateCase] = []
    for pred in predicates:
        if pred.where in seen:
            continue
        seen.add(pred.where)
        out.append(pred)
    return out


def numeric_boundary_predicates(col: str, edges: list[int], nullable: bool = True) -> list[PredicateCase]:
    predicates: list[PredicateCase] = []
    if nullable:
        predicates.append(PredicateCase(f"{col}_is_null", f"{col} IS NULL"))
    values = sorted(set(v for edge in edges for v in (edge - 1, edge, edge + 1)))
    for v in values:
        label = str(v).replace("-", "neg")
        predicates.extend(
            [
                PredicateCase(f"{col}_eq_{label}", f"{col} = {v}"),
                PredicateCase(f"{col}_lt_{label}", f"{col} < {v}"),
                PredicateCase(f"{col}_le_{label}", f"{col} <= {v}"),
                PredicateCase(f"{col}_gt_{label}", f"{col} > {v}"),
                PredicateCase(f"{col}_ge_{label}", f"{col} >= {v}"),
                PredicateCase(f"{col}_ne_{label}", f"{col} != {v}"),
            ]
        )
    for lo, hi in zip(values, values[2:]):
        predicates.append(PredicateCase(f"{col}_between_{lo}_{hi}", f"{col} BETWEEN {lo} AND {hi}"))
    if values:
        sample = ",".join(str(v) for v in values[::2][:6])
        predicates.append(PredicateCase(f"{col}_in_edges", f"{col} IN ({sample})"))
        predicates.append(
            PredicateCase(
                f"{col}_or_split",
                f"({col} BETWEEN {values[0]} AND {values[min(2, len(values)-1)]}) OR "
                f"({col} BETWEEN {values[max(0, len(values)-3)]} AND {values[-1]})",
            )
        )
        if nullable:
            predicates.append(PredicateCase(f"{col}_or_null", f"({col} < {values[len(values)//2]}) OR ({col} IS NULL)"))
    return dedupe_predicates(predicates)


def sql_quote(value: str) -> str:
    return "'" + value.replace("'", "''") + "'"


def string_boundary_predicates(col: str, edges: list[str], nullable: bool = True) -> list[PredicateCase]:
    probes = ["", "a", "b", "c", "l", "m", "n", "z"]
    probes.extend(edges)
    values = sorted(set(probes))
    predicates: list[PredicateCase] = []
    if nullable:
        predicates.append(PredicateCase(f"{col}_is_null", f"{col} IS NULL"))
    for value in values:
        label = value if value else "empty"
        quoted = sql_quote(value)
        predicates.extend(
            [
                PredicateCase(f"{col}_eq_{label}", f"{col} = {quoted}"),
                PredicateCase(f"{col}_lt_{label}", f"{col} < {quoted}"),
                PredicateCase(f"{col}_le_{label}", f"{col} <= {quoted}"),
                PredicateCase(f"{col}_gt_{label}", f"{col} > {quoted}"),
                PredicateCase(f"{col}_ge_{label}", f"{col} >= {quoted}"),
                PredicateCase(f"{col}_ne_{label}", f"{col} != {quoted}"),
            ]
        )
    predicates.extend(
        [
            PredicateCase(f"{col}_between_a_m", f"{col} BETWEEN 'a' AND 'm'"),
            PredicateCase(f"{col}_in_edges", f"{col} IN ('', 'a', 'b', 'm', 'z')"),
            PredicateCase(f"{col}_or_null", f"({col} < 'b') OR ({col} IS NULL)"),
            PredicateCase(f"{col}_cast_eq", f"CAST({col} AS CHAR) = 'b'"),
        ]
    )
    return dedupe_predicates(predicates)


def date_boundary_predicates(col: str, edges: list[str], nullable: bool = True) -> list[PredicateCase]:
    values = sorted(set(["0001-01-01", "2023-12-31", "2024-01-01", "2024-01-02", "2024-01-31", "2024-02-01", "2025-01-01"] + edges))
    predicates: list[PredicateCase] = []
    if nullable:
        predicates.append(PredicateCase(f"{col}_is_null", f"{col} IS NULL"))
    for value in values:
        quoted = sql_quote(value)
        label = value.replace("-", "")
        predicates.extend(
            [
                PredicateCase(f"{col}_eq_{label}", f"{col} = {quoted}"),
                PredicateCase(f"{col}_lt_{label}", f"{col} < {quoted}"),
                PredicateCase(f"{col}_le_{label}", f"{col} <= {quoted}"),
                PredicateCase(f"{col}_gt_{label}", f"{col} > {quoted}"),
                PredicateCase(f"{col}_ge_{label}", f"{col} >= {quoted}"),
                PredicateCase(f"{col}_ne_{label}", f"{col} != {quoted}"),
            ]
        )
    predicates.extend(
        [
            PredicateCase(f"{col}_between_jan", f"{col} BETWEEN '2024-01-01' AND '2024-02-01'"),
            PredicateCase(f"{col}_in_edges", f"{col} IN ('2024-01-01', '2024-01-02', '2024-02-01', '2025-01-01')"),
            PredicateCase(f"{col}_or", f"({col} < '2024-01-02') OR ({col} >= '2025-01-01')"),
            PredicateCase(f"{col}_cast_eq", f"CAST({col} AS DATE) = '2024-01-01'"),
        ]
    )
    return dedupe_predicates(predicates)


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


def run_with_mode(args: argparse.Namespace, db: str, mode: str, sql: str) -> Result:
    return run_mysql(args, f"SET @@session.tidb_partition_prune_mode = '{mode}';\n{sql}", db)


def explain_with_mode(args: argparse.Namespace, db: str, mode: str, sql: str) -> str:
    res = run_with_mode(args, db, mode, "EXPLAIN FORMAT='brief' " + sql)
    if res.rc != 0:
        return res.err
    return " | ".join(rows(res)[:6])


def table_cases() -> list[TableCase]:
    int_rows = ",".join(
        [
            "(1, NULL, 0, 'null-low')",
            "(2, -1, 1, 'neg')",
            "(3, 0, 2, 'zero')",
            "(4, 1, 3, 'one')",
            "(5, 2, 4, 'two')",
            "(6, 3, 5, 'three')",
            "(7, 4, 6, 'four')",
            "(8, 9, 7, 'nine')",
            "(9, 10, 8, 'ten')",
            "(10, 11, 9, 'eleven')",
            "(11, NULL, 10, 'null-high')",
        ]
    )
    int_predicates = numeric_boundary_predicates("a", [0, 3, 10]) + [
        PredicateCase("is_null", "a IS NULL"),
        PredicateCase("lt_zero", "a < 0"),
        PredicateCase("le_zero", "a <= 0"),
        PredicateCase("between_0_3", "a BETWEEN 0 AND 3"),
        PredicateCase("in_edges", "a IN (-1,0,2,3,10,11)"),
        PredicateCase("neq_edge", "a != 3"),
        PredicateCase("or_null_lt3", "(a < 3) OR (a IS NULL)"),
        PredicateCase("or_split", "(a BETWEEN -1 AND 1) OR (a BETWEEN 9 AND 11)"),
        PredicateCase("cast_eq", "CAST(a AS SIGNED) = 3"),
    ]
    int_predicates = dedupe_predicates(int_predicates)

    multi_rows = ",".join(
        [
            "(1, NULL, 0, 'n0')",
            "(2, -1, 9, 'neg-high')",
            "(3, 0, 0, 'zero-zero')",
            "(4, 0, 2, 'zero-two')",
            "(5, 1, 1, 'one-one')",
            "(6, 2, 9, 'two-nine')",
            "(7, 3, 0, 'three-zero')",
            "(8, 3, 3, 'three-three')",
            "(9, 9, 9, 'nine-nine')",
            "(10, 10, 0, 'ten-zero')",
        ]
    )
    multi_predicates = numeric_boundary_predicates("a", [0, 3, 10]) + numeric_boundary_predicates("b", [0, 3, 10], nullable=False) + [
        PredicateCase("a_eq_b_range", "a = 0 AND b BETWEEN 0 AND 2"),
        PredicateCase("prefix_a_between", "a BETWEEN 0 AND 3"),
        PredicateCase("prefix_with_tail", "a = 2 AND b > 5"),
        PredicateCase("or_prefixes", "(a = 0 AND b = 2) OR (a = 3 AND b = 0)"),
        PredicateCase("null_or_prefix", "(a IS NULL) OR (a = 0 AND b = 0)"),
        PredicateCase("tuple_like_prefix_low", "a = 3 AND b < 3"),
        PredicateCase("tuple_like_prefix_high", "a = 3 AND b >= 3"),
    ]
    multi_predicates = dedupe_predicates(multi_predicates)

    date_rows = ",".join(
        [
            "(1, NULL, 'null-date')",
            "(2, '0001-01-01', 'early')",
            "(3, '2024-01-01', 'd1')",
            "(4, '2024-01-02', 'd2')",
            "(5, '2024-02-01', 'm2')",
            "(6, '2025-01-01', 'y2')",
        ]
    )
    date_predicates = date_boundary_predicates("d", ["2024-01-01", "2024-02-01"]) + [
        PredicateCase("date_is_null", "d IS NULL"),
        PredicateCase("date_lt_edge", "d < '2024-01-01'"),
        PredicateCase("date_eq_edge", "d = '2024-01-01'"),
        PredicateCase("date_between", "d BETWEEN '2024-01-01' AND '2024-02-01'"),
        PredicateCase("date_or", "(d < '2024-01-02') OR (d >= '2025-01-01')"),
    ]
    date_predicates = dedupe_predicates(date_predicates)

    unsigned_rows = ",".join(
        [
            "(1, 0, -2147483648, 'zero-min')",
            "(2, 2, 9, 'two-nine')",
            "(3, 3, -2147483648, 'three-min')",
            "(4, 3, 0, 'three-zero')",
            "(5, 4, -2147483648, 'four-min')",
            "(6, 4, 0, 'four-zero')",
            "(7, 4, 1, 'four-one')",
            "(8, 4, 4, 'four-four')",
            "(9, 7, 0, 'seven-zero')",
            "(10, 11, 9, 'eleven-nine')",
        ]
    )
    unsigned_predicates = numeric_boundary_predicates("a", [3, 4, 7, 11], nullable=False) + [
        PredicateCase("b_signed_min", "b = -2147483648"),
        PredicateCase("a4_b_min_or_zero", "a = 4 AND b IN (-2147483648, 0, 1)"),
        PredicateCase("a_between_b_range", "a BETWEEN 3 AND 7 AND b BETWEEN -2147483648 AND 4"),
        PredicateCase("or_unsigned_edges", "(a = 3 AND b = -2147483648) OR (a = 7 AND b = 0)"),
    ]
    unsigned_predicates = dedupe_predicates(unsigned_predicates)

    string_rows = ",".join(
        [
            "(1, NULL, 'null-s')",
            "(2, '', 'empty')",
            "(3, 'a', 'a')",
            "(4, 'b', 'b')",
            "(5, 'c', 'c')",
            "(6, 'l', 'l')",
            "(7, 'm', 'm')",
            "(8, 'n', 'n')",
            "(9, 'z', 'z')",
        ]
    )
    string_predicates = string_boundary_predicates("s", ["b", "m"])

    ts_rows = ",".join(
        [
            "(1, '2020-04-04 00:00:00', 'before')",
            "(2, '2020-04-04 23:59:59', 'before-edge')",
            "(3, '2020-04-05 00:00:00', 'edge')",
            "(4, '2020-04-05 00:00:01', 'after-edge')",
            "(5, '2020-04-12 00:00:00', 'edge2')",
            "(6, '2020-04-14 00:00:42', 'late')",
        ]
    )
    ts_predicates = [
        PredicateCase("ts_lt_edge", "ts < '2020-04-05 00:00:00'"),
        PredicateCase("ts_eq_edge", "ts = '2020-04-05 00:00:00'"),
        PredicateCase("ts_gt_edge", "ts > '2020-04-05 00:00:00'"),
        PredicateCase("ts_between", "ts BETWEEN '2020-04-04 23:59:59' AND '2020-04-05 00:00:01'"),
        PredicateCase("ts_or", "(ts <= '2020-04-05 00:00:00') OR (ts >= '2020-04-12 00:00:00')"),
        PredicateCase("unix_eq", "unix_timestamp(ts) = unix_timestamp('2020-04-05 00:00:00')"),
        PredicateCase("unix_lt", "unix_timestamp(ts) < unix_timestamp('2020-04-05 00:00:00')"),
        PredicateCase("floor_unix_between", "floor(unix_timestamp(ts)) BETWEEN unix_timestamp('2020-04-04 23:59:59') AND unix_timestamp('2020-04-05 00:00:01')"),
    ]

    list_int_rows = ",".join(
        [
            "(1, NULL, 'null')",
            "(2, -1, 'neg')",
            "(3, 0, 'zero')",
            "(4, 1, 'one')",
            "(5, 2, 'two-default')",
            "(6, 3, 'three')",
            "(7, 4, 'four-default')",
            "(8, 10, 'ten')",
            "(9, 11, 'eleven-default')",
        ]
    )
    list_int_predicates = numeric_boundary_predicates("a", [0, 1, 3, 10]) + [
        PredicateCase("null_safe_null", "a <=> NULL"),
        PredicateCase("null_safe_three", "a <=> 3"),
        PredicateCase("not_in_values", "a NOT IN (-1,0,1,3,10)"),
        PredicateCase("in_values_and_default", "a IN (1,2,3,4,10,11)"),
        PredicateCase("range_hits_default", "a BETWEEN 1 AND 4"),
        PredicateCase("or_null_default", "(a IS NULL) OR (a = 2) OR (a = 10)"),
    ]
    list_int_predicates = dedupe_predicates(list_int_predicates)

    list_multi_rows = ",".join(
        [
            "(1, 1, 1, 'p0-11')",
            "(2, 1, 2, 'p0-12')",
            "(3, 1, 3, 'def-13')",
            "(4, 2, 1, 'p0-21')",
            "(5, 2, 2, 'def-22')",
            "(6, 3, 3, 'p1-33')",
            "(7, NULL, 3, 'p1-null3')",
            "(8, NULL, 4, 'def-null4')",
            "(9, 4, NULL, 'def-4null')",
        ]
    )
    list_multi_predicates = dedupe_predicates(
        numeric_boundary_predicates("a", [1, 2, 3], nullable=True)
        + numeric_boundary_predicates("b", [1, 2, 3], nullable=True)
        + [
            PredicateCase("point_in_p0", "a = 1 AND b = 1"),
            PredicateCase("same_a_default", "a = 1 AND b = 3"),
            PredicateCase("both_default", "a = 2 AND b = 2"),
            PredicateCase("null_tuple_listed", "a IS NULL AND b = 3"),
            PredicateCase("null_tuple_default", "a IS NULL AND b = 4"),
            PredicateCase("b_only_listed_and_default", "b = 1"),
            PredicateCase("a_only_listed_and_default", "a = 1"),
            PredicateCase("or_default_and_listed", "(a = 1 AND b = 1) OR (a = 2 AND b = 2)"),
            PredicateCase("or_null_tuple", "(a IS NULL AND b = 3) OR (a = 4 AND b IS NULL)"),
        ]
    )

    list_string_rows = ",".join(
        [
            "(1, NULL, 'null')",
            "(2, '', 'empty')",
            "(3, 'a', 'a')",
            "(4, 'D', 'd')",
            "(5, 'Y', 'y')",
            "(6, 'm', 'm-default')",
            "(7, 'z', 'z-default')",
        ]
    )
    list_string_predicates = string_boundary_predicates("s", ["D", "Y"]) + [
        PredicateCase("null_safe_null", "s <=> NULL"),
        PredicateCase("null_safe_d", "s <=> 'D'"),
        PredicateCase("not_in_values", "s NOT IN ('', 'a', 'D', 'Y')"),
        PredicateCase("in_values_and_default", "s IN ('', 'a', 'D', 'm', 'Y', 'z')"),
        PredicateCase("range_hits_default", "s BETWEEN 'D' AND 'z'"),
    ]
    list_string_predicates = dedupe_predicates(list_string_predicates)

    return [
        TableCase(
            name="range_columns_int",
            create_sql="""
CREATE TABLE t(
  id INT,
  a INT NULL,
  b INT,
  tag VARCHAR(32),
  KEY(id)
) PARTITION BY RANGE COLUMNS(a) (
  PARTITION p0 VALUES LESS THAN (0),
  PARTITION p1 VALUES LESS THAN (3),
  PARTITION p2 VALUES LESS THAN (10),
  PARTITION pmax VALUES LESS THAN (MAXVALUE)
)""",
            create_ref_sql="""
CREATE TABLE t_ref(
  id INT,
  a INT NULL,
  b INT,
  tag VARCHAR(32),
  KEY(id)
)""",
            insert_sql=f"INSERT INTO t VALUES {int_rows}",
            select_expr="CONCAT_WS(',', id, IFNULL(CAST(a AS CHAR), 'NULL'), b, tag)",
            predicates=int_predicates,
        ),
        TableCase(
            name="range_columns_multi",
            create_sql="""
CREATE TABLE t(
  id INT,
  a INT NULL,
  b INT,
  tag VARCHAR(32),
  KEY(id)
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
  b INT,
  tag VARCHAR(32),
  KEY(id)
)""",
            insert_sql=f"INSERT INTO t VALUES {multi_rows}",
            select_expr="CONCAT_WS(',', id, IFNULL(CAST(a AS CHAR), 'NULL'), b, tag)",
            predicates=multi_predicates,
        ),
        TableCase(
            name="range_columns_date",
            create_sql="""
CREATE TABLE t(
  id INT,
  d DATE NULL,
  tag VARCHAR(32),
  KEY(id)
) PARTITION BY RANGE COLUMNS(d) (
  PARTITION p0 VALUES LESS THAN ('2024-01-01'),
  PARTITION p1 VALUES LESS THAN ('2024-02-01'),
  PARTITION pmax VALUES LESS THAN (MAXVALUE)
)""",
            create_ref_sql="""
CREATE TABLE t_ref(
  id INT,
  d DATE NULL,
  tag VARCHAR(32),
  KEY(id)
)""",
            insert_sql=f"INSERT INTO t VALUES {date_rows}",
            select_expr="CONCAT_WS(',', id, IFNULL(CAST(d AS CHAR), 'NULL'), tag)",
            predicates=date_predicates,
        ),
        TableCase(
            name="range_columns_unsigned_multi",
            create_sql="""
CREATE TABLE t(
  id INT,
  a INT UNSIGNED NOT NULL,
  b INT,
  tag VARCHAR(32),
  KEY(id)
) PARTITION BY RANGE COLUMNS(a,b) (
  PARTITION p0 VALUES LESS THAN (3, MAXVALUE),
  PARTITION p1 VALUES LESS THAN (4, -2147483648),
  PARTITION p2 VALUES LESS THAN (4, 1),
  PARTITION p3 VALUES LESS THAN (4, 4),
  PARTITION p4 VALUES LESS THAN (7, 0),
  PARTITION pmax VALUES LESS THAN (MAXVALUE, MAXVALUE)
)""",
            create_ref_sql="""
CREATE TABLE t_ref(
  id INT,
  a INT UNSIGNED NOT NULL,
  b INT,
  tag VARCHAR(32),
  KEY(id)
)""",
            insert_sql=f"INSERT INTO t VALUES {unsigned_rows}",
            select_expr="CONCAT_WS(',', id, a, b, tag)",
            predicates=unsigned_predicates,
        ),
        TableCase(
            name="range_columns_string",
            create_sql="""
CREATE TABLE t(
  id INT,
  s VARCHAR(8) COLLATE utf8mb4_general_ci NULL,
  tag VARCHAR(32),
  KEY(id)
) PARTITION BY RANGE COLUMNS(s) (
  PARTITION p0 VALUES LESS THAN ('b'),
  PARTITION p1 VALUES LESS THAN ('m'),
  PARTITION pmax VALUES LESS THAN (MAXVALUE)
)""",
            create_ref_sql="""
CREATE TABLE t_ref(
  id INT,
  s VARCHAR(8) COLLATE utf8mb4_general_ci NULL,
  tag VARCHAR(32),
  KEY(id)
)""",
            insert_sql=f"INSERT INTO t VALUES {string_rows}",
            select_expr="CONCAT_WS(',', id, IFNULL(s, 'NULL'), tag)",
            predicates=string_predicates,
        ),
        TableCase(
            name="range_expr_floor_unix_timestamp",
            create_sql="""
CREATE TABLE t(
  id INT,
  ts TIMESTAMP NULL,
  tag VARCHAR(32),
  KEY(id)
) PARTITION BY RANGE (floor(unix_timestamp(ts))) (
  PARTITION p0 VALUES LESS THAN (unix_timestamp('2020-04-05 00:00:00')),
  PARTITION p1 VALUES LESS THAN (unix_timestamp('2020-04-12 00:00:00')),
  PARTITION pmax VALUES LESS THAN (MAXVALUE)
)""",
            create_ref_sql="""
CREATE TABLE t_ref(
  id INT,
  ts TIMESTAMP NULL,
  tag VARCHAR(32),
  KEY(id)
)""",
            insert_sql=f"INSERT INTO t VALUES {ts_rows}",
            select_expr="CONCAT_WS(',', id, IFNULL(CAST(ts AS CHAR), 'NULL'), tag)",
            predicates=ts_predicates,
        ),
        TableCase(
            name="list_columns_int_default",
            create_sql="""
CREATE TABLE t(
  id INT,
  a INT NULL,
  tag VARCHAR(32),
  KEY(id)
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
  tag VARCHAR(32),
  KEY(id)
)""",
            insert_sql=f"INSERT INTO t VALUES {list_int_rows}",
            select_expr="CONCAT_WS(',', id, IFNULL(CAST(a AS CHAR), 'NULL'), tag)",
            predicates=list_int_predicates,
        ),
        TableCase(
            name="list_columns_multi_default",
            create_sql="""
CREATE TABLE t(
  id INT,
  a INT NULL,
  b INT NULL,
  tag VARCHAR(32),
  KEY(id)
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
  tag VARCHAR(32),
  KEY(id)
)""",
            insert_sql=f"INSERT INTO t VALUES {list_multi_rows}",
            select_expr="CONCAT_WS(',', id, IFNULL(CAST(a AS CHAR), 'NULL'), IFNULL(CAST(b AS CHAR), 'NULL'), tag)",
            predicates=list_multi_predicates,
        ),
        TableCase(
            name="list_columns_string_default",
            create_sql="""
CREATE TABLE t(
  id INT,
  s VARCHAR(8) COLLATE utf8mb4_general_ci NULL,
  tag VARCHAR(32),
  KEY(id)
) PARTITION BY LIST COLUMNS(s) (
  PARTITION pnull VALUES IN (NULL),
  PARTITION p0 VALUES IN ('','a'),
  PARTITION p1 VALUES IN ('D','Y'),
  PARTITION pdef DEFAULT
)""",
            create_ref_sql="""
CREATE TABLE t_ref(
  id INT,
  s VARCHAR(8) COLLATE utf8mb4_general_ci NULL,
  tag VARCHAR(32),
  KEY(id)
)""",
            insert_sql=f"INSERT INTO t VALUES {list_string_rows}",
            select_expr="CONCAT_WS(',', id, IFNULL(s, 'NULL'), tag)",
            predicates=list_string_predicates,
        ),
    ]


def run_case(args: argparse.Namespace, db: str, case: TableCase) -> list[Finding]:
    findings: list[Finding] = []
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
        part_sql = f"SELECT {case.select_expr} FROM t WHERE {pred.where} ORDER BY id"
        ref_sql = f"SELECT {case.select_expr} FROM t_ref WHERE {pred.where} ORDER BY id"
        static = run_with_mode(args, db, "static", part_sql)
        dynamic = run_with_mode(args, db, "dynamic", part_sql)
        ref = run_mysql(args, ref_sql, db)
        if static.rc != ref.rc or dynamic.rc != ref.rc or rows(static) != rows(ref) or rows(dynamic) != rows(ref):
            finding = Finding(
                table_case=case.name,
                predicate_case=pred.name,
                where=pred.where,
                ref_rows=rows(ref),
                static_rows=rows(static),
                dynamic_rows=rows(dynamic),
                static_plan=explain_with_mode(args, db, "static", part_sql),
                dynamic_plan=explain_with_mode(args, db, "dynamic", part_sql),
            )
            findings.append(finding)
            print(
                "\n".join(
                    [
                        f"HIT table={case.name} pred={pred.name}",
                        f"where={pred.where}",
                        f"ref_rc={ref.rc} static_rc={static.rc} dynamic_rc={dynamic.rc}",
                        f"ref_rows={finding.ref_rows}",
                        f"static_rows={finding.static_rows}",
                        f"dynamic_rows={finding.dynamic_rows}",
                        f"ref_err={ref.err}",
                        f"static_err={static.err}",
                        f"dynamic_err={dynamic.err}",
                        f"static_plan={finding.static_plan}",
                        f"dynamic_plan={finding.dynamic_plan}",
                    ]
                ),
                flush=True,
            )
    exec_ok(args, "DROP TABLE IF EXISTS t", db)
    exec_ok(args, "DROP TABLE IF EXISTS t_ref", db)
    return findings


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default=os.environ.get("MYSQL", "mysql"))
    parser.add_argument("--host", default=os.environ.get("TIDB_HOST", "127.0.0.1"))
    parser.add_argument("--port", type=int, default=int(os.environ.get("TIDB_PORT", "14000")))
    parser.add_argument("--user", default=os.environ.get("TIDB_USER", "root"))
    parser.add_argument("--db-prefix", default="ai_native_part_prune")
    parser.add_argument("--case", default="", help="run only cases whose name contains this substring")
    parser.add_argument("--max-predicates", type=int, default=0, help="per-case predicate cap; 0 means no cap")
    parser.add_argument("--progress-every", type=int, default=25, help="print progress every N predicates; 0 disables")
    args = parser.parse_args()

    db = f"{args.db_prefix}_{int(time.time())}"
    print(f"mysql={shlex.join(mysql_args(args))}")
    print(f"database={db}")
    exec_ok(args, f"DROP DATABASE IF EXISTS {quote_db(db)}")
    exec_ok(args, f"CREATE DATABASE {quote_db(db)}")

    findings: list[Finding] = []
    try:
        for case in table_cases():
            if args.case and args.case not in case.name:
                continue
            print(f"CASE {case.name}", flush=True)
            findings.extend(run_case(args, db, case))
    finally:
        exec_ok(args, f"DROP DATABASE IF EXISTS {quote_db(db)}")

    print(f"SUMMARY findings={len(findings)}")
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main())
