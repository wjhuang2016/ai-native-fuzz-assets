#!/usr/bin/env python3
"""Placement-bundle failure probe for DDL object references.

The target owner is the PD placement rule bundle. When a DDL changes a table,
partition, or policy reference, metadata and bundle notification must stay
atomic from the user's point of view: persistent bundle failure must not leave a
new reference behind, and retryable failure must preserve dependencies.
"""

from __future__ import annotations

import argparse
import dataclasses
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request
from collections.abc import Callable


INFOSYNC_FP = "github.com/pingcap/tidb/pkg/domain/infosync"


@dataclasses.dataclass
class Result:
    rc: int
    out: str
    err: str


@dataclasses.dataclass
class CaseOutcome:
    name: str
    status: str
    detail: str
    alter_rc: int
    alter_err: str


@dataclasses.dataclass
class Case:
    name: str
    setup: list[str]
    alter: str
    failpoint_action: str
    expect_alter: str
    oracle: Callable[[argparse.Namespace, str], tuple[bool, str]]


def mysql_args(args: argparse.Namespace, database: str | None = None) -> list[str]:
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
    if database:
        cmd.append(database)
    return cmd


def run_mysql(args: argparse.Namespace, sql: str, database: str | None = None) -> Result:
    proc = subprocess.run(
        mysql_args(args, database) + ["-e", sql],
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    return Result(proc.returncode, proc.stdout.rstrip("\n"), proc.stderr.rstrip("\n"))


def exec_ok(args: argparse.Namespace, sql: str, database: str | None = None) -> None:
    res = run_mysql(args, sql, database)
    if res.rc != 0:
        raise RuntimeError(f"SQL failed rc={res.rc}: {sql}\n{res.err}")


def quote_db(name: str) -> str:
    return "`" + name.replace("`", "``") + "`"


def rows(res: Result) -> list[str]:
    if res.out == "":
        return []
    return res.out.splitlines()


def combined(res: Result) -> str:
    return (res.err + "\n" + res.out).strip()


def show_create(args: argparse.Namespace, db: str, table_name: str = "t") -> str:
    res = run_mysql(args, f"SHOW CREATE TABLE `{table_name}`", db)
    if res.rc != 0:
        return res.err
    return "\n".join(rows(res))


def show_placement(args: argparse.Namespace, db: str, target: str) -> str:
    res = run_mysql(args, f"SHOW PLACEMENT WHERE TARGET='{target}'", db)
    if res.rc != 0:
        return res.err
    return "\n".join(rows(res))


def drop_policy(args: argparse.Namespace, db: str, policy_name: str) -> Result:
    return run_mysql(args, f"DROP PLACEMENT POLICY `{policy_name}`", db)


def expect_policy_in_use(res: Result) -> bool:
    text = combined(res).lower()
    return res.rc != 0 and ("8241" in text or "still in use" in text or "placement policy" in text)


def failpoint_request(args: argparse.Namespace, method: str, failpoint: str = "", body: str | None = None) -> tuple[int, str]:
    url = args.status_url.rstrip("/") + "/fail/"
    if failpoint:
        url += failpoint
    data = body.encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            return resp.getcode(), resp.read().decode(errors="replace")
    except urllib.error.HTTPError as err:
        return err.code, err.read().decode(errors="replace")
    except OSError as err:
        return 0, str(err)


def failpoint_available(args: argparse.Namespace) -> tuple[bool, str]:
    code, body = failpoint_request(args, "GET")
    if code == 200:
        return True, body
    return False, f"failpoint API unavailable at {args.status_url}: code={code} body={body}"


def set_failpoint(args: argparse.Namespace, action: str) -> None:
    code, body = failpoint_request(args, "PUT", f"{INFOSYNC_FP}/putRuleBundlesError", action)
    if code not in (200, 204):
        raise RuntimeError(f"failed to set putRuleBundlesError: code={code} body={body}")


def clear_failpoint(args: argparse.Namespace) -> None:
    failpoint_request(args, "DELETE", f"{INFOSYNC_FP}/putRuleBundlesError")


def table_placement_failed_cleanly(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    ddl = show_create(args, db)
    if "PLACEMENT POLICY" in ddl:
        return False, "failed ALTER TABLE PLACEMENT left policy ref in SHOW CREATE: " + ddl
    drop = drop_policy(args, db, "pp1")
    if drop.rc != 0:
        return False, "failed ALTER TABLE PLACEMENT still references pp1: " + combined(drop)
    return True, "persistent bundle failure left table metadata unchanged and policy droppable"


def table_placement_retry_succeeded(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    ddl = show_create(args, db)
    if "PLACEMENT POLICY=`pp1`" not in ddl:
        return False, "retry ALTER TABLE PLACEMENT did not persist policy ref: " + ddl
    drop = drop_policy(args, db, "pp1")
    if not expect_policy_in_use(drop):
        return False, "retry ALTER TABLE PLACEMENT did not protect pp1 dependency: " + combined(drop)
    return True, "retry succeeded and table dependency remained in-use"


def partition_placement_failed_cleanly(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    ddl = show_create(args, db)
    if "PLACEMENT POLICY=`pp2`" in ddl:
        return False, "failed ALTER PARTITION PLACEMENT left new pp2 ref: " + ddl
    if "PLACEMENT POLICY=`pp1`" not in ddl:
        return False, "failed ALTER PARTITION PLACEMENT lost original pp1 ref: " + ddl
    pp2_drop = drop_policy(args, db, "pp2")
    if pp2_drop.rc != 0:
        return False, "failed ALTER PARTITION PLACEMENT still references pp2: " + combined(pp2_drop)
    pp1_drop = drop_policy(args, db, "pp1")
    if not expect_policy_in_use(pp1_drop):
        return False, "failed ALTER PARTITION PLACEMENT lost original pp1 dependency: " + combined(pp1_drop)
    return True, "persistent bundle failure kept original partition policy and did not reference new policy"


def alter_policy_failed_cleanly(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    placement = show_placement(args, db, "POLICY pp1")
    if "FOLLOWERS=2" in placement:
        return False, "failed ALTER PLACEMENT POLICY changed policy settings: " + placement
    drop = drop_policy(args, db, "pp1")
    if not expect_policy_in_use(drop):
        return False, "failed ALTER PLACEMENT POLICY lost table dependency: " + combined(drop)
    return True, "persistent bundle failure kept policy settings and table dependency"


def alter_policy_retry_succeeded(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    placement = show_placement(args, db, "POLICY pp1")
    if "FOLLOWERS=2" not in placement:
        return False, "retry ALTER PLACEMENT POLICY did not update settings: " + placement
    drop = drop_policy(args, db, "pp1")
    if not expect_policy_in_use(drop):
        return False, "retry ALTER PLACEMENT POLICY lost table dependency: " + combined(drop)
    return True, "retry updated policy settings and preserved dependency"


def cases() -> list[Case]:
    return [
        Case(
            name="persistent_alter_table_placement_failure_keeps_metadata",
            setup=[
                "CREATE PLACEMENT POLICY pp1 FOLLOWERS=1",
                "CREATE TABLE t(a INT)",
            ],
            alter="ALTER TABLE t PLACEMENT POLICY pp1",
            failpoint_action="return(true)",
            expect_alter="fail",
            oracle=table_placement_failed_cleanly,
        ),
        Case(
            name="retry_alter_table_placement_preserves_dependency",
            setup=[
                "CREATE PLACEMENT POLICY pp1 FOLLOWERS=1",
                "CREATE TABLE t(a INT)",
            ],
            alter="ALTER TABLE t PLACEMENT POLICY pp1",
            failpoint_action="1*return(false)",
            expect_alter="success",
            oracle=table_placement_retry_succeeded,
        ),
        Case(
            name="persistent_alter_partition_placement_failure_keeps_original_ref",
            setup=[
                "CREATE PLACEMENT POLICY pp1 FOLLOWERS=1",
                "CREATE PLACEMENT POLICY pp2 FOLLOWERS=2",
                "CREATE TABLE t(a INT) PARTITION BY RANGE(a) (PARTITION p0 VALUES LESS THAN (10) PLACEMENT POLICY pp1, PARTITION p1 VALUES LESS THAN (MAXVALUE))",
            ],
            alter="ALTER TABLE t PARTITION p0 PLACEMENT POLICY pp2",
            failpoint_action="return(true)",
            expect_alter="fail",
            oracle=partition_placement_failed_cleanly,
        ),
        Case(
            name="persistent_alter_policy_failure_keeps_settings",
            setup=[
                "CREATE PLACEMENT POLICY pp1 FOLLOWERS=1",
                "CREATE TABLE t(a INT) PLACEMENT POLICY pp1",
            ],
            alter="ALTER PLACEMENT POLICY pp1 FOLLOWERS=2",
            failpoint_action="return(true)",
            expect_alter="fail",
            oracle=alter_policy_failed_cleanly,
        ),
        Case(
            name="retry_alter_policy_preserves_dependents",
            setup=[
                "CREATE PLACEMENT POLICY pp1 FOLLOWERS=1",
                "CREATE TABLE t(a INT) PLACEMENT POLICY pp1",
            ],
            alter="ALTER PLACEMENT POLICY pp1 FOLLOWERS=2",
            failpoint_action="1*return(false)",
            expect_alter="success",
            oracle=alter_policy_retry_succeeded,
        ),
    ]


def cleanup_case(args: argparse.Namespace, db: str) -> None:
    run_mysql(args, "DROP TABLE IF EXISTS t", db)
    for policy_name in ("pp2", "pp1"):
        run_mysql(args, f"DROP PLACEMENT POLICY IF EXISTS `{policy_name}`", db)


def setup_case(args: argparse.Namespace, db: str, case: Case) -> None:
    cleanup_case(args, db)
    for sql in case.setup:
        exec_ok(args, sql, db)


def run_case(args: argparse.Namespace, db: str, case: Case) -> CaseOutcome:
    setup_case(args, db, case)
    set_failpoint(args, case.failpoint_action)
    try:
        alter_res = run_mysql(args, case.alter, db)
    finally:
        clear_failpoint(args)

    try:
        if case.expect_alter == "fail":
            if alter_res.rc == 0:
                return CaseOutcome(case.name, "FINDING", "DDL unexpectedly succeeded under persistent placement-bundle failure", alter_res.rc, alter_res.err)
        elif case.expect_alter == "success":
            if alter_res.rc != 0:
                return CaseOutcome(case.name, "FINDING", "DDL should retry placement-bundle failure but failed: " + combined(alter_res), alter_res.rc, alter_res.err)
        else:
            raise AssertionError(f"unknown expect_alter {case.expect_alter}")
        ok, detail = case.oracle(args, db)
        if not ok:
            return CaseOutcome(case.name, "FINDING", detail, alter_res.rc, alter_res.err)
        return CaseOutcome(case.name, "OK", detail, alter_res.rc, alter_res.err)
    finally:
        cleanup_case(args, db)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default=os.environ.get("MYSQL", "mysql"))
    parser.add_argument("--host", default=os.environ.get("MYSQL_HOST", "127.0.0.1"))
    parser.add_argument("--port", default=os.environ.get("MYSQL_PORT", "14000"))
    parser.add_argument("--user", default=os.environ.get("MYSQL_USER", "root"))
    parser.add_argument("--status-url", default=os.environ.get("TIDB_STATUS_URL", "http://127.0.0.1:18080"))
    parser.add_argument("--database-prefix", default="ai_native_ddl_bundle_fail")
    parser.add_argument("--keep-db", action="store_true")
    parser.add_argument("--require-failpoint", action="store_true")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    suffix = time.strftime("%Y%m%d_%H%M%S")
    db = f"{args.database_prefix}_{suffix}"

    health = run_mysql(args, "SELECT 1")
    if health.rc != 0:
        print(f"cannot connect to TiDB/MySQL at {args.host}:{args.port}: {health.err}", file=sys.stderr)
        return 2

    fp_ok, fp_detail = failpoint_available(args)
    if not fp_ok:
        print("SKIP\tplacement-bundle failure\tputRuleBundlesError\t" + fp_detail)
        print(f"SUMMARY total=0 findings=0 skipped={len(cases())}")
        return 2 if args.require_failpoint else 0

    exec_ok(args, "SET GLOBAL tidb_placement_mode = 'STRICT'")
    exec_ok(args, f"DROP DATABASE IF EXISTS {quote_db(db)}")
    exec_ok(args, f"CREATE DATABASE {quote_db(db)}")

    outcomes: list[CaseOutcome] = []
    try:
        for case in cases():
            outcome = run_case(args, db, case)
            outcomes.append(outcome)
            print("\t".join([outcome.status, "placement-bundle failure", case.name, outcome.detail.replace("\n", " ")[:500]]))
    finally:
        clear_failpoint(args)
        cleanup_case(args, db)
        if not args.keep_db:
            run_mysql(args, f"DROP DATABASE IF EXISTS {quote_db(db)}")

    unexpected = [out for out in outcomes if out.status == "FINDING"]
    print(f"SUMMARY total={len(outcomes)} findings={len(unexpected)} skipped=0")
    if unexpected:
        print("UNEXPECTED_FINDINGS")
        for out in unexpected:
            print(f"- {out.name}: {out.detail}; alter_err={out.alter_err}")
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
