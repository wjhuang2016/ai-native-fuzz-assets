#!/usr/bin/env python3
"""Predicate-simplification proof-obligation probe.

The oracle compares a normal WHERE predicate with the same predicate wrapped in
CASE. In SQL WHERE, only TRUE survives, so these two forms should return the
same rows:

    WHERE P
    WHERE CASE WHEN P THEN 1 ELSE 0 END = 1
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
    family: str
    name: str
    pred: str


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


def rowset_sql(predicate: str, case_wrapped: bool) -> str:
    where = predicate
    if case_wrapped:
        where = f"CASE WHEN ({predicate}) THEN 1 ELSE 0 END = 1"
    return (
        "SELECT COALESCE(GROUP_CONCAT(CONCAT_WS(',', id, IFNULL(CAST(a AS CHAR), 'NULL'), "
        "IFNULL(CAST(b AS CHAR), 'NULL'), IFNULL(s, 'NULL')) ORDER BY id SEPARATOR '|'), '<empty>') "
        f"FROM t WHERE {where}"
    )


def explain_sql(predicate: str) -> str:
    return "EXPLAIN FORMAT='brief' SELECT id FROM t WHERE " + predicate + " ORDER BY id"


def setup(args: argparse.Namespace, db: str) -> None:
    exec_ok(
        args,
        """
CREATE TABLE t(
  id INT PRIMARY KEY,
  a INT NULL,
  b INT NULL,
  s VARCHAR(8) COLLATE utf8mb4_general_ci NULL,
  sb VARCHAR(8) COLLATE utf8mb4_bin NULL,
  KEY ia(a),
  KEY ib(b),
  KEY is_g(s),
  KEY is_b(sb)
)""",
        db,
    )
    exec_ok(
        args,
        """
INSERT INTO t VALUES
  (1,NULL,NULL,NULL,NULL),
  (2,-1,0,'a','a'),
  (3,0,1,'A','A'),
  (4,1,1,'b','b'),
  (5,2,2,'B','B'),
  (6,3,NULL,'',''),
  (7,10,3,'m','m')
""",
        db,
    )


def cases() -> list[PredicateCase]:
    cases_out: list[PredicateCase] = []

    scalars = [
        ("eq_0", "a = 0"),
        ("eq_1", "a = 1"),
        ("ne_1", "a != 1"),
        ("lt_1", "a < 1"),
        ("le_1", "a <= 1"),
        ("gt_1", "a > 1"),
        ("ge_1", "a >= 1"),
        ("is_null", "a IS NULL"),
        ("in_0_1_null", "a IN (0, 1, NULL)"),
        ("in_0_1", "a IN (0, 1)"),
    ]
    branches = [
        ("eq_0", "a = 0"),
        ("eq_1", "a = 1"),
        ("ne_1", "a != 1"),
        ("lt_0", "a < 0"),
        ("gt_2", "a > 2"),
        ("in_1_2", "a IN (1, 2)"),
        ("in_null_2", "a IN (NULL, 2)"),
        ("is_null", "a IS NULL"),
    ]
    for s_name, scalar in scalars:
        for left_name, left in branches:
            for right_name, right in branches:
                cases_out.append(
                    PredicateCase(
                        "scalar_and_or",
                        f"{s_name}__{left_name}_or_{right_name}",
                        f"({scalar}) AND (({left}) OR ({right}))",
                    )
                )

    in_lists = [
        ("in_0_1_2", "a IN (0, 1, 2)"),
        ("in_1_null", "a IN (1, NULL)"),
        ("in_null", "a IN (NULL)"),
        ("in_dup", "a IN (1, 1, 2)"),
    ]
    not_equals = [
        ("ne_1", "a != 1"),
        ("ne_2", "a != 2"),
        ("ne_null", "a != NULL"),
    ]
    for in_name, in_pred in in_lists:
        for ne_name, ne_pred in not_equals:
            cases_out.append(PredicateCase("in_and_ne", f"{in_name}__{ne_name}", f"({in_pred}) AND ({ne_pred})"))
            cases_out.append(PredicateCase("in_and_ne_rev", f"{ne_name}__{in_name}", f"({ne_pred}) AND ({in_pred})"))

    for in_name, in_pred in in_lists:
        cases_out.append(PredicateCase("null_and_in", f"is_null__{in_name}", f"a IS NULL AND ({in_pred})"))
        cases_out.append(PredicateCase("null_or_in", f"is_null__or__{in_name}", f"a IS NULL OR ({in_pred})"))

    string_atoms = [
        ("s_eq_a", "s = 'a'"),
        ("s_eq_A_bin", "s = _utf8mb4'A' COLLATE utf8mb4_bin"),
        ("s_ne_A_bin", "s != _utf8mb4'A' COLLATE utf8mb4_bin"),
        ("s_in_a_A", "s IN ('a', 'A')"),
        ("s_lt_b", "s < 'b'"),
        ("s_is_null", "s IS NULL"),
        ("sb_eq_a", "sb = 'a'"),
        ("sb_ne_A_ci", "sb != _utf8mb4'A' COLLATE utf8mb4_general_ci"),
    ]
    for left_name, left in string_atoms:
        for right_name, right in string_atoms:
            cases_out.append(PredicateCase("string_and", f"{left_name}__{right_name}", f"({left}) AND ({right})"))
            cases_out.append(PredicateCase("string_or", f"{left_name}__or__{right_name}", f"({left}) OR ({right})"))

    return cases_out


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default=os.environ.get("MYSQL", "mysql"))
    parser.add_argument("--host", default=os.environ.get("TIDB_HOST", "127.0.0.1"))
    parser.add_argument("--port", type=int, default=int(os.environ.get("TIDB_PORT", "14000")))
    parser.add_argument("--user", default=os.environ.get("TIDB_USER", "root"))
    parser.add_argument("--db-prefix", default="ai_native_pred_simpl")
    parser.add_argument("--case", default="", help="run only cases whose family/name/predicate contains this substring")
    parser.add_argument("--max-cases", type=int, default=0, help="0 means no cap")
    parser.add_argument("--progress-every", type=int, default=100)
    args = parser.parse_args()

    db = f"{args.db_prefix}_{int(time.time())}"
    print(f"mysql={shlex.join(mysql_args(args))}")
    print(f"database={db}")
    exec_ok(args, f"DROP DATABASE IF EXISTS {quote_db(db)}")
    exec_ok(args, f"CREATE DATABASE {quote_db(db)}")

    findings = 0
    try:
        setup(args, db)
        selected = [
            c for c in cases() if not args.case or args.case in c.family or args.case in c.name or args.case in c.pred
        ]
        if args.max_cases > 0:
            selected = selected[: args.max_cases]
        print(f"CASES count={len(selected)}", flush=True)
        for i, case in enumerate(selected, 1):
            if args.progress_every > 0 and (i == 1 or i % args.progress_every == 0 or i == len(selected)):
                print(f"PROGRESS {i}/{len(selected)} family={case.family} name={case.name}", flush=True)
            normal = run_mysql(args, rowset_sql(case.pred, False), db)
            wrapped = run_mysql(args, rowset_sql(case.pred, True), db)
            if normal.rc != wrapped.rc or normal.out != wrapped.out:
                findings += 1
                plan = run_mysql(args, explain_sql(case.pred), db)
                print(
                    "\n".join(
                        [
                            f"HIT family={case.family} name={case.name}",
                            f"predicate={case.pred}",
                            f"normal_rc={normal.rc} wrapped_rc={wrapped.rc}",
                            f"normal_rows={normal.out}",
                            f"wrapped_rows={wrapped.out}",
                            f"normal_err={normal.err}",
                            f"wrapped_err={wrapped.err}",
                            "plan=" + " | ".join(plan.out.splitlines()[:8]),
                        ]
                    ),
                    flush=True,
                )
    finally:
        exec_ok(args, f"DROP DATABASE IF EXISTS {quote_db(db)}")

    print(f"SUMMARY findings={findings}")
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main())
