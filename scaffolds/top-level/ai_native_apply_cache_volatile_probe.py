#!/usr/bin/env python3
"""Probe Apply cache reuse of volatile correlated subquery results.

Proof obligation:

    Apply cache may reuse inner results for the same correlated outer values
    only when the inner result is a pure function of those values. Volatile
    expressions such as UUID() must be evaluated for each subquery execution,
    not replayed from a cache entry keyed only by correlated columns.
"""

from __future__ import annotations

import argparse
import dataclasses
import re
import subprocess
import sys


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


def parse_group_counts(rows: list[str], prefix: str) -> dict[int, tuple[int, int]]:
    parsed: dict[int, tuple[int, int]] = {}
    for row in rows:
        parts = row.split("\t")
        if len(parts) < 4 or parts[0] != prefix:
            continue
        parsed[int(parts[1])] = (int(parts[2]), int(parts[3]))
    return parsed


def plan_has(plan: list[str], pattern: str) -> bool:
    return any(re.search(pattern, line, re.IGNORECASE) for line in plan)


def apply_cache_volatile_cell(args: argparse.Namespace) -> tuple[bool, str]:
    outer_rows = []
    row_id = 1
    for a, n in [(1, 24), (2, 16)]:
        for _ in range(n):
            outer_rows.append(f"({row_id},{a})")
            row_id += 1

    sql = f"""
DROP DATABASE IF EXISTS ai_apply_cache_probe;
CREATE DATABASE ai_apply_cache_probe;
USE ai_apply_cache_probe;
SET tidb_enable_parallel_apply = 1;
SET tidb_executor_concurrency = 1;
SET tidb_mem_quota_apply_cache = 33554432;
CREATE TABLE outer_t(id INT PRIMARY KEY, a INT, KEY(a));
CREATE TABLE inner_t(a INT, KEY(a));
INSERT INTO outer_t VALUES {",".join(outer_rows)};
INSERT INTO inner_t VALUES (1),(2);
ANALYZE TABLE outer_t, inner_t;

SELECT 'MARK','plan_on';
EXPLAIN ANALYZE
SELECT id, a,
       (SELECT UUID() FROM inner_t WHERE inner_t.a = outer_t.a LIMIT 1) AS u
FROM outer_t
ORDER BY id;
SELECT 'END','plan_on';

SELECT 'MARK','uuid_on';
SELECT 'ON_UUID', a, COUNT(*) AS n, COUNT(DISTINCT u) AS distinct_u
FROM (
  SELECT id, a,
         (SELECT UUID() FROM inner_t WHERE inner_t.a = outer_t.a LIMIT 1) AS u
  FROM outer_t
) AS x
GROUP BY a
ORDER BY a;
SELECT 'END','uuid_on';

SELECT 'MARK','det_on';
SELECT 'ON_DET', a, COUNT(*) AS n, COUNT(DISTINCT v) AS distinct_v
FROM (
  SELECT id, a,
         (SELECT CONCAT('v', inner_t.a) FROM inner_t WHERE inner_t.a = outer_t.a LIMIT 1) AS v
  FROM outer_t
) AS x
GROUP BY a
ORDER BY a;
SELECT 'END','det_on';

SET tidb_mem_quota_apply_cache = 0;

SELECT 'MARK','plan_off';
EXPLAIN ANALYZE
SELECT id, a,
       (SELECT UUID() FROM inner_t WHERE inner_t.a = outer_t.a LIMIT 1) AS u
FROM outer_t
ORDER BY id;
SELECT 'END','plan_off';

SELECT 'MARK','uuid_off';
SELECT 'OFF_UUID', a, COUNT(*) AS n, COUNT(DISTINCT u) AS distinct_u
FROM (
  SELECT id, a,
         (SELECT UUID() FROM inner_t WHERE inner_t.a = outer_t.a LIMIT 1) AS u
  FROM outer_t
) AS x
GROUP BY a
ORDER BY a;
SELECT 'END','uuid_off';

SELECT 'MARK','det_off';
SELECT 'OFF_DET', a, COUNT(*) AS n, COUNT(DISTINCT v) AS distinct_v
FROM (
  SELECT id, a,
         (SELECT CONCAT('v', inner_t.a) FROM inner_t WHERE inner_t.a = outer_t.a LIMIT 1) AS v
  FROM outer_t
) AS x
GROUP BY a
ORDER BY a;
SELECT 'END','det_off';
"""
    res = run_mysql(args, sql)
    if res.rc != 0:
        return False, "probe SQL failed: " + combined(res)

    sections = collect_sections(res.out)
    plan_on = sections.get("plan_on", [])
    plan_off = sections.get("plan_off", [])
    uuid_on = parse_group_counts(sections.get("uuid_on", []), "ON_UUID")
    uuid_off = parse_group_counts(sections.get("uuid_off", []), "OFF_UUID")
    det_on = parse_group_counts(sections.get("det_on", []), "ON_DET")
    det_off = parse_group_counts(sections.get("det_off", []), "OFF_DET")

    if not plan_has(plan_on, r"Apply_.*cache:ON"):
        return False, "INVALID cache-on plan did not trigger Apply cache: " + " | ".join(plan_on[:8])
    if not plan_has(plan_off, r"Apply_.*cache:OFF"):
        return False, "INVALID cache-off plan did not disable Apply cache: " + " | ".join(plan_off[:8])

    expected_groups = {1, 2}
    if set(uuid_on) != expected_groups or set(uuid_off) != expected_groups:
        return False, f"INVALID missing UUID groups: on={uuid_on!r}; off={uuid_off!r}"
    if set(det_on) != expected_groups or set(det_off) != expected_groups:
        return False, f"INVALID missing deterministic groups: on={det_on!r}; off={det_off!r}"

    bad_on = {a: counts for a, counts in uuid_on.items() if counts[1] < counts[0]}
    good_off = all(distinct_u == n for n, distinct_u in uuid_off.values())
    det_control = all(distinct_v == 1 and n > 1 for n, distinct_v in det_on.values()) and all(
        distinct_v == 1 and n > 1 for n, distinct_v in det_off.values()
    )

    if bad_on and good_off and det_control:
        detail = (
            "Apply cache ON reused UUID() results for duplicate correlated keys: "
            f"uuid_on={uuid_on!r}; uuid_off={uuid_off!r}; "
            f"det_on={det_on!r}; det_off={det_off!r}"
        )
        return False, detail

    if not good_off:
        return False, f"INVALID cache-off UUID control collided or failed: {uuid_off!r}"
    if not det_control:
        return False, f"INVALID deterministic control failed: det_on={det_on!r}; det_off={det_off!r}"

    return True, (
        "Apply cache UUID result matched cache-off reference; "
        f"uuid_on={uuid_on!r}; uuid_off={uuid_off!r}"
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mysql", default="mysql")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", default="14000")
    parser.add_argument("--user", default="root")
    args = parser.parse_args()

    ok, detail = apply_cache_volatile_cell(args)
    if ok:
        print("OK\tapply_cache_volatile\t" + detail.replace("\n", " ")[:1200])
        print("SUMMARY total=1 findings=0 skipped=0")
        return 0
    prefix = "INVALID" if detail.startswith("INVALID") or detail.startswith("probe SQL failed") else "FINDING"
    print(prefix + "\tapply_cache_volatile\t" + detail.replace("\n", " ")[:1200])
    if prefix == "FINDING":
        print("SUMMARY total=1 findings=1 skipped=0")
        return 1
    print("SUMMARY total=1 findings=0 skipped=1")
    return 2


if __name__ == "__main__":
    sys.exit(main())
