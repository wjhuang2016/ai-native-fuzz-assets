#!/usr/bin/env python3
"""Restore-path placement reference probe (methodology v2 pilot).

Proof obligation:
    FLASHBACK DATABASE / RECOVER TABLE must not restore metadata that references
    a placement policy the live catalog cannot resolve. The sibling table path
    (clearTablePlacementAndBundles) defines the intended semantics: recovered
    objects drop their placement refs.

Cells carry trigger evidence per methodology v2:
    a green cell without proof that the target path fired is INVALID, not green.
"""

from __future__ import annotations

import argparse
import dataclasses
import subprocess
import time


@dataclasses.dataclass
class Result:
    rc: int
    out: str
    err: str


@dataclasses.dataclass
class Outcome:
    name: str
    status: str  # RED / GREEN(triggered) / INVALID(untriggered) / SKIP(capability)
    detail: str


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


def exec_ok(args: argparse.Namespace, sql: str, database: str | None = None) -> Result:
    res = run_mysql(args, sql, database)
    if res.rc != 0:
        raise RuntimeError(f"SQL failed rc={res.rc}: {sql}\n{res.err}")
    return res


def policy_exists(args: argparse.Namespace, name: str) -> bool:
    res = exec_ok(
        args,
        f"SELECT COUNT(*) FROM information_schema.placement_policies WHERE policy_name='{name}'",
    )
    return res.out.strip() == "1"


def show_create_db(args: argparse.Namespace, db: str) -> str:
    return exec_ok(args, f"SHOW CREATE DATABASE `{db}`").out


def show_create_table(args: argparse.Namespace, db: str, tbl: str) -> str:
    return exec_ok(args, f"SHOW CREATE TABLE `{db}`.`{tbl}`").out


def cleanup(args: argparse.Namespace, dbs: list[str], policies: list[str]) -> None:
    for db in dbs:
        run_mysql(args, f"DROP DATABASE IF EXISTS `{db}`")
    for p in policies:
        run_mysql(args, f"DROP PLACEMENT POLICY IF EXISTS `{p}`")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default="mysql")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", default="14000")
    parser.add_argument("--user", default="root")
    args = parser.parse_args()

    sfx = time.strftime("%H%M%S")
    outcomes: list[Outcome] = []

    # Capability fingerprint (methodology rule: record before classifying).
    ver = run_mysql(args, "SELECT VERSION()")
    print(f"FINGERPRINT version={ver.out.strip()}")
    cap = run_mysql(args, f"CREATE PLACEMENT POLICY cap_probe_{sfx} FOLLOWERS=1")
    if cap.rc != 0:
        print(f"SUMMARY total=0 findings=0 skipped=ALL  # placement not supported: {cap.err}")
        return 0
    run_mysql(args, f"DROP PLACEMENT POLICY cap_probe_{sfx}")

    dbs = [f"ai_fbp_{i}_{sfx}" for i in range(4)]
    pols = [f"ai_fbp_pol{i}_{sfx}" for i in range(4)]

    try:
        # ---- cell 1 (RED candidate): FLASHBACK DATABASE with dropped policy ----
        name = "flashback_db_ref_to_dropped_policy"
        exec_ok(args, f"CREATE PLACEMENT POLICY `{pols[0]}` FOLLOWERS=1")
        exec_ok(args, f"CREATE DATABASE `{dbs[0]}` PLACEMENT POLICY=`{pols[0]}`")
        exec_ok(args, f"DROP DATABASE `{dbs[0]}`")
        drop_pol = run_mysql(args, f"DROP PLACEMENT POLICY `{pols[0]}`")
        if drop_pol.rc != 0:
            # System protects the policy while the DB is recoverable -> family green.
            outcomes.append(Outcome(name, "GREEN(triggered)",
                                    f"policy drop blocked while db recoverable: {drop_pol.err[:120]}"))
        else:
            fb = run_mysql(args, f"FLASHBACK DATABASE `{dbs[0]}`")
            if fb.rc != 0:
                outcomes.append(Outcome(name, "GREEN(triggered)",
                                        f"flashback blocked on missing policy: {fb.err[:120]}"))
            else:
                # trigger evidence: policy drop succeeded AND flashback succeeded.
                sc = show_create_db(args, dbs[0])
                dangling = pols[0] in sc and not policy_exists(args, pols[0])
                if dangling:
                    outcomes.append(Outcome(name, "RED",
                                            f"SHOW CREATE DATABASE references missing policy: {sc[:200]}"))
                else:
                    outcomes.append(Outcome(name, "GREEN(triggered)",
                                            f"ref cleared or resolved: {sc[:200]}"))

                # ---- cell 2 (consequence): CREATE TABLE inheritance in recovered DB ----
                ct = run_mysql(args, f"CREATE TABLE `{dbs[0]}`.c1(a INT)")
                if dangling:
                    if ct.rc != 0:
                        outcomes.append(Outcome("dangling_consequence_create_table", "RED",
                                                f"CREATE TABLE in recovered db fails: {ct.err[:160]}"))
                    else:
                        tsc = show_create_table(args, dbs[0], "c1")
                        st = "RED" if pols[0] in tsc else "GREEN(triggered)"
                        outcomes.append(Outcome("dangling_consequence_create_table", st,
                                                f"table created; ref propagated={pols[0] in tsc}"))

                    # ---- cell 3 (consequence): same-name policy recreation / ID-vs-name ----
                    exec_ok(args, f"CREATE PLACEMENT POLICY `{pols[0]}` FOLLOWERS=2")
                    ct2 = run_mysql(args, f"CREATE TABLE `{dbs[0]}`.c2(a INT)")
                    det = f"recreate same name: create-table rc={ct2.rc}"
                    if ct2.rc == 0:
                        det += f"; table ref={pols[0] in show_create_table(args, dbs[0], 'c2')}"
                    outcomes.append(Outcome("dangling_same_name_recreate", "INFO", det))

        # ---- cell 4 (control): recover TABLE clears its ref (design trigger evidence) ----
        name = "recover_table_clears_ref_control"
        exec_ok(args, f"CREATE PLACEMENT POLICY `{pols[1]}` FOLLOWERS=1")
        exec_ok(args, f"CREATE DATABASE `{dbs[1]}`")
        exec_ok(args, f"CREATE TABLE `{dbs[1]}`.t(a INT) PLACEMENT POLICY=`{pols[1]}`")
        exec_ok(args, f"DROP TABLE `{dbs[1]}`.t")
        exec_ok(args, f"RECOVER TABLE `{dbs[1]}`.t")
        sc = show_create_table(args, dbs[1], "t")
        if pols[1] in sc:
            outcomes.append(Outcome(name, "RED", f"recovered TABLE kept ref: {sc[:200]}"))
        else:
            outcomes.append(Outcome(name, "GREEN(triggered)",
                                    "recovered table ref cleared (clearTablePlacementAndBundles fired)"))

        # ---- cell 5 (asymmetry doc): FLASHBACK DB with policy still alive ----
        name = "flashback_db_policy_alive_asymmetry"
        exec_ok(args, f"CREATE PLACEMENT POLICY `{pols[2]}` FOLLOWERS=1")
        exec_ok(args, f"CREATE DATABASE `{dbs[2]}` PLACEMENT POLICY=`{pols[2]}`")
        exec_ok(args, f"CREATE TABLE `{dbs[2]}`.t(a INT) PLACEMENT POLICY=`{pols[2]}`")
        exec_ok(args, f"DROP DATABASE `{dbs[2]}`")
        exec_ok(args, f"FLASHBACK DATABASE `{dbs[2]}`")
        db_keeps = pols[2] in show_create_db(args, dbs[2])
        tbl_keeps = pols[2] in show_create_table(args, dbs[2], "t")
        st = "INFO" if db_keeps != tbl_keeps else "GREEN(triggered)"
        outcomes.append(Outcome(name, st, f"db_keeps_ref={db_keeps} table_keeps_ref={tbl_keeps}"))

        # ---- cell 6 (sibling): FLASHBACK DB, TABLE-level ref to dropped policy ----
        name = "flashback_db_table_ref_dropped_policy"
        exec_ok(args, f"CREATE PLACEMENT POLICY `{pols[3]}` FOLLOWERS=1")
        exec_ok(args, f"CREATE DATABASE `{dbs[3]}`")
        exec_ok(args, f"CREATE TABLE `{dbs[3]}`.t(a INT) PLACEMENT POLICY=`{pols[3]}`")
        exec_ok(args, f"DROP DATABASE `{dbs[3]}`")
        dp = run_mysql(args, f"DROP PLACEMENT POLICY `{pols[3]}`")
        if dp.rc != 0:
            outcomes.append(Outcome(name, "GREEN(triggered)", f"policy drop blocked: {dp.err[:120]}"))
        else:
            fb = run_mysql(args, f"FLASHBACK DATABASE `{dbs[3]}`")
            if fb.rc != 0:
                outcomes.append(Outcome(name, "RED", f"flashback itself failed: {fb.err[:160]}"))
            else:
                sc = show_create_table(args, dbs[3], "t")
                st = "RED" if pols[3] in sc else "GREEN(triggered)"
                outcomes.append(Outcome(name, st, f"table ref after flashback-db: present={pols[3] in sc}"))

    finally:
        cleanup(args, dbs, pols)

    for o in outcomes:
        print("\t".join([o.status, o.name, o.detail.replace("\n", " ")[:400]]))
    reds = [o for o in outcomes if o.status == "RED"]
    print(f"SUMMARY total={len(outcomes)} findings={len(reds)} skipped=0")
    return 1 if reds else 0


if __name__ == "__main__":
    raise SystemExit(main())
