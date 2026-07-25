#!/usr/bin/env python3
"""Compare TiKV-pushed operators with equivalent type-preserving TiDB-root paths."""

from __future__ import annotations

import argparse
import json
import subprocess
from pathlib import Path
from typing import Any


MARKERS = (
    "__PUSH_PLAN__",
    "__ROOT_PLAN__",
    "__PUSH_ROWS__",
    "__ROOT_ROWS__",
    "__END__",
)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", required=True, help="Path to the mysql client")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=4000)
    parser.add_argument("--user", default="root")
    parser.add_argument("--database", required=True)
    parser.add_argument("--table", required=True)
    parser.add_argument("--id-column", default="id")
    parser.add_argument("--predicates", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    return parser.parse_args()


def split_sections(stdout: str) -> dict[str, list[str]]:
    sections: dict[str, list[str]] = {marker: [] for marker in MARKERS}
    current: str | None = None
    for line in stdout.splitlines():
        if line in sections:
            current = line
            continue
        if current is not None:
            sections[current].append(line)
    return sections


def run_case(args: argparse.Namespace, case: dict[str, Any]) -> dict[str, Any]:
    kind = case.get("kind", "predicate")
    expression = case.get("predicate") or case.get("expression")
    if kind == "predicate":
        push = f"SELECT {args.id_column} FROM {args.table} WHERE {expression}"
        root = (
            f"SELECT {args.id_column} FROM {args.table} "
            f"WHERE ({expression}) OR RAND() < 0"
        )
        push_rows_query = f"{push} ORDER BY {args.id_column}"
        root_rows_query = f"{root} ORDER BY {args.id_column}"
    elif kind == "topn":
        limit = int(case.get("limit", 5))
        push = (
            f"SELECT {args.id_column} FROM {args.table} "
            f"ORDER BY {expression}, {args.id_column} LIMIT {limit}"
        )
        root = (
            f"SELECT {args.id_column} FROM "
            f"(SELECT {args.id_column}, {expression} AS _e FROM {args.table} "
            f"LIMIT 18446744073709551615) AS _root "
            f"ORDER BY _e, {args.id_column} LIMIT {limit}"
        )
        push_rows_query = push
        root_rows_query = root
    elif kind == "aggregate":
        functions = case.get("functions", ["MIN", "MAX", "COUNT"])
        push_items = ", ".join(f"{fn}({expression})" for fn in functions)
        root_items = ", ".join(f"{fn}(_e)" for fn in functions)
        push = f"SELECT {push_items} FROM {args.table}"
        root = (
            f"SELECT {root_items} FROM "
            f"(SELECT {expression} AS _e FROM {args.table} "
            f"LIMIT 18446744073709551615) AS _root"
        )
        push_rows_query = push
        root_rows_query = root
    elif kind == "groupby":
        push = (
            f"SELECT {expression}, COUNT(*) "
            f"FROM {args.table} GROUP BY {expression} ORDER BY 1, 2"
        )
        root = (
            "SELECT _e, COUNT(*) FROM "
            f"(SELECT {expression} AS _e FROM {args.table} "
            "LIMIT 18446744073709551615) AS _root "
            "GROUP BY _e ORDER BY 1, 2"
        )
        push_rows_query = push
        root_rows_query = root
    else:
        raise ValueError(f"unsupported case kind: {kind}")
    sql = "\n".join(
        [
            "SELECT '__PUSH_PLAN__';",
            f"EXPLAIN FORMAT='brief' {push};",
            "SELECT '__ROOT_PLAN__';",
            f"EXPLAIN FORMAT='brief' {root};",
            "SELECT '__PUSH_ROWS__';",
            f"{push_rows_query};",
            "SELECT '__ROOT_ROWS__';",
            f"{root_rows_query};",
            "SELECT '__END__';",
        ]
    )
    proc = subprocess.run(
        [
            args.mysql,
            "--batch",
            "--raw",
            "--skip-column-names",
            "-h",
            args.host,
            "-P",
            str(args.port),
            "-u",
            args.user,
            args.database,
        ],
        input=sql,
        text=True,
        capture_output=True,
        check=False,
    )
    sections = split_sections(proc.stdout)
    push_plan = sections["__PUSH_PLAN__"]
    root_plan = sections["__ROOT_PLAN__"]
    push_rows = sections["__PUSH_ROWS__"]
    root_rows = sections["__ROOT_ROWS__"]
    operator = {
        "predicate": "Selection",
        "topn": "TopN",
        "aggregate": "Agg",
        "groupby": "Agg",
    }[kind]
    push_is_cop = any(
        operator in line and "\tcop[tikv]\t" in line for line in push_plan
    )
    root_is_root = any(
        operator in line and "\troot\t" in line for line in root_plan
    ) and not any(
        operator in line and "\tcop[tikv]\t" in line for line in root_plan
    )
    return {
        "name": case["name"],
        "kind": kind,
        "expression": expression,
        "status": (
            "MISMATCH"
            if proc.returncode == 0
            and push_is_cop
            and root_is_root
            and push_rows != root_rows
            else "MATCH"
            if proc.returncode == 0 and push_is_cop and root_is_root
            else "INVALID"
        ),
        "push_rows": push_rows,
        "root_rows": root_rows,
        "push_is_cop": push_is_cop,
        "root_is_root": root_is_root,
        "push_plan": push_plan,
        "root_plan": root_plan,
        "stderr": proc.stderr.strip(),
        "returncode": proc.returncode,
    }


def main() -> None:
    args = parse_args()
    cases = json.loads(args.predicates.read_text())
    results = [run_case(args, case) for case in cases]
    kinds = {case.get("kind", "predicate") for case in cases}
    oracle = (
        "pushed operator versus type-preserving derived-table root barrier"
        if kinds != {"predicate"}
        else "pushed Selection versus root Selection with a false volatile disjunct"
    )
    report = {
        "oracle": oracle,
        "database": args.database,
        "table": args.table,
        "summary": {
            status: sum(result["status"] == status for result in results)
            for status in ("MISMATCH", "MATCH", "INVALID")
        },
        "results": results,
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
    print(json.dumps(report["summary"], sort_keys=True))


if __name__ == "__main__":
    main()
