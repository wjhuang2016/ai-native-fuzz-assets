#!/usr/bin/env python3
"""Resource-group SWITCH_GROUP reference-owner screen.

The proof obligation under test is:

    if one DDL object references another DDL object, DDL that removes the
    referenced object must either rewrite the reference or block.

`QUERY_LIMIT=(ACTION=SWITCH_GROUP(...))` looks like a resource-group to
resource-group reference. This probe first checks whether TiDB actually treats
the switch target as an existence-validated DDL object reference. If missing
targets are allowed at create/alter time, the stored name is classified as a
name-bound parameter rather than a maintained reference owner.
"""

from __future__ import annotations

import argparse
import dataclasses
import subprocess
from collections.abc import Callable


@dataclasses.dataclass
class Result:
    rc: int
    out: str
    err: str


@dataclasses.dataclass
class Outcome:
    name: str
    status: str
    detail: str


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


def exec_ok(args: argparse.Namespace, sql: str) -> None:
    res = run_mysql(args, sql)
    if res.rc != 0:
        raise RuntimeError(f"SQL failed rc={res.rc}: {sql}\n{combined(res)}")


def cleanup_groups(args: argparse.Namespace, *names: str) -> None:
    for name in names:
        run_mysql(args, f"DROP RESOURCE GROUP IF EXISTS {name}")


def query_limit(args: argparse.Namespace, name: str) -> str | None:
    res = run_mysql(args, f"SELECT query_limit FROM information_schema.resource_groups WHERE name = '{name}'")
    if res.rc != 0:
        raise RuntimeError("query information_schema.resource_groups failed: " + combined(res))
    if not res.out:
        return None
    return res.out.strip()


def create_group(args: argparse.Namespace, name: str, switch_target: str | None = None) -> Result:
    suffix = ""
    if switch_target:
        suffix = f" QUERY_LIMIT=(EXEC_ELAPSED='1s' ACTION=SWITCH_GROUP({switch_target}))"
    return run_mysql(args, f"CREATE RESOURCE GROUP {name} RU_PER_SEC = 100{suffix}")


def missing_switch_target_allowed(args: argparse.Namespace) -> tuple[bool, str]:
    src = "ai_native_rg_missing_src"
    missing = "ai_native_rg_missing_target"
    try:
        cleanup_groups(args, src, missing)
        create = create_group(args, src, missing)
        if create.rc != 0:
            return False, combined(create)
        visible = query_limit(args, src) or ""
        return missing in visible, visible
    finally:
        cleanup_groups(args, src, missing)


def run_case(args: argparse.Namespace, name: str, fn: Callable[[argparse.Namespace], Outcome]) -> Outcome:
    try:
        return fn(args)
    except Exception as exc:
        return Outcome(name, "finding", f"probe crashed: {exc}")


def case_missing_switch_target_semantics(args: argparse.Namespace) -> Outcome:
    name = "missing_switch_target_semantics"
    allowed, detail = missing_switch_target_allowed(args)
    if allowed:
        return Outcome(
            name,
            "ok",
            "CREATE RESOURCE GROUP allows SWITCH_GROUP to a missing name; classify as name-bound parameter: "
            + detail,
        )
    return Outcome(name, "ok", "CREATE RESOURCE GROUP validates SWITCH_GROUP target: " + detail)


def case_drop_switch_target_semantics(args: argparse.Namespace) -> Outcome:
    name = "drop_switch_target_semantics"
    src = "ai_native_rg_src"
    target = "ai_native_rg_target"
    try:
        cleanup_groups(args, src, target)
        exec_ok(args, f"CREATE RESOURCE GROUP {target} RU_PER_SEC = 100")
        create = create_group(args, src, target)
        if create.rc != 0:
            return Outcome(name, "skipped", "could not create source SWITCH_GROUP reference: " + combined(create))

        drop = run_mysql(args, f"DROP RESOURCE GROUP {target}")
        if drop.rc != 0:
            return Outcome(name, "ok", "DROP RESOURCE GROUP blocks referenced switch target: " + combined(drop))

        visible = query_limit(args, src) or ""
        if target not in visible:
            return Outcome(name, "ok", "DROP RESOURCE GROUP succeeded and source no longer shows target: " + visible)

        missing_allowed, missing_detail = missing_switch_target_allowed(args)
        if missing_allowed:
            return Outcome(
                name,
                "ok",
                "DROP target leaves stored SWITCH_GROUP name, but create already allows missing targets; "
                "not a maintained DDL reference. source=" + visible,
            )
        return Outcome(
            name,
            "finding",
            "CREATE validates SWITCH_GROUP targets, but DROP target succeeded and left source reference: "
            + visible
            + "; missing-target check="
            + missing_detail,
        )
    finally:
        cleanup_groups(args, src, target)


def case_alter_query_limit_null_releases_name(args: argparse.Namespace) -> Outcome:
    name = "alter_query_limit_null_releases_name"
    src = "ai_native_rg_src"
    target = "ai_native_rg_target"
    try:
        cleanup_groups(args, src, target)
        exec_ok(args, f"CREATE RESOURCE GROUP {target} RU_PER_SEC = 100")
        create = create_group(args, src, target)
        if create.rc != 0:
            return Outcome(name, "skipped", "could not create source SWITCH_GROUP reference: " + combined(create))
        exec_ok(args, f"ALTER RESOURCE GROUP {src} QUERY_LIMIT=NULL")
        visible = query_limit(args, src)
        if visible != "NULL":
            return Outcome(name, "finding", "ALTER ... QUERY_LIMIT=NULL did not clear query_limit: " + str(visible))
        drop = run_mysql(args, f"DROP RESOURCE GROUP {target}")
        if drop.rc != 0:
            return Outcome(name, "finding", "target not droppable after clearing QUERY_LIMIT: " + combined(drop))
        return Outcome(name, "ok", "ALTER ... QUERY_LIMIT=NULL clears the stored switch-group name")
    finally:
        cleanup_groups(args, src, target)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default="mysql")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", default="14000")
    parser.add_argument("--user", default="root")
    args = parser.parse_args()

    cases: list[tuple[str, Callable[[argparse.Namespace], Outcome]]] = [
        ("missing_switch_target_semantics", case_missing_switch_target_semantics),
        ("drop_switch_target_semantics", case_drop_switch_target_semantics),
        ("alter_query_limit_null_releases_name", case_alter_query_limit_null_releases_name),
    ]

    outcomes = [run_case(args, name, fn) for name, fn in cases]
    findings = 0
    skipped = 0
    for outcome in outcomes:
        print(f"{outcome.status} {outcome.name}")
        if outcome.detail:
            print(f"  {outcome.detail}")
        if outcome.status == "finding":
            findings += 1
        if outcome.status == "skipped":
            skipped += 1
    print(f"SUMMARY total={len(outcomes)} findings={findings} skipped={skipped}")
    return 1 if findings else 0


if __name__ == "__main__":
    raise SystemExit(main())
