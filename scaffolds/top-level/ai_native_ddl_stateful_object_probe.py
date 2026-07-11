#!/usr/bin/env python3
"""Stateful DDL object-reference probe.

This is the failpoint-backed companion to ai_native_ddl_object_reference_probe.py.
It targets DDL rollback windows where placement-policy refs, partition refs, and
global/local index copies are temporarily present at the same time.
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


DDL_FP = "github.com/pingcap/tidb/pkg/ddl"


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
    failpoint: str
    setup: list[str]
    alter: str
    oracle: Callable[[argparse.Namespace, str], tuple[bool, str]]
    failpoint_action: str = "return(true)"
    expect_alter: str = "fail"


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


def index_line(ddl: str, index_name: str) -> str:
    marker = f"`{index_name}`"
    for line in ddl.splitlines():
        if marker in line:
            return line
    return ""


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


def set_failpoint(args: argparse.Namespace, name: str, action: str = "return(true)") -> None:
    code, body = failpoint_request(args, "PUT", f"{DDL_FP}/{name}", action)
    if code not in (200, 204):
        raise RuntimeError(f"failed to set failpoint {name}: code={code} body={body}")


def clear_failpoint(args: argparse.Namespace, name: str) -> None:
    failpoint_request(args, "DELETE", f"{DDL_FP}/{name}")


def policy_and_nonpartitioned_rollback_oracle(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    ddl = show_create(args, db)
    if "PARTITION BY" in ddl.upper():
        return False, "rollback left unexpected partition metadata: " + ddl
    if "GLOBAL" in ddl.upper():
        return False, "rollback left unexpected global index marker: " + ddl
    part_drop = drop_policy(args, db, "pp_part")
    if part_drop.rc != 0:
        return False, "rollback left added partition policy referenced: " + combined(part_drop)
    table_drop = drop_policy(args, db, "pp_table")
    if not expect_policy_in_use(table_drop):
        return False, "rollback lost original table policy reference: " + combined(table_drop)
    check = run_mysql(args, "ADMIN CHECK TABLE t", db)
    if check.rc != 0:
        return False, "ADMIN CHECK failed after rollback: " + combined(check)
    via_index = run_mysql(args, "SELECT GROUP_CONCAT(CONCAT_WS(':', a, b) ORDER BY a SEPARATOR ',') FROM t USE INDEX(idx_a)", db)
    via_table = run_mysql(args, "SELECT GROUP_CONCAT(CONCAT_WS(':', a, b) ORDER BY a SEPARATOR ',') FROM t IGNORE INDEX(idx_a)", db)
    if via_index.rc != 0 or via_table.rc != 0 or via_index.out != via_table.out:
        return False, f"rowset mismatch after rollback: via_index={via_index.out}/{via_index.err}; via_table={via_table.out}/{via_table.err}"
    return True, "rollback restored non-partitioned table, released added policy, preserved table policy and rowsets"


def remove_partitioning_rollback_oracle(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    ddl = show_create(args, db)
    if "PARTITION BY" not in ddl.upper():
        return False, "rollback of REMOVE PARTITIONING lost original partition metadata: " + ddl
    if "GLOBAL" not in ddl.upper():
        return False, "rollback of REMOVE PARTITIONING lost original global index marker: " + ddl
    table_drop = drop_policy(args, db, "pp_table")
    if not expect_policy_in_use(table_drop):
        return False, "rollback lost table policy reference: " + combined(table_drop)
    part_drop = drop_policy(args, db, "pp_part")
    if not expect_policy_in_use(part_drop):
        return False, "rollback lost partition policy reference: " + combined(part_drop)
    check = run_mysql(args, "ADMIN CHECK TABLE t", db)
    if check.rc != 0:
        return False, "ADMIN CHECK failed after remove-partitioning rollback: " + combined(check)
    via_index = run_mysql(args, "SELECT GROUP_CONCAT(CONCAT_WS(':', a, b) ORDER BY a SEPARATOR ',') FROM t USE INDEX(idx_a)", db)
    via_table = run_mysql(args, "SELECT GROUP_CONCAT(CONCAT_WS(':', a, b) ORDER BY a SEPARATOR ',') FROM t IGNORE INDEX(idx_a)", db)
    if via_index.rc != 0 or via_table.rc != 0 or via_index.out != via_table.out:
        return False, f"rowset mismatch after remove rollback: via_index={via_index.out}/{via_index.err}; via_table={via_table.out}/{via_table.err}"
    return True, "rollback restored partition metadata, policy refs, global marker, and rowsets"


def truncate_cancel_oracle(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    ddl = show_create(args, db)
    if "PARTITION BY" not in ddl.upper() or "GLOBAL" not in ddl.upper():
        return False, "cancelled TRUNCATE PARTITION did not preserve original partition/global metadata: " + ddl
    table_drop = drop_policy(args, db, "pp_table")
    if not expect_policy_in_use(table_drop):
        return False, "cancelled truncate lost table policy reference: " + combined(table_drop)
    part_drop = drop_policy(args, db, "pp_part")
    if not expect_policy_in_use(part_drop):
        return False, "cancelled truncate lost partition policy reference: " + combined(part_drop)
    check = run_mysql(args, "ADMIN CHECK TABLE t", db)
    if check.rc != 0:
        return False, "ADMIN CHECK failed after cancelled truncate: " + combined(check)
    via_index = run_mysql(args, "SELECT GROUP_CONCAT(CONCAT_WS(':', a, b) ORDER BY a SEPARATOR ',') FROM t USE INDEX(idx_b)", db)
    via_table = run_mysql(args, "SELECT GROUP_CONCAT(CONCAT_WS(':', a, b) ORDER BY a SEPARATOR ',') FROM t IGNORE INDEX(idx_b)", db)
    if via_index.rc != 0 or via_table.rc != 0 or via_index.out != via_table.out or via_table.out != "1:100,20:200,30:300":
        return False, f"rowset mismatch after cancelled truncate: via_index={via_index.out}/{via_index.err}; via_table={via_table.out}/{via_table.err}"
    return True, "cancelled truncate preserved partition metadata, policy refs, global marker, and rowsets"


def truncate_retry_success_oracle(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    ddl = show_create(args, db)
    if "PARTITION BY" not in ddl.upper() or "GLOBAL" not in ddl.upper():
        return False, "TRUNCATE PARTITION retry success lost partition/global metadata: " + ddl
    table_drop = drop_policy(args, db, "pp_table")
    if not expect_policy_in_use(table_drop):
        return False, "truncate retry lost table policy reference: " + combined(table_drop)
    part_drop = drop_policy(args, db, "pp_part")
    if not expect_policy_in_use(part_drop):
        return False, "truncate retry lost partition policy reference: " + combined(part_drop)
    check = run_mysql(args, "ADMIN CHECK TABLE t", db)
    if check.rc != 0:
        return False, "ADMIN CHECK failed after truncate retry: " + combined(check)
    via_index = run_mysql(args, "SELECT GROUP_CONCAT(CONCAT_WS(':', a, b) ORDER BY a SEPARATOR ',') FROM t USE INDEX(idx_b)", db)
    via_table = run_mysql(args, "SELECT GROUP_CONCAT(CONCAT_WS(':', a, b) ORDER BY a SEPARATOR ',') FROM t IGNORE INDEX(idx_b)", db)
    if via_index.rc != 0 or via_table.rc != 0 or via_index.out != via_table.out or via_table.out != "20:200,30:300":
        return False, f"rowset mismatch after truncate retry: via_index={via_index.out}/{via_index.err}; via_table={via_table.out}/{via_table.err}"
    return True, "truncate retry preserved metadata and removed only truncated partition rows"


def partition_by_retry_success_oracle(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    ddl = show_create(args, db)
    if "PARTITION BY" not in ddl.upper():
        return False, "PARTITION BY retry success did not leave partition metadata: " + ddl
    idx_a_line = index_line(ddl, "idx_a")
    idx_b_line = index_line(ddl, "idx_b")
    if "GLOBAL" not in idx_a_line.upper():
        return False, "PARTITION BY retry success did not make idx_a global: " + ddl
    if "GLOBAL" in idx_b_line.upper():
        return False, "PARTITION BY retry success made idx_b unexpectedly global: " + ddl
    table_drop = drop_policy(args, db, "pp_table")
    if not expect_policy_in_use(table_drop):
        return False, "PARTITION BY retry lost table policy reference: " + combined(table_drop)
    part_drop = drop_policy(args, db, "pp_part")
    if not expect_policy_in_use(part_drop):
        return False, "PARTITION BY retry lost new partition policy reference: " + combined(part_drop)
    check = run_mysql(args, "ADMIN CHECK TABLE t", db)
    if check.rc != 0:
        return False, "ADMIN CHECK failed after PARTITION BY retry: " + combined(check)
    via_index = run_mysql(args, "SELECT GROUP_CONCAT(CONCAT_WS(':', a, b) ORDER BY a SEPARATOR ',') FROM t USE INDEX(idx_a)", db)
    via_table = run_mysql(args, "SELECT GROUP_CONCAT(CONCAT_WS(':', a, b) ORDER BY a SEPARATOR ',') FROM t IGNORE INDEX(idx_a)", db)
    if via_index.rc != 0 or via_table.rc != 0 or via_index.out != via_table.out or via_table.out != "1:10,2:20,3:30":
        return False, f"rowset mismatch after PARTITION BY retry: via_index={via_index.out}/{via_index.err}; via_table={via_table.out}/{via_table.err}"
    return True, "retry preserved partition metadata, placement refs, global/local markers, and rowsets"


def remove_partitioning_retry_success_oracle(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    ddl = show_create(args, db)
    if "PARTITION BY" in ddl.upper():
        return False, "REMOVE PARTITIONING retry left partition metadata: " + ddl
    if "GLOBAL" in ddl.upper():
        return False, "REMOVE PARTITIONING retry left global index marker: " + ddl
    part_drop = drop_policy(args, db, "pp_part")
    if part_drop.rc != 0:
        return False, "REMOVE PARTITIONING retry left partition policy referenced: " + combined(part_drop)
    table_drop = drop_policy(args, db, "pp_table")
    if not expect_policy_in_use(table_drop):
        return False, "REMOVE PARTITIONING retry lost table policy reference: " + combined(table_drop)
    check = run_mysql(args, "ADMIN CHECK TABLE t", db)
    if check.rc != 0:
        return False, "ADMIN CHECK failed after REMOVE PARTITIONING retry: " + combined(check)
    via_index = run_mysql(args, "SELECT GROUP_CONCAT(CONCAT_WS(':', a, b) ORDER BY a SEPARATOR ',') FROM t USE INDEX(idx_a)", db)
    via_table = run_mysql(args, "SELECT GROUP_CONCAT(CONCAT_WS(':', a, b) ORDER BY a SEPARATOR ',') FROM t IGNORE INDEX(idx_a)", db)
    if via_index.rc != 0 or via_table.rc != 0 or via_index.out != via_table.out or via_table.out != "1:10,2:20,3:30":
        return False, f"rowset mismatch after REMOVE PARTITIONING retry: via_index={via_index.out}/{via_index.err}; via_table={via_table.out}/{via_table.err}"
    return True, "retry removed partition metadata, released partition policy, cleared global marker, preserved table policy and rowsets"


def cases() -> list[Case]:
    partition_by_setup = [
        "CREATE PLACEMENT POLICY pp_table FOLLOWERS=1",
        "CREATE PLACEMENT POLICY pp_part FOLLOWERS=2",
        "CREATE TABLE t(a INT UNSIGNED NOT NULL, b INT NOT NULL, UNIQUE KEY idx_a(a), UNIQUE KEY idx_b(b)) PLACEMENT POLICY pp_table",
        "INSERT INTO t VALUES (1, 10), (2, 20), (3, 30)",
    ]
    partition_by_alter = (
        "ALTER TABLE t PARTITION BY HASH(b) "
        "(PARTITION p0 PLACEMENT POLICY pp_part, PARTITION p1, PARTITION p2) "
        "UPDATE INDEXES (idx_a GLOBAL)"
    )
    remove_partitioning_setup = [
        "CREATE PLACEMENT POLICY pp_table FOLLOWERS=1",
        "CREATE PLACEMENT POLICY pp_part FOLLOWERS=2",
        "CREATE TABLE t(a INT UNSIGNED NOT NULL, b INT NOT NULL, UNIQUE KEY idx_b(b), UNIQUE KEY idx_a(a) GLOBAL) PLACEMENT POLICY pp_table PARTITION BY HASH(b) (PARTITION p0 PLACEMENT POLICY pp_part, PARTITION p1, PARTITION p2)",
        "INSERT INTO t VALUES (1, 10), (2, 20), (3, 30)",
    ]
    truncate_setup = [
        "CREATE PLACEMENT POLICY pp_table FOLLOWERS=1",
        "CREATE PLACEMENT POLICY pp_part FOLLOWERS=2",
        "CREATE TABLE t(a INT NOT NULL, b INT NOT NULL, UNIQUE KEY idx_b(b) GLOBAL) PLACEMENT POLICY pp_table PARTITION BY RANGE(a) (PARTITION p0 VALUES LESS THAN (10) PLACEMENT POLICY pp_part, PARTITION p1 VALUES LESS THAN (MAXVALUE))",
        "INSERT INTO t VALUES (1, 100), (20, 200), (30, 300)",
    ]
    return [
        Case(
            name="rollback_partition_by_added_policy_global_index",
            failpoint="reorgPartRollback2",
            setup=partition_by_setup,
            alter=partition_by_alter,
            oracle=policy_and_nonpartitioned_rollback_oracle,
        ),
        Case(
            name="rollback3_partition_by_added_policy_global_index",
            failpoint="reorgPartRollback3",
            setup=partition_by_setup,
            alter=partition_by_alter,
            oracle=policy_and_nonpartitioned_rollback_oracle,
        ),
        Case(
            name="rollback4_partition_by_added_policy_global_index",
            failpoint="reorgPartRollback4",
            setup=partition_by_setup,
            alter=partition_by_alter,
            oracle=policy_and_nonpartitioned_rollback_oracle,
        ),
        Case(
            name="rollback_remove_partitioning_policy_global_index",
            failpoint="reorgPartRollback2",
            setup=remove_partitioning_setup,
            alter="ALTER TABLE t REMOVE PARTITIONING",
            oracle=remove_partitioning_rollback_oracle,
        ),
        Case(
            name="rollback3_remove_partitioning_policy_global_index",
            failpoint="reorgPartRollback3",
            setup=remove_partitioning_setup,
            alter="ALTER TABLE t REMOVE PARTITIONING",
            oracle=remove_partitioning_rollback_oracle,
        ),
        Case(
            name="rollback4_remove_partitioning_policy_global_index",
            failpoint="reorgPartRollback4",
            setup=remove_partitioning_setup,
            alter="ALTER TABLE t REMOVE PARTITIONING",
            oracle=remove_partitioning_rollback_oracle,
        ),
        Case(
            name="retry_fail4_partition_by_policy_global_index",
            failpoint="reorgPartFail4",
            setup=partition_by_setup,
            alter=partition_by_alter,
            oracle=partition_by_retry_success_oracle,
            failpoint_action="1*return(true)",
            expect_alter="success",
        ),
        Case(
            name="retry_fail5_partition_by_policy_global_index",
            failpoint="reorgPartFail5",
            setup=partition_by_setup,
            alter=partition_by_alter,
            oracle=partition_by_retry_success_oracle,
            failpoint_action="1*return(true)",
            expect_alter="success",
        ),
        Case(
            name="retry_fail4_remove_partitioning_policy_global_index",
            failpoint="reorgPartFail4",
            setup=remove_partitioning_setup,
            alter="ALTER TABLE t REMOVE PARTITIONING",
            oracle=remove_partitioning_retry_success_oracle,
            failpoint_action="1*return(true)",
            expect_alter="success",
        ),
        Case(
            name="retry_fail5_remove_partitioning_policy_global_index",
            failpoint="reorgPartFail5",
            setup=remove_partitioning_setup,
            alter="ALTER TABLE t REMOVE PARTITIONING",
            oracle=remove_partitioning_retry_success_oracle,
            failpoint_action="1*return(true)",
            expect_alter="success",
        ),
        Case(
            name="cancel_truncate_partition_policy_global_index",
            failpoint="truncatePartCancel1",
            setup=truncate_setup,
            alter="ALTER TABLE t TRUNCATE PARTITION p0",
            oracle=truncate_cancel_oracle,
        ),
        Case(
            name="retry_truncate_fail1_partition_policy_global_index",
            failpoint="truncatePartFail1",
            setup=truncate_setup,
            alter="ALTER TABLE t TRUNCATE PARTITION p0",
            oracle=truncate_retry_success_oracle,
            failpoint_action="1*return(true)",
            expect_alter="success",
        ),
        Case(
            name="retry_truncate_fail2_partition_policy_global_index",
            failpoint="truncatePartFail2",
            setup=truncate_setup,
            alter="ALTER TABLE t TRUNCATE PARTITION p0",
            oracle=truncate_retry_success_oracle,
            failpoint_action="1*return(true)",
            expect_alter="success",
        ),
        Case(
            name="retry_truncate_fail3_partition_policy_global_index",
            failpoint="truncatePartFail3",
            setup=truncate_setup,
            alter="ALTER TABLE t TRUNCATE PARTITION p0",
            oracle=truncate_retry_success_oracle,
            failpoint_action="1*return(true)",
            expect_alter="success",
        ),
    ]


def cleanup_case(args: argparse.Namespace, db: str) -> None:
    run_mysql(args, "DROP TABLE IF EXISTS t", db)
    for policy_name in ("pp_table", "pp_part"):
        run_mysql(args, f"DROP PLACEMENT POLICY IF EXISTS `{policy_name}`", db)


def setup_case(args: argparse.Namespace, db: str, case: Case) -> None:
    cleanup_case(args, db)
    for sql in case.setup:
        exec_ok(args, sql, db)


def run_case(args: argparse.Namespace, db: str, case: Case) -> CaseOutcome:
    setup_case(args, db, case)
    set_failpoint(args, case.failpoint, case.failpoint_action)
    try:
        alter_res = run_mysql(args, case.alter, db)
    finally:
        clear_failpoint(args, case.failpoint)

    try:
        if case.expect_alter == "fail":
            if alter_res.rc == 0:
                return CaseOutcome(case.name, "FINDING", "DDL unexpectedly succeeded under rollback/cancel failpoint", alter_res.rc, alter_res.err)
            text = combined(alter_res)
            if "Injected error" not in text and "8214" not in text and "rollback" not in text.lower():
                return CaseOutcome(case.name, "FINDING", "DDL failed with unexpected error family: " + text, alter_res.rc, alter_res.err)
        elif case.expect_alter == "success":
            if alter_res.rc != 0:
                return CaseOutcome(case.name, "FINDING", "DDL should succeed after transient failpoint but failed: " + combined(alter_res), alter_res.rc, alter_res.err)
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
    parser.add_argument("--database-prefix", default="ai_native_ddl_stateful_obj")
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
        print("SKIP\tfailpoint\tstateful object-reference\tfailpoint_api\t" + fp_detail)
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
            print("\t".join([outcome.status, "stateful object-reference", case.failpoint, outcome.name, outcome.detail.replace("\n", " ")[:500]]))
    finally:
        for case in cases():
            clear_failpoint(args, case.failpoint)
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
