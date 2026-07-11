#!/usr/bin/env python3
"""Probe metrics_summary METRICS_NAME extractor case semantics.

Target proof obligation:

    METRICS_NAME predicates on information_schema.metrics_summary must preserve
    the SQL-visible utf8mb4_bin comparison semantics. If the extractor lowercases
    the requested metric name and drops the original predicate, returned rows
    must still satisfy that predicate.
"""

from __future__ import annotations

import argparse
import dataclasses
import subprocess
import sys


@dataclasses.dataclass
class Result:
    rc: int
    out: str
    err: str


def run_mysql(args: argparse.Namespace, sql: str) -> Result:
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
    proc = subprocess.run(
        cmd,
        input=sql,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    return Result(proc.returncode, proc.stdout.strip(), proc.stderr.strip())


def combined(res: Result) -> str:
    return (res.out + "\n" + res.err).strip()


def collect_sections(output: str) -> dict[str, list[str]]:
    sections: dict[str, list[str]] = {}
    current: str | None = None
    for raw in output.splitlines():
        line = raw.rstrip("\n")
        if line.startswith("MARK\t"):
            current = line.split("\t", 1)[1]
            sections[current] = []
            continue
        if line.startswith("END\t"):
            current = None
            continue
        if current is not None:
            sections[current].append(line)
    return sections


def metrics_summary_name_case_cell(args: argparse.Namespace) -> tuple[bool, str]:
    sql = """
SELECT 'MARK','column_contract';
SHOW FULL COLUMNS FROM information_schema.metrics_summary LIKE 'METRICS_NAME';
SELECT 'END','column_contract';

SELECT 'MARK','plain_wrong_fast';
SELECT metrics_name, metrics_name = 'TIDB_QPS'
FROM information_schema.metrics_summary
WHERE metrics_name = 'TIDB_QPS'
ORDER BY metrics_name
LIMIT 3;
SELECT 'END','plain_wrong_fast';

SELECT 'MARK','plain_wrong_ref';
SELECT metrics_name, metrics_name = 'TIDB_QPS'
FROM information_schema.metrics_summary
WHERE metrics_name = 'tidb_qps'
  AND CASE WHEN metrics_name = 'TIDB_QPS' THEN TRUE ELSE FALSE END
ORDER BY metrics_name
LIMIT 3;
SELECT 'END','plain_wrong_ref';

SELECT 'MARK','lower_wrong_fast';
SELECT metrics_name, LOWER(metrics_name), LOWER(metrics_name) = 'TIDB_QPS'
FROM information_schema.metrics_summary
WHERE metrics_name = 'tidb_qps'
  AND LOWER(metrics_name) = 'TIDB_QPS'
ORDER BY metrics_name
LIMIT 3;
SELECT 'END','lower_wrong_fast';

SELECT 'MARK','lower_wrong_ref';
SELECT metrics_name, LOWER(metrics_name), LOWER(metrics_name) = 'TIDB_QPS'
FROM information_schema.metrics_summary
WHERE metrics_name = 'tidb_qps'
  AND CASE WHEN LOWER(metrics_name) = 'TIDB_QPS' THEN TRUE ELSE FALSE END
ORDER BY metrics_name
LIMIT 3;
SELECT 'END','lower_wrong_ref';

SELECT 'MARK','green_plain_fast';
SELECT metrics_name, metrics_name = 'tidb_qps'
FROM information_schema.metrics_summary
WHERE metrics_name = 'tidb_qps'
ORDER BY metrics_name
LIMIT 3;
SELECT 'END','green_plain_fast';

SELECT 'MARK','green_lower_fast';
SELECT metrics_name, LOWER(metrics_name), LOWER(metrics_name) = 'tidb_qps'
FROM information_schema.metrics_summary
WHERE metrics_name = 'tidb_qps'
  AND LOWER(metrics_name) = 'tidb_qps'
ORDER BY metrics_name
LIMIT 3;
SELECT 'END','green_lower_fast';
"""
    res = run_mysql(args, sql)
    if res.rc != 0:
        return False, "probe SQL failed: " + combined(res)

    sections = collect_sections(res.out)
    contract = sections.get("column_contract", [])
    plain_fast = sections.get("plain_wrong_fast", [])
    plain_ref = sections.get("plain_wrong_ref", [])
    lower_fast = sections.get("lower_wrong_fast", [])
    lower_ref = sections.get("lower_wrong_ref", [])
    green_plain = sections.get("green_plain_fast", [])
    green_lower = sections.get("green_lower_fast", [])

    if not any("METRICS_NAME\tvarchar(64)\tutf8mb4_bin" in line for line in contract):
        return False, f"INVALID column contract not binary: {contract!r}"

    if green_plain != ["tidb_qps\t1"]:
        return False, f"INVALID plain green control failed: {green_plain!r}"
    if green_lower != ["tidb_qps\ttidb_qps\t1"]:
        return False, f"INVALID lower green control failed: {green_lower!r}"

    findings: list[str] = []
    if plain_fast == ["tidb_qps\t0"] and not plain_ref:
        findings.append(
            "METRICS_NAME='TIDB_QPS' returned scalar-false row: "
            f"{plain_fast!r}"
        )
    if lower_fast == ["tidb_qps\ttidb_qps\t0"] and not lower_ref:
        findings.append(
            "LOWER(METRICS_NAME)='TIDB_QPS' returned scalar-false row: "
            f"{lower_fast!r}"
        )

    if findings:
        return False, "; ".join(findings)

    return True, (
        "metrics_summary METRICS_NAME predicates matched CASE/self reference; "
        f"plain_fast={plain_fast!r}; plain_ref={plain_ref!r}; "
        f"lower_fast={lower_fast!r}; lower_ref={lower_ref!r}"
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default="mysql")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", default="14000")
    parser.add_argument("--user", default="root")
    args = parser.parse_args()

    ok, detail = metrics_summary_name_case_cell(args)
    if ok:
        print("OK\tmetrics_summary_name_case\t" + detail.replace("\n", " ")[:1200])
        print("SUMMARY total=1 findings=0 skipped=0")
        return 0
    prefix = "INVALID" if detail.startswith("INVALID") or detail.startswith("probe SQL failed") else "FINDING"
    print(prefix + "\tmetrics_summary_name_case\t" + detail.replace("\n", " ")[:1200])
    if prefix == "FINDING":
        print("SUMMARY total=1 findings=1 skipped=0")
        return 1
    print("SUMMARY total=1 findings=0 skipped=1")
    return 2


if __name__ == "__main__":
    sys.exit(main())
