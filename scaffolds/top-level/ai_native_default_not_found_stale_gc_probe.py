#!/usr/bin/env python3
"""Probe autocommit stale-read GC ownership and the DefaultNotFound consequence.

The default owner-only mode is non-destructive apart from a dedicated database and
advancing tidb_external_ts. The accelerated window mode is for an isolated test
cluster only: it advances PD's global GC safe point and temporarily changes TiKV's
Write CF buffer size.
"""

from __future__ import annotations

import argparse
import dataclasses
import struct
import subprocess
import sys
import threading
import time

try:
    import pymysql
except ImportError as exc:  # pragma: no cover - environment preflight
    raise SystemExit("PyMySQL is required: python -m pip install pymysql") from exc


@dataclasses.dataclass
class WindowStats:
    green: int = 0
    wrong_result: int = 0
    default_not_found: int = 0
    other_error: int = 0
    first_error: str = ""


def connect(args: argparse.Namespace, database: str | None = None):
    return pymysql.connect(
        host=args.host,
        port=args.port,
        user=args.user,
        password=args.password,
        database=database,
        autocommit=True,
        charset="utf8mb4",
    )


def batches(total: int, size: int = 250):
    for start in range(1, total + 1, size):
        yield start, min(start + size, total + 1)


def prepare_matrix(args: argparse.Namespace) -> tuple[int, int]:
    conn = connect(args)
    cur = conn.cursor()
    cur.execute(f"DROP DATABASE IF EXISTS `{args.database}`")
    cur.execute(f"CREATE DATABASE `{args.database}`")
    cur.execute(f"USE `{args.database}`")
    cur.execute("CREATE TABLE t(id BIGINT PRIMARY KEY, v LONGTEXT, note VARCHAR(16))")

    value_a = "A" * args.value_size
    for start, end in batches(args.rows):
        cur.executemany(
            "INSERT INTO t VALUES(%s,%s,'A')",
            [(row_id, value_a) for row_id in range(start, end)],
        )

    cur.execute("START TRANSACTION")
    cur.execute("SELECT @@tidb_current_ts")
    snapshot_ts = int(cur.fetchone()[0])
    cur.execute(f"SET GLOBAL tidb_external_ts={snapshot_ts}")
    cur.execute("COMMIT")

    for start, end in batches(args.rows):
        cur.execute(
            "UPDATE t SET v=CONCAT('B',SUBSTRING(v,2)), note='B' "
            "WHERE id >= %s AND id < %s",
            (start, end),
        )

    cur.execute(
        "SELECT tidb_table_id FROM information_schema.tables "
        "WHERE table_schema=%s AND table_name='t'",
        (args.database,),
    )
    table_id = int(cur.fetchone()[0])
    conn.close()
    print(f"MATRIX_READY snapshot_ts={snapshot_ts} table_id={table_id} rows={args.rows}")
    return snapshot_ts, table_id


def verify_baselines(args: argparse.Namespace) -> None:
    conn = connect(args, args.database)
    cur = conn.cursor()
    cur.execute("SET tidb_enable_external_ts_read=ON")
    cur.execute("SELECT COUNT(*),MIN(LEFT(v,1)),MAX(LEFT(v,1)),MIN(note),MAX(note) FROM t")
    stale = cur.fetchone()
    cur.execute("SET tidb_enable_external_ts_read=OFF")
    cur.execute("SELECT COUNT(*),MIN(LEFT(v,1)),MAX(LEFT(v,1)),MIN(note),MAX(note) FROM t")
    current = cur.fetchone()
    conn.close()
    expected_stale = (args.rows, "A", "A", "A", "A")
    expected_current = (args.rows, "B", "B", "B", "B")
    if stale != expected_stale or current != expected_current:
        raise RuntimeError(
            f"invalid baseline: stale={stale!r}/{expected_stale!r}, "
            f"current={current!r}/{expected_current!r}"
        )
    print(f"BASELINE stale={stale!r} current={current!r}")


def probe_owner(args: argparse.Namespace, snapshot_ts: int) -> bool:
    marker = "ai-native-default-not-found-owner"
    result: dict[str, object] = {}

    def anchor() -> None:
        try:
            conn = connect(args, args.database)
            cur = conn.cursor()
            cur.execute("SET tidb_enable_external_ts_read=ON")
            cur.execute("SET cte_max_recursion_depth=2000")
            cur.execute("SET max_execution_time=30000")
            cur.execute(
                f"/* {marker} */ WITH RECURSIVE seq(n) AS "
                "(SELECT 0 UNION ALL SELECT n+1 FROM seq WHERE n<999) "
                "SELECT (SELECT LENGTH(v) FROM t WHERE id=1)+SLEEP(0.005) FROM seq"
            )
            rows = cur.fetchall()
            result["rows"] = len(rows)
            result["values"] = sorted({row[0] for row in rows})
            conn.close()
        except Exception as exc:  # pragma: no cover - reported to caller
            result["error"] = repr(exc)

    thread = threading.Thread(target=anchor)
    thread.start()
    txn_start = None
    deadline = time.time() + 4
    observer = connect(args)
    cur = observer.cursor()
    while time.time() < deadline:
        cur.execute(
            "SELECT txnstart FROM information_schema.processlist "
            "WHERE db=%s AND info LIKE %s LIMIT 1",
            (args.database, f"%{marker}%"),
        )
        row = cur.fetchone()
        if row is not None:
            txn_start = "" if row[0] is None else str(row[0])
            break
        time.sleep(0.05)
    observer.close()
    thread.join(timeout=15)
    if thread.is_alive() or "error" in result:
        raise RuntimeError(f"owner anchor failed: {result!r}")
    if txn_start is None:
        raise RuntimeError("owner anchor was not visible in processlist")

    registered = str(snapshot_ts) in txn_start
    label = "OWNER_GREEN" if registered else "OWNER_RED"
    print(
        f"{label} snapshot_ts={snapshot_ts} processlist_txnstart={txn_start!r} "
        f"anchor={result!r}"
    )
    return registered


def run_checked(command: list[str]) -> str:
    proc = subprocess.run(command, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    if proc.returncode != 0:
        raise RuntimeError(f"command failed rc={proc.returncode}: {command!r}\n{proc.stdout}")
    return proc.stdout.strip()


def set_write_buffer(args: argparse.Namespace, value: str) -> None:
    for host in args.tikv_host:
        run_checked(
            [
                args.tikv_ctl,
                "--host",
                host,
                "modify-tikv-config",
                "-n",
                "rocksdb.writecf.write-buffer-size",
                "-v",
                value,
            ]
        )
    print(f"WRITE_BUFFER value={value} stores={args.tikv_host!r}")


def memcomparable_bytes(raw: bytes) -> bytes:
    encoded = bytearray()
    offset = 0
    while True:
        group = raw[offset : offset + 8]
        offset += len(group)
        pad = 8 - len(group)
        encoded.extend(group)
        encoded.extend(b"\x00" * pad)
        encoded.append(0xFF - pad)
        if pad:
            break
    return bytes(encoded)


def table_data_prefix(table_id: int) -> bytes:
    raw = b"t" + struct.pack(">Q", table_id ^ (1 << 63))
    return b"z" + memcomparable_bytes(raw)


def escaped(data: bytes) -> str:
    out = []
    for byte in data:
        if 0x20 <= byte < 0x7F and byte != 0x5C:
            out.append(chr(byte))
        else:
            out.append(f"\\x{byte:02x}")
    return "".join(out)


def compact_table(args: argparse.Namespace, table_id: int) -> None:
    start = escaped(table_data_prefix(table_id))
    end = escaped(table_data_prefix(table_id + 1))
    output = run_checked(
        [
            args.tikv_ctl,
            "--pd",
            args.pd,
            "compact-cluster",
            "-d",
            "kv",
            "-c",
            "write",
            "-b",
            "force",
            "-n",
            "1",
            "-f",
            start,
            "-t",
            end,
        ]
    )
    print(f"COMPACTION range=[{start},{end}) output={output!r}")


def trigger_flush_writes(args: argparse.Namespace) -> None:
    conn = connect(args, args.database)
    cur = conn.cursor()
    for start, end in batches(args.rows):
        cur.execute("UPDATE t SET note='C' WHERE id >= %s AND id < %s", (start, end))
    conn.close()
    print(f"FLUSH_TRIGGER_WRITES rows={args.rows}")


def run_window(args: argparse.Namespace, table_id: int) -> WindowStats:
    stats = WindowStats()
    stats_lock = threading.Lock()
    stop = threading.Event()
    barrier = threading.Barrier(args.readers + 1)
    expected = (args.value_size, "A")
    key_count = min(args.point_keys, args.rows)

    def reader(index: int) -> None:
        conn = connect(args, args.database)
        cur = conn.cursor()
        cur.execute("SET tidb_enable_external_ts_read=ON")
        key = index % key_count + 1
        barrier.wait()
        while not stop.is_set():
            try:
                cur.execute("SELECT LENGTH(v),note FROM t WHERE id=%s", (key,))
                row = cur.fetchone()
                with stats_lock:
                    if row == expected:
                        stats.green += 1
                    else:
                        stats.wrong_result += 1
                        stats.first_error = stats.first_error or f"key={key} row={row!r}"
                        stop.set()
            except Exception as exc:  # exact text is the product-level oracle
                text = repr(exc)
                with stats_lock:
                    if "DefaultNotFound" in text or "default value not found" in text.lower():
                        stats.default_not_found += 1
                    else:
                        stats.other_error += 1
                    stats.first_error = stats.first_error or f"key={key} error={text}"
                    stop.set()
            key = key % key_count + 1
        conn.close()

    threads = [threading.Thread(target=reader, args=(index,), daemon=True) for index in range(args.readers)]
    for thread in threads:
        thread.start()
    barrier.wait()
    print(f"POINT_READERS_STARTED readers={args.readers} keys=1..{key_count}")

    buffer_changed = False
    try:
        output = run_checked([args.force_gc_safepoint, "-pd", args.pd])
        witness = next((line for line in output.splitlines() if line.startswith("old=")), output[-500:])
        print(f"GC_SAFEPOINT_FORCED {witness}")
        time.sleep(args.gc_poll_delay)
        set_write_buffer(args, args.write_buffer)
        buffer_changed = True
        trigger_flush_writes(args)
        time.sleep(args.compaction_delay)
        compact_table(args, table_id)

        deadline = time.time() + args.duration
        while time.time() < deadline and not stop.wait(0.5):
            pass
    finally:
        stop.set()
        for thread in threads:
            thread.join(timeout=5)
        if buffer_changed:
            set_write_buffer(args, args.restore_write_buffer)

    print(f"WINDOW_RESULT {dataclasses.asdict(stats)!r}")
    return stats


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=4000)
    parser.add_argument("--user", default="root")
    parser.add_argument("--password", default="")
    parser.add_argument("--database", default="ai_native_default_not_found_probe")
    parser.add_argument("--rows", type=int, default=20000)
    parser.add_argument("--value-size", type=int, default=4096)
    parser.add_argument("--execute-window", action="store_true")
    parser.add_argument("--force-gc-safepoint")
    parser.add_argument("--pd", default="127.0.0.1:2379")
    parser.add_argument("--tikv-ctl")
    parser.add_argument("--tikv-host", action="append", default=[])
    parser.add_argument("--readers", type=int, default=64)
    parser.add_argument("--point-keys", type=int, default=256)
    parser.add_argument("--duration", type=float, default=90)
    parser.add_argument("--gc-poll-delay", type=float, default=12)
    parser.add_argument("--compaction-delay", type=float, default=2)
    parser.add_argument("--write-buffer", default="1MB")
    parser.add_argument("--restore-write-buffer", default="128MB")
    args = parser.parse_args()
    if args.execute_window and (
        not args.force_gc_safepoint or not args.tikv_ctl or not args.tikv_host
    ):
        parser.error(
            "--execute-window requires --force-gc-safepoint, --tikv-ctl, and at least one --tikv-host"
        )
    return args


def main() -> int:
    args = parse_args()
    snapshot_ts, table_id = prepare_matrix(args)
    verify_baselines(args)
    registered = probe_owner(args, snapshot_ts)
    if not args.execute_window:
        return 0 if registered else 1
    if registered:
        print("SKIP window: master registered the stale-read TS; forcing PD would bypass the fixed owner")
        return 0

    stats = run_window(args, table_id)
    if stats.default_not_found:
        print(
            "FINDING\tstale_read_gc_default_not_found\t"
            f"green={stats.green} default_not_found={stats.default_not_found} "
            f"first={stats.first_error!r}"
        )
        return 1
    if stats.wrong_result:
        print(
            "FINDING\tstale_read_gc_wrong_result\t"
            f"green={stats.green} wrong_result={stats.wrong_result} first={stats.first_error!r}"
        )
        return 1
    print(f"INCONCLUSIVE no consequence observed: {dataclasses.asdict(stats)!r}")
    return 2


if __name__ == "__main__":
    sys.exit(main())
