#!/usr/bin/env python3
"""cluster_log sub-millisecond equality probe (methodology v2, selector S3).

Proof obligation:
    A memtable predicate extractor may prefilter by the backend log-search
    precision, but it must not drop the SQL-visible predicate unless the backend
    request is exactly equivalent.

Bug:
    ClusterLogTableExtractor extracts `time = const` as a nanosecond timestamp,
    then truncates it to milliseconds before sending SearchLogRequest. The
    original predicate is removed, so a literal such as `.609500` can return log
    rows at `.609` even though the row evaluates `time = '.609500'` as false.

Oracle:
    Compare the fast `WHERE time = probe` arm with a CASE-wrapped scalar recheck
    over the same millisecond window. A returned row whose own predicate value is
    false is a deterministic wrong-result signal.
"""

from __future__ import annotations

import argparse
import datetime as dt
import subprocess
import sys


def q(args: argparse.Namespace, sql: str) -> tuple[int, str, str]:
    proc = subprocess.run(
        [
            args.mysql,
            f"-h{args.host}",
            f"-P{args.port}",
            f"-u{args.user}",
            "--batch",
            "--raw",
            "--skip-column-names",
            "--connect-timeout=5",
            "-e",
            sql,
        ],
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    return proc.returncode, proc.stdout.strip(), proc.stderr.strip()


def parse_log_time(value: str) -> dt.datetime:
    if "." not in value:
        value = value + ".000"
    main, frac = value.split(".", 1)
    frac = (frac + "000000")[:6]
    return dt.datetime.strptime(f"{main}.{frac}", "%Y/%m/%d %H:%M:%S.%f")


def fmt_log_time(value: dt.datetime) -> str:
    # cluster_log displays millisecond precision. The probe literal uses six
    # digits to stay in the same millisecond while being SQL-unequal.
    return value.strftime("%Y/%m/%d %H:%M:%S.%f")


def scalar(sql_value: str) -> str:
    return sql_value.replace("\\", "\\\\").replace("'", "''")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--mysql", default="mysql")
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", default="14000")
    ap.add_argument("--user", default="root")
    ap.add_argument("--start", default="2026/07/01 00:00:00")
    ap.add_argument("--end", default="2026/07/04 00:00:00")
    args = ap.parse_args()

    rc, version, err = q(args, "SELECT VERSION()")
    if rc != 0:
        print(f"ERROR version query failed: {err}", file=sys.stderr)
        return 2
    print(f"FINGERPRINT version={version.splitlines()[-1]}")

    sample_sql = (
        "SELECT time FROM information_schema.cluster_log "
        f"WHERE time >= '{scalar(args.start)}' AND time < '{scalar(args.end)}' "
        "AND message LIKE '%' ORDER BY time DESC LIMIT 1"
    )
    rc, out, err = q(args, sample_sql)
    if rc != 0:
        print(f"ERROR sample query failed: {err}", file=sys.stderr)
        return 2
    if not out:
        print("INVALID(untriggered)\tcluster_log_subms_eq\tno log rows in requested window")
        print("SUMMARY total=1 findings=0 skipped=1")
        return 0

    base_text = out.splitlines()[-1]
    base = parse_log_time(base_text)
    probe = base + dt.timedelta(microseconds=500)
    upper = base + dt.timedelta(milliseconds=1)
    probe_text = fmt_log_time(probe)
    upper_text = fmt_log_time(upper)

    fast_sql = (
        "SELECT COUNT(*), COALESCE(SUM(time = '" + scalar(probe_text) + "'),0), "
        "MIN(time), MAX(time) FROM information_schema.cluster_log "
        "WHERE time = '" + scalar(probe_text) + "' AND message LIKE '%'"
    )
    ref_sql = (
        "SELECT COUNT(*), COALESCE(SUM(time = '" + scalar(probe_text) + "'),0), "
        "MIN(time), MAX(time) FROM information_schema.cluster_log "
        "WHERE time >= '" + scalar(base_text) + "' "
        "AND time < '" + scalar(upper_text) + "' "
        "AND message LIKE '%' "
        "AND CASE WHEN time = '" + scalar(probe_text) + "' THEN 1 ELSE 0 END = 1"
    )
    row_sql = (
        "SELECT time, type, level, time = '" + scalar(probe_text) + "' AS pred, "
        "LEFT(message,80) FROM information_schema.cluster_log "
        "WHERE time = '" + scalar(probe_text) + "' AND message LIKE '%' LIMIT 5"
    )

    rc, fast_out, fast_err = q(args, fast_sql)
    if rc != 0:
        print(f"ERROR fast query failed: {fast_err}", file=sys.stderr)
        return 2
    rc, ref_out, ref_err = q(args, ref_sql)
    if rc != 0:
        print(f"ERROR ref query failed: {ref_err}", file=sys.stderr)
        return 2
    rc, rows_out, rows_err = q(args, row_sql)
    if rc != 0:
        print(f"ERROR row query failed: {rows_err}", file=sys.stderr)
        return 2

    fast_count, fast_true, fast_min, fast_max = fast_out.split("\t")
    ref_count, ref_true, ref_min, ref_max = ref_out.split("\t")
    print(f"DATA base={base_text} probe={probe_text} window=[{base_text},{upper_text})")
    print(
        f"FAST count={fast_count} true_predicate_sum={fast_true} "
        f"min={fast_min} max={fast_max}"
    )
    print(
        f"REF  count={ref_count} true_predicate_sum={ref_true} "
        f"min={ref_min} max={ref_max}"
    )
    if rows_out:
        print("FAST_ROWS")
        print(rows_out)

    finding = int(fast_count) > 0 and int(fast_true) == 0 and int(ref_count) == 0
    if finding:
        print(
            "RED\tcluster_log_subms_eq\tfast path returned rows whose own "
            "time=probe predicate is false; CASE oracle returned 0 rows"
        )
    else:
        print("GREEN(triggered)\tcluster_log_subms_eq\tfast path matched CASE oracle")
    print(f"SUMMARY total=1 findings={1 if finding else 0} skipped=0")
    return 1 if finding else 0


if __name__ == "__main__":
    raise SystemExit(main())
