#!/usr/bin/env python3
"""Probe for stats-lock ownership after EXCHANGE PARTITION.

This is a small method-validation probe, not a broad test suite. The target
proof obligation is:

    LOCK STATS t creates visible locks for t and its partitions.
    EXCHANGE PARTITION swaps physical IDs, but UNLOCK STATS t should not leave
    a user-visible stats lock behind on the exchanged standalone table.
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


def stats_lock_exchange_cell(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    qdb = quote_ident(db)
    sql = f"""
DROP DATABASE IF EXISTS {qdb};
CREATE DATABASE {qdb};
USE {qdb};
CREATE TABLE t(a INT, b VARCHAR(10), INDEX idx_b(b))
PARTITION BY RANGE(a) (
  PARTITION p0 VALUES LESS THAN (10),
  PARTITION p1 VALUES LESS THAN (20)
);
INSERT INTO t VALUES (1,'a'),(11,'b');
CREATE TABLE t1(a INT, b VARCHAR(10), INDEX idx_b(b));
INSERT INTO t1 VALUES (2,'x');
ANALYZE TABLE t;
ANALYZE TABLE t1;
SET @@tidb_partition_prune_mode='dynamic';
LOCK STATS t;
SELECT 'MARK','before_show';
SHOW STATS_LOCKED WHERE db_name='{db}';
SELECT 'END','before_show';
ALTER TABLE t EXCHANGE PARTITION p0 WITH TABLE t1;
SELECT 'MARK','after_show';
SHOW STATS_LOCKED WHERE db_name='{db}';
SELECT 'END','after_show';
UNLOCK STATS t;
SELECT 'MARK','after_unlock_t';
SHOW STATS_LOCKED WHERE db_name='{db}';
SELECT 'END','after_unlock_t';
UNLOCK STATS t1;
DROP DATABASE {qdb};
"""
    res = run_mysql(args, sql)
    if res.rc != 0:
        run_mysql(args, f"DROP DATABASE IF EXISTS {qdb}")
        return False, "setup/probe SQL failed: " + combined(res)

    sections = collect_sections(res.out)
    before_show = sections.get("before_show", [])
    after_show = sections.get("after_show", [])
    after_unlock_t = sections.get("after_unlock_t", [])

    expected_before = {
        f"{db}\tt\tglobal\tlocked",
        f"{db}\tt\tp0\tlocked",
        f"{db}\tt\tp1\tlocked",
    }
    before_set = set(before_show)
    controls_ok = before_set == expected_before
    if not controls_ok:
        return False, (
            "INVALID control failed before exchange; "
            f"before_show={before_show!r}"
        )

    after_set = set(after_show)
    global_still_locked = f"{db}\tt\tglobal\tlocked" in after_set
    lock_moved_to_t1 = f"{db}\tt1\t\tlocked" in after_set
    p0_missing = f"{db}\tt\tp0\tlocked" not in after_set
    p1_still_locked = f"{db}\tt\tp1\tlocked" in after_set
    after_unlock_set = set(after_unlock_t)
    t1_left_after_unlock_t = after_unlock_set == {f"{db}\tt1\t\tlocked"}

    if (
        global_still_locked
        and lock_moved_to_t1
        and p0_missing
        and p1_still_locked
        and t1_left_after_unlock_t
    ):
        return False, (
            "LOCK STATS t followed by EXCHANGE PARTITION leaves the exchanged table t1 locked "
            "after UNLOCK STATS t; "
            f"after_show={after_show!r}; after_unlock_t={after_unlock_t!r}"
        )

    return True, (
        "stats lock/unlock ownership stayed consistent across exchange; "
        f"after_show={after_show!r}; after_unlock_t={after_unlock_t!r}"
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default="mysql")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", default="14000")
    parser.add_argument("--user", default="root")
    parser.add_argument("--database-prefix", default="ai_stats_exchange")
    args = parser.parse_args()

    db = f"{args.database_prefix}_{uuid.uuid4().hex[:8]}"
    ok, detail = stats_lock_exchange_cell(args, db)
    if ok:
        print("OK\tstats_lock_exchange_partition\t" + detail.replace("\n", " ")[:1200])
        print("SUMMARY total=1 findings=0 skipped=0")
        return 0
    print("FINDING\tstats_lock_exchange_partition\t" + detail.replace("\n", " ")[:1200])
    print("SUMMARY total=1 findings=1 skipped=0")
    return 1


if __name__ == "__main__":
    sys.exit(main())
