#!/usr/bin/env python3
"""DDL reference ownership matrix probe.

The probe is intentionally DDL-only. It checks whether metadata references are
rewritten or blocked when a column is renamed, changed, modified, or dropped.
Known project findings are kept as controls so new unexpected cells stand out.
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
    known_control: bool = False
    skip_if_setup_unsupported: bool = False
    block_error_contains: tuple[str, ...] = ()


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


def show_create(args: argparse.Namespace, db: str, table_name: str = "t") -> str:
    res = run_mysql(args, f"SHOW CREATE TABLE `{table_name}`", db)
    if res.rc != 0:
        return res.err
    return "\n".join(rows(res))


def require_show_create_contains(table_name: str, *needles: str) -> Callable[[argparse.Namespace, str], tuple[bool, str]]:
    def oracle(args: argparse.Namespace, db: str) -> tuple[bool, str]:
        ddl = show_create(args, db, table_name)
        missing = [needle for needle in needles if needle not in ddl]
        if missing:
            return False, f"SHOW CREATE missing {missing}; got={ddl}"
        return True, "SHOW CREATE contains " + ", ".join(needles)

    return oracle


def require_insert_fails(sql: str, errno_or_text: str) -> Callable[[argparse.Namespace, str], tuple[bool, str]]:
    def oracle(args: argparse.Namespace, db: str) -> tuple[bool, str]:
        res = run_mysql(args, sql, db)
        combined = res.err + "\n" + res.out
        if res.rc == 0:
            return False, f"expected insert to fail but it succeeded: {sql}"
        if errno_or_text not in combined:
            return False, f"insert failed with unexpected error: {combined}"
        return True, f"insert failed as expected with {errno_or_text}"

    return oracle


def fk_child_oracle(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    ddl = show_create(args, db, "c")
    if "FOREIGN KEY (`pid2`)" not in ddl:
        return False, "child FK was not rewritten to pid2: " + ddl
    bad = run_mysql(args, "INSERT INTO c VALUES (2, 999)", db)
    if bad.rc == 0 or "1452" not in bad.err:
        return False, f"FK enforcement missing or unexpected error rc={bad.rc} err={bad.err}"
    return True, "child FK metadata and enforcement survived rename"


def fk_parent_oracle(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    ddl = show_create(args, db, "c")
    if "REFERENCES `p` (`id2`)" not in ddl:
        return False, "child FK reference was not rewritten to parent id2: " + ddl
    bad = run_mysql(args, "INSERT INTO c VALUES (2, 999)", db)
    if bad.rc == 0 or "1452" not in bad.err:
        return False, f"FK enforcement missing or unexpected error rc={bad.rc} err={bad.err}"
    return True, "parent FK reference and enforcement survived rename"


def check_known_check_loss(args: argparse.Namespace, db: str) -> tuple[bool, str]:
    invalid = run_mysql(args, "INSERT INTO t(id, a, bb) VALUES (100, 1, -1)", db)
    if invalid.rc != 0 and "Unknown column 'bb'" in invalid.err:
        invalid = run_mysql(args, "INSERT INTO t(id, a, b) VALUES (100, 1, -1)", db)
    ddl = show_create(args, db)
    if invalid.rc == 0:
        return False, "known bug present: CHECK enforcement was lost; SHOW CREATE=" + ddl
    if "3819" in invalid.err or "Check constraint" in invalid.err:
        return True, "CHECK still enforced"
    return False, f"CHECK insert failed with unexpected error: {invalid.err}; SHOW CREATE={ddl}"


def cases() -> list[Case]:
    return [
        Case(
            name="known_check_change_rename_multicol",
            owner="CHECK",
            operation="CHANGE COLUMN",
            expected="known_control",
            setup=[
                "SET GLOBAL tidb_enable_check_constraint = 1",
                "CREATE TABLE t(id INT PRIMARY KEY, a INT, b INT, CONSTRAINT chk_ab CHECK (a > 0 AND b > 0))",
                "INSERT INTO t VALUES (1, 1, 1)",
            ],
            alter="ALTER TABLE t CHANGE COLUMN b bb INT",
            oracle=check_known_check_loss,
            known_control=True,
        ),
        Case(
            name="known_check_multischema_change_rename",
            owner="CHECK",
            operation="CHANGE COLUMN + ADD COLUMN",
            expected="known_control",
            setup=[
                "SET GLOBAL tidb_enable_check_constraint = 1",
                "CREATE TABLE t(id INT PRIMARY KEY, a INT, b INT, CONSTRAINT chk_ab CHECK (a > 0 AND b > 0))",
                "INSERT INTO t VALUES (1, 1, 1)",
            ],
            alter="ALTER TABLE t CHANGE COLUMN b bb INT, ADD COLUMN c INT",
            oracle=check_known_check_loss,
            known_control=True,
        ),
        Case(
            name="check_modify_same_name_preserve_or_block",
            owner="CHECK",
            operation="MODIFY COLUMN",
            expected="rewrite_or_block",
            setup=[
                "SET GLOBAL tidb_enable_check_constraint = 1",
                "CREATE TABLE t(id INT PRIMARY KEY, a INT, b INT, CONSTRAINT chk_ab CHECK (a > 0 AND b > 0))",
                "INSERT INTO t VALUES (1, 1, 1)",
            ],
            alter="ALTER TABLE t MODIFY COLUMN b BIGINT",
            oracle=check_known_check_loss,
        ),
        Case(
            name="known_partial_predicate_rename_error_family",
            owner="partial-index predicate",
            operation="RENAME COLUMN",
            expected="block",
            setup=[
                "CREATE TABLE t(id INT PRIMARY KEY, a INT, b INT, INDEX pi(b) WHERE a < 3)",
            ],
            alter="ALTER TABLE t RENAME COLUMN a TO aa",
            known_control=True,
        ),
        Case(
            name="partial_predicate_multischema_change_block",
            owner="partial-index predicate",
            operation="CHANGE COLUMN + ADD COLUMN",
            expected="block",
            setup=[
                "CREATE TABLE t(id INT PRIMARY KEY, a INT, b INT, INDEX pi(b) WHERE a < 3)",
            ],
            alter="ALTER TABLE t CHANGE COLUMN a aa INT, ADD COLUMN c INT",
            block_error_contains=("partial index", "8272"),
        ),
        Case(
            name="generated_virtual_rename_base",
            owner="generated column",
            operation="RENAME COLUMN",
            expected="block",
            setup=[
                "CREATE TABLE t(a INT, g INT GENERATED ALWAYS AS (a + 1) VIRTUAL)",
            ],
            alter="ALTER TABLE t RENAME COLUMN a TO aa",
            block_error_contains=("generated", "3108"),
        ),
        Case(
            name="generated_stored_change_base",
            owner="generated column",
            operation="CHANGE COLUMN",
            expected="block",
            setup=[
                "CREATE TABLE t(a INT, g INT GENERATED ALWAYS AS (a + 1) STORED)",
            ],
            alter="ALTER TABLE t CHANGE COLUMN a aa INT",
            block_error_contains=("generated", "3108"),
        ),
        Case(
            name="generated_virtual_drop_base",
            owner="generated column",
            operation="DROP COLUMN",
            expected="block",
            setup=[
                "CREATE TABLE t(a INT, g INT GENERATED ALWAYS AS (a + 1) VIRTUAL)",
            ],
            alter="ALTER TABLE t DROP COLUMN a",
            block_error_contains=("generated", "3108"),
        ),
        Case(
            name="generated_virtual_modify_base_type",
            owner="generated column",
            operation="MODIFY COLUMN",
            expected="block",
            setup=[
                "CREATE TABLE t(a INT, g BIGINT GENERATED ALWAYS AS (a + 1) VIRTUAL)",
            ],
            alter="ALTER TABLE t MODIFY COLUMN a BIGINT",
            block_error_contains=("generated", "3108", "3106"),
        ),
        Case(
            name="partition_range_expr_rename_key",
            owner="partition expression",
            operation="RENAME COLUMN",
            expected="block",
            setup=[
                "CREATE TABLE t(id INT, a INT, b INT) PARTITION BY RANGE (a) (PARTITION p0 VALUES LESS THAN (10), PARTITION p1 VALUES LESS THAN (MAXVALUE))",
            ],
            alter="ALTER TABLE t RENAME COLUMN a TO aa",
            block_error_contains=("partitioning function dependency", "3855"),
        ),
        Case(
            name="partition_range_expr_change_key",
            owner="partition expression",
            operation="CHANGE COLUMN",
            expected="block",
            setup=[
                "CREATE TABLE t(id INT, a INT, b INT) PARTITION BY RANGE (a) (PARTITION p0 VALUES LESS THAN (10), PARTITION p1 VALUES LESS THAN (MAXVALUE))",
            ],
            alter="ALTER TABLE t CHANGE COLUMN a aa INT",
            block_error_contains=("partitioning function dependency", "3855"),
        ),
        Case(
            name="partition_range_columns_drop_key",
            owner="partition columns",
            operation="DROP COLUMN",
            expected="block",
            setup=[
                "CREATE TABLE t(id INT, a INT, b INT) PARTITION BY RANGE COLUMNS (a) (PARTITION p0 VALUES LESS THAN (10), PARTITION p1 VALUES LESS THAN (MAXVALUE))",
            ],
            alter="ALTER TABLE t DROP COLUMN a",
            block_error_contains=("partitioning function dependency", "3855"),
        ),
        Case(
            name="partition_multischema_change_key",
            owner="partition expression",
            operation="CHANGE COLUMN + ADD COLUMN",
            expected="block",
            setup=[
                "CREATE TABLE t(id INT, a INT, b INT) PARTITION BY RANGE (a) (PARTITION p0 VALUES LESS THAN (10), PARTITION p1 VALUES LESS THAN (MAXVALUE))",
            ],
            alter="ALTER TABLE t CHANGE COLUMN a aa INT, ADD COLUMN c INT",
            block_error_contains=("partitioning function dependency", "3855"),
        ),
        Case(
            name="partition_hash_rename_key",
            owner="partition expression",
            operation="RENAME COLUMN",
            expected="block",
            setup=[
                "CREATE TABLE t(id INT, a INT, b INT) PARTITION BY HASH (a) PARTITIONS 3",
            ],
            alter="ALTER TABLE t RENAME COLUMN a TO aa",
            block_error_contains=("partitioning function dependency", "3855"),
        ),
        Case(
            name="ttl_rename_column_rewrite",
            owner="TTL",
            operation="RENAME COLUMN",
            expected="rewrite",
            setup=[
                "CREATE TABLE t(id INT PRIMARY KEY, t1 TIMESTAMP) TTL = `t1` + INTERVAL 1 DAY",
            ],
            alter="ALTER TABLE t RENAME COLUMN t1 TO t1x",
            oracle=require_show_create_contains("t", "TTL=`t1x` + INTERVAL 1 DAY"),
        ),
        Case(
            name="ttl_multischema_change_rewrite",
            owner="TTL",
            operation="CHANGE COLUMN + ADD COLUMN",
            expected="rewrite",
            setup=[
                "CREATE TABLE t(id INT PRIMARY KEY, t1 TIMESTAMP) TTL = `t1` + INTERVAL 1 DAY",
            ],
            alter="ALTER TABLE t CHANGE COLUMN t1 t1x TIMESTAMP, ADD COLUMN c INT",
            oracle=require_show_create_contains("t", "TTL=`t1x` + INTERVAL 1 DAY"),
        ),
        Case(
            name="ttl_change_column_rewrite",
            owner="TTL",
            operation="CHANGE COLUMN",
            expected="rewrite",
            setup=[
                "CREATE TABLE t(id INT PRIMARY KEY, t1 TIMESTAMP) TTL = `t1` + INTERVAL 1 DAY",
            ],
            alter="ALTER TABLE t CHANGE COLUMN t1 t1x TIMESTAMP",
            oracle=require_show_create_contains("t", "TTL=`t1x` + INTERVAL 1 DAY"),
        ),
        Case(
            name="ttl_drop_column_block",
            owner="TTL",
            operation="DROP COLUMN",
            expected="block",
            setup=[
                "CREATE TABLE t(id INT PRIMARY KEY, t1 TIMESTAMP) TTL = `t1` + INTERVAL 1 DAY",
            ],
            alter="ALTER TABLE t DROP COLUMN t1",
            block_error_contains=("TTL", "8149"),
        ),
        Case(
            name="ttl_modify_to_nontime_block",
            owner="TTL",
            operation="MODIFY COLUMN",
            expected="block",
            setup=[
                "CREATE TABLE t(id INT PRIMARY KEY, t1 TIMESTAMP) TTL = `t1` + INTERVAL 1 DAY",
            ],
            alter="ALTER TABLE t MODIFY COLUMN t1 INT",
            block_error_contains=("TTL", "8148"),
        ),
        Case(
            name="fk_child_rename_rewrite",
            owner="FK child",
            operation="RENAME COLUMN",
            expected="rewrite",
            setup=[
                "SET GLOBAL tidb_enable_foreign_key = 1",
                "CREATE TABLE p(id INT PRIMARY KEY)",
                "CREATE TABLE c(id INT PRIMARY KEY, pid INT, INDEX(pid), CONSTRAINT fk_pid FOREIGN KEY(pid) REFERENCES p(id))",
                "INSERT INTO p VALUES (1)",
                "INSERT INTO c VALUES (1, 1)",
            ],
            alter="ALTER TABLE c RENAME COLUMN pid TO pid2",
            oracle=fk_child_oracle,
        ),
        Case(
            name="fk_child_change_rewrite",
            owner="FK child",
            operation="CHANGE COLUMN",
            expected="rewrite",
            setup=[
                "SET GLOBAL tidb_enable_foreign_key = 1",
                "CREATE TABLE p(id INT PRIMARY KEY)",
                "CREATE TABLE c(id INT PRIMARY KEY, pid INT, INDEX(pid), CONSTRAINT fk_pid FOREIGN KEY(pid) REFERENCES p(id))",
                "INSERT INTO p VALUES (1)",
                "INSERT INTO c VALUES (1, 1)",
            ],
            alter="ALTER TABLE c CHANGE COLUMN pid pid2 INT",
            oracle=fk_child_oracle,
        ),
        Case(
            name="fk_parent_rename_rewrite",
            owner="FK parent",
            operation="RENAME COLUMN",
            expected="rewrite",
            setup=[
                "SET GLOBAL tidb_enable_foreign_key = 1",
                "CREATE TABLE p(id INT PRIMARY KEY)",
                "CREATE TABLE c(id INT PRIMARY KEY, pid INT, INDEX(pid), CONSTRAINT fk_pid FOREIGN KEY(pid) REFERENCES p(id))",
                "INSERT INTO p VALUES (1)",
                "INSERT INTO c VALUES (1, 1)",
            ],
            alter="ALTER TABLE p RENAME COLUMN id TO id2",
            oracle=fk_parent_oracle,
        ),
        Case(
            name="fk_parent_change_rewrite",
            owner="FK parent",
            operation="CHANGE COLUMN",
            expected="rewrite",
            setup=[
                "SET GLOBAL tidb_enable_foreign_key = 1",
                "CREATE TABLE p(id INT PRIMARY KEY)",
                "CREATE TABLE c(id INT PRIMARY KEY, pid INT, INDEX(pid), CONSTRAINT fk_pid FOREIGN KEY(pid) REFERENCES p(id))",
                "INSERT INTO p VALUES (1)",
                "INSERT INTO c VALUES (1, 1)",
            ],
            alter="ALTER TABLE p CHANGE COLUMN id id2 INT",
            oracle=fk_parent_oracle,
        ),
        Case(
            name="fk_child_drop_block",
            owner="FK child",
            operation="DROP COLUMN",
            expected="block",
            setup=[
                "SET GLOBAL tidb_enable_foreign_key = 1",
                "CREATE TABLE p(id INT PRIMARY KEY)",
                "CREATE TABLE c(id INT PRIMARY KEY, pid INT, INDEX(pid), CONSTRAINT fk_pid FOREIGN KEY(pid) REFERENCES p(id))",
            ],
            alter="ALTER TABLE c DROP COLUMN pid",
            block_error_contains=("foreign key", "1828"),
        ),
        Case(
            name="fk_parent_drop_block",
            owner="FK parent",
            operation="DROP COLUMN",
            expected="block",
            setup=[
                "SET GLOBAL tidb_enable_foreign_key = 1",
                "CREATE TABLE p(id INT, extra INT, UNIQUE KEY uid(id))",
                "CREATE TABLE c(id INT PRIMARY KEY, pid INT, INDEX(pid), CONSTRAINT fk_pid FOREIGN KEY(pid) REFERENCES p(id))",
            ],
            alter="ALTER TABLE p DROP COLUMN id",
            block_error_contains=("foreign key", "1829", "1830", "Cannot drop"),
        ),
        Case(
            name="fk_child_modify_incompatible_block",
            owner="FK child",
            operation="MODIFY COLUMN",
            expected="block",
            setup=[
                "SET GLOBAL tidb_enable_foreign_key = 1",
                "CREATE TABLE p(id INT PRIMARY KEY)",
                "CREATE TABLE c(id INT PRIMARY KEY, pid INT, INDEX(pid), CONSTRAINT fk_pid FOREIGN KEY(pid) REFERENCES p(id))",
            ],
            alter="ALTER TABLE c MODIFY COLUMN pid VARCHAR(20)",
            block_error_contains=("incompatible", "3780"),
        ),
        Case(
            name="ordinary_index_rename_rewrite",
            owner="ordinary index",
            operation="RENAME COLUMN",
            expected="rewrite",
            setup=[
                "CREATE TABLE t(id INT PRIMARY KEY, a INT, KEY idx_a(a))",
            ],
            alter="ALTER TABLE t RENAME COLUMN a TO aa",
            oracle=require_show_create_contains("t", "KEY `idx_a` (`aa`)"),
        ),
        Case(
            name="global_index_rename_rewrite",
            owner="global index",
            operation="RENAME COLUMN",
            expected="rewrite",
            setup=[
                "CREATE TABLE t(a INT, b INT, UNIQUE KEY uk_b(b) GLOBAL) PARTITION BY RANGE (a) (PARTITION p0 VALUES LESS THAN (10), PARTITION p1 VALUES LESS THAN (MAXVALUE))",
            ],
            alter="ALTER TABLE t RENAME COLUMN b TO bb",
            oracle=require_show_create_contains("t", "UNIQUE KEY `uk_b` (`bb`)"),
            skip_if_setup_unsupported=True,
        ),
    ]


def setup_case(args: argparse.Namespace, db: str, case: Case) -> tuple[bool, str]:
    exec_ok(args, "DROP TABLE IF EXISTS c", db)
    exec_ok(args, "DROP TABLE IF EXISTS p", db)
    exec_ok(args, "DROP TABLE IF EXISTS t", db)
    for sql in case.setup:
        res = run_mysql(args, sql, db)
        if res.rc != 0:
            if case.skip_if_setup_unsupported:
                return False, res.err
            raise RuntimeError(f"setup failed case={case.name}: {sql}\n{res.err}")
    return True, ""


def run_case(args: argparse.Namespace, db: str, case: Case) -> CaseOutcome:
    ok, setup_err = setup_case(args, db, case)
    if not ok:
        return CaseOutcome(case.name, case.owner, case.operation, "SKIP", setup_err, -1, setup_err)

    alter_res = run_mysql(args, case.alter, db)
    if case.expected == "block":
        if alter_res.rc == 0:
            detail = "DDL unexpectedly succeeded; reference may have gone stale"
            return CaseOutcome(case.name, case.owner, case.operation, "FINDING", detail, alter_res.rc, alter_res.err)
        if case.known_control:
            return CaseOutcome(case.name, case.owner, case.operation, "KNOWN", alter_res.err, alter_res.rc, alter_res.err)
        combined = alter_res.err + "\n" + alter_res.out
        if case.block_error_contains and not any(needle in combined for needle in case.block_error_contains):
            detail = "blocked with unexpected error family: " + combined
            return CaseOutcome(case.name, case.owner, case.operation, "FINDING", detail, alter_res.rc, alter_res.err)
        return CaseOutcome(case.name, case.owner, case.operation, "OK", alter_res.err, alter_res.rc, alter_res.err)

    if case.expected == "rewrite":
        if alter_res.rc != 0:
            detail = "DDL should rewrite metadata but failed"
            return CaseOutcome(case.name, case.owner, case.operation, "FINDING", detail, alter_res.rc, alter_res.err)
        if case.oracle is not None:
            oracle_ok, detail = case.oracle(args, db)
            if not oracle_ok:
                return CaseOutcome(case.name, case.owner, case.operation, "FINDING", detail, alter_res.rc, alter_res.err)
            return CaseOutcome(case.name, case.owner, case.operation, "OK", detail, alter_res.rc, alter_res.err)
        return CaseOutcome(case.name, case.owner, case.operation, "OK", "DDL succeeded", alter_res.rc, alter_res.err)

    if case.expected == "rewrite_or_block":
        if alter_res.rc != 0:
            return CaseOutcome(case.name, case.owner, case.operation, "OK", "blocked: " + alter_res.err, alter_res.rc, alter_res.err)
        if case.oracle is not None:
            oracle_ok, detail = case.oracle(args, db)
            if not oracle_ok:
                return CaseOutcome(case.name, case.owner, case.operation, "FINDING", detail, alter_res.rc, alter_res.err)
            return CaseOutcome(case.name, case.owner, case.operation, "OK", detail, alter_res.rc, alter_res.err)
        return CaseOutcome(case.name, case.owner, case.operation, "OK", "DDL succeeded", alter_res.rc, alter_res.err)

    if case.expected == "known_control":
        if alter_res.rc != 0:
            return CaseOutcome(case.name, case.owner, case.operation, "FIXED_OR_BLOCKED", alter_res.err, alter_res.rc, alter_res.err)
        if case.oracle is not None:
            oracle_ok, detail = case.oracle(args, db)
            status = "OK" if oracle_ok else "KNOWN"
            return CaseOutcome(case.name, case.owner, case.operation, status, detail, alter_res.rc, alter_res.err)
        return CaseOutcome(case.name, case.owner, case.operation, "KNOWN", "known-control DDL succeeded", alter_res.rc, alter_res.err)

    raise AssertionError(f"unknown expectation {case.expected}")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default=os.environ.get("MYSQL", "mysql"))
    parser.add_argument("--host", default=os.environ.get("MYSQL_HOST", "127.0.0.1"))
    parser.add_argument("--port", default=os.environ.get("MYSQL_PORT", "14000"))
    parser.add_argument("--user", default=os.environ.get("MYSQL_USER", "root"))
    parser.add_argument("--database-prefix", default="ai_native_ddl_ref")
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
        if not args.keep_db:
            run_mysql(args, f"DROP DATABASE IF EXISTS {quote_db(db)}")

    unexpected = [out for out in outcomes if out.status == "FINDING"]
    known = [out for out in outcomes if out.status == "KNOWN"]
    skipped = [out for out in outcomes if out.status == "SKIP"]
    print(f"SUMMARY total={len(outcomes)} findings={len(unexpected)} known_controls={len(known)} skipped={len(skipped)}")
    if unexpected:
        print("UNEXPECTED_FINDINGS")
        for out in unexpected:
            print(f"- {out.name}: {out.detail}; alter_err={out.alter_err}")
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
