#!/usr/bin/env python3
"""tidb_hot_regions_history update_time timezone probe.

Proof obligation:
    The hot-regions-history extractor converts UPDATE_TIME predicates with the
    session timezone and drops the original predicate. Returned UPDATE_TIME
    values must therefore be rendered/evaluated in the same SQL-visible
    timezone, or the shortcut can return rows that fail the user's predicate.
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


def mysql_args(args: argparse.Namespace) -> list[str]:
    return [
        args.mysql,
        f"-h{args.host}",
        f"-P{args.port}",
        f"-u{args.user}",
        "--batch",
        "--raw",
        "--skip-column-names",
        "--connect-timeout=5",
    ]


def run_mysql(args: argparse.Namespace, sql: str) -> Result:
    proc = subprocess.run(
        mysql_args(args) + ["-e", sql],
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    return Result(proc.returncode, proc.stdout.rstrip("\n"), proc.stderr.rstrip("\n"))


def combined(res: Result) -> str:
    return (res.err + "\n" + res.out).strip()


def parse_count_self(line: str) -> tuple[int, str, str, int | None] | None:
    parts = line.split("\t")
    if len(parts) < 4:
        return None
    try:
        count = int(parts[0])
        self_ok = None if parts[3] == "NULL" else int(parts[3])
    except ValueError:
        return None
    return count, parts[1], parts[2], self_ok


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default="mysql")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", default="14000")
    parser.add_argument("--user", default="root")
    parser.add_argument("--lookback-hours", type=int, default=2)
    args = parser.parse_args()

    sample_sql = f"""
SET @@max_execution_time=30000;
SET time_zone='+00:00';
SELECT DATE_FORMAT(MAX(update_time), '%Y-%m-%d %H:%i:%s')
FROM information_schema.tidb_hot_regions_history
WHERE update_time > NOW() - INTERVAL {args.lookback_hours} HOUR
  AND update_time < NOW();
"""
    sample = run_mysql(args, sample_sql)
    if sample.rc != 0 or not sample.out.strip() or sample.out.strip() == "NULL":
        print(f"SKIP\thot_regions_history_timezone\tno recent hot-region sample: {combined(sample)}")
        return 0

    try:
        utc_start = dt.datetime.strptime(sample.out.splitlines()[-1].strip(), "%Y-%m-%d %H:%M:%S")
    except ValueError:
        print(f"SKIP\thot_regions_history_timezone\tbad sample timestamp: {sample.out!r}")
        return 0
    utc_end = utc_start + dt.timedelta(seconds=1)
    plus14_start = utc_start + dt.timedelta(hours=14)
    plus14_end = utc_end + dt.timedelta(hours=14)

    utc_lo = utc_start.strftime("%Y-%m-%d %H:%M:%S")
    utc_hi = utc_end.strftime("%Y-%m-%d %H:%M:%S")
    p14_lo = plus14_start.strftime("%Y-%m-%d %H:%M:%S")
    p14_hi = plus14_end.strftime("%Y-%m-%d %H:%M:%S")

    utc_control = run_mysql(
        args,
        f"""
SET @@max_execution_time=30000;
SET time_zone='+00:00';
SELECT COUNT(*), MIN(update_time), MAX(update_time),
       SUM(update_time >= '{utc_lo}' AND update_time < '{utc_hi}')
FROM information_schema.tidb_hot_regions_history
WHERE update_time >= '{utc_lo}' AND update_time < '{utc_hi}';
""",
    )
    explain = run_mysql(
        args,
        f"""
SET @@max_execution_time=30000;
SET time_zone='+14:00';
EXPLAIN SELECT COUNT(*)
FROM information_schema.tidb_hot_regions_history
WHERE update_time >= '{p14_lo}' AND update_time < '{p14_hi}';
""",
    )
    fast = run_mysql(
        args,
        f"""
SET @@max_execution_time=30000;
SET time_zone='+14:00';
SELECT COUNT(*), MIN(update_time), MAX(update_time),
       SUM(update_time >= '{p14_lo}' AND update_time < '{p14_hi}')
FROM information_schema.tidb_hot_regions_history
WHERE update_time >= '{p14_lo}' AND update_time < '{p14_hi}';
""",
    )
    ref = run_mysql(
        args,
        f"""
SET @@max_execution_time=30000;
SET time_zone='+14:00';
SELECT COUNT(*), MIN(update_time), MAX(update_time)
FROM information_schema.tidb_hot_regions_history
WHERE update_time >= '{p14_lo}' AND update_time < '{p14_hi}'
  AND CASE
        WHEN update_time >= '{p14_lo}' AND update_time < '{p14_hi}' THEN 1
        ELSE 0
      END = 1;
""",
    )

    utc_parts = parse_count_self(utc_control.out.splitlines()[-1]) if utc_control.rc == 0 and utc_control.out else None
    fast_parts = parse_count_self(fast.out.splitlines()[-1]) if fast.rc == 0 and fast.out else None
    ref_count = None
    if ref.rc == 0 and ref.out.strip():
        try:
            ref_count = int(ref.out.splitlines()[-1].split("\t")[0])
        except ValueError:
            ref_count = None

    trigger = "TIDB_HOT_REGIONS_HISTORY" in explain.out and "start_time:" in explain.out and "end_time:" in explain.out
    utc_ok = utc_parts is not None and utc_parts[0] > 0 and utc_parts[3] == utc_parts[0]
    finding = (
        trigger
        and utc_ok
        and fast_parts is not None
        and fast_parts[0] > 0
        and fast_parts[3] == 0
        and ref_count == 0
    )

    if finding:
        detail = (
            f"utc_window=[{utc_lo},{utc_hi}), plus14_window=[{p14_lo},{p14_hi}), "
            f"utc_control={utc_control.out.splitlines()[-1]!r}, "
            f"fast={fast.out.splitlines()[-1]!r}, ref={ref.out.splitlines()[-1]!r}"
        )
        print(f"FINDING\thot_regions_history_timezone\t{detail}")
        print("SUMMARY total=1 findings=1 skipped=0")
        return 1

    print("INFO\thot_regions_history_timezone\tno finding")
    print(f"DETAIL sample={sample.out!r}")
    print(f"DETAIL explain rc={explain.rc} {explain.out!r} {combined(explain)!r}")
    print(f"DETAIL utc_control rc={utc_control.rc} {utc_control.out!r} {combined(utc_control)!r}")
    print(f"DETAIL fast rc={fast.rc} {fast.out!r} {combined(fast)!r}")
    print(f"DETAIL ref rc={ref.rc} {ref.out!r} {combined(ref)!r}")
    print("SUMMARY total=1 findings=0 skipped=0")
    return 0


if __name__ == "__main__":
    sys.exit(main())
