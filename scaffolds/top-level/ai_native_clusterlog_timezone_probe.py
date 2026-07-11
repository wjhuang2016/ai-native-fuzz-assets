#!/usr/bin/env python3
"""cluster_log time-range extractor timezone probe (methodology v2, selector S3).

Proof obligation:
    A memtable predicate extractor that prefilters SQL-visible rows must interpret
    predicate literals under the SQL-visible semantics. cluster_log's time filter
    must use the session time zone, like slow_query/metrics/statements_summary do.

Bug: ClusterLogTableExtractor.Extract calls extractTimeRange(..., "time", time.Local)
     (memtable_predicate_extractor.go:816) instead of StmtCtx.TimeZone(), and the
     matched time predicate is dropped from `remained` (no scalar recheck). So under
     a session time_zone != server local zone, the absolute filter window is wrong.

Oracle (no-shortcut / absolute-instant equivalence, deterministic):
    A fixed literal window under two session time zones must select DIFFERENT
    absolute instants. If both return the identical row set, the extractor ignored
    the session zone.
"""

from __future__ import annotations

import argparse
import subprocess


def q(args, sql):
    p = subprocess.run(
        [args.mysql, f"-h{args.host}", f"-P{args.port}", f"-u{args.user}",
         "--batch", "--raw", "--skip-column-names", "--connect-timeout=5", "-e", sql],
        text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    return p.returncode, p.stdout.strip(), p.stderr.strip()


def count(args, tz, lo, hi):
    rc, out, err = q(args, (
        f"SET time_zone='{tz}';"
        "SELECT COUNT(*) FROM information_schema.cluster_log "
        f"WHERE message LIKE '%' AND time >= '{lo}' AND time <= '{hi}'"))
    if rc != 0:
        raise RuntimeError(f"query failed tz={tz}: {err}")
    return int(out.splitlines()[-1])


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--mysql", default="mysql")
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", default="14000")
    ap.add_argument("--user", default="root")
    # A fixed 10-min window that has log rows at UTC. Pass explicit values that
    # bracket a busy recent minute (pd/tikv heartbeats guarantee rows).
    ap.add_argument("--utc-lo", required=True, help="e.g. 2026-07-02 13:00:00")
    ap.add_argument("--utc-hi", required=True, help="e.g. 2026-07-02 13:10:00")
    args = ap.parse_args()

    ver_rc, ver, _ = q(args, "SELECT VERSION()")
    print(f"FINGERPRINT version={ver} system_tz=UTC-assumed")

    lo, hi = args.utc_lo, args.utc_hi
    # Trigger evidence: baseline at +00:00 must be non-empty, else the cell is INVALID.
    n_utc = count(args, "+00:00", lo, hi)
    if n_utc == 0:
        print("INVALID(untriggered)\tcluster_log_tz\tbaseline window empty; pick a window with logs")
        print("SUMMARY total=1 findings=0 skipped=0")
        return 0

    # Same literals, extreme session zone. Correct semantics => different absolute
    # window (14h earlier) => should differ from n_utc. Bug => identical.
    n_p14 = count(args, "+14:00", lo, hi)

    # Reverse: under +14:00, the literal that (if tz-respected) targets the SAME
    # absolute instant as the UTC window is lo/hi + 14h. Bug => 0 (future instant).
    def plus14(ts):
        # crude +14h on 'YYYY-MM-DD HH:MM:SS' via SQL to avoid client tz assumptions
        rc, out, err = q(args, f"SELECT DATE_FORMAT('{ts}' + INTERVAL 14 HOUR, '%Y-%m-%d %H:%i:%s')")
        return out.splitlines()[-1]
    lo14, hi14 = plus14(lo), plus14(hi)
    n_p14_shifted = count(args, "+14:00", lo14, hi14)

    print(f"DATA baseline(+00:00,{lo}..{hi})={n_utc}  "
          f"same-literal(+14:00)={n_p14}  tz-shifted(+14:00,{lo14}..{hi14})={n_p14_shifted}")

    forward_bug = (n_p14 == n_utc and n_p14 > 0)   # same-literal returns identical recent rows
    reverse_bug = (n_p14_shifted == 0)             # tz-respecting literal misses them
    if forward_bug and reverse_bug:
        status, detail = "RED", (
            f"extractor ignores session time_zone: same literal window returns identical "
            f"{n_p14} rows across +00:00/+14:00 (should differ by 14h), and the tz-respecting "
            f"+14:00 literal returns 0 (should return {n_utc}).")
    elif forward_bug or reverse_bug:
        status, detail = "RED", f"partial signal forward={forward_bug} reverse={reverse_bug}"
    else:
        status, detail = "GREEN(triggered)", (
            f"session tz respected: same-literal(+14:00)={n_p14} != baseline={n_utc}")
    print(f"{status}\tcluster_log_tz_extractor\t{detail}")
    print(f"SUMMARY total=1 findings={1 if status=='RED' else 0} skipped=0")
    return 1 if status == "RED" else 0


if __name__ == "__main__":
    raise SystemExit(main())
