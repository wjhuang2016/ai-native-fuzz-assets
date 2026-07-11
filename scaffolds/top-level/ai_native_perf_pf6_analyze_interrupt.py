#!/usr/bin/env python3
"""
PF-6 / PS1 probe: interrupt ANALYZE after some partition results have been sent.

The goal is not to fuzz ANALYZE broadly.  It checks one proof obligation:
after an interrupted multi-task ANALYZE, user-visible job state and stats state
must not look like a clean full success.
"""

from __future__ import annotations

import argparse
import subprocess
import sys
import time
import urllib.error
import urllib.request
from dataclasses import dataclass


FP_SEND = "github.com/pingcap/tidb/pkg/executor/analyzeBeforeSendToSaveResults"


@dataclass
class CmdResult:
    rc: int
    out: str
    err: str


def mysql_cmd(args: argparse.Namespace, sql: str, db: str | None = None) -> list[str]:
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
    cmd.extend(["-e", sql])
    return cmd


def run_mysql(args: argparse.Namespace, sql: str, db: str | None = None, timeout: int = 30) -> CmdResult:
    proc = subprocess.run(
        mysql_cmd(args, sql, db),
        text=True,
        capture_output=True,
        timeout=timeout,
    )
    return CmdResult(proc.returncode, proc.stdout.strip(), proc.stderr.strip())


def must_mysql(args: argparse.Namespace, sql: str, db: str | None = None, timeout: int = 30) -> str:
    res = run_mysql(args, sql, db, timeout)
    if res.rc != 0:
        raise RuntimeError(f"mysql failed rc={res.rc}\nsql={sql}\nstdout={res.out}\nstderr={res.err}")
    return res.out


def failpoint_request(args: argparse.Namespace, method: str, name: str = "", body: str | None = None) -> tuple[int, str]:
    url = args.status_url.rstrip("/") + "/fail/"
    if name:
        url += name
    data = body.encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            return resp.getcode(), resp.read().decode(errors="replace")
    except urllib.error.HTTPError as err:
        return err.code, err.read().decode(errors="replace")
    except OSError as err:
        return 0, str(err)


def set_failpoint(args: argparse.Namespace, action: str) -> None:
    code, body = failpoint_request(args, "PUT", FP_SEND, action)
    if code not in (200, 204):
        raise RuntimeError(f"failed to set failpoint: code={code} body={body}")


def clear_failpoint(args: argparse.Namespace) -> None:
    failpoint_request(args, "DELETE", FP_SEND)


def print_block(title: str, body: str) -> None:
    print(f"\n## {title}")
    print(body if body else "(empty)")


def setup_table(args: argparse.Namespace) -> None:
    old_auto = must_mysql(args, "select @@global.tidb_enable_auto_analyze")
    args._old_auto_analyze = old_auto.strip()
    must_mysql(args, "set global tidb_enable_auto_analyze=0")
    must_mysql(args, f"drop database if exists `{args.db}`", timeout=60)
    must_mysql(args, f"delete from mysql.analyze_jobs where table_schema = '{args.db}'")
    must_mysql(args, f"create database `{args.db}`")
    must_mysql(
        args,
        """
        create table t (
            a int not null,
            b int not null,
            c varchar(32),
            primary key(a),
            key idx_b(b)
        ) partition by hash(a) partitions 4
        """,
        args.db,
    )
    batch = []
    total = args.rows
    for i in range(total):
        batch.append(f"({i},{i % 97},'v{i % 17}')")
        if len(batch) == 800 or i == total - 1:
            must_mysql(args, "insert into t values " + ",".join(batch), args.db, timeout=60)
            batch = []
    must_mysql(args, "select count(*) from t", args.db)


def restore_global(args: argparse.Namespace) -> None:
    old = getattr(args, "_old_auto_analyze", None)
    if old is not None:
        run_mysql(args, f"set global tidb_enable_auto_analyze={old}")


def find_analyze_process(args: argparse.Namespace) -> tuple[str, str] | None:
    sql = f"""
        select id, concat(command, '|', time, '|', ifnull(state,''), '|', ifnull(info,''))
        from information_schema.processlist
        where db = '{args.db}'
          and lower(info) like '%analyze table t%'
        order by time desc
        limit 1
    """
    res = run_mysql(args, sql)
    if res.rc != 0 or not res.out:
        return None
    first = res.out.splitlines()[0].split("\t", 1)
    if len(first) != 2:
        return None
    return first[0], first[1]


def wait_for_process(args: argparse.Namespace, timeout: float = 20.0) -> tuple[str, str]:
    deadline = time.time() + timeout
    while time.time() < deadline:
        found = find_analyze_process(args)
        if found:
            return found
        time.sleep(0.2)
    raise TimeoutError("ANALYZE process did not appear in information_schema.processlist")


def snapshot(args: argparse.Namespace, label: str) -> None:
    jobs = run_mysql(
        args,
        f"""
        select id, partition_name, state, processed_rows, ifnull(process_id,0), ifnull(fail_reason,'')
        from mysql.analyze_jobs
        where table_schema = '{args.db}' and table_name = 't'
        order by id
        """,
    ).out
    meta = run_mysql(args, f"show stats_meta where db_name='{args.db}'").out
    hist = run_mysql(args, f"show stats_histograms where db_name='{args.db}'").out
    status = run_mysql(args, "show analyze status").out
    status = "\n".join(line for line in status.splitlines() if line.startswith(args.db + "\t"))
    print_block(f"{label}: analyze_jobs", jobs)
    print_block(f"{label}: show analyze status", status)
    print_block(f"{label}: stats_meta", meta)
    print_block(f"{label}: stats_histograms", hist)


def classify_after_interrupt(args: argparse.Namespace, analyze_rc: int, analyze_out: str, analyze_err: str) -> str:
    jobs = run_mysql(
        args,
        f"""
        select state, count(*)
        from mysql.analyze_jobs
        where table_schema = '{args.db}' and table_name = 't'
        group by state
        order by state
        """,
    ).out
    live = run_mysql(
        args,
        f"""
        select count(*)
        from mysql.analyze_jobs
        where table_schema = '{args.db}' and table_name = 't'
          and state in ('pending','running')
        """,
    ).out.strip()
    failed = "failed" in jobs.lower()
    finished = "finished" in jobs.lower()
    interrupted = analyze_rc != 0 or "interrupted" in (analyze_out + "\n" + analyze_err).lower()
    hist = run_mysql(args, f"show stats_histograms where db_name='{args.db}'").out
    has_stats = bool(hist.strip())

    if live and live != "0":
        return "RED_CANDIDATE: interrupted ANALYZE left pending/running analyze_jobs"
    if interrupted and finished and not failed:
        return "RED_CANDIDATE: client saw interruption but analyze_jobs only show finished"
    if interrupted and failed and has_stats:
        return "INFO_OR_WEAK_CANDIDATE: failed ANALYZE left some visible stats; needs user-harm oracle"
    if interrupted and failed and not has_stats:
        return "GREEN: interruption is visible as failed and no stats are exposed"
    return "INVALID_OR_NEEDS_RETRY: trigger did not produce a clean interrupted ANALYZE"


def run_probe(args: argparse.Namespace) -> int:
    clear_failpoint(args)
    setup_table(args)
    snapshot(args, "before")

    analyze_sql = "set @@tidb_analyze_partition_concurrency=1; set @@tidb_analyze_version=2; analyze table t"
    analyze_proc: subprocess.Popen[str] | None = None
    try:
        set_failpoint(args, args.failpoint_action)
        analyze_proc = subprocess.Popen(
            mysql_cmd(args, analyze_sql, args.db),
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        pid, info = wait_for_process(args)
        print_block("trigger: analyze process", f"id={pid}\n{info}")

        time.sleep(args.pause_wait)
        if analyze_proc.poll() is not None:
            out, err = analyze_proc.communicate()
            print_block("trigger: analyze completed before kill", f"rc={analyze_proc.returncode}\nstdout={out}\nstderr={err}")
            print("CLASSIFICATION: INVALID_OR_NEEDS_RETRY: failpoint did not hold ANALYZE")
            return 2

        snapshot(args, "while_paused_before_kill")
        kill = run_mysql(args, f"kill query {pid}")
        print_block("trigger: kill query", f"rc={kill.rc}\nstdout={kill.out}\nstderr={kill.err}")
        time.sleep(0.5)
        clear_failpoint(args)
        out, err = analyze_proc.communicate(timeout=30)
        print_block("trigger: analyze client result", f"rc={analyze_proc.returncode}\nstdout={out.strip()}\nstderr={err.strip()}")
        snapshot(args, "after_interrupt")
        classification = classify_after_interrupt(args, analyze_proc.returncode or 0, out, err)
        print(f"\nCLASSIFICATION: {classification}")

        clean = run_mysql(
            args,
            "set @@tidb_analyze_partition_concurrency=1; set @@tidb_analyze_version=2; analyze table t",
            args.db,
            timeout=120,
        )
        print_block("clean rerun", f"rc={clean.rc}\nstdout={clean.out}\nstderr={clean.err}")
        snapshot(args, "after_clean_rerun")
        return 0 if not classification.startswith("RED_CANDIDATE") else 1
    finally:
        clear_failpoint(args)
        if analyze_proc and analyze_proc.poll() is None:
            analyze_proc.terminate()
            try:
                analyze_proc.communicate(timeout=5)
            except subprocess.TimeoutExpired:
                analyze_proc.kill()
                analyze_proc.communicate()
        restore_global(args)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=14000)
    parser.add_argument("--user", default="root")
    parser.add_argument("--status-url", default="http://127.0.0.1:18080")
    parser.add_argument("--db", default="ai_perf_analyze_interrupt")
    parser.add_argument("--rows", type=int, default=20000)
    parser.add_argument("--pause-wait", type=float, default=2.0)
    parser.add_argument("--failpoint-action", default="2*off->pause")
    args = parser.parse_args()
    try:
        return run_probe(args)
    except Exception as exc:
        clear_failpoint(args)
        print(f"ERROR: {exc}", file=sys.stderr)
        return 3


if __name__ == "__main__":
    raise SystemExit(main())
