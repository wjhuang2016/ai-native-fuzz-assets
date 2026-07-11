#!/usr/bin/env python3
"""tikv_region_peers region_id point lookup not-found probe.

Proof obligation:
    A system-table extractor may use REGION_ID = const as a PD point lookup, but a
    non-existing region id is not a SQL execution error. It is an empty rowset for
    that predicate, and in an IN-list it must not abort rows for existing ids.
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


def first_int(res: Result) -> int | None:
    if res.rc != 0 or not res.out.strip():
        return None
    first = res.out.splitlines()[0].split("\t")[0]
    try:
        return int(first)
    except ValueError:
        return None


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default="mysql")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", default="14000")
    parser.add_argument("--user", default="root")
    args = parser.parse_args()

    total = run_mysql(args, "SELECT COUNT(*) FROM information_schema.tikv_region_peers")
    if first_int(total) is None or first_int(total) == 0:
        print(f"SKIP\ttikv_region_peers_region_id_not_found\tno region peer rows: {combined(total)}")
        return 0

    existing = run_mysql(args, "SELECT MIN(region_id) FROM information_schema.tikv_region_peers")
    existing_id = first_int(existing)
    if existing_id is None:
        print(f"SKIP\ttikv_region_peers_region_id_not_found\tcannot find existing region id: {combined(existing)}")
        return 0

    missing_id = 0

    explain_missing = run_mysql(
        args,
        f"EXPLAIN SELECT region_id, store_id FROM information_schema.tikv_region_peers "
        f"WHERE region_id={missing_id} LIMIT 1",
    )
    fast_missing = run_mysql(
        args,
        f"SELECT COUNT(*) FROM information_schema.tikv_region_peers WHERE region_id={missing_id}",
    )
    ref_missing = run_mysql(
        args,
        f"SELECT COUNT(*), SUM(CASE WHEN region_id={missing_id} THEN 1 ELSE 0 END) "
        f"FROM information_schema.tikv_region_peers "
        f"WHERE CASE WHEN region_id={missing_id} THEN TRUE ELSE FALSE END",
    )

    explain_mixed = run_mysql(
        args,
        f"EXPLAIN SELECT region_id, store_id FROM information_schema.tikv_region_peers "
        f"WHERE region_id IN ({missing_id},{existing_id}) LIMIT 10",
    )
    fast_mixed = run_mysql(
        args,
        f"SELECT COUNT(*) FROM information_schema.tikv_region_peers "
        f"WHERE region_id IN ({missing_id},{existing_id})",
    )
    ref_mixed = run_mysql(
        args,
        f"SELECT COUNT(*), SUM(CASE WHEN region_id IN ({missing_id},{existing_id}) THEN 1 ELSE 0 END) "
        f"FROM information_schema.tikv_region_peers "
        f"WHERE CASE WHEN region_id IN ({missing_id},{existing_id}) THEN TRUE ELSE FALSE END",
    )

    fast_existing = run_mysql(
        args,
        f"SELECT COUNT(*) FROM information_schema.tikv_region_peers WHERE region_id={existing_id}",
    )
    ref_existing = run_mysql(
        args,
        f"SELECT COUNT(*), SUM(CASE WHEN region_id={existing_id} THEN 1 ELSE 0 END) "
        f"FROM information_schema.tikv_region_peers "
        f"WHERE CASE WHEN region_id={existing_id} THEN TRUE ELSE FALSE END",
    )
    store_zero = run_mysql(
        args,
        "SELECT COUNT(*) FROM information_schema.tikv_region_peers WHERE store_id=0",
    )

    ref_missing_ok = ref_missing.rc == 0 and ref_missing.out.startswith("0\t")
    ref_mixed_parts = ref_mixed.out.split("\t") if ref_mixed.rc == 0 else []
    mixed_ref_ok = (
        len(ref_mixed_parts) >= 2
        and ref_mixed_parts[0].isdigit()
        and ref_mixed_parts[1].isdigit()
        and int(ref_mixed_parts[0]) > 0
        and ref_mixed_parts[0] == ref_mixed_parts[1]
    )
    existing_ok = (
        fast_existing.rc == 0
        and ref_existing.rc == 0
        and fast_existing.out.strip().isdigit()
        and ref_existing.out.split("\t")[0] == fast_existing.out.strip()
    )
    fast_triggered = (
        "region_ids:[" in explain_missing.out
        and f"region_ids:[{missing_id}" in explain_missing.out
        and "region_ids:[" in explain_mixed.out
    )
    pd_error = (
        fast_missing.rc != 0
        and fast_mixed.rc != 0
        and ("request pd http api failed" in combined(fast_missing).lower() or "regionnotfound" in combined(fast_missing).lower())
    )
    store_control_ok = store_zero.rc == 0 and store_zero.out.strip() == "0"

    finding = fast_triggered and pd_error and ref_missing_ok and mixed_ref_ok and existing_ok and store_control_ok

    if finding:
        detail = (
            f"missing_id={missing_id}, existing_id={existing_id}, "
            f"fast_missing_err={combined(fast_missing)[:180]!r}, "
            f"ref_missing={ref_missing.out!r}, "
            f"fast_mixed_err={combined(fast_mixed)[:180]!r}, ref_mixed={ref_mixed.out!r}, "
            f"fast_existing={fast_existing.out!r}, ref_existing={ref_existing.out!r}, "
            f"store_zero={store_zero.out!r}"
        )
        print(f"FINDING\ttikv_region_peers_region_id_not_found\t{detail}")
        print("SUMMARY total=1 findings=1 skipped=0")
        return 1

    print("INFO\ttikv_region_peers_region_id_not_found\tno finding")
    print(f"DETAIL explain_missing={explain_missing.out!r}")
    print(f"DETAIL fast_missing rc={fast_missing.rc} {combined(fast_missing)!r}")
    print(f"DETAIL ref_missing rc={ref_missing.rc} {ref_missing.out!r}")
    print(f"DETAIL explain_mixed={explain_mixed.out!r}")
    print(f"DETAIL fast_mixed rc={fast_mixed.rc} {combined(fast_mixed)!r}")
    print(f"DETAIL ref_mixed rc={ref_mixed.rc} {ref_mixed.out!r}")
    print(f"DETAIL fast_existing rc={fast_existing.rc} {fast_existing.out!r}")
    print(f"DETAIL ref_existing rc={ref_existing.rc} {ref_existing.out!r}")
    print(f"DETAIL store_zero rc={store_zero.rc} {store_zero.out!r}")
    print("SUMMARY total=1 findings=0 skipped=0")
    return 0


if __name__ == "__main__":
    sys.exit(main())
