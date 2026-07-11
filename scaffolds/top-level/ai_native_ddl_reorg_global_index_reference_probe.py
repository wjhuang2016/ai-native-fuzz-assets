#!/usr/bin/env python3
"""DDL reference probe for REORGANIZE PARTITION + global index.

This is a small method-validation probe, not a broad test suite. It checks the
specific DDL owner contract:

    REORGANIZE PARTITION must rebuild the replacement global index for every
    still-live partition, not only the partitions being reorganized.
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


def run_mysql(args: argparse.Namespace, sql: str, db: str | None = None) -> Result:
    cmd = [
        "mysql",
        f"-h{args.host}",
        f"-P{args.port}",
        f"-u{args.user}",
        "--batch",
        "--raw",
        "--skip-column-names",
    ]
    if db:
        cmd.append(db)
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


def must(args: argparse.Namespace, sql: str, db: str | None = None) -> Result:
    res = run_mysql(args, sql, db)
    if res.rc != 0:
        raise RuntimeError(f"SQL failed: {sql}\n{combined(res)}")
    return res


def global_index_reorg_cell(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    setup = [
        """CREATE TABLE t(a INT, b INT, UNIQUE KEY idx_b(b) GLOBAL)
           PARTITION BY RANGE(a) (
             PARTITION p0 VALUES LESS THAN (10),
             PARTITION p1 VALUES LESS THAN (20),
             PARTITION pmax VALUES LESS THAN (MAXVALUE)
           )""",
        "INSERT INTO t VALUES (12, 120), (30, 300)",
    ]
    for sql in setup:
        must(args, sql, db)

    alter = run_mysql(
        args,
        """ALTER TABLE t REORGANIZE PARTITION p1 INTO (
             PARTITION p1a VALUES LESS THAN (15),
             PARTITION p1b VALUES LESS THAN (20)
           )""",
        db,
    )
    if alter.rc != 0:
        return False, "DDL failed before oracle; unsupported or blocked: " + combined(alter)

    via_index = must(
        args,
        "SELECT GROUP_CONCAT(CONCAT(a, ':', b) ORDER BY b) FROM t USE INDEX(idx_b) WHERE b >= 0",
        db,
    ).out
    via_table = must(
        args,
        "SELECT GROUP_CONCAT(CONCAT(a, ':', b) ORDER BY b) FROM t IGNORE INDEX(idx_b) WHERE b >= 0",
        db,
    ).out
    admin_check = run_mysql(args, "ADMIN CHECK TABLE t", db)
    show_create = must(args, "SHOW CREATE TABLE t", db).out.replace("\t", " ")

    if via_index != via_table or admin_check.rc != 0:
        return False, (
            "REORGANIZE PARTITION succeeded but replacement global index is incomplete; "
            f"via_index={via_index!r}; via_table={via_table!r}; "
            f"admin_check={combined(admin_check)!r}; show_create={show_create!r}"
        )
    return True, "global index rowset and ADMIN CHECK matched after REORGANIZE PARTITION"


def placement_reorg_control(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    setup = [
        "CREATE PLACEMENT POLICY pp_old FOLLOWERS=1",
        "CREATE PLACEMENT POLICY pp_a FOLLOWERS=2",
        "CREATE PLACEMENT POLICY pp_b FOLLOWERS=3",
        """CREATE TABLE tp(a INT)
           PARTITION BY RANGE(a) (
             PARTITION p0 VALUES LESS THAN (10),
             PARTITION p1 VALUES LESS THAN (20) PLACEMENT POLICY pp_old,
             PARTITION pmax VALUES LESS THAN (MAXVALUE)
           )""",
        """ALTER TABLE tp REORGANIZE PARTITION p1 INTO (
             PARTITION p1a VALUES LESS THAN (15) PLACEMENT POLICY pp_a,
             PARTITION p1b VALUES LESS THAN (20) PLACEMENT POLICY pp_b
           )""",
    ]
    for sql in setup:
        res = run_mysql(args, sql, db)
        if res.rc != 0:
            return False, "placement setup failed: " + combined(res)

    show_create = must(args, "SHOW CREATE TABLE tp", db).out
    old_drop = run_mysql(args, "DROP PLACEMENT POLICY pp_old", db)
    new_a_drop = run_mysql(args, "DROP PLACEMENT POLICY pp_a", db)
    new_b_drop = run_mysql(args, "DROP PLACEMENT POLICY pp_b", db)

    if old_drop.rc != 0:
        return False, "old policy was not released: " + combined(old_drop)
    if new_a_drop.rc == 0 or new_b_drop.rc == 0:
        return False, "new partition policy was not protected: " + show_create
    if "pp_old" in show_create or "pp_a" not in show_create or "pp_b" not in show_create:
        return False, "SHOW CREATE policy rewrite mismatch: " + show_create
    return True, "placement refs rewrote: old policy released, new policies still in use"


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", default="14000")
    parser.add_argument("--user", default="root")
    parser.add_argument("--database-prefix", default="ai_native_reorg_gidx")
    args = parser.parse_args()

    db = f"{args.database_prefix}_{uuid.uuid4().hex[:8]}"
    findings = 0
    total = 0
    try:
        for policy in ("pp_old", "pp_a", "pp_b"):
            run_mysql(args, f"DROP PLACEMENT POLICY IF EXISTS {policy}")
        must(args, f"DROP DATABASE IF EXISTS `{db}`")
        must(args, f"CREATE DATABASE `{db}`")
        cells = [
            ("global_index_reorg_middle_partition", global_index_reorg_cell),
            ("placement_reorg_control", placement_reorg_control),
        ]
        for name, fn in cells:
            total += 1
            ok, detail = fn(args, db)
            status = "OK" if ok else "FINDING"
            if not ok:
                findings += 1
            print("\t".join([status, name, detail.replace("\n", " ")[:900]]))
        print(f"SUMMARY total={total} findings={findings} skipped=0")
        return 1 if findings else 0
    finally:
        run_mysql(args, f"DROP DATABASE IF EXISTS `{db}`")
        for policy in ("pp_old", "pp_a", "pp_b"):
            run_mysql(args, f"DROP PLACEMENT POLICY IF EXISTS {policy}")


if __name__ == "__main__":
    sys.exit(main())
