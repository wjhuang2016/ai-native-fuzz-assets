#!/usr/bin/env python3
"""Probe statements_summary coarse time-range skip semantics.

Proof obligation:

    A shortcut may use summary_begin_time / summary_end_time predicates to avoid
    scanning statement-summary rows only when the shortcut's coarse range is a
    necessary consequence of the original SQL predicate. The predicates

        summary_begin_time <= A AND summary_end_time >= B

    are satisfiable for any row whose summary window spans [A, B], even when
    B > A. Treating B > A as an empty request range is therefore unsafe.
"""

from __future__ import annotations

import argparse
import dataclasses
import datetime as dt
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


def parse_dt(value: str) -> dt.datetime:
    return dt.datetime.strptime(value, "%Y-%m-%d %H:%M:%S")


def fmt_dt(value: dt.datetime) -> str:
    return value.strftime("%Y-%m-%d %H:%M:%S")


def statements_summary_coarse_range_cell(args: argparse.Namespace) -> tuple[bool, str]:
    sample_sql = """
SET time_zone = '+00:00';
SELECT 'MARK','sample';
SELECT summary_begin_time, summary_end_time,
       TIMESTAMPDIFF(SECOND, summary_begin_time, summary_end_time) AS span_s
FROM information_schema.statements_summary
WHERE TIMESTAMPDIFF(SECOND, summary_begin_time, summary_end_time) >= 60
ORDER BY summary_end_time DESC, digest_text
LIMIT 1;
SELECT 'END','sample';
"""
    sample_res = run_mysql(args, sample_sql)
    if sample_res.rc != 0:
        return False, "probe SQL failed during sample: " + combined(sample_res)

    sample_sections = collect_sections(sample_res.out)
    sample_rows = sample_sections.get("sample", [])
    if not sample_rows:
        return False, "INVALID no statements_summary row with span >= 60s"
    parts = sample_rows[0].split("\t")
    if len(parts) != 3:
        return False, f"INVALID malformed sample row: {sample_rows!r}"
    begin = parse_dt(parts[0])
    end = parse_dt(parts[1])
    span = int(parts[2])
    if span < 60 or not begin < end:
        return False, f"INVALID unusable sample begin/end/span: {sample_rows!r}"

    a = begin + (end - begin) / 3
    b = begin + (end - begin) * 2 / 3
    a_s, b_s = fmt_dt(a), fmt_dt(b)

    probe_sql = f"""
SET time_zone = '+00:00';

SELECT 'MARK','plan_fast';
EXPLAIN
SELECT digest, summary_begin_time, summary_end_time
FROM information_schema.statements_summary
WHERE summary_begin_time <= TIMESTAMP '{a_s}'
  AND summary_end_time >= TIMESTAMP '{b_s}'
LIMIT 3;
SELECT 'END','plan_fast';

SELECT 'MARK','fast';
SELECT 'FAST', COUNT(*) AS n
FROM information_schema.statements_summary
WHERE summary_begin_time <= TIMESTAMP '{a_s}'
  AND summary_end_time >= TIMESTAMP '{b_s}';
SELECT 'END','fast';

SELECT 'MARK','ref';
SELECT 'REF', COUNT(*) AS n,
       COALESCE(SUM(summary_begin_time <= TIMESTAMP '{a_s}'), 0) AS begin_ok,
       COALESCE(SUM(summary_end_time >= TIMESTAMP '{b_s}'), 0) AS end_ok
FROM information_schema.statements_summary
WHERE CASE WHEN summary_begin_time <= TIMESTAMP '{a_s}' THEN TRUE ELSE FALSE END
  AND CASE WHEN summary_end_time >= TIMESTAMP '{b_s}' THEN TRUE ELSE FALSE END;
SELECT 'END','ref';

SELECT 'MARK','green_overlap';
EXPLAIN
SELECT digest
FROM information_schema.statements_summary
WHERE summary_begin_time <= TIMESTAMP '{b_s}'
  AND summary_end_time >= TIMESTAMP '{a_s}'
LIMIT 3;
SELECT 'END','green_overlap';

SELECT 'MARK','green_count';
SELECT 'GREEN', COUNT(*) AS n
FROM information_schema.statements_summary
WHERE summary_begin_time <= TIMESTAMP '{b_s}'
  AND summary_end_time >= TIMESTAMP '{a_s}';
SELECT 'END','green_count';
"""
    probe_res = run_mysql(args, probe_sql)
    if probe_res.rc != 0:
        return False, "probe SQL failed: " + combined(probe_res)

    sections = collect_sections(probe_res.out)
    plan_fast = sections.get("plan_fast", [])
    fast = sections.get("fast", [])
    ref = sections.get("ref", [])
    green_plan = sections.get("green_overlap", [])
    green_count = sections.get("green_count", [])

    if not any("skip_request: true" in line.lower() for line in plan_fast):
        return False, f"INVALID fast plan did not trigger skip_request: {plan_fast!r}"
    if not fast or fast[0].split("\t")[:2] != ["FAST", "0"]:
        return False, f"INVALID fast arm did not return zero rows: {fast!r}"
    if not ref:
        return False, f"INVALID missing reference result: {ref!r}"
    ref_parts = ref[0].split("\t")
    if len(ref_parts) != 4 or ref_parts[0] != "REF":
        return False, f"INVALID malformed reference result: {ref!r}"
    ref_n, begin_ok, end_ok = map(int, ref_parts[1:])
    if ref_n <= 0:
        return False, f"INVALID reference found no satisfiable row: sample={sample_rows!r}; ref={ref!r}"
    if begin_ok != ref_n or end_ok != ref_n:
        return False, f"INVALID reference rows do not satisfy projected predicates: {ref!r}"

    # A non-reversed overlap condition should not be skipped and should find rows on the same data.
    if any("skip_request: true" in line.lower() for line in green_plan):
        return False, f"INVALID green overlap plan was skipped: {green_plan!r}"
    if not green_count:
        return False, f"INVALID missing green result: {green_count!r}"
    green_parts = green_count[0].split("\t")
    if len(green_parts) != 2 or green_parts[0] != "GREEN" or int(green_parts[1]) <= 0:
        return False, f"INVALID green overlap found no rows: {green_count!r}"

    detail = (
        "statements_summary skipped satisfiable interval-overlap predicates: "
        f"window=[{parts[0]},{parts[1]}], A={a_s}, B={b_s}, "
        f"fast={fast!r}, ref={ref!r}, green={green_count!r}"
    )
    return False, detail


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default="mysql")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", default="14000")
    parser.add_argument("--user", default="root")
    args = parser.parse_args()

    ok, detail = statements_summary_coarse_range_cell(args)
    if ok:
        print("OK\tstatements_summary_coarse_range\t" + detail.replace("\n", " ")[:1200])
        print("SUMMARY total=1 findings=0 skipped=0")
        return 0
    prefix = "INVALID" if detail.startswith("INVALID") or detail.startswith("probe SQL failed") else "FINDING"
    print(prefix + "\tstatements_summary_coarse_range\t" + detail.replace("\n", " ")[:1200])
    if prefix == "FINDING":
        print("SUMMARY total=1 findings=1 skipped=0")
        return 1
    print("SUMMARY total=1 findings=0 skipped=1")
    return 2


if __name__ == "__main__":
    sys.exit(main())
