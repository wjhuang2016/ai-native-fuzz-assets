#!/usr/bin/env python3
"""Targeted partial-index differential probe for the AI-native DDL work.

The probe intentionally keeps the engine small: each case creates a table with
one partial index, then compares result sets under partial-index hints against
an IGNORE INDEX/table-scan baseline.
"""

from __future__ import annotations

import argparse
import dataclasses
import os
import random
import shlex
import subprocess
import sys
import time
from typing import Iterable, TextIO


@dataclasses.dataclass
class Result:
    rc: int
    out: str
    err: str


@dataclasses.dataclass
class Case:
    name: str
    setup: list[str]
    checks: list[str]
    dml: list[str] = dataclasses.field(default_factory=list)


@dataclasses.dataclass
class MatrixFinding:
    family: str
    condition: str
    predicate: str
    order: str
    variant: str
    baseline_rows: list[str]
    got_rows: list[str]
    plan: str


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


def run_mysql_with_session(args: argparse.Namespace, db: str, session_stmts: list[str], sql: str) -> Result:
    stmts = session_stmts + [sql]
    return run_mysql(args, ";\n".join(stmts) + ";", db)


def exec_ok(args: argparse.Namespace, sql: str, database: str | None = None) -> None:
    res = run_mysql(args, sql, database)
    if res.rc != 0:
        raise RuntimeError(f"SQL failed rc={res.rc}: {sql}\n{res.err}")


def quote_db(name: str) -> str:
    return "`" + name.replace("`", "``") + "`"


def rows(res: Result) -> list[str]:
    if res.out == "":
        return []
    return res.out.splitlines()


def csv_field(value: object) -> str:
    text = str(value)
    return '"' + text.replace('"', '""') + '"'


def compare_query(args: argparse.Namespace, db: str, case_name: str, query: str, explain: bool = True) -> list[str]:
    """Compare no-hint/ignore-index/force-index variants for one SELECT."""
    variants = {
        "baseline": query.format(hint="IGNORE INDEX(pi)"),
        "use": query.format(hint="USE INDEX(pi)"),
        "force": query.format(hint="FORCE INDEX(pi)"),
    }
    outputs: dict[str, Result] = {}
    for label, sql in variants.items():
        outputs[label] = run_mysql(args, sql, db)

    findings: list[str] = []
    matrix_findings: list[MatrixFinding] = []
    base = outputs["baseline"]
    for label in ("use", "force"):
        got = outputs[label]
        if got.rc != base.rc or rows(got) != rows(base):
            findings.append(
                "\n".join(
                    [
                        f"HIT case={case_name} variant={label}",
                        f"query={variants[label]}",
                        f"baseline_rc={base.rc} rc={got.rc}",
                        f"baseline_rows={rows(base)}",
                        f"rows={rows(got)}",
                        f"baseline_err={base.err}",
                        f"err={got.err}",
                    ]
                )
            )

    if explain:
        for label, sql in variants.items():
            explain_res = run_mysql(args, "EXPLAIN FORMAT='brief' " + sql, db)
            one_line = " | ".join(rows(explain_res)[:4])
            print(f"PLAN case={case_name} variant={label} rc={explain_res.rc} {one_line}")
    return findings


def parse_marked_blocks(lines: list[str]) -> dict[str, list[str]]:
    blocks: dict[str, list[str]] = {}
    current: str | None = None
    for line in lines:
        if line.startswith("MARK\t"):
            current = line.split("\t", 1)[1]
            blocks[current] = []
            continue
        if line.startswith("LAST\t"):
            if current is not None:
                blocks.setdefault(current + "_last_plan_from_cache", []).append(line.split("\t", 1)[1])
            continue
        if current is not None:
            blocks[current].append(line)
    return blocks


def run_prepared_script(args: argparse.Namespace, db: str, case_name: str, stmts: list[str]) -> dict[str, list[str]]:
    res = run_mysql(args, ";\n".join(stmts) + ";", db)
    if res.rc != 0:
        raise RuntimeError(f"prepared case failed {case_name}: {res.err}")
    blocks = parse_marked_blocks(rows(res))
    for key in sorted(blocks):
        print(f"PREP case={case_name} {key}={blocks[key]}")
    return blocks


def run_plan_cache_checks(args: argparse.Namespace, db: str) -> list[str]:
    findings: list[str] = []

    exec_ok(args, "DROP TABLE IF EXISTS t", db)
    exec_ok(args, "CREATE TABLE t(id INT PRIMARY KEY, a INT NULL, b INT, s VARCHAR(20))", db)
    exec_ok(args, f"INSERT INTO t VALUES {base_rows()}", db)
    exec_ok(args, "ALTER TABLE t ADD INDEX pi(b) WHERE a IS NOT NULL", db)
    blocks = run_prepared_script(
        args,
        db,
        "pc_is_not_null_nulleq",
        [
            "SET tidb_enable_prepared_plan_cache=1",
            "SET tidb_enable_collect_execution_info=0",
            "PREPARE stmt FROM 'SELECT CONCAT_WS('','', id, IFNULL(a, ''NULL''), b) FROM t USE INDEX(pi) WHERE a <=> ? ORDER BY id'",
            "SET @p=10",
            "SELECT 'MARK', 'p10'",
            "EXECUTE stmt USING @p",
            "SELECT 'LAST', @@last_plan_from_cache",
            "SET @p=NULL",
            "SELECT 'MARK', 'pnull'",
            "EXECUTE stmt USING @p",
            "SELECT 'LAST', @@last_plan_from_cache",
            "DEALLOCATE PREPARE stmt",
        ],
    )
    direct_null = rows(run_mysql(args, "SELECT CONCAT_WS(',', id, IFNULL(a, 'NULL'), b) FROM t IGNORE INDEX(pi) WHERE a <=> NULL ORDER BY id", db))
    if blocks.get("pnull") != direct_null:
        findings.append(
            "\n".join(
                [
                    "HIT case=pc_is_not_null_nulleq variant=prepared_null",
                    f"prepared_rows={blocks.get('pnull')}",
                    f"direct_rows={direct_null}",
                    f"last_plan_from_cache={blocks.get('pnull_last_plan_from_cache')}",
                ]
            )
        )

    exec_ok(args, "DROP TABLE IF EXISTS t", db)
    exec_ok(args, "CREATE TABLE t(id INT PRIMARY KEY, a INT NULL, b INT, s VARCHAR(20))", db)
    exec_ok(args, f"INSERT INTO t VALUES {base_rows()}", db)
    exec_ok(args, "ALTER TABLE t ADD INDEX pi(b) WHERE a > 10", db)
    blocks = run_prepared_script(
        args,
        db,
        "pc_gt_threshold",
        [
            "SET tidb_enable_prepared_plan_cache=1",
            "SET tidb_enable_collect_execution_info=0",
            "PREPARE stmt FROM 'SELECT CONCAT_WS('','', id, IFNULL(a, ''NULL''), b) FROM t USE INDEX(pi) WHERE a > ? ORDER BY id'",
            "SET @p=20",
            "SELECT 'MARK', 'p20'",
            "EXECUTE stmt USING @p",
            "SELECT 'LAST', @@last_plan_from_cache",
            "SET @p=5",
            "SELECT 'MARK', 'p5'",
            "EXECUTE stmt USING @p",
            "SELECT 'LAST', @@last_plan_from_cache",
            "DEALLOCATE PREPARE stmt",
        ],
    )
    direct_p5 = rows(run_mysql(args, "SELECT CONCAT_WS(',', id, IFNULL(a, 'NULL'), b) FROM t IGNORE INDEX(pi) WHERE a > 5 ORDER BY id", db))
    if blocks.get("p5") != direct_p5:
        findings.append(
            "\n".join(
                [
                    "HIT case=pc_gt_threshold variant=prepared_p5",
                    f"prepared_rows={blocks.get('p5')}",
                    f"direct_rows={direct_p5}",
                    f"last_plan_from_cache={blocks.get('p5_last_plan_from_cache')}",
                ]
            )
        )

    exec_ok(args, "DROP TABLE IF EXISTS t", db)
    return findings


def int_condition(rng: random.Random) -> str:
    n = rng.choice([-5, 0, 1, 2, 3, 10, 11, 20])
    op = rng.choice([">", ">=", "<", "<=", "=", "!=", "<>"])
    return f"a {op} {n}"


def int_filter(rng: random.Random) -> str:
    n = rng.choice([-5, 0, 1, 2, 3, 10, 11, 20, 21, 100])
    m = rng.choice([-5, 0, 1, 2, 3, 10, 11, 20, 21, 100])
    lo, hi = sorted([n, m])
    atoms = [
        f"a > {n}",
        f"a >= {n}",
        f"a < {n}",
        f"a <= {n}",
        f"a = {n}",
        f"a != {n}",
        f"a <> {n}",
        f"a <=> {n}",
        "a <=> NULL",
        "a IS NULL",
        "a IS NOT NULL",
        f"a BETWEEN {lo} AND {hi}",
        f"a IN ({n}, {m})",
        f"b BETWEEN {lo} AND {hi}",
        f"b >= {n}",
    ]
    return rng.choice(atoms)


def random_where(rng: random.Random) -> str:
    first = int_filter(rng)
    if rng.random() < 0.45:
        return first
    joiner = rng.choice(["AND", "OR"])
    second = int_filter(rng)
    return f"({first}) {joiner} ({second})"


def random_partial_condition(rng: random.Random) -> str:
    choices = [
        "a IS NULL",
        "a IS NOT NULL",
        int_condition(rng),
    ]
    return rng.choice(choices)


def run_random_checks(args: argparse.Namespace, db: str, count: int) -> list[str]:
    if count <= 0:
        return []
    rng = random.Random(20260630)
    findings: list[str] = []
    exec_ok(args, "DROP TABLE IF EXISTS t", db)
    for i in range(count):
        cond = random_partial_condition(rng)
        exec_ok(args, "DROP TABLE IF EXISTS t", db)
        exec_ok(args, "CREATE TABLE t(id INT PRIMARY KEY, a INT NULL, b INT, c INT, s VARCHAR(20))", db)
        exec_ok(args, f"INSERT INTO t(id,a,b,s) VALUES {base_rows()}", db)
        add_idx = run_mysql(args, f"ALTER TABLE t ADD INDEX pi(b) WHERE {cond}", db)
        if add_idx.rc != 0:
            print(f"RANDOM skip={i} cond={cond!r} err={add_idx.err.splitlines()[-1:]}")
            continue
        if rng.random() < 0.35:
            for dml in [
                "UPDATE t SET a = a + 7 WHERE id IN (2, 7, 8)",
                "UPDATE t SET a = NULL WHERE id IN (9)",
                "DELETE FROM t WHERE id = 11",
                "INSERT INTO t(id,a,b,s) VALUES (100, 20, 100, 'r100')",
            ]:
                exec_ok(args, dml, db)
            exec_ok(args, "ADMIN CHECK TABLE t", db)
        where = random_where(rng)
        order = rng.choice(["ORDER BY id", "ORDER BY b, id", "ORDER BY b DESC, id"])
        limit = "" if rng.random() < 0.65 else f" LIMIT {rng.choice([1, 2, 3, 5])}"
        query = f"SELECT id,a,b FROM t {{hint}} WHERE {where} {order}{limit}"
        case_name = f"random_{i}_cond_{cond}_where_{where}"
        got = compare_query(args, db, case_name, query, explain=False)
        if got:
            explain_findings = compare_query(args, db, case_name, query, explain=True)
            findings.extend(explain_findings or got)
    exec_ok(args, "DROP TABLE IF EXISTS t", db)
    return findings


def matrix_conditions() -> list[tuple[str, str]]:
    return [
        ("upper_bound", "a < 0"),
        ("upper_bound", "a <= 0"),
        ("upper_bound", "a < 3"),
        ("upper_bound", "a <= 3"),
        ("upper_bound", "a < 10"),
        ("upper_bound", "a <= 10"),
        ("lower_bound", "a > 3"),
        ("lower_bound", "a >= 3"),
        ("lower_bound", "a > 10"),
        ("lower_bound", "a >= 10"),
        ("point", "a = 3"),
        ("excluded_point", "a != 3"),
        ("excluded_point", "a <> 3"),
        ("null", "a IS NULL"),
        ("null", "a IS NOT NULL"),
    ]


def matrix_predicates() -> list[tuple[str, str]]:
    return [
        ("lower_overlap", "a >= -5"),
        ("lower_overlap", "a > -5"),
        ("lower_overlap", "a >= 0"),
        ("lower_overlap", "a > 0"),
        ("wide_upper", "a <= 100"),
        ("wide_upper", "a < 100"),
        ("wide_range", "a BETWEEN -5 AND 100"),
        ("wide_range", "a BETWEEN 0 AND 100"),
        ("boundary_range", "a BETWEEN 0 AND 3"),
        ("boundary_range", "a BETWEEN 3 AND 100"),
        ("boundary_range", "a BETWEEN 3 AND 10"),
        ("point_set", "a IN (1,2,3)"),
        ("point_set", "a IN (3,10)"),
        ("point", "a = 3"),
        ("excluded_point", "a != 3"),
        ("excluded_point", "a <> 3"),
        ("null", "a IS NULL"),
        ("null", "a IS NOT NULL"),
        ("or_widening", "(a >= 0) OR (a IS NULL)"),
        ("or_widening", "(a BETWEEN 0 AND 2) OR (a BETWEEN 10 AND 100)"),
    ]


def matrix_orders() -> list[str]:
    return [
        "ORDER BY id",
        "ORDER BY b, id",
        "ORDER BY b DESC, id",
        "ORDER BY b, id LIMIT 3",
    ]


def matrix_insert_rows() -> str:
    values = [
        "(1, NULL, 1, 'null1')",
        "(2, -1, 2, 'neg1')",
        "(3, 0, 3, 'zero')",
        "(4, 1, 4, 'one')",
        "(5, 2, 5, 'two')",
        "(6, 3, 6, 'three')",
        "(7, 4, 7, 'four')",
        "(8, 10, 8, 'ten')",
        "(9, 100, 9, 'hundred')",
        "(10, NULL, 10, 'null2')",
    ]
    return ",".join(values)


def insert_rows(args: argparse.Namespace, db: str, values: list[str], chunk_size: int = 400) -> None:
    for start in range(0, len(values), chunk_size):
        chunk = ",".join(values[start : start + chunk_size])
        exec_ok(args, f"INSERT INTO t VALUES {chunk}", db)


def compact_plan(explain: Result, max_lines: int = 12) -> str:
    if explain.rc != 0:
        return explain.err
    return " | ".join(rows(explain)[:max_lines])


def explain_one_line(args: argparse.Namespace, db: str, sql: str) -> str:
    explain = run_mysql(args, "EXPLAIN FORMAT='brief' " + sql, db)
    return compact_plan(explain)


def explain_one_line_with_session(args: argparse.Namespace, db: str, session_stmts: list[str], sql: str) -> str:
    explain = run_mysql_with_session(args, db, session_stmts, "EXPLAIN FORMAT='brief' " + sql)
    return compact_plan(explain)


def write_matrix_csv(path: str, findings: list[MatrixFinding]) -> None:
    with open(path, "w", encoding="utf-8") as f:
        f.write("family,condition,predicate,order,variant,baseline_rows,got_rows,plan\n")
        for item in findings:
            f.write(
                ",".join(
                    [
                        csv_field(item.family),
                        csv_field(item.condition),
                        csv_field(item.predicate),
                        csv_field(item.order),
                        csv_field(item.variant),
                        csv_field(item.baseline_rows),
                        csv_field(item.got_rows),
                        csv_field(item.plan),
                    ]
                )
                + "\n"
            )


def run_matrix_checks(args: argparse.Namespace, db: str, out: TextIO) -> list[MatrixFinding]:
    findings: list[MatrixFinding] = []
    total = 0
    skipped: list[tuple[str, str]] = []
    exec_ok(args, "DROP TABLE IF EXISTS t", db)

    for cond_family, condition in matrix_conditions():
        exec_ok(args, "DROP TABLE IF EXISTS t", db)
        exec_ok(args, "CREATE TABLE t(id INT PRIMARY KEY, a INT NULL, b INT, s VARCHAR(20))", db)
        exec_ok(args, f"INSERT INTO t VALUES {matrix_insert_rows()}", db)
        add_idx = run_mysql(args, f"ALTER TABLE t ADD INDEX pi(b) WHERE {condition}", db)
        if add_idx.rc != 0:
            skipped.append((condition, add_idx.err.splitlines()[-1] if add_idx.err else "unknown"))
            continue

        # Let stats_meta catch the real row count; this makes no-hint ORDER BY
        # choices reproducible enough without using ANALYZE, which can hide the bug.
        time.sleep(args.matrix_stats_wait)

        for pred_family, predicate in matrix_predicates():
            for order in matrix_orders():
                total += 1
                baseline_sql = f"SELECT CONCAT_WS(',', id, IFNULL(a, 'NULL'), b) FROM t IGNORE INDEX(pi) WHERE {predicate} {order}"
                variants = {
                    "use": f"SELECT CONCAT_WS(',', id, IFNULL(a, 'NULL'), b) FROM t USE INDEX(pi) WHERE {predicate} {order}",
                    "force": f"SELECT CONCAT_WS(',', id, IFNULL(a, 'NULL'), b) FROM t FORCE INDEX(pi) WHERE {predicate} {order}",
                    "no_hint": f"SELECT CONCAT_WS(',', id, IFNULL(a, 'NULL'), b) FROM t WHERE {predicate} {order}",
                }
                baseline = run_mysql(args, baseline_sql, db)
                if baseline.rc != 0:
                    raise RuntimeError(f"matrix baseline failed: {baseline_sql}\n{baseline.err}")
                baseline_rows = rows(baseline)
                for variant, sql in variants.items():
                    got = run_mysql(args, sql, db)
                    if got.rc != baseline.rc or rows(got) != baseline_rows:
                        plan = explain_one_line(args, db, sql)
                        finding = MatrixFinding(
                            family=f"{cond_family}/{pred_family}",
                            condition=condition,
                            predicate=predicate,
                            order=order,
                            variant=variant,
                            baseline_rows=baseline_rows,
                            got_rows=rows(got),
                            plan=plan,
                        )
                        findings.append(finding)
                        print(
                            "MATRIX_HIT "
                            f"family={finding.family!r} "
                            f"cond={condition!r} pred={predicate!r} order={order!r} "
                            f"variant={variant} base={baseline_rows} got={finding.got_rows}",
                            file=out,
                            flush=True,
                        )
        print(f"MATRIX_PROGRESS condition={condition!r} total={total} hits={len(findings)}", file=out, flush=True)

    exec_ok(args, "DROP TABLE IF EXISTS t", db)
    print(f"MATRIX_SUMMARY total={total} hits={len(findings)} skipped={len(skipped)}", file=out, flush=True)
    for condition, err in skipped:
        print(f"MATRIX_SKIP condition={condition!r} err={err}", file=out, flush=True)
    return findings


def no_hint_target_cases() -> list[tuple[str, str, str, list[str]]]:
    return [
        (
            "upper_lt3_ge0",
            "a < 3",
            "a >= 0",
            ["ORDER BY b LIMIT 5", "ORDER BY b DESC LIMIT 5", "ORDER BY b, id LIMIT 5"],
        ),
        (
            "upper_le10_or_widen",
            "a <= 10",
            "(a BETWEEN 0 AND 2) OR (a BETWEEN 10 AND 100)",
            ["ORDER BY b LIMIT 5", "ORDER BY b DESC LIMIT 5", "ORDER BY b, id LIMIT 5"],
        ),
        (
            "excluded_ne3_range",
            "a != 3",
            "a BETWEEN 0 AND 3",
            ["ORDER BY b LIMIT 5", "ORDER BY b DESC LIMIT 5", "ORDER BY b, id LIMIT 5"],
        ),
    ]


def no_hint_session_profiles() -> list[tuple[str, list[str]]]:
    return [
        ("default", []),
        (
            "topn_cost",
            ["SET SESSION tidb_opt_partial_ordered_index_for_topn='COST'"],
        ),
        (
            "ordering_aggressive",
            [
                "SET SESSION tidb_opt_partial_ordered_index_for_topn='COST'",
                "SET SESSION tidb_opt_ordering_index_selectivity_threshold=1",
                "SET SESSION tidb_opt_ordering_index_selectivity_ratio=0",
            ],
        ),
        (
            "cheap_index_expensive_topn",
            [
                "SET SESSION tidb_opt_partial_ordered_index_for_topn='COST'",
                "SET SESSION tidb_opt_index_scan_cost_factor=0.01",
                "SET SESSION tidb_opt_index_lookup_cost_factor=0.01",
                "SET SESSION tidb_opt_topn_cost_factor=100",
                "SET SESSION tidb_opt_ordering_index_selectivity_threshold=1",
                "SET SESSION tidb_opt_ordering_index_selectivity_ratio=0",
            ],
        ),
    ]


def no_hint_data_profiles(row_count: int) -> list[tuple[str, list[str]]]:
    matrix_values = matrix_insert_rows().split("),")
    matrix_values = [v if v.endswith(")") else v + ")" for v in matrix_values]

    topn_values = [
        "(1, 0, 1000000, 'partial_zero')",
        "(2, 1, 1000001, 'partial_one')",
        "(3, 2, 1000002, 'partial_two')",
        "(4, NULL, 1000003, 'null_guard')",
    ]
    for i in range(max(20, row_count)):
        row_id = i + 10
        a = 3 + (i % 98)
        b = i + 1
        topn_values.append(f"({row_id}, {a}, {b}, 'out_{i}')")

    boundary_values = [
        "(1, 0, 1000, 'zero')",
        "(2, 1, 1001, 'one')",
        "(3, 2, 1002, 'two')",
        "(4, 3, 1, 'excluded_three')",
        "(5, 10, 2, 'ten')",
        "(6, 100, 3, 'hundred')",
        "(7, NULL, 4, 'null')",
    ]
    for i in range(max(20, row_count // 4)):
        row_id = i + 20
        a = 11 + (i % 80)
        b = 10 + i
        boundary_values.append(f"({row_id}, {a}, {b}, 'wide_{i}')")

    return [
        ("matrix_small", matrix_values),
        ("topn_nonpartial_low_b", topn_values),
        ("boundary_low_b", boundary_values),
    ]


def apply_stats_mode(args: argparse.Namespace, db: str, mode: str, wait_seconds: float) -> None:
    if mode == "fresh":
        return
    if mode == "wait_meta":
        time.sleep(wait_seconds)
        return
    if mode == "analyze":
        exec_ok(args, "ANALYZE TABLE t", db)
        return
    raise ValueError(f"unknown stats mode: {mode}")


def plan_uses_partial_index(plan: str) -> bool:
    return "index:pi" in plan or "index:pi(" in plan


def run_no_hint_stats_checks(args: argparse.Namespace, db: str, out: TextIO) -> list[MatrixFinding]:
    findings: list[MatrixFinding] = []
    total = 0
    partial_plan_count = 0
    stats_modes = ["fresh", "wait_meta", "analyze"]

    for data_name, values in no_hint_data_profiles(args.no_hint_row_count):
        for case_name, condition, predicate, orders in no_hint_target_cases():
            exec_ok(args, "DROP TABLE IF EXISTS t", db)
            exec_ok(args, "CREATE TABLE t(id INT PRIMARY KEY, a INT NULL, b INT, s VARCHAR(40))", db)
            insert_rows(args, db, values)
            add_idx = run_mysql(args, f"ALTER TABLE t ADD INDEX pi(b) WHERE {condition}", db)
            if add_idx.rc != 0:
                print(f"NOHINT_SKIP data={data_name} case={case_name} cond={condition!r} err={add_idx.err}", file=out, flush=True)
                continue

            for stats_mode in stats_modes:
                apply_stats_mode(args, db, stats_mode, args.no_hint_stats_wait)
                for session_name, session_stmts in no_hint_session_profiles():
                    for order in orders:
                        total += 1
                        baseline_sql = f"SELECT CONCAT_WS(',', id, IFNULL(a, 'NULL'), b) FROM t IGNORE INDEX(pi) WHERE {predicate} {order}"
                        no_hint_sql = f"SELECT CONCAT_WS(',', id, IFNULL(a, 'NULL'), b) FROM t WHERE {predicate} {order}"
                        baseline = run_mysql_with_session(args, db, session_stmts, baseline_sql)
                        got = run_mysql_with_session(args, db, session_stmts, no_hint_sql)
                        plan = explain_one_line_with_session(args, db, session_stmts, no_hint_sql)
                        uses_pi = plan_uses_partial_index(plan)
                        if uses_pi:
                            partial_plan_count += 1
                            print(
                                "NOHINT_PLAN "
                                f"data={data_name} case={case_name} stats={stats_mode} session={session_name} "
                                f"order={order!r} plan={plan}",
                                file=out,
                                flush=True,
                            )
                        if got.rc != baseline.rc or rows(got) != rows(baseline):
                            finding = MatrixFinding(
                                family=f"no_hint/{case_name}/{data_name}/{stats_mode}/{session_name}",
                                condition=condition,
                                predicate=predicate,
                                order=order,
                                variant="no_hint",
                                baseline_rows=rows(baseline),
                                got_rows=rows(got),
                                plan=plan,
                            )
                            findings.append(finding)
                            print(
                                "NOHINT_HIT "
                                f"family={finding.family!r} cond={condition!r} pred={predicate!r} "
                                f"order={order!r} base={finding.baseline_rows} got={finding.got_rows} plan={plan}",
                                file=out,
                                flush=True,
                            )

        print(
            f"NOHINT_PROGRESS data={data_name} total={total} partial_plans={partial_plan_count} hits={len(findings)}",
            file=out,
            flush=True,
        )

    exec_ok(args, "DROP TABLE IF EXISTS t", db)
    print(f"NOHINT_SUMMARY total={total} partial_plans={partial_plan_count} hits={len(findings)}", file=out, flush=True)
    return findings


def base_rows() -> str:
    values = [
        "(1, NULL, 1, 'n')",
        "(2, -5, 2, 'neg')",
        "(3, 0, 3, 'zero')",
        "(4, 1, 4, 'one')",
        "(5, 2, 5, 'two')",
        "(6, 3, 6, 'three')",
        "(7, 10, 7, 'ten')",
        "(8, 11, 8, 'eleven')",
        "(9, 20, 9, 'twenty')",
        "(10, 21, 10, 'twentyone')",
        "(11, 100, 11, 'hundred')",
        "(12, NULL, 12, 'n2')",
    ]
    return ",".join(values)


def cases() -> Iterable[Case]:
    common_create = "CREATE TABLE t(id INT PRIMARY KEY, a INT NULL, b INT, s VARCHAR(20))"
    insert = f"INSERT INTO t VALUES {base_rows()}"

    yield Case(
        name="gt_boundary",
        setup=[common_create, insert, "ALTER TABLE t ADD INDEX pi(b) WHERE a > 10"],
        checks=[
            "SELECT id,a,b FROM t {hint} WHERE a > 20 AND b >= 0 ORDER BY id",
            "SELECT id,a,b FROM t {hint} WHERE a >= 20 AND b >= 0 ORDER BY id",
            "SELECT id,a,b FROM t {hint} WHERE a = 20 AND b >= 0 ORDER BY id",
            "SELECT id,a,b FROM t {hint} WHERE a > 10 AND b >= 0 ORDER BY id",
            "SELECT id,a,b FROM t {hint} WHERE a > 5 AND b >= 0 ORDER BY id",
            "SELECT id,a,b FROM t {hint} WHERE b >= 0 ORDER BY id",
        ],
    )

    yield Case(
        name="gte_boundary_in_or_between",
        setup=[common_create, insert, "ALTER TABLE t ADD INDEX pi(b) WHERE a >= 10"],
        checks=[
            "SELECT id,a,b FROM t {hint} WHERE a BETWEEN 10 AND 21 ORDER BY id",
            "SELECT id,a,b FROM t {hint} WHERE a IN (10, 20, 21) ORDER BY id",
            "SELECT id,a,b FROM t {hint} WHERE a > 10 OR a = 10 ORDER BY id",
            "SELECT id,a,b FROM t {hint} WHERE a >= 11 OR a = 10 ORDER BY id",
            "SELECT id,a,b FROM t {hint} WHERE a > 9 ORDER BY id",
        ],
    )

    yield Case(
        name="is_not_null_plan_cache_shape",
        setup=[common_create, insert, "ALTER TABLE t ADD INDEX pi(b) WHERE a IS NOT NULL"],
        checks=[
            "SELECT id,a,b FROM t {hint} WHERE a = 10 ORDER BY id",
            "SELECT id,a,b FROM t {hint} WHERE a <=> 10 ORDER BY id",
            "SELECT id,a,b FROM t {hint} WHERE a IS NOT NULL ORDER BY id",
            "SELECT id,a,b FROM t {hint} WHERE a > 10 OR a < 0 ORDER BY id",
            "SELECT id,a,b FROM t {hint} WHERE a IS NULL ORDER BY id",
        ],
    )

    yield Case(
        name="is_null",
        setup=[common_create, insert, "ALTER TABLE t ADD INDEX pi(b) WHERE a IS NULL"],
        checks=[
            "SELECT id,a,b FROM t {hint} WHERE a IS NULL ORDER BY id",
            "SELECT id,a,b FROM t {hint} WHERE a <=> NULL ORDER BY id",
            "SELECT id,a,b FROM t {hint} WHERE a IS NULL OR a = 10 ORDER BY id",
            "SELECT id,a,b FROM t {hint} WHERE b >= 0 ORDER BY id",
        ],
    )

    yield Case(
        name="dml_membership_transition",
        setup=[
            common_create,
            insert,
            "ALTER TABLE t ADD INDEX pi(b) WHERE a > 10",
        ],
        dml=[
            "UPDATE t SET a = 30 WHERE id IN (1, 2)",
            "UPDATE t SET a = 0 WHERE id IN (9, 10)",
            "DELETE FROM t WHERE id = 11",
            "INSERT INTO t VALUES (13, 50, 13, 'new_in'), (14, 5, 14, 'new_out')",
            "ADMIN CHECK TABLE t",
        ],
        checks=[
            "SELECT id,a,b FROM t {hint} WHERE a > 10 ORDER BY id",
            "SELECT id,a,b FROM t {hint} WHERE a > 10 AND b BETWEEN 1 AND 13 ORDER BY id",
        ],
    )

    yield Case(
        name="topn_ordered",
        setup=[
            "SET SESSION tidb_opt_partial_ordered_index_for_topn='COST'",
            common_create,
            insert,
            "ALTER TABLE t ADD INDEX pi(b) WHERE a > 10",
        ],
        checks=[
            "SELECT id,a,b FROM t {hint} WHERE a > 10 ORDER BY b LIMIT 3",
            "SELECT id,a,b FROM t {hint} WHERE a > 10 ORDER BY b DESC LIMIT 3",
            "SELECT id,a,b FROM t {hint} WHERE a > 5 ORDER BY b LIMIT 3",
        ],
    )

    yield Case(
        name="string_collation",
        setup=[
            "CREATE TABLE t(id INT PRIMARY KEY, a VARCHAR(20) COLLATE utf8mb4_general_ci, b INT, s VARCHAR(20))",
            "INSERT INTO t VALUES (1, NULL, 1, 'n'),(2, 'A', 2, 'cap'),(3, 'a', 3, 'low'),(4, 'b', 4, 'bee'),(5, 'z', 5, 'zed')",
            "ALTER TABLE t ADD INDEX pi(b) WHERE a >= 'b'",
        ],
        checks=[
            "SELECT id,a,b FROM t {hint} WHERE a >= 'b' ORDER BY id",
            "SELECT id,a,b FROM t {hint} WHERE a > 'a' ORDER BY id",
            "SELECT id,a,b FROM t {hint} WHERE a = 'b' ORDER BY id",
            "SELECT id,a,b FROM t {hint} WHERE a >= 'a' ORDER BY id",
        ],
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default=os.environ.get("MYSQL", "mysql"))
    parser.add_argument("--host", default=os.environ.get("TIDB_HOST", "127.0.0.1"))
    parser.add_argument("--port", type=int, default=int(os.environ.get("TIDB_PORT", "14000")))
    parser.add_argument("--user", default=os.environ.get("TIDB_USER", "root"))
    parser.add_argument("--db-prefix", default="ai_native_pi")
    parser.add_argument("--random-cases", type=int, default=0)
    parser.add_argument("--skip-fixed", action="store_true")
    parser.add_argument("--matrix", action="store_true")
    parser.add_argument("--matrix-output", default="")
    parser.add_argument("--matrix-stats-wait", type=float, default=0.25)
    parser.add_argument("--no-hint-stats", action="store_true")
    parser.add_argument("--no-hint-output", default="")
    parser.add_argument("--no-hint-row-count", type=int, default=3000)
    parser.add_argument("--no-hint-stats-wait", type=float, default=0.25)
    args = parser.parse_args()

    db = f"{args.db_prefix}_{int(time.time())}"
    print(f"mysql={shlex.join(mysql_args(args))}")
    print(f"database={db}")
    exec_ok(args, f"DROP DATABASE IF EXISTS {quote_db(db)}")
    exec_ok(args, f"CREATE DATABASE {quote_db(db)}")
    exec_ok(args, "SET GLOBAL tidb_ddl_enable_fast_reorg=ON")
    exec_ok(args, "SET GLOBAL tidb_enable_fast_table_check=ON")

    findings: list[str] = []
    matrix_findings: list[MatrixFinding] = []
    no_hint_findings: list[MatrixFinding] = []
    try:
        if not args.skip_fixed:
            for case in cases():
                print(f"\nCASE {case.name}")
                exec_ok(args, "DROP TABLE IF EXISTS t", db)
                for sql in case.setup:
                    exec_ok(args, sql, db)
                for sql in case.dml:
                    exec_ok(args, sql, db)
                for query in case.checks:
                    findings.extend(compare_query(args, db, case.name, query))
            print("\nCASE plan_cache")
            findings.extend(run_plan_cache_checks(args, db))
        print(f"\nCASE random count={args.random_cases}")
        findings.extend(run_random_checks(args, db, args.random_cases))
        if args.matrix:
            print("\nCASE matrix")
            matrix_findings = run_matrix_checks(args, db, sys.stdout)
            if args.matrix_output:
                write_matrix_csv(args.matrix_output, matrix_findings)
                print(f"MATRIX_OUTPUT {args.matrix_output}")
        if args.no_hint_stats:
            print("\nCASE no_hint_stats")
            no_hint_findings = run_no_hint_stats_checks(args, db, sys.stdout)
            if args.no_hint_output:
                write_matrix_csv(args.no_hint_output, no_hint_findings)
                print(f"NOHINT_OUTPUT {args.no_hint_output}")
    finally:
        exec_ok(args, f"DROP DATABASE IF EXISTS {quote_db(db)}")

    print(
        f"\nSUMMARY findings={len(findings)} "
        f"matrix_findings={len(matrix_findings)} no_hint_findings={len(no_hint_findings)}"
    )
    for item in findings:
        print(item)
    return 1 if findings or matrix_findings or no_hint_findings else 0


if __name__ == "__main__":
    sys.exit(main())
