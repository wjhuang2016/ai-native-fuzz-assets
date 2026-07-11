#!/usr/bin/env python3
"""DDL object-reference ownership matrix probe.

This probe stays DDL-only. It checks references whose owner is not a column
expression: placement policies referenced by tables/partitions, and global/local
index state referenced by partition DDL.
"""

from __future__ import annotations

import argparse
import dataclasses
import os
import subprocess
import sys
import time
from collections.abc import Callable


@dataclasses.dataclass
class Result:
    rc: int
    out: str
    err: str


@dataclasses.dataclass
class CaseOutcome:
    name: str
    owner: str
    operation: str
    status: str
    detail: str
    alter_rc: int
    alter_err: str


@dataclasses.dataclass
class Case:
    name: str
    owner: str
    operation: str
    expected: str
    setup: list[str]
    alter: str
    oracle: Callable[[argparse.Namespace, str], tuple[bool, str]] | None = None
    block_error_contains: tuple[str, ...] = ()
    skip_if_setup_unsupported: bool = False


POLICIES = ("pp1", "pp2", "pp3", "pp_table", "pp_part")
TABLES = ("c", "p", "nt", "pt", "t")


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


def contains_any(text: str, needles: tuple[str, ...]) -> bool:
    lowered = text.lower()
    return any(needle.lower() in lowered for needle in needles)


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
    return res.rc != 0 and contains_any(combined(res), ("8241", "still in use", "placement policy"))


def show_placement(args: argparse.Namespace, db: str, target: str) -> str:
    res = run_mysql(args, f"SHOW PLACEMENT WHERE TARGET='{target}'", db)
    if res.rc != 0:
        return res.err
    return "\n".join(rows(res))


def require_policy_rewritten(old_policy: str, new_policy: str, table_name: str = "t") -> Callable[[argparse.Namespace, str], tuple[bool, str]]:
    def oracle(args: argparse.Namespace, db: str) -> tuple[bool, str]:
        old_drop = drop_policy(args, db, old_policy)
        if old_drop.rc != 0:
            return False, f"old policy {old_policy} is still referenced: {combined(old_drop)}"
        new_drop = drop_policy(args, db, new_policy)
        if not expect_policy_in_use(new_drop):
            return False, f"new policy {new_policy} is not referenced as expected: {combined(new_drop)}"
        ddl = show_create(args, db, table_name)
        return True, f"{old_policy} became droppable; {new_policy} remains in use; SHOW CREATE={ddl}"

    return oracle


def placement_remove_partitioning_oracle(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    ddl = show_create(args, db)
    if "PARTITION BY" in ddl.upper():
        return False, "REMOVE PARTITIONING left partition metadata in SHOW CREATE: " + ddl
    part_drop = drop_policy(args, db, "pp_part")
    if part_drop.rc != 0:
        return False, "partition policy pp_part is still referenced after REMOVE PARTITIONING: " + combined(part_drop)
    table_drop = drop_policy(args, db, "pp_table")
    if not expect_policy_in_use(table_drop):
        return False, "table policy pp_table should still be referenced: " + combined(table_drop)
    return True, "partition policy was released; table policy stayed in use"


def placement_drop_partition_releases_policy_oracle(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    ddl = show_create(args, db)
    if "`p0`" in ddl:
        return False, "DROP PARTITION left p0 in SHOW CREATE: " + ddl
    part_drop = drop_policy(args, db, "pp_part")
    if part_drop.rc != 0:
        return False, "dropped partition policy pp_part is still referenced: " + combined(part_drop)
    return True, "DROP PARTITION released the dropped partition policy"


def placement_truncate_partition_preserves_policy_oracle(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    ddl = show_create(args, db)
    if "`p0`" not in ddl:
        return False, "TRUNCATE PARTITION removed p0 from SHOW CREATE: " + ddl
    part_drop = drop_policy(args, db, "pp_part")
    if not expect_policy_in_use(part_drop):
        return False, "truncated partition policy pp_part should remain in use: " + combined(part_drop)
    insert = run_mysql(args, "INSERT INTO t VALUES (1, 10)", db)
    if insert.rc != 0:
        return False, "partition p0 was not writable after TRUNCATE PARTITION: " + combined(insert)
    return True, "TRUNCATE PARTITION preserved the partition policy and partition usability"


def require_index_global_status(table_name: str, expected: dict[str, bool]) -> Callable[[argparse.Namespace, str], tuple[bool, str]]:
    def oracle(args: argparse.Namespace, db: str) -> tuple[bool, str]:
        ddl = show_create(args, db, table_name)
        for index_name, want_global in expected.items():
            line = index_line(ddl, index_name)
            if not line:
                return False, f"index {index_name} missing from SHOW CREATE: {ddl}"
            has_global = "GLOBAL" in line.upper()
            if has_global != want_global:
                return False, f"index {index_name} global={has_global}, want={want_global}; line={line}; ddl={ddl}"
        check = run_mysql(args, f"ADMIN CHECK TABLE `{table_name}`", db)
        if check.rc != 0:
            return False, "ADMIN CHECK TABLE failed: " + combined(check)
        return True, "index global/local state and ADMIN CHECK matched"

    return oracle


def global_partition_by_oracle(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    ok, detail = require_index_global_status("t", {"idx_a": True, "idx_b": False})(args, db)
    if not ok:
        return False, detail
    via_index = run_mysql(args, "SELECT GROUP_CONCAT(CONCAT_WS(':', a, b) ORDER BY a SEPARATOR ',') FROM t USE INDEX(idx_a)", db)
    via_table = run_mysql(args, "SELECT GROUP_CONCAT(CONCAT_WS(':', a, b) ORDER BY a SEPARATOR ',') FROM t IGNORE INDEX(idx_a)", db)
    if via_index.rc != 0 or via_table.rc != 0 or via_index.out != via_table.out:
        return False, f"global index rowset mismatch: via_index={via_index.out}/{via_index.err}; via_table={via_table.out}/{via_table.err}"
    return True, "idx_a became global, idx_b stayed local, rowsets matched"


def global_remove_partitioning_oracle(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    ddl = show_create(args, db)
    if "PARTITION BY" in ddl.upper():
        return False, "REMOVE PARTITIONING left partition metadata in SHOW CREATE: " + ddl
    if "GLOBAL" in ddl.upper():
        return False, "global index marker survived REMOVE PARTITIONING: " + ddl
    check = run_mysql(args, "ADMIN CHECK TABLE t", db)
    if check.rc != 0:
        return False, "ADMIN CHECK TABLE failed: " + combined(check)
    via_index = run_mysql(args, "SELECT GROUP_CONCAT(CONCAT_WS(':', a, b) ORDER BY a SEPARATOR ',') FROM t USE INDEX(idx_a)", db)
    via_table = run_mysql(args, "SELECT GROUP_CONCAT(CONCAT_WS(':', a, b) ORDER BY a SEPARATOR ',') FROM t IGNORE INDEX(idx_a)", db)
    if via_index.rc != 0 or via_table.rc != 0 or via_index.out != via_table.out:
        return False, f"post-remove index rowset mismatch: via_index={via_index.out}/{via_index.err}; via_table={via_table.out}/{via_table.err}"
    return True, "partitioning removed; global marker cleared; rowsets matched"


def global_drop_partition_oracle(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    check = run_mysql(args, "ADMIN CHECK TABLE t", db)
    if check.rc != 0:
        return False, "ADMIN CHECK TABLE failed after DROP PARTITION: " + combined(check)
    via_index = run_mysql(args, "SELECT GROUP_CONCAT(CONCAT_WS(':', a, b) ORDER BY a SEPARATOR ',') FROM t USE INDEX(idx_b)", db)
    via_table = run_mysql(args, "SELECT GROUP_CONCAT(CONCAT_WS(':', a, b) ORDER BY a SEPARATOR ',') FROM t IGNORE INDEX(idx_b)", db)
    if via_index.rc != 0 or via_table.rc != 0:
        return False, f"query failed after DROP PARTITION: via_index={combined(via_index)}; via_table={combined(via_table)}"
    if via_index.out != via_table.out or via_table.out != "20:200,30:300":
        return False, f"DROP PARTITION rowset mismatch: via_index={via_index.out}; via_table={via_table.out}"
    return True, "DROP PARTITION cleaned visible global-index rowset"


def global_truncate_partition_oracle(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    check = run_mysql(args, "ADMIN CHECK TABLE t", db)
    if check.rc != 0:
        return False, "ADMIN CHECK TABLE failed after TRUNCATE PARTITION: " + combined(check)
    via_index = run_mysql(args, "SELECT GROUP_CONCAT(CONCAT_WS(':', a, b) ORDER BY a SEPARATOR ',') FROM t USE INDEX(idx_b)", db)
    via_table = run_mysql(args, "SELECT GROUP_CONCAT(CONCAT_WS(':', a, b) ORDER BY a SEPARATOR ',') FROM t IGNORE INDEX(idx_b)", db)
    if via_index.rc != 0 or via_table.rc != 0:
        return False, f"query failed after TRUNCATE PARTITION: via_index={combined(via_index)}; via_table={combined(via_table)}"
    if via_index.out != via_table.out or via_table.out != "20:200,30:300":
        return False, f"TRUNCATE PARTITION rowset mismatch: via_index={via_index.out}; via_table={via_table.out}"
    return True, "TRUNCATE PARTITION cleaned visible global-index rowset"


def mixed_remove_partitioning_oracle(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    ddl = show_create(args, db)
    if "PARTITION BY" in ddl.upper():
        return False, "REMOVE PARTITIONING left partition metadata in SHOW CREATE: " + ddl
    if "GLOBAL" in ddl.upper():
        return False, "global index marker survived REMOVE PARTITIONING: " + ddl
    part_drop = drop_policy(args, db, "pp_part")
    if part_drop.rc != 0:
        return False, "partition policy pp_part is still referenced after mixed REMOVE PARTITIONING: " + combined(part_drop)
    table_drop = drop_policy(args, db, "pp_table")
    if not expect_policy_in_use(table_drop):
        return False, "table policy pp_table should remain referenced after mixed REMOVE PARTITIONING: " + combined(table_drop)
    check = run_mysql(args, "ADMIN CHECK TABLE t", db)
    if check.rc != 0:
        return False, "ADMIN CHECK TABLE failed after mixed REMOVE PARTITIONING: " + combined(check)
    via_index = run_mysql(args, "SELECT GROUP_CONCAT(CONCAT_WS(':', a, b) ORDER BY a SEPARATOR ',') FROM t USE INDEX(idx_a)", db)
    via_table = run_mysql(args, "SELECT GROUP_CONCAT(CONCAT_WS(':', a, b) ORDER BY a SEPARATOR ',') FROM t IGNORE INDEX(idx_a)", db)
    if via_index.rc != 0 or via_table.rc != 0 or via_index.out != via_table.out:
        return False, f"mixed remove rowset mismatch: via_index={via_index.out}/{via_index.err}; via_table={via_table.out}/{via_table.err}"
    return True, "REMOVE PARTITIONING released partition policy, preserved table policy, and rewrote global index"


def alter_policy_table_dependency_oracle(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    placement = show_placement(args, db, "POLICY pp1")
    if "FOLLOWERS=2" not in placement:
        return False, "ALTER PLACEMENT POLICY did not update policy settings: " + placement
    policy_drop = drop_policy(args, db, "pp1")
    if not expect_policy_in_use(policy_drop):
        return False, "table dependency on altered policy was not preserved: " + combined(policy_drop)
    ddl = show_create(args, db)
    if "PLACEMENT POLICY=`pp1`" not in ddl:
        return False, "table policy reference disappeared after ALTER PLACEMENT POLICY: " + ddl
    return True, "policy settings updated and table dependency remained in-use"


def alter_policy_partition_dependency_oracle(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    placement = show_placement(args, db, "POLICY pp1")
    if "FOLLOWERS=2" not in placement:
        return False, "ALTER PLACEMENT POLICY did not update policy settings: " + placement
    policy_drop = drop_policy(args, db, "pp1")
    if not expect_policy_in_use(policy_drop):
        return False, "partition dependency on altered policy was not preserved: " + combined(policy_drop)
    ddl = show_create(args, db)
    if "PARTITION `p0`" not in ddl or "PLACEMENT POLICY=`pp1`" not in ddl:
        return False, "partition policy reference disappeared after ALTER PLACEMENT POLICY: " + ddl
    insert = run_mysql(args, "INSERT INTO t VALUES (1, 10)", db)
    if insert.rc != 0:
        return False, "partition remained referenced but was not writable: " + combined(insert)
    return True, "policy settings updated and partition dependency remained in-use"


def cases() -> list[Case]:
    return [
        Case(
            name="placement_drop_table_policy_block",
            owner="placement policy",
            operation="DROP policy referenced by table",
            expected="block",
            setup=[
                "CREATE PLACEMENT POLICY pp1 FOLLOWERS=1",
                "CREATE TABLE t(a INT) PLACEMENT POLICY pp1",
            ],
            alter="DROP PLACEMENT POLICY pp1",
            block_error_contains=("8241", "still in use", "Placement policy"),
            skip_if_setup_unsupported=True,
        ),
        Case(
            name="placement_drop_partition_policy_block",
            owner="placement policy",
            operation="DROP policy referenced by partition",
            expected="block",
            setup=[
                "CREATE PLACEMENT POLICY pp1 FOLLOWERS=1",
                "CREATE TABLE t(a INT) PARTITION BY RANGE(a) (PARTITION p0 VALUES LESS THAN (10) PLACEMENT POLICY pp1, PARTITION p1 VALUES LESS THAN (MAXVALUE))",
            ],
            alter="DROP PLACEMENT POLICY pp1",
            block_error_contains=("8241", "still in use", "Placement policy"),
            skip_if_setup_unsupported=True,
        ),
        Case(
            name="placement_table_policy_rewrite",
            owner="placement policy",
            operation="ALTER TABLE PLACEMENT POLICY",
            expected="rewrite",
            setup=[
                "CREATE PLACEMENT POLICY pp1 FOLLOWERS=1",
                "CREATE PLACEMENT POLICY pp2 FOLLOWERS=2",
                "CREATE TABLE t(a INT) PLACEMENT POLICY pp1",
            ],
            alter="ALTER TABLE t PLACEMENT POLICY pp2",
            oracle=require_policy_rewritten("pp1", "pp2"),
            skip_if_setup_unsupported=True,
        ),
        Case(
            name="placement_partition_policy_rewrite",
            owner="placement policy",
            operation="ALTER TABLE PARTITION PLACEMENT POLICY",
            expected="rewrite",
            setup=[
                "CREATE PLACEMENT POLICY pp1 FOLLOWERS=1",
                "CREATE PLACEMENT POLICY pp2 FOLLOWERS=2",
                "CREATE TABLE t(a INT) PARTITION BY RANGE(a) (PARTITION p0 VALUES LESS THAN (10) PLACEMENT POLICY pp1, PARTITION p1 VALUES LESS THAN (MAXVALUE))",
            ],
            alter="ALTER TABLE t PARTITION p0 PLACEMENT POLICY pp2",
            oracle=require_policy_rewritten("pp1", "pp2"),
            skip_if_setup_unsupported=True,
        ),
        Case(
            name="placement_remove_partitioning_releases_partition_policy",
            owner="placement policy",
            operation="REMOVE PARTITIONING",
            expected="rewrite",
            setup=[
                "CREATE PLACEMENT POLICY pp_table FOLLOWERS=1",
                "CREATE PLACEMENT POLICY pp_part FOLLOWERS=2",
                "CREATE TABLE t(a INT, b INT) PLACEMENT POLICY pp_table PARTITION BY RANGE(a) (PARTITION p0 VALUES LESS THAN (10) PLACEMENT POLICY pp_part, PARTITION p1 VALUES LESS THAN (MAXVALUE))",
                "INSERT INTO t VALUES (1, 1), (20, 20)",
            ],
            alter="ALTER TABLE t REMOVE PARTITIONING",
            oracle=placement_remove_partitioning_oracle,
            skip_if_setup_unsupported=True,
        ),
        Case(
            name="placement_multischema_table_policy_remove_partitioning_block",
            owner="placement policy",
            operation="ALTER TABLE PLACEMENT + REMOVE PARTITIONING",
            expected="block",
            setup=[
                "CREATE PLACEMENT POLICY pp1 FOLLOWERS=1",
                "CREATE TABLE t(a INT) PARTITION BY HASH(a) PARTITIONS 3",
            ],
            alter="ALTER TABLE t PLACEMENT POLICY pp1 REMOVE PARTITIONING",
            block_error_contains=("8200", "Unsupported multi schema change", "alter table placement"),
            skip_if_setup_unsupported=True,
        ),
        Case(
            name="placement_alter_policy_table_dependency_preserved",
            owner="placement policy",
            operation="ALTER PLACEMENT POLICY used by table",
            expected="rewrite",
            setup=[
                "CREATE PLACEMENT POLICY pp1 FOLLOWERS=1",
                "CREATE TABLE t(a INT) PLACEMENT POLICY pp1",
            ],
            alter="ALTER PLACEMENT POLICY pp1 FOLLOWERS=2",
            oracle=alter_policy_table_dependency_oracle,
            skip_if_setup_unsupported=True,
        ),
        Case(
            name="placement_alter_policy_partition_dependency_preserved",
            owner="placement policy",
            operation="ALTER PLACEMENT POLICY used by partition",
            expected="rewrite",
            setup=[
                "CREATE PLACEMENT POLICY pp1 FOLLOWERS=1",
                "CREATE TABLE t(a INT, b INT) PARTITION BY RANGE(a) (PARTITION p0 VALUES LESS THAN (10) PLACEMENT POLICY pp1, PARTITION p1 VALUES LESS THAN (MAXVALUE))",
            ],
            alter="ALTER PLACEMENT POLICY pp1 FOLLOWERS=2",
            oracle=alter_policy_partition_dependency_oracle,
            skip_if_setup_unsupported=True,
        ),
        Case(
            name="global_index_missing_update_indexes_block",
            owner="global/local index",
            operation="ALTER TABLE PARTITION BY without required GLOBAL",
            expected="block",
            setup=[
                "CREATE TABLE t(a INT, b INT, UNIQUE KEY idx_a(a))",
            ],
            alter="ALTER TABLE t PARTITION BY HASH(b) PARTITIONS 3",
            block_error_contains=("8264", "Global Index is needed"),
            skip_if_setup_unsupported=True,
        ),
        Case(
            name="global_index_partition_by_update_indexes_rewrite",
            owner="global/local index",
            operation="ALTER TABLE PARTITION BY UPDATE INDEXES",
            expected="rewrite",
            setup=[
                "CREATE TABLE t(a INT UNSIGNED NOT NULL, b VARCHAR(255), UNIQUE KEY idx_a(a), UNIQUE KEY idx_b(b))",
                "INSERT INTO t VALUES (1, 'a'), (2, 'b'), (3, 'c')",
            ],
            alter="ALTER TABLE t PARTITION BY KEY(b) PARTITIONS 3 UPDATE INDEXES (idx_a GLOBAL)",
            oracle=global_partition_by_oracle,
            skip_if_setup_unsupported=True,
        ),
        Case(
            name="global_index_remove_partitioning_rewrite_local",
            owner="global/local index",
            operation="REMOVE PARTITIONING",
            expected="rewrite",
            setup=[
                "CREATE TABLE t(a INT UNSIGNED NOT NULL, b VARCHAR(255), UNIQUE KEY idx_b(b), UNIQUE KEY idx_a(a) GLOBAL) PARTITION BY KEY(b) PARTITIONS 3",
                "INSERT INTO t VALUES (1, 'a'), (2, 'b'), (3, 'c')",
            ],
            alter="ALTER TABLE t REMOVE PARTITIONING",
            oracle=global_remove_partitioning_oracle,
            skip_if_setup_unsupported=True,
        ),
        Case(
            name="global_index_exchange_partition_block",
            owner="global/local index",
            operation="EXCHANGE PARTITION with global index",
            expected="block",
            setup=[
                "CREATE TABLE pt(a INT, b INT, UNIQUE KEY idx_b(b) GLOBAL) PARTITION BY RANGE(a) (PARTITION p0 VALUES LESS THAN (10), PARTITION p1 VALUES LESS THAN (MAXVALUE))",
                "CREATE TABLE nt(a INT, b INT, UNIQUE KEY idx_b(b))",
            ],
            alter="ALTER TABLE pt EXCHANGE PARTITION p0 WITH TABLE nt",
            block_error_contains=("1731", "global index"),
            skip_if_setup_unsupported=True,
        ),
        Case(
            name="global_index_drop_partition_rowset_cleanup",
            owner="global/local index",
            operation="DROP PARTITION",
            expected="rewrite",
            setup=[
                "CREATE TABLE t(a INT, b INT, UNIQUE KEY idx_b(b) GLOBAL) PARTITION BY RANGE(a) (PARTITION p0 VALUES LESS THAN (10), PARTITION p1 VALUES LESS THAN (MAXVALUE))",
                "INSERT INTO t VALUES (1, 100), (20, 200), (30, 300)",
            ],
            alter="ALTER TABLE t DROP PARTITION p0",
            oracle=global_drop_partition_oracle,
            skip_if_setup_unsupported=True,
        ),
        Case(
            name="placement_drop_partition_releases_partition_policy",
            owner="placement policy",
            operation="DROP PARTITION",
            expected="rewrite",
            setup=[
                "CREATE PLACEMENT POLICY pp_part FOLLOWERS=1",
                "CREATE TABLE t(a INT, b INT) PARTITION BY RANGE(a) (PARTITION p0 VALUES LESS THAN (10) PLACEMENT POLICY pp_part, PARTITION p1 VALUES LESS THAN (MAXVALUE))",
                "INSERT INTO t VALUES (1, 1), (20, 20)",
            ],
            alter="ALTER TABLE t DROP PARTITION p0",
            oracle=placement_drop_partition_releases_policy_oracle,
            skip_if_setup_unsupported=True,
        ),
        Case(
            name="placement_truncate_partition_preserves_partition_policy",
            owner="placement policy",
            operation="TRUNCATE PARTITION",
            expected="rewrite",
            setup=[
                "CREATE PLACEMENT POLICY pp_part FOLLOWERS=1",
                "CREATE TABLE t(a INT, b INT) PARTITION BY RANGE(a) (PARTITION p0 VALUES LESS THAN (10) PLACEMENT POLICY pp_part, PARTITION p1 VALUES LESS THAN (MAXVALUE))",
                "INSERT INTO t VALUES (1, 1), (20, 20)",
            ],
            alter="ALTER TABLE t TRUNCATE PARTITION p0",
            oracle=placement_truncate_partition_preserves_policy_oracle,
            skip_if_setup_unsupported=True,
        ),
        Case(
            name="global_index_truncate_partition_rowset_cleanup",
            owner="global/local index",
            operation="TRUNCATE PARTITION",
            expected="rewrite",
            setup=[
                "CREATE TABLE t(a INT, b INT, UNIQUE KEY idx_b(b) GLOBAL) PARTITION BY RANGE(a) (PARTITION p0 VALUES LESS THAN (10), PARTITION p1 VALUES LESS THAN (MAXVALUE))",
                "INSERT INTO t VALUES (1, 100), (20, 200), (30, 300)",
            ],
            alter="ALTER TABLE t TRUNCATE PARTITION p0",
            oracle=global_truncate_partition_oracle,
            skip_if_setup_unsupported=True,
        ),
        Case(
            name="mixed_placement_global_remove_partitioning",
            owner="placement policy + global/local index",
            operation="REMOVE PARTITIONING",
            expected="rewrite",
            setup=[
                "CREATE PLACEMENT POLICY pp_table FOLLOWERS=1",
                "CREATE PLACEMENT POLICY pp_part FOLLOWERS=2",
                "CREATE TABLE t(a INT UNSIGNED NOT NULL, b INT NOT NULL, UNIQUE KEY idx_b(b), UNIQUE KEY idx_a(a) GLOBAL) PLACEMENT POLICY pp_table PARTITION BY HASH(b) (PARTITION p0 PLACEMENT POLICY pp_part, PARTITION p1, PARTITION p2)",
                "INSERT INTO t VALUES (1, 10), (2, 20), (3, 30)",
            ],
            alter="ALTER TABLE t REMOVE PARTITIONING",
            oracle=mixed_remove_partitioning_oracle,
            skip_if_setup_unsupported=True,
        ),
    ]


def cleanup_case(args: argparse.Namespace, db: str) -> None:
    for table_name in TABLES:
        run_mysql(args, f"DROP TABLE IF EXISTS `{table_name}`", db)
    for policy_name in POLICIES:
        run_mysql(args, f"DROP PLACEMENT POLICY IF EXISTS `{policy_name}`", db)


def setup_case(args: argparse.Namespace, db: str, case: Case) -> tuple[bool, str]:
    cleanup_case(args, db)
    for sql in case.setup:
        res = run_mysql(args, sql, db)
        if res.rc != 0:
            if case.skip_if_setup_unsupported:
                cleanup_case(args, db)
                return False, combined(res)
            raise RuntimeError(f"setup failed case={case.name}: {sql}\n{res.err}")
    return True, ""


def run_case(args: argparse.Namespace, db: str, case: Case) -> CaseOutcome:
    ok, setup_err = setup_case(args, db, case)
    if not ok:
        return CaseOutcome(case.name, case.owner, case.operation, "SKIP", setup_err, -1, setup_err)

    alter_res = run_mysql(args, case.alter, db)
    try:
        if case.expected == "block":
            if alter_res.rc == 0:
                return CaseOutcome(
                    case.name,
                    case.owner,
                    case.operation,
                    "FINDING",
                    "DDL unexpectedly succeeded; object reference may have gone stale",
                    alter_res.rc,
                    alter_res.err,
                )
            if case.block_error_contains and not contains_any(combined(alter_res), case.block_error_contains):
                return CaseOutcome(
                    case.name,
                    case.owner,
                    case.operation,
                    "FINDING",
                    "blocked with unexpected error family: " + combined(alter_res),
                    alter_res.rc,
                    alter_res.err,
                )
            return CaseOutcome(case.name, case.owner, case.operation, "OK", combined(alter_res), alter_res.rc, alter_res.err)

        if case.expected == "rewrite":
            if alter_res.rc != 0:
                return CaseOutcome(
                    case.name,
                    case.owner,
                    case.operation,
                    "FINDING",
                    "DDL should rewrite/release object references but failed: " + combined(alter_res),
                    alter_res.rc,
                    alter_res.err,
                )
            if case.oracle is not None:
                oracle_ok, detail = case.oracle(args, db)
                if not oracle_ok:
                    return CaseOutcome(case.name, case.owner, case.operation, "FINDING", detail, alter_res.rc, alter_res.err)
                return CaseOutcome(case.name, case.owner, case.operation, "OK", detail, alter_res.rc, alter_res.err)
            return CaseOutcome(case.name, case.owner, case.operation, "OK", "DDL succeeded", alter_res.rc, alter_res.err)
    finally:
        cleanup_case(args, db)

    raise AssertionError(f"unknown expectation {case.expected}")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default=os.environ.get("MYSQL", "mysql"))
    parser.add_argument("--host", default=os.environ.get("MYSQL_HOST", "127.0.0.1"))
    parser.add_argument("--port", default=os.environ.get("MYSQL_PORT", "14000"))
    parser.add_argument("--user", default=os.environ.get("MYSQL_USER", "root"))
    parser.add_argument("--database-prefix", default="ai_native_ddl_obj_ref")
    parser.add_argument("--keep-db", action="store_true")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    suffix = time.strftime("%Y%m%d_%H%M%S")
    db = f"{args.database_prefix}_{suffix}"

    health = run_mysql(args, "SELECT 1")
    if health.rc != 0:
        print(f"cannot connect to TiDB/MySQL at {args.host}:{args.port}: {health.err}", file=sys.stderr)
        return 2

    exec_ok(args, "SET GLOBAL tidb_placement_mode = 'STRICT'")
    exec_ok(args, f"DROP DATABASE IF EXISTS {quote_db(db)}")
    exec_ok(args, f"CREATE DATABASE {quote_db(db)}")

    outcomes: list[CaseOutcome] = []
    try:
        for case in cases():
            outcome = run_case(args, db, case)
            outcomes.append(outcome)
            print(
                "\t".join(
                    [
                        outcome.status,
                        outcome.owner,
                        outcome.operation,
                        outcome.name,
                        outcome.detail.replace("\n", " ")[:500],
                    ]
                )
            )
    finally:
        cleanup_case(args, db)
        if not args.keep_db:
            run_mysql(args, f"DROP DATABASE IF EXISTS {quote_db(db)}")

    unexpected = [out for out in outcomes if out.status == "FINDING"]
    skipped = [out for out in outcomes if out.status == "SKIP"]
    print(f"SUMMARY total={len(outcomes)} findings={len(unexpected)} skipped={len(skipped)}")
    if unexpected:
        print("UNEXPECTED_FINDINGS")
        for out in unexpected:
            print(f"- {out.name}: {out.detail}; alter_err={out.alter_err}")
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
