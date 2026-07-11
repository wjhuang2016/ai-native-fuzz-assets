#!/usr/bin/env python3
"""Probe InfoSchema scalar-pushdown predicate extraction.

Target proof obligation:

    LOWER/UPPER(TABLE_NAME) = const must keep the SQL-visible scalar predicate
    semantics. If the InfoSchema extractor uses the scalar function only as a
    prefilter and drops the original predicate, the returned rows must still
    satisfy the original expression.
"""

from __future__ import annotations

import argparse
import dataclasses
import subprocess
import sys
import uuid


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


def quote_ident(name: str) -> str:
    return "`" + name.replace("`", "``") + "`"


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


def infoschema_scalar_pushdown_cell(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    qdb = quote_ident(db)
    sql = f"""
DROP DATABASE IF EXISTS {qdb};
CREATE DATABASE {qdb};
USE {qdb};
CREATE TABLE Acase(id INT);

SELECT 'MARK','lower_upper_fast';
SELECT table_name, LOWER(table_name), LOWER(table_name) = 'ACASE'
FROM information_schema.tables
WHERE table_schema='{db}' AND LOWER(table_name) = 'ACASE'
ORDER BY table_name;
SELECT 'END','lower_upper_fast';

SELECT 'MARK','lower_upper_ref';
SELECT table_name, LOWER(table_name), LOWER(table_name) = 'ACASE'
FROM information_schema.tables
WHERE table_schema='{db}'
  AND CASE WHEN LOWER(table_name) = 'ACASE' THEN TRUE ELSE FALSE END
ORDER BY table_name;
SELECT 'END','lower_upper_ref';

SELECT 'MARK','upper_lower_fast';
SELECT table_name, UPPER(table_name), UPPER(table_name) = 'acase'
FROM information_schema.tables
WHERE table_schema='{db}' AND UPPER(table_name) = 'acase'
ORDER BY table_name;
SELECT 'END','upper_lower_fast';

SELECT 'MARK','upper_lower_ref';
SELECT table_name, UPPER(table_name), UPPER(table_name) = 'acase'
FROM information_schema.tables
WHERE table_schema='{db}'
  AND CASE WHEN UPPER(table_name) = 'acase' THEN TRUE ELSE FALSE END
ORDER BY table_name;
SELECT 'END','upper_lower_ref';

SELECT 'MARK','lower_lower_control_fast';
SELECT table_name, LOWER(table_name), LOWER(table_name) = 'acase'
FROM information_schema.tables
WHERE table_schema='{db}' AND LOWER(table_name) = 'acase'
ORDER BY table_name;
SELECT 'END','lower_lower_control_fast';

SELECT 'MARK','lower_lower_control_ref';
SELECT table_name, LOWER(table_name), LOWER(table_name) = 'acase'
FROM information_schema.tables
WHERE table_schema='{db}'
  AND CASE WHEN LOWER(table_name) = 'acase' THEN TRUE ELSE FALSE END
ORDER BY table_name;
SELECT 'END','lower_lower_control_ref';

DROP DATABASE {qdb};
"""
    res = run_mysql(args, sql)
    if res.rc != 0:
        run_mysql(args, f"DROP DATABASE IF EXISTS {qdb}")
        return False, "setup/probe SQL failed: " + combined(res)

    sections = collect_sections(res.out)
    lower_fast = sections.get("lower_upper_fast", [])
    lower_ref = sections.get("lower_upper_ref", [])
    upper_fast = sections.get("upper_lower_fast", [])
    upper_ref = sections.get("upper_lower_ref", [])
    control_fast = sections.get("lower_lower_control_fast", [])
    control_ref = sections.get("lower_lower_control_ref", [])

    control_expected = [f"Acase\tacase\t1"]
    if control_fast != control_expected or control_ref != control_expected:
        return False, (
            "INVALID control failed; "
            f"control_fast={control_fast!r}; control_ref={control_ref!r}"
        )

    findings: list[str] = []
    if lower_fast and not lower_ref and all(line.endswith("\t0") for line in lower_fast):
        findings.append(
            "LOWER(TABLE_NAME)='ACASE' fast path returned scalar-false rows: "
            f"{lower_fast!r}"
        )
    if upper_fast and not upper_ref and all(line.endswith("\t0") for line in upper_fast):
        findings.append(
            "UPPER(TABLE_NAME)='acase' fast path returned scalar-false rows: "
            f"{upper_fast!r}"
        )

    if findings:
        return False, "; ".join(findings)

    return True, (
        "scalar-pushdown InfoSchema predicates matched CASE reference; "
        f"lower_fast={lower_fast!r}; lower_ref={lower_ref!r}; "
        f"upper_fast={upper_fast!r}; upper_ref={upper_ref!r}"
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default="mysql")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", default="14000")
    parser.add_argument("--user", default="root")
    parser.add_argument("--database-prefix", default="ai_is_scalar")
    args = parser.parse_args()

    db = f"{args.database_prefix}_{uuid.uuid4().hex[:8]}"
    ok, detail = infoschema_scalar_pushdown_cell(args, db)
    if ok:
        print("OK\tinfoschema_scalar_pushdown_case\t" + detail.replace("\n", " ")[:1200])
        print("SUMMARY total=1 findings=0 skipped=0")
        return 0
    print("FINDING\tinfoschema_scalar_pushdown_case\t" + detail.replace("\n", " ")[:1200])
    print("SUMMARY total=1 findings=1 skipped=0")
    return 1


if __name__ == "__main__":
    sys.exit(main())
