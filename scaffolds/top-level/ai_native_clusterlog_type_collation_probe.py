#!/usr/bin/env python3
"""cluster diagnostic table `type` collation probe (methodology v2, selector S3).

Proof obligation (collation sub-family of S3):
    extractCol(valueToLower=true) lowercases the user's equality/IN value and drops
    the original predicate (no scalar recheck), while the column is declared
    utf8mb4_bin (case-sensitive). So `type='PD'` matches 'pd' rows, violating the
    column's SQL-visible collation.

Source:
    pkg/planner/core/memtable_predicate_extractor.go extractCol (:292) drops the
    matched EQ/IN/OR predicate from `remained`; merge (:274) lowercases when
    valueToLower. `type` is passed true by ClusterTableExtractor:737,
    ClusterLogTableExtractor:805, InspectionRuleTableExtractor:1271, but FALSE by
    HotRegionsHistoryTableExtractor:942 — same column name, inconsistent case rule.

Classification: INFO (contract-ambiguous). Technically a bin-collation violation,
    but case-insensitive matching of fixed enum values may be intended. Evidence
    for owner ruling, not a confirmed finding. Oracle: LIKE is NOT consumed by
    extractCol, so `col LIKE 'PD'` is the case-sensitive scalar reference.
"""

from __future__ import annotations

import argparse
import subprocess


def q(args, sql):
    p = subprocess.run(
        [args.mysql, f"-h{args.host}", f"-P{args.port}", f"-u{args.user}",
         "--batch", "--raw", "--skip-column-names", "--connect-timeout=5", "-e", sql],
        text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    return p.returncode, p.stdout.strip(), p.stderr.strip()


def scalar(args, sql):
    rc, out, err = q(args, "SET time_zone='+00:00';" + sql)
    if rc != 0:
        raise RuntimeError(err)
    return out.splitlines()[-1] if out else ""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--mysql", default="mysql")
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", default="14000")
    ap.add_argument("--user", default="root")
    ap.add_argument("--lo", default="2026-07-02 13:00:00")
    ap.add_argument("--hi", default="2026-07-02 13:05:00")
    args = ap.parse_args()

    _, ver, _ = q(args, "SELECT VERSION()")
    coll = scalar(args, "SELECT COLLATION_NAME FROM information_schema.columns "
                        "WHERE TABLE_SCHEMA='information_schema' AND TABLE_NAME='CLUSTER_LOG' "
                        "AND COLUMN_NAME='TYPE'")
    print(f"FINGERPRINT version={ver} cluster_log.type collation={coll}")

    win = (f"message LIKE '%' AND time>='{args.lo}' AND time<='{args.hi}'")
    lowers = scalar(args, f"SELECT COUNT(*) FROM information_schema.cluster_log WHERE type='pd' AND {win}")
    upper_eq = scalar(args, f"SELECT COUNT(*) FROM information_schema.cluster_log WHERE type='PD' AND {win}")
    upper_like = scalar(args, f"SELECT COUNT(*) FROM information_schema.cluster_log WHERE type LIKE 'PD' AND {win}")
    ret_types = scalar(args, f"SELECT GROUP_CONCAT(DISTINCT type) FROM information_schema.cluster_log WHERE type='PD' AND {win}")

    lowers, upper_eq, upper_like = int(lowers or 0), int(upper_eq or 0), int(upper_like or 0)
    print(f"DATA collation={coll}  eq_'pd'(control)={lowers}  "
          f"eq_'PD'(extractor)={upper_eq}  LIKE_'PD'(scalar_ref)={upper_like}  "
          f"types_returned_for_'PD'=[{ret_types}]")

    if lowers == 0:
        print("SKIP(capability)\tcluster_log_type_collation\tno pd rows in window; widen --lo/--hi")
        print("SUMMARY total=1 findings=0 skipped=1")
        return 0

    # bin collation => 'PD' should match 0; extractor returns lowercase rows.
    violation = coll == "utf8mb4_bin" and upper_eq > 0 and upper_like == 0
    if violation:
        print("INFO(contract-ambiguous)\tcluster_log_type_collation\t"
              f"type='PD' returns {upper_eq} rows (all type='pd') under utf8mb4_bin; "
              f"case-sensitive LIKE reference returns 0. extractCol(valueToLower=true) "
              f"overrides column collation and drops the predicate. Owner must rule: "
              f"should diagnostic enum columns honor declared bin collation? "
              f"Cross-extractor inconsistency (type: true x3, false x1) is evidence it is not uniform design.")
        print("SUMMARY total=1 findings=0 info=1 skipped=0")
    else:
        print("GREEN(triggered)\tcluster_log_type_collation\t"
              f"case-sensitive semantics respected (eq_'PD'={upper_eq}, LIKE_'PD'={upper_like})")
        print("SUMMARY total=1 findings=0 skipped=0")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
