#!/usr/bin/env python3
"""Minimal asset store for validating incremental AI-native fuzz loops."""

from __future__ import annotations

import argparse
import hashlib
import json
import sqlite3
import re
from collections import Counter, defaultdict
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parent
DEFAULT_DB = ROOT / "assets.sqlite3"
SCHEMA = ROOT / "schema.sql"


ADMISSION_C3_DIRECT = "C3_DIRECT"
ADMISSION_C2_WITH_LIFT = "C2_WITH_LIFT"
ADMISSION_NOT_ADMITTED = "NOT_ADMITTED"
ADMISSION_RANK = {
    ADMISSION_C3_DIRECT: 2,
    ADMISSION_C2_WITH_LIFT: 1,
    ADMISSION_NOT_ADMITTED: 0,
}


def canonical(value: Any) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True)


def slug(value: str, max_len: int = 72) -> str:
    cleaned = re.sub(r"[^a-z0-9]+", "-", value.lower()).strip("-")
    return (cleaned or "item")[:max_len].rstrip("-")


def connect(path: Path) -> sqlite3.Connection:
    conn = sqlite3.connect(path)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA foreign_keys = ON")
    return conn


def init_db(path: Path) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with connect(path) as conn:
        conn.executescript(SCHEMA.read_text())


def asset_hash(record: dict[str, Any]) -> str:
    body = {
        key: record.get(key)
        for key in (
            "asset_key",
            "asset_type",
            "name",
            "module",
            "selector",
            "lifecycle_status",
            "trust_level",
            "payload",
            "provenance",
        )
    }
    return hashlib.sha256(canonical(body).encode()).hexdigest()


def import_asset(conn: sqlite3.Connection, record: dict[str, Any]) -> None:
    digest = asset_hash(record)
    payload = canonical(record.get("payload", {}))
    provenance = canonical(record.get("provenance", {}))
    conn.execute(
        """
        INSERT INTO asset(
            asset_key, asset_type, name, module, selector, lifecycle_status,
            trust_level, payload_json, provenance_json, content_hash
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(asset_key) DO UPDATE SET
            asset_type = excluded.asset_type,
            name = excluded.name,
            module = excluded.module,
            selector = excluded.selector,
            lifecycle_status = excluded.lifecycle_status,
            trust_level = excluded.trust_level,
            payload_json = excluded.payload_json,
            provenance_json = excluded.provenance_json,
            content_hash = excluded.content_hash,
            updated_at = CASE
                WHEN asset.content_hash != excluded.content_hash THEN CURRENT_TIMESTAMP
                ELSE asset.updated_at
            END
        """,
        (
            record["asset_key"],
            record["asset_type"],
            record["name"],
            record["module"],
            record.get("selector"),
            record.get("lifecycle_status", "candidate"),
            record.get("trust_level", "hypothesis"),
            payload,
            provenance,
            digest,
        ),
    )
    conn.execute(
        """
        INSERT OR IGNORE INTO asset_revision(
            asset_key, content_hash, payload_json, provenance_json
        ) VALUES (?, ?, ?, ?)
        """,
        (record["asset_key"], digest, payload, provenance),
    )


def import_link(conn: sqlite3.Connection, record: dict[str, Any]) -> None:
    conn.execute(
        """
        INSERT INTO asset_link(source_key, target_key, relation, rationale)
        VALUES (?, ?, ?, ?)
        ON CONFLICT(source_key, target_key, relation) DO UPDATE SET
            rationale = excluded.rationale
        """,
        (
            record["source_key"],
            record["target_key"],
            record["relation"],
            record.get("rationale"),
        ),
    )


def import_run(conn: sqlite3.Connection, record: dict[str, Any]) -> None:
    conn.execute(
        """
        INSERT INTO run_result(
            run_key, obligation_key, verdict, code_ref_json, environment_json,
            evidence_json, lessons_json
        ) VALUES (?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(run_key) DO UPDATE SET
            verdict = excluded.verdict,
            code_ref_json = excluded.code_ref_json,
            environment_json = excluded.environment_json,
            evidence_json = excluded.evidence_json,
            lessons_json = excluded.lessons_json
        """,
        (
            record["run_key"],
            record["obligation_key"],
            record["verdict"],
            canonical(record.get("code_ref", {})),
            canonical(record.get("environment", {})),
            canonical(record.get("evidence", {})),
            canonical(record.get("lessons", {})),
        ),
    )
    conn.execute("DELETE FROM run_asset WHERE run_key = ?", (record["run_key"],))
    for item in record.get("used_assets", []):
        conn.execute(
            "INSERT INTO run_asset(run_key, asset_key, role) VALUES (?, ?, ?)",
            (record["run_key"], item["asset_key"], item["role"]),
        )


def import_target(conn: sqlite3.Connection, record: dict[str, Any]) -> None:
    payload = dict(record.get("payload", {}))
    if "admission" in record:
        payload["admission"] = record["admission"]
    conn.execute(
        """
        INSERT INTO target_queue(
            target_key, title, module, selector, status, discoverability,
            obligation_class, priority, consequence, effort, uncertainty,
            payload_json, provenance_json
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(target_key) DO UPDATE SET
            title = excluded.title,
            module = excluded.module,
            selector = excluded.selector,
            status = excluded.status,
            discoverability = excluded.discoverability,
            obligation_class = excluded.obligation_class,
            priority = excluded.priority,
            consequence = excluded.consequence,
            effort = excluded.effort,
            uncertainty = excluded.uncertainty,
            payload_json = excluded.payload_json,
            provenance_json = excluded.provenance_json,
            updated_at = CURRENT_TIMESTAMP
        """,
        (
            record["target_key"],
            record["title"],
            record["module"],
            record["selector"],
            record.get("status", "candidate"),
            record["discoverability"],
            record["obligation_class"],
            int(record.get("priority", 0)),
            int(record.get("consequence", 1)),
            int(record.get("effort", 5)),
            int(record.get("uncertainty", 5)),
            canonical(payload),
            canonical(record.get("provenance", {})),
        ),
    )


def import_jsonl(path: Path, source: Path) -> Counter[str]:
    init_db(path)
    records = []
    for lineno, raw in enumerate(source.read_text().splitlines(), 1):
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue
        try:
            records.append(json.loads(raw))
        except json.JSONDecodeError as exc:
            raise SystemExit(f"{source}:{lineno}: {exc}") from exc

    counts: Counter[str] = Counter()
    order = {"target": 0, "asset": 1, "link": 2, "run": 3}
    records.sort(key=lambda item: order.get(item.get("record_type", "asset"), 99))
    with connect(path) as conn:
        for record in records:
            record_type = record.get("record_type", "asset")
            if record_type == "asset":
                import_asset(conn, record)
            elif record_type == "link":
                import_link(conn, record)
            elif record_type == "run":
                import_run(conn, record)
            elif record_type == "target":
                import_target(conn, record)
            else:
                raise SystemExit(f"unsupported record_type: {record_type}")
            counts[record_type] += 1
    return counts


def decode_asset(row: sqlite3.Row) -> dict[str, Any]:
    return {
        "asset_key": row["asset_key"],
        "asset_type": row["asset_type"],
        "name": row["name"],
        "module": row["module"],
        "selector": row["selector"],
        "lifecycle_status": row["lifecycle_status"],
        "trust_level": row["trust_level"],
        "payload": json.loads(row["payload_json"]),
        "provenance": json.loads(row["provenance_json"]),
        "content_hash": row["content_hash"],
    }


def build_pack(path: Path, module: str, selector: str) -> dict[str, Any]:
    with connect(path) as conn:
        targets = conn.execute(
            """
            SELECT * FROM asset
            WHERE asset_type = 'obligation' AND module = ? AND selector = ?
              AND lifecycle_status != 'retired'
            ORDER BY updated_at DESC, asset_key
            """,
            (module, selector),
        ).fetchall()
        if not targets:
            raise SystemExit(f"no obligation for module={module!r}, selector={selector!r}")

        target_keys = [row["asset_key"] for row in targets]
        placeholders = ",".join("?" for _ in target_keys)
        links = conn.execute(
            f"""
            SELECT * FROM asset_link
            WHERE source_key IN ({placeholders}) OR target_key IN ({placeholders})
            ORDER BY source_key, relation, target_key
            """,
            (*target_keys, *target_keys),
        ).fetchall()
        linked_keys = set(target_keys)
        for link in links:
            linked_keys.add(link["source_key"])
            linked_keys.add(link["target_key"])
        module_profiles = conn.execute(
            """
            SELECT asset_key FROM asset
            WHERE asset_type = 'module_profile' AND module = ?
              AND lifecycle_status != 'retired'
            """,
            (module,),
        ).fetchall()
        linked_keys.update(row["asset_key"] for row in module_profiles)

        asset_placeholders = ",".join("?" for _ in linked_keys)
        assets = conn.execute(
            f"SELECT * FROM asset WHERE asset_key IN ({asset_placeholders}) ORDER BY asset_type, asset_key",
            tuple(sorted(linked_keys)),
        ).fetchall()
        decoded = [decode_asset(row) for row in assets]
        by_type: dict[str, list[dict[str, Any]]] = defaultdict(list)
        source_kinds: Counter[str] = Counter()
        for asset in decoded:
            by_type[asset["asset_type"]].append(asset)
            source_kinds[asset["provenance"].get("source_kind", "unknown")] += 1

        required = {"selector", "oracle", "scenario", "fault_point", "schedule_template"}
        present = set(by_type)
        runs = conn.execute(
            f"""
            SELECT * FROM run_result
            WHERE obligation_key IN ({placeholders})
            ORDER BY created_at, run_key
            """,
            tuple(target_keys),
        ).fetchall()

    return {
        "query": {"module": module, "selector": selector},
        "targets": target_keys,
        "assets_by_type": dict(by_type),
        "links": [dict(row) for row in links],
        "open_gaps": sorted(required - present),
        "reuse_summary": {
            "asset_count": len(decoded),
            "by_source_kind": dict(source_kinds),
        },
        "prior_runs": [
            {
                "run_key": row["run_key"],
                "verdict": row["verdict"],
                "code_ref": json.loads(row["code_ref_json"]),
                "environment": json.loads(row["environment_json"]),
                "evidence": json.loads(row["evidence_json"]),
                "lessons": json.loads(row["lessons_json"]),
            }
            for row in runs
        ],
    }


def decode_target(row: sqlite3.Row) -> dict[str, Any]:
    payload = json.loads(row["payload_json"])
    return {
        "target_key": row["target_key"],
        "title": row["title"],
        "module": row["module"],
        "selector": row["selector"],
        "status": row["status"],
        "discoverability": row["discoverability"],
        "obligation_class": row["obligation_class"],
        "priority": row["priority"],
        "consequence": row["consequence"],
        "effort": row["effort"],
        "uncertainty": row["uncertainty"],
        "payload": payload,
        "provenance": json.loads(row["provenance_json"]),
    }


def severity_admission(target: dict[str, Any]) -> dict[str, Any]:
    """Decide whether a target may consume a severity-focused MINE_BUG slot."""
    payload = target.get("payload", {})
    admission = payload.get("admission", ADMISSION_NOT_ADMITTED)
    consequence = int(target["consequence"])

    if admission not in ADMISSION_RANK:
        return {
            "admission": ADMISSION_NOT_ADMITTED,
            "eligible_for_mine_bug": False,
            "reason": f"invalid admission value: {admission!r}",
        }
    if admission == ADMISSION_C3_DIRECT:
        if consequence != 3:
            return {
                "admission": ADMISSION_NOT_ADMITTED,
                "eligible_for_mine_bug": False,
                "reason": "C3_DIRECT requires consequence=3",
            }
        if not payload.get("severity_oracle"):
            return {
                "admission": ADMISSION_NOT_ADMITTED,
                "eligible_for_mine_bug": False,
                "reason": "C3_DIRECT requires a named severity_oracle",
            }
        return {
            "admission": ADMISSION_C3_DIRECT,
            "eligible_for_mine_bug": True,
            "reason": "direct C3 consequence and oracle are declared",
        }
    if admission == ADMISSION_C2_WITH_LIFT:
        if consequence != 2:
            return {
                "admission": ADMISSION_NOT_ADMITTED,
                "eligible_for_mine_bug": False,
                "reason": "C2_WITH_LIFT requires consequence=2",
            }
        if not payload.get("severity_oracle") or not payload.get("c3_lift_oracle"):
            return {
                "admission": ADMISSION_NOT_ADMITTED,
                "eligible_for_mine_bug": False,
                "reason": "C2_WITH_LIFT requires severity_oracle and c3_lift_oracle",
            }
        return {
            "admission": ADMISSION_C2_WITH_LIFT,
            "eligible_for_mine_bug": True,
            "reason": "bounded C2 investigation has a named C3 lift oracle",
        }
    return {
        "admission": ADMISSION_NOT_ADMITTED,
        "eligible_for_mine_bug": False,
        "reason": "target has no declared C3 consequence or bounded C3 lift",
    }


def target_asset_state(conn: sqlite3.Connection, target: dict[str, Any]) -> dict[str, Any]:
    admission = severity_admission(target)
    requested_obligation = target.get("payload", {}).get("obligation_key")
    needs_new_obligation = bool(target.get("payload", {}).get("broad_oracle")) and not requested_obligation
    rows = conn.execute(
        """
        SELECT asset_key, asset_type, module, provenance_json FROM asset
        WHERE lifecycle_status != 'retired'
          AND (selector = ? OR module = ?)
        """,
        (target["selector"], target["module"]),
    ).fetchall()
    general_required = {"selector", "oracle", "scenario", "schedule_template"}
    target_required = {"module_profile", "obligation", "fault_point"}
    general_present = set()
    target_present = set()
    source_kinds: Counter[str] = Counter()
    for row in rows:
        source_kinds[json.loads(row["provenance_json"]).get("source_kind", "unknown")] += 1
        if row["module"] == "shared" and row["asset_type"] in general_required:
            general_present.add(row["asset_type"])
        if row["module"] == target["module"] and row["asset_type"] in target_required:
            if row["asset_type"] == "obligation" and needs_new_obligation:
                continue
            if (
                row["asset_type"] == "obligation"
                and requested_obligation
                and row["asset_key"] != requested_obligation
            ):
                continue
            target_present.add(row["asset_type"])

    if needs_new_obligation:
        obligations = []
    elif requested_obligation:
        obligations = conn.execute(
            """
            SELECT asset_key FROM asset
            WHERE asset_key = ? AND asset_type = 'obligation'
              AND module = ? AND selector = ?
              AND lifecycle_status != 'retired'
            ORDER BY asset_key
            """,
            (requested_obligation, target["module"], target["selector"]),
        ).fetchall()
    else:
        obligations = conn.execute(
            """
            SELECT asset_key FROM asset
            WHERE asset_type = 'obligation' AND module = ? AND selector = ?
              AND lifecycle_status != 'retired'
            ORDER BY asset_key
            """,
            (target["module"], target["selector"]),
        ).fetchall()
    obligation_keys = [row["asset_key"] for row in obligations]
    verdicts: Counter[str] = Counter()
    if obligation_keys:
        placeholders = ",".join("?" for _ in obligation_keys)
        run_rows = conn.execute(
            f"SELECT verdict, COUNT(*) AS n FROM run_result WHERE obligation_key IN ({placeholders}) GROUP BY verdict",
            tuple(obligation_keys),
        ).fetchall()
        verdicts.update({row["verdict"]: row["n"] for row in run_rows})

    missing_general = sorted(general_required - general_present)
    missing_target = sorted(target_required - target_present)
    if target["status"] in {"validated", "retired", "blocked"}:
        next_state = target["status"]
    elif missing_general:
        next_state = "needs_method_assets"
    elif missing_target:
        next_state = "needs_target_analysis"
    elif not verdicts:
        next_state = "ready_to_execute"
    elif verdicts.get("RED") and verdicts.get("GREEN"):
        next_state = "validated"
    else:
        next_state = "needs_counterpart_run"

    missing_count = len(missing_general) + len(missing_target)
    reuse_count = source_kinds.get("methodology_asset", 0)
    score = (
        target["priority"] * 100
        + target["consequence"] * 20
        - target["effort"] * 5
        - target["uncertainty"] * 2
        + reuse_count * 3
        - missing_count * 15
    )
    return {
        "next_state": next_state,
        "score": score,
        **admission,
        "missing_general_assets": missing_general,
        "missing_target_assets": missing_target,
        "obligation_keys": obligation_keys,
        "runs": dict(verdicts),
        "asset_source_kinds": dict(source_kinds),
    }


def queue(path: Path, include_done: bool = False) -> dict[str, Any]:
    init_db(path)
    with connect(path) as conn:
        rows = conn.execute(
            """
            SELECT * FROM target_queue
            ORDER BY priority DESC, consequence DESC, effort ASC, target_key
            """
        ).fetchall()
        targets = []
        for row in rows:
            target = decode_target(row)
            state = target_asset_state(conn, target)
            if not include_done and state["next_state"] in {"validated", "retired"}:
                continue
            target["state"] = state
            targets.append(target)
    return {"targets": targets}


def next_target(path: Path) -> dict[str, Any]:
    queued = queue(path, include_done=False)["targets"]
    active = [
        item
        for item in queued
        if item["state"]["next_state"] not in {"blocked", "retired"}
        and item["state"]["eligible_for_mine_bug"]
    ]
    if not active:
        unadmitted = [
            {
                "target_key": item["target_key"],
                "consequence": item["consequence"],
                "reason": item["state"]["reason"],
            }
            for item in queued
            if item["state"]["next_state"] not in {"blocked", "retired"}
        ]
        return {
            "next": None,
            "reason": "no severity-admitted targets",
            "unadmitted_active_targets": unadmitted,
        }
    active.sort(
        key=lambda item: (
            -ADMISSION_RANK[item["state"]["admission"]],
            -item["consequence"],
            -item["state"]["score"],
            item["effort"],
            item["target_key"],
        )
    )
    return {"next": active[0], "alternates": active[1:4]}


def choose_refill_discoverability(oracle_key: str, base_target: dict[str, Any]) -> str:
    if base_target["discoverability"] in {"CLUSTER_TOPOLOGY", "STRESS_PERF"}:
        return base_target["discoverability"]
    if any(token in oracle_key for token in ("topology", "owner", "handoff")):
        return "CLUSTER_TOPOLOGY"
    if any(token in oracle_key for token in ("error", "leak", "cancel", "stale", "gc")):
        return "FAULT_INJECTION"
    return base_target["discoverability"]


def build_refill_target(oracle: dict[str, Any], obligation: dict[str, Any], base_target: dict[str, Any]) -> dict[str, Any]:
    oracle_key = oracle["asset_key"]
    obligation_key = obligation["asset_key"]
    target_key = (
        "target.refill."
        f"{slug(base_target['target_key'], 64)}."
        f"{slug(oracle_key, 48)}.v1"
    )
    return {
        "record_type": "target",
        "target_key": target_key,
        "title": f"Refill {oracle['name']} from {base_target['title']}",
        "module": obligation["module"],
        "selector": obligation["selector"],
        "status": "candidate",
        "discoverability": choose_refill_discoverability(oracle_key, base_target),
        "obligation_class": f"{base_target['obligation_class']}-REFILL",
        "priority": max(10, int(base_target["priority"]) - 12),
        "consequence": int(base_target["consequence"]),
        "effort": min(10, int(base_target["effort"]) + 2),
        "uncertainty": min(10, int(base_target["uncertainty"]) + 2),
        "payload": {
            "admission": ADMISSION_NOT_ADMITTED,
            "base_target": base_target["target_key"],
            "base_obligation": obligation_key,
            "broad_oracle": oracle_key,
            "expected_next_step": (
                "derive a new concrete P/Q/F obligation or live-lift schedule from this broad oracle; "
                "then add module-specific obligation/fault assets before execution"
            ),
            "stop_rule": (
                "do not execute until a target-specific obligation_key and fault/observer are added; "
                "record INVALID(harness) if the required observer or hold point is unavailable"
            ),
        },
        "provenance": {
            "source_kind": "refill_candidate",
            "source": "store.py refill from oracle debt",
            "introduced_for": "asset-store-refill",
        },
    }


def is_recursive_refill_base(target: dict[str, Any]) -> bool:
    return (
        target["target_key"].startswith("target.refill.")
        or "REFILL" in target.get("obligation_class", "")
        or target.get("provenance", {}).get("source_kind") == "refill_candidate"
    )


def refill(path: Path, limit: int = 5, include_covered: bool = False, jsonl_output: Path | None = None) -> dict[str, Any]:
    init_db(path)
    with connect(path) as conn:
        target_rows = conn.execute(
            "SELECT * FROM target_queue ORDER BY updated_at DESC, target_key"
        ).fetchall()
        targets = [decode_target(row) for row in target_rows]
        covered_oracles = {
            target["payload"].get("broad_oracle")
            for target in targets
            if target["payload"].get("broad_oracle") and target["status"] != "retired"
        }
        targets_by_obligation: dict[str, list[dict[str, Any]]] = defaultdict(list)
        for target in targets:
            obligation_key = target["payload"].get("obligation_key")
            if obligation_key and target["status"] == "validated":
                targets_by_obligation[obligation_key].append(target)

        oracle_rows = conn.execute(
            """
            SELECT * FROM asset
            WHERE asset_type = 'oracle'
              AND lifecycle_status != 'retired'
              AND trust_level NOT IN ('execution_verified', 'trusted')
            ORDER BY updated_at DESC, asset_key
            """
        ).fetchall()

        candidates: list[dict[str, Any]] = []
        skipped: list[dict[str, Any]] = []
        for oracle_row in oracle_rows:
            oracle = decode_asset(oracle_row)
            oracle_key = oracle["asset_key"]
            if not include_covered and oracle_key in covered_oracles:
                skipped.append({"oracle_key": oracle_key, "reason": "already_has_refill_target"})
                continue

            obligation_rows = conn.execute(
                """
                SELECT a.* FROM asset_link AS l
                JOIN asset AS a ON a.asset_key = l.source_key
                WHERE l.target_key = ?
                  AND l.relation = 'judged_by'
                  AND a.asset_type = 'obligation'
                  AND a.lifecycle_status = 'validated'
                ORDER BY a.updated_at DESC, a.asset_key
                """,
                (oracle_key,),
            ).fetchall()
            if not obligation_rows:
                skipped.append({"oracle_key": oracle_key, "reason": "no_validated_source_obligation"})
                continue

            built_for_oracle = False
            saw_recursive_refill_base = False
            for obligation_row in obligation_rows:
                obligation = decode_asset(obligation_row)
                all_base_targets = targets_by_obligation.get(obligation["asset_key"], [])
                base_targets = [
                    target for target in all_base_targets if not is_recursive_refill_base(target)
                ]
                if not base_targets:
                    if all_base_targets:
                        saw_recursive_refill_base = True
                        skipped.append(
                            {
                                "oracle_key": oracle_key,
                                "reason": "recursive_refill_base_only",
                                "obligation_key": obligation["asset_key"],
                                "base_targets": [target["target_key"] for target in all_base_targets],
                            }
                        )
                    continue
                candidate = build_refill_target(oracle, obligation, base_targets[0])
                existing = conn.execute(
                    "SELECT 1 FROM target_queue WHERE target_key = ?",
                    (candidate["target_key"],),
                ).fetchone()
                if existing:
                    skipped.append(
                        {
                            "oracle_key": oracle_key,
                            "reason": "candidate_key_exists",
                            "target_key": candidate["target_key"],
                        }
                    )
                    built_for_oracle = True
                    break
                candidates.append(candidate)
                built_for_oracle = True
                break
            if not built_for_oracle:
                if not saw_recursive_refill_base:
                    skipped.append({"oracle_key": oracle_key, "reason": "no_validated_base_target"})
            if len(candidates) >= limit:
                break

    if jsonl_output is not None:
        jsonl_output.parent.mkdir(parents=True, exist_ok=True)
        jsonl_output.write_text(
            "".join(canonical(candidate) + "\n" for candidate in candidates)
        )

    return {
        "candidates": candidates,
        "skipped": skipped,
        "jsonl_output": str(jsonl_output) if jsonl_output else None,
    }


IDENTITY_TOKEN_SOURCE_SEEDS = [
    {
        "name": "DDL owner epoch result token",
        "target_key": "target.source.ddl-owner-epoch-token.v1",
        "covered_asset_key": "obligation.ddl-job-scheduler.owner-epoch-token-unique-across-handoff.v1",
        "module": "ddl/job-scheduler",
        "title": "SOURCE_TARGETS: DDL owner epoch token equality gates reorg result",
        "source_paths": ["pkg/ddl/job_scheduler.go", "pkg/ddl/reorg.go", "pkg/ddl/ddl.go"],
        "markers": ["ownerTS", "OnBecomeOwner", "time.Now().Unix()", "res.ownerTS != curTS"],
        "consequence": 3,
        "priority": 74,
        "payload": {
            "expected_shape": "owner epoch token is minted at owner handoff and later gates async reorg result acceptance",
            "known_result": "validated via issue51846 refill owner epoch RED/GREEN",
        },
    },
    {
        "name": "BR registry heartbeat stale-task token",
        "target_key": "target.source.br-registry-heartbeat-token-precision.v1",
        "covered_asset_key": "obligation.br-restore-registry.heartbeat-token-precision.v1",
        "module": "br/registry",
        "title": "SOURCE_TARGETS screen: BR registry heartbeat timestamp as stale-task token",
        "source_paths": ["br/pkg/registry/registration.go", "br/pkg/registry/heartbeat.go"],
        "markers": ["last_heartbeat_time", "isTaskStale", "time.Now().UTC().Unix()", "expectedHeartbeatTimestamp"],
        "consequence": 2,
        "priority": 55,
        "payload": {
            "expected_shape": "heartbeat token equality gates stale-task transition",
            "known_result": "retired via INVALID(schedule-proof); default cadence does not prove collision",
        },
    },
    {
        "name": "BR storewatch same-second reboot token",
        "target_key": "target.source.br-storewatch-same-second-reboot.v1",
        "covered_asset_key": "obligation.br-storewatch.reboot-notified-after-offline-up-same-token.v1",
        "module": "br/storewatch",
        "title": "SOURCE_TARGETS: BR storewatch must notify reboot after same-second Offline->Up restart",
        "source_paths": ["br/pkg/utils/storewatch/watching.go", "br/pkg/backup/store.go", "br/pkg/restore/data/data.go"],
        "markers": ["StartTimestamp", "OnReboot", "OnDisconnect"],
        "consequence": 2,
        "priority": 72,
        "payload": {
            "expected_shape": "store lifecycle token equality gates reboot callback",
            "known_result": "validated via storewatch same-second reboot RED/GREEN",
        },
    },
    {
        "name": "TiFlash MPP logical core cache StartTimestamp token",
        "target_key": "target.source.tiflash-mpp-logical-core-starttimestamp.v1",
        "covered_asset_key": "obligation.planner-tiflash-mpp.logical-core-cache-starttimestamp.v1",
        "module": "planner/tiflash-mpp-cache",
        "title": "SOURCE_TARGETS: TiFlash MPP logical core cache must refresh after same-second restart/config change",
        "source_paths": [
            "pkg/planner/core/optimizer.go",
            "pkg/domain/infosync/tiflash_manager.go",
            "pkg/store/copr/mpp_probe.go",
        ],
        "markers": ["StartTimestamp", "LogicalCPUCount", "GlobalMPPServerInfoManager", "tiflash.StartTime.Unix()"],
        "consequence": 1,
        "priority": 57,
        "payload": {
            "expected_shape": (
                "cached TiFlash logical core count is trusted when address and seconds-level "
                "StartTimestamp match"
            ),
            "known_result": "candidate only; requires G3 schedule proof and user-visible effect proof",
        },
    },
]


STATE_INGRESS_SOURCE_SEEDS = [
    {
        "name": "Binding history statement-summary lookup",
        "target_key": "target.source.binding-history-executeinternal-txreadts.v1",
        "covered_asset_key": "obligation.binding-history-preserves-pending-txreadts.v1",
        "module": "planner/binding-history",
        "title": "SOURCE_TARGETS: binding-history ExecuteInternal must not consume pending one-shot state",
        "source_paths": ["pkg/planner/core/planbuilder.go", "pkg/session/session.go"],
        "markers": ["fetchRecordFromClusterStmtSummary", "ExecuteInternal", "TxnReadTS"],
        "consequence": 2,
        "priority": 78,
        "payload": {
            "expected_shape": "user-visible management statement performs current-session internal SQL between one-shot state setup and the intended user read",
            "known_result": "validated via binding-history tx_read_ts TSO-stable RED/GREEN",
        },
    },
    {
        "name": "DDL foreign-key current-session restricted SQL",
        "target_key": "target.source.ddl-foreign-key-use-cur-session-state-ingress.v1",
        "covered_asset_key": None,
        "module": "ddl/foreign-key",
        "title": "SOURCE_TARGETS: DDL foreign-key restricted SQL should not consume pending one-shot state",
        "source_paths": ["pkg/ddl/foreign_key.go"],
        "markers": ["ExecOptionUseCurSession", "GetRestrictedSQLExecutor", "ExecRestrictedSQL"],
        "consequence": 2,
        "priority": 66,
        "payload": {
            "expected_shape": "a DDL validation path runs restricted SQL on the current session while a one-shot read timestamp may be pending",
            "first_oracle_to_try": "SET TRANSACTION stale-read control around a foreign-key DDL wrapper; require direct AS OF and current-rowset controls before any RED claim",
        },
    },
    {
        "name": "Executor user-management ExecuteInternal lookup",
        "target_key": "target.source.executor-user-management-executeinternal-state-ingress.v1",
        "covered_asset_key": None,
        "module": "executor/user-management",
        "title": "SOURCE_TARGETS: user-management ExecuteInternal lookups should not consume pending one-shot state",
        "source_paths": ["pkg/executor/simple.go"],
        "markers": ["readPasswordLockingInfo", "ExecuteInternal", "mysql.user"],
        "consequence": 1,
        "priority": 61,
        "payload": {
            "expected_shape": "a user-management statement performs internal mysql.user reads/writes through ExecuteInternal before the next user read",
            "first_oracle_to_try": "only execute after proving the management statement is accepted under pending tx_read_ts and that the contract expects preservation",
        },
    },
    {
        "name": "Planner index-advisor ExecuteInternal lookup",
        "target_key": "target.source.planner-index-advisor-executeinternal-state-ingress.v1",
        "covered_asset_key": None,
        "module": "planner/index-advisor",
        "title": "SOURCE_TARGETS: index-advisor ExecuteInternal should not consume pending one-shot state",
        "source_paths": ["pkg/planner/indexadvisor/utils.go"],
        "markers": ["ExecuteInternal", "DrainRecordSet", "index advisor"],
        "consequence": 1,
        "priority": 58,
        "payload": {
            "expected_shape": "planner helper executes internal SQL through the current session while one-shot session state may be pending",
            "first_oracle_to_try": "screen for a user-visible wrapper before execution; retire if only offline/helper code can call it",
        },
    },
]


STATE_INGRESS_KNOWN_PATHS = {
    "pkg/planner/core/planbuilder.go": "covered_by_binding_history",
    "pkg/planner/indexadvisor/utils.go": "covered_by_index_advisor",
    "pkg/ddl/foreign_key.go": "retired_session_ownership_proof",
    "pkg/executor/simple.go": "retired_sys_session_proof",
    "pkg/infoschema/infoschema.go": "retired_sys_executor_factory_proof",
    "pkg/executor/brie.go": "retired_new_glue_session_proof",
    "pkg/executor/grant.go": "covered_by_grant_revoke_pooled_session_state",
    "pkg/executor/revoke.go": "covered_by_grant_revoke_pooled_session_state",
    "pkg/infoschema/issyncer/syncer.go": "retired_background_sys_session_pool_proof",
    "pkg/executor/importer/job.go": "retired_task_manager_new_session_proof",
    "pkg/executor/importer/precheck.go": "retired_new_session_precheck_proof",
}


STATE_INGRESS_USER_WRAPPER_PREFIXES = (
    "pkg/executor/",
    "pkg/planner/",
    "pkg/infoschema/",
)


STATE_INGRESS_BACKGROUND_PREFIXES = (
    "br/",
    "pkg/session/",
    "pkg/store/gcworker/",
    "pkg/ttl/",
    "pkg/telemetry/",
    "pkg/domain/",
    "pkg/resourcegroup/",
    "pkg/timer/",
)


TERMINAL_ACTION_METHODS = (
    "Close",
    "Flush",
    "Commit",
    "Abort",
    "Rollback",
    "Finish",
    "Cleanup",
)


TERMINAL_ACTION_KNOWN_PATHS = {
    "pkg/dxf/importinto/encode_and_sort_operator.go": "covered_by_importinto_chunkworker_close",
    "pkg/objstore/s3store/client.go": "covered_by_s3_multipart_terminal_state",
    "br/pkg/storage/s3.go": "covered_by_issue48164_concurrent_writer_error_identity",
}


TERMINAL_ACTION_SCREENED_PREFIXES = (
    "cmd/",
    "tools/",
    "pkg/testkit/",
    "pkg/parser/",
    "pkg/server/",
)


TERMINAL_ACTION_SCREENED_FRAGMENTS = (
    "/testutils/",
    "/mock/",
    "/utiltest/",
    "mockstore/",
)


def iter_go_files(repo: Path):
    skip_parts = {".git", "vendor", "node_modules", "assets.sqlite3"}
    for item in repo.rglob("*.go"):
        if any(part in skip_parts for part in item.parts):
            continue
        yield item


def iter_go_functions(repo: Path):
    func_pattern = re.compile(r"^func\s+(?:\([^)]*\)\s*)?(?P<name>[A-Za-z_][A-Za-z0-9_]*)\s*\(")
    for source in iter_go_files(repo):
        rel = str(source.relative_to(repo))
        if rel.endswith("_test.go"):
            continue
        try:
            lines = source.read_text(errors="ignore").splitlines()
        except OSError:
            continue
        index = 0
        while index < len(lines):
            match = func_pattern.match(lines[index].strip())
            if not match:
                index += 1
                continue
            start = index
            depth = 0
            seen_open = False
            end = index
            cursor = index
            while cursor < len(lines):
                line = lines[cursor]
                depth += line.count("{") - line.count("}")
                if "{" in line:
                    seen_open = True
                if seen_open and depth <= 0 and cursor > index:
                    end = cursor
                    break
                cursor += 1
            if end == index:
                end = min(index + 80, len(lines) - 1)
            yield {
                "path": rel,
                "name": match.group("name"),
                "start_line": start + 1,
                "end_line": end + 1,
                "lines": lines[start : end + 1],
            }
            index = max(cursor + 1, index + 1)


def classify_state_ingress_ownership(rel: str, text: str, hit_lines: list[dict[str, Any]]) -> dict[str, Any]:
    def is_live_line(hit: dict[str, Any]) -> bool:
        stripped = hit["text"].strip()
        return bool(stripped) and not stripped.startswith("//")

    def has_live_hit(pattern: str, *, ignore_function_definition: bool = False) -> bool:
        matcher = re.compile(pattern)
        for hit in hit_lines:
            if not is_live_line(hit):
                continue
            if ignore_function_definition and hit["text"].strip().startswith("func "):
                continue
            if matcher.search(hit["text"]):
                return True
        return False

    if rel in STATE_INGRESS_KNOWN_PATHS:
        return {
            "verdict": "covered_or_retired",
            "reason": STATE_INGRESS_KNOWN_PATHS[rel],
        }
    if any(rel.startswith(prefix) for prefix in STATE_INGRESS_BACKGROUND_PREFIXES):
        return {
            "verdict": "screened_out",
            "reason": "background_or_internal_session_scope",
        }
    if rel.startswith("pkg/ddl/"):
        return {
            "verdict": "screened_out",
            "reason": "ddl_worker_session_requires_manual_ownership_proof",
        }
    if "/internal/" in rel:
        return {
            "verdict": "screened_out",
            "reason": "internal_helper_requires_external_wrapper_proof",
        }

    use_cur_session = has_live_hit(r"ExecOptionUseCurSession|UseCurSession")
    execute_internal_call = has_live_hit(r"ExecuteInternal\s*\(", ignore_function_definition=True)
    restricted_sql_call = has_live_hit(r"ExecRestrictedSQL\s*\(")
    restricted_sql_without_use_cur_session = restricted_sql_call and not use_cur_session
    isolated_markers = [
        ("sys_session", r"GetSysSession|sysSessionPool|SysSessionPool|AdvancedSysSessionPool|syssession\."),
        ("pooled_session", r"sessPool\.Get|sPool\.Get|SessionPool|DestroyableSessionPool"),
        ("new_session", r"CreateSessionWithDomain|NewSession\("),
    ]
    isolated_marker_reasons = [
        reason for reason, pattern in isolated_markers if re.search(pattern, text)
    ]
    for reason, pattern in isolated_markers:
        if re.search(pattern, text) and not (use_cur_session or execute_internal_call):
            return {
                "verdict": "screened_out",
                "reason": reason,
            }

    auxiliary_statement_wrapper = (
        rel.startswith("pkg/executor/importer/")
        or rel == "pkg/executor/brie.go"
    )
    user_wrapper = any(rel.startswith(prefix) for prefix in STATE_INGRESS_USER_WRAPPER_PREFIXES)
    if restricted_sql_without_use_cur_session and not execute_internal_call:
        return {
            "verdict": "screened_out",
            "reason": "restricted_sql_without_use_cur_session",
            "signals": {
                "use_cur_session": use_cur_session,
                "execute_internal_call": execute_internal_call,
                "restricted_sql_call": restricted_sql_call,
                "isolated_marker_reasons": isolated_marker_reasons,
                "sample_hits": hit_lines[:3],
            },
        }

    if user_wrapper and (use_cur_session or execute_internal_call):
        reason = "probable_user_session_wrapper"
        if auxiliary_statement_wrapper:
            reason = "probable_auxiliary_statement_wrapper"
        return {
            "verdict": "candidate",
            "reason": reason,
            "signals": {
                "use_cur_session": use_cur_session,
                "execute_internal_call": execute_internal_call,
                "restricted_sql_call": restricted_sql_call,
                "auxiliary_statement_wrapper": auxiliary_statement_wrapper,
                "isolated_marker_reasons": isolated_marker_reasons,
            },
        }
    return {
        "verdict": "screened_out",
        "reason": "no_user_session_wrapper_signal",
        "signals": {
            "use_cur_session": use_cur_session,
            "execute_internal_call": execute_internal_call,
            "restricted_sql_call": restricted_sql_call,
            "isolated_marker_reasons": isolated_marker_reasons,
            "sample_hits": hit_lines[:3],
        },
    }


def dynamic_state_ingress_candidates(
    repo: Path,
    target_status: dict[str, str],
    existing_count: int,
    limit: int,
) -> tuple[list[dict[str, Any]], list[dict[str, Any]], list[dict[str, Any]]]:
    hit_pattern = re.compile(r"(ExecuteInternal|ExecRestrictedSQL|ExecOptionUseCurSession|UseCurSession)")
    grouped_hits: dict[str, list[dict[str, Any]]] = defaultdict(list)
    file_text: dict[str, str] = {}
    for source in iter_go_files(repo):
        rel = str(source.relative_to(repo))
        if rel.endswith("_test.go"):
            continue
        try:
            lines = source.read_text(errors="ignore").splitlines()
        except OSError:
            continue
        hits = []
        for lineno, line in enumerate(lines, 1):
            if hit_pattern.search(line):
                hits.append({"path": rel, "line": lineno, "text": line.strip()})
        if hits:
            grouped_hits[rel] = hits
            file_text[rel] = "\n".join(lines)

    scored: list[tuple[int, str, dict[str, Any], list[dict[str, Any]]]] = []
    screened: list[dict[str, Any]] = []
    for rel, hits in grouped_hits.items():
        ownership = classify_state_ingress_ownership(rel, file_text[rel], hits)
        if ownership["verdict"] != "candidate":
            screened.append({"path": rel, "ownership": ownership, "hits": hits[:5]})
            continue
        signals = ownership.get("signals", {})
        score = 0
        if rel.startswith("pkg/executor/"):
            score += 15
        if rel.startswith("pkg/planner/"):
            score += 12
        if rel.startswith("pkg/infoschema/"):
            score += 18
        if signals.get("use_cur_session"):
            score += 40
        if signals.get("execute_internal_call"):
            score += 12
        if signals.get("restricted_sql_call"):
            score += 5
        if signals.get("auxiliary_statement_wrapper"):
            score -= 12
        if signals.get("isolated_marker_reasons"):
            score -= 10
        score += min(len(hits), 5)
        scored.append((score, rel, ownership, hits))

    scored.sort(key=lambda item: (-item[0], item[1]))
    candidates: list[dict[str, Any]] = []
    skipped: list[dict[str, Any]] = []
    remaining = max(limit - existing_count, 0)
    for score, rel, ownership, hits in scored:
        if remaining <= 0:
            break
        path_slug = slug(rel.removesuffix(".go"), 64)
        target_key = f"target.source.dynamic-state-ingress.{path_slug}.v1"
        if target_key in target_status:
            skipped.append(
                {
                    "path": rel,
                    "reason": "retired_target_exists" if target_status[target_key] == "retired" else "target_exists",
                    "status": target_status[target_key],
                    "target_key": target_key,
                }
            )
            continue
        module = rel.removesuffix(".go")
        candidate = {
            "record_type": "target",
            "target_key": target_key,
            "title": f"SOURCE_TARGETS: {module} internal SQL must not consume pending one-shot state",
            "module": module,
            "selector": "STATE_INGRESS_INTERNAL_SQL",
            "status": "candidate",
            "discoverability": "SOURCE_ONLY",
            "obligation_class": "S-SOURCE-STATE-INGRESS",
            "priority": 45 + min(max(score, 0), 45),
            "consequence": 1,
            "effort": 5,
            "uncertainty": 8,
            "payload": {
                "source_rule": "state-ingress dynamic internal SQL",
                "source_paths": [rel],
                "evidence": [{"path": rel, "matched_lines": hits[:8]}],
                "ownership_gate": ownership,
                "expected_shape": (
                    "a probable user-visible wrapper runs internal SQL through the current session "
                    "while one-shot session state may be pending"
                ),
                "expected_next_step": (
                    "prove the exact user wrapper and session ownership first; retire if the SQL "
                    "runs on a sys, DDL worker, pooled, or background session"
                ),
                "first_oracle_to_try": (
                    "reuse oracle.pending-txreadts-preserved-across-internal-sql.v1 only after "
                    "the product contract and wrapper ownership are explicit"
                ),
                "stop_rule": (
                    "do not execute from a dynamic source hit alone; require target-specific P/Q/F, "
                    "a user-visible SQL wrapper, session-ownership proof, and AS OF/current rowset controls"
                ),
            },
            "provenance": {
                "source_kind": "source_target_candidate",
                "source": "store.py source-targets state-ingress dynamic scan",
                "introduced_for": "source-targets",
            },
        }
        candidates.append(candidate)
        remaining -= 1
    return candidates, skipped, screened



def source_targets_identity_token(path: Path, repo: Path, limit: int, jsonl_output: Path | None) -> dict[str, Any]:
    init_db(path)
    repo = repo.resolve()
    if not repo.exists():
        raise SystemExit(f"repo does not exist: {repo}")

    with connect(path) as conn:
        target_status = {
            row["target_key"]: row["status"]
            for row in conn.execute("SELECT target_key, status FROM target_queue").fetchall()
        }
        asset_keys = {
            row["asset_key"]
            for row in conn.execute("SELECT asset_key FROM asset").fetchall()
        }

    candidates: list[dict[str, Any]] = []
    skipped: list[dict[str, Any]] = []
    for seed in IDENTITY_TOKEN_SOURCE_SEEDS:
        evidence = []
        missing_paths = []
        for rel in seed["source_paths"]:
            source = repo / rel
            if not source.exists():
                missing_paths.append(rel)
                continue
            text = source.read_text(errors="ignore")
            matched = [marker for marker in seed["markers"] if marker in text]
            if matched:
                evidence.append({"path": rel, "matched_markers": matched})

        covered_asset = seed.get("covered_asset_key")
        if seed["target_key"] in target_status:
            status = target_status[seed["target_key"]]
            skipped.append(
                {
                    "name": seed["name"],
                    "reason": "retired_target_exists" if status == "retired" else "target_exists",
                    "status": status,
                    "target_key": seed["target_key"],
                }
            )
            continue
        if covered_asset and covered_asset in asset_keys:
            skipped.append(
                {
                    "name": seed["name"],
                    "reason": "covered_asset_exists",
                    "asset_key": covered_asset,
                    "evidence": evidence,
                }
            )
            continue
        if missing_paths:
            skipped.append({"name": seed["name"], "reason": "missing_source_paths", "missing": missing_paths})
            continue
        if not evidence:
            skipped.append({"name": seed["name"], "reason": "markers_not_found"})
            continue

        candidate = {
            "record_type": "target",
            "target_key": seed["target_key"],
            "title": seed["title"],
            "module": seed["module"],
            "selector": "IDENTITY_TOKEN_ASYNC_FILTER",
            "status": "candidate",
            "discoverability": "SOURCE_ONLY",
            "obligation_class": "S-SOURCE-IDENTITY",
            "priority": seed["priority"],
            "consequence": seed["consequence"],
            "effort": 4,
            "uncertainty": 6,
            "payload": {
                **seed["payload"],
                "source_rule": "identity-token async filter",
                "source_paths": seed["source_paths"],
                "evidence": evidence,
                "expected_next_step": "derive target-specific P/Q/F and prove G3 product-feasible collision schedule before execution",
                "stop_rule": "do not execute on token precision alone; record INVALID(schedule-proof) if lifecycle collision cannot occur under product timing",
            },
            "provenance": {
                "source_kind": "source_target_candidate",
                "source": "store.py source-targets identity-token",
                "introduced_for": "source-targets",
            },
        }
        candidates.append(candidate)
        if len(candidates) >= limit:
            break

    token_pattern = re.compile(
        r"(ownerTS|StartTimestamp|last_heartbeat_time|HeartbeatTimestamp|expectedHeartbeatTimestamp|initialHeartbeatTimestamp)"
    )
    comparison_pattern = re.compile(r"(==|!=)")
    time_sites = []
    token_comparisons = []
    for source in iter_go_files(repo):
        rel = str(source.relative_to(repo))
        try:
            lines = source.read_text(errors="ignore").splitlines()
        except OSError:
            continue
        for lineno, line in enumerate(lines, 1):
            if "time.Now().Unix()" in line or "time.Now().UTC().Unix()" in line:
                time_sites.append({"path": rel, "line": lineno, "text": line.strip()})
            if token_pattern.search(line) and comparison_pattern.search(line):
                token_comparisons.append({"path": rel, "line": lineno, "text": line.strip()})
            if len(time_sites) >= 80 and len(token_comparisons) >= 80:
                break

    if jsonl_output is not None:
        jsonl_output.parent.mkdir(parents=True, exist_ok=True)
        jsonl_output.write_text("".join(canonical(candidate) + "\n" for candidate in candidates))

    return {
        "rule": "identity-token",
        "repo": str(repo),
        "candidates": candidates,
        "skipped": skipped,
        "raw_hits": {
            "time_sites_sample": time_sites[:40],
            "token_comparisons_sample": token_comparisons[:40],
        },
        "jsonl_output": str(jsonl_output) if jsonl_output else None,
    }


def source_targets_state_ingress(path: Path, repo: Path, limit: int, jsonl_output: Path | None) -> dict[str, Any]:
    init_db(path)
    repo = repo.resolve()
    if not repo.exists():
        raise SystemExit(f"repo does not exist: {repo}")

    with connect(path) as conn:
        target_status = {
            row["target_key"]: row["status"]
            for row in conn.execute("SELECT target_key, status FROM target_queue").fetchall()
        }
        asset_keys = {
            row["asset_key"]
            for row in conn.execute("SELECT asset_key FROM asset").fetchall()
        }

    candidates: list[dict[str, Any]] = []
    skipped: list[dict[str, Any]] = []
    for seed in STATE_INGRESS_SOURCE_SEEDS:
        evidence = []
        missing_paths = []
        for rel in seed["source_paths"]:
            source = repo / rel
            if not source.exists():
                missing_paths.append(rel)
                continue
            text = source.read_text(errors="ignore")
            matched = [marker for marker in seed["markers"] if marker in text]
            if matched:
                evidence.append({"path": rel, "matched_markers": matched})

        covered_asset = seed.get("covered_asset_key")
        if seed["target_key"] in target_status:
            status = target_status[seed["target_key"]]
            skipped.append(
                {
                    "name": seed["name"],
                    "reason": "retired_target_exists" if status == "retired" else "target_exists",
                    "status": status,
                    "target_key": seed["target_key"],
                }
            )
            continue
        if covered_asset and covered_asset in asset_keys:
            skipped.append(
                {
                    "name": seed["name"],
                    "reason": "covered_asset_exists",
                    "asset_key": covered_asset,
                    "evidence": evidence,
                }
            )
            continue
        if missing_paths:
            skipped.append({"name": seed["name"], "reason": "missing_source_paths", "missing": missing_paths})
            continue
        if not evidence:
            skipped.append({"name": seed["name"], "reason": "markers_not_found"})
            continue

        candidate = {
            "record_type": "target",
            "target_key": seed["target_key"],
            "title": seed["title"],
            "module": seed["module"],
            "selector": "STATE_INGRESS_INTERNAL_SQL",
            "status": "candidate",
            "discoverability": "SOURCE_ONLY",
            "obligation_class": "S-SOURCE-STATE-INGRESS",
            "priority": seed["priority"],
            "consequence": seed["consequence"],
            "effort": 5,
            "uncertainty": 7,
            "payload": {
                **seed["payload"],
                "source_rule": "state-ingress internal SQL",
                "source_paths": seed["source_paths"],
                "evidence": evidence,
                "expected_next_step": (
                    "derive a target-specific P/Q/F obligation, prove the outer user statement is "
                    "product-feasible under pending one-shot state, then choose a rowset or state "
                    "oracle before execution"
                ),
                "stop_rule": (
                    "do not execute from internal SQL presence alone; retire if the wrapper is not "
                    "user-visible, uses an isolated sys session, or the product contract says the "
                    "one-shot state is consumed by any next statement"
                ),
            },
            "provenance": {
                "source_kind": "source_target_candidate",
                "source": "store.py source-targets state-ingress",
                "introduced_for": "source-targets",
            },
        }
        candidates.append(candidate)
        if len(candidates) >= limit:
            break

    dynamic_candidates, dynamic_skipped, screened_out = dynamic_state_ingress_candidates(
        repo,
        target_status,
        len(candidates),
        limit,
    )
    candidates.extend(dynamic_candidates)
    skipped.extend(dynamic_skipped)

    ingress_hits = []
    hit_pattern = re.compile(r"(ExecuteInternal|ExecRestrictedSQL|ExecOptionUseCurSession|UseCurSession)")
    for source in iter_go_files(repo):
        rel = str(source.relative_to(repo))
        if rel.endswith("_test.go"):
            continue
        try:
            lines = source.read_text(errors="ignore").splitlines()
        except OSError:
            continue
        for lineno, line in enumerate(lines, 1):
            if hit_pattern.search(line):
                ingress_hits.append({"path": rel, "line": lineno, "text": line.strip()})
            if len(ingress_hits) >= 80:
                break
        if len(ingress_hits) >= 80:
            break

    if jsonl_output is not None:
        jsonl_output.parent.mkdir(parents=True, exist_ok=True)
        jsonl_output.write_text("".join(canonical(candidate) + "\n" for candidate in candidates))

    return {
        "rule": "state-ingress",
        "repo": str(repo),
        "candidates": candidates,
        "skipped": skipped,
        "raw_hits": {
            "state_ingress_sites_sample": ingress_hits[:40],
            "screened_out_sample": screened_out[:40],
        },
        "jsonl_output": str(jsonl_output) if jsonl_output else None,
    }


def source_targets_pooled_session_state(path: Path, repo: Path, limit: int, jsonl_output: Path | None) -> dict[str, Any]:
    init_db(path)
    repo = repo.resolve()
    if not repo.exists():
        raise SystemExit(f"repo does not exist: {repo}")

    with connect(path) as conn:
        target_status = {
            row["target_key"]: row["status"]
            for row in conn.execute("SELECT target_key, status FROM target_queue").fetchall()
        }

    assignment_pattern = re.compile(
        r"(?<![A-Za-z0-9_\.])(?P<receiver>[A-Za-z_][A-Za-z0-9_]*)\.GetSessionVars\(\)\.(?P<field>User|CurrentDB|SQLMode|TimeZone|SnapshotTS|TxnReadTS|OptimizerUseInvisibleIndexes)\s*=(?!=)"
    )
    acquire_pattern = re.compile(r"(?P<receiver>[A-Za-z_][A-Za-z0-9_]*)\s*,\s*(?:err|e)\s*:=\s*.*GetSysSession\(")
    reader_pattern = re.compile(r"(GetSessionVars\(\)\.User|Grantor|CreatedBy|created_by|CurrentDB|SQLMode|TimeZone)")

    candidates: list[dict[str, Any]] = []
    skipped: list[dict[str, Any]] = []
    raw_hits: list[dict[str, Any]] = []
    screened: list[dict[str, Any]] = []

    for source in iter_go_files(repo):
        rel = str(source.relative_to(repo))
        if rel.endswith("_test.go"):
            continue
        try:
            lines = source.read_text(errors="ignore").splitlines()
        except OSError:
            continue

        acquired_vars: set[str] = set()
        acquire_hits: list[dict[str, Any]] = []
        assignment_hits: list[dict[str, Any]] = []
        reader_hits: list[dict[str, Any]] = []

        for lineno, line in enumerate(lines, 1):
            acquire_match = acquire_pattern.search(line)
            if acquire_match:
                acquired_vars.add(acquire_match.group("receiver"))
                acquire_hits.append({"path": rel, "line": lineno, "text": line.strip()})
            assignment_match = assignment_pattern.search(line)
            if assignment_match:
                assignment_hits.append(
                    {
                        "path": rel,
                        "line": lineno,
                        "text": line.strip(),
                        "receiver": assignment_match.group("receiver"),
                        "field": assignment_match.group("field"),
                    }
                )
            if reader_pattern.search(line):
                reader_hits.append({"path": rel, "line": lineno, "text": line.strip()})

        if not assignment_hits:
            continue
        raw_hits.extend(assignment_hits[:4])

        sys_session_assignments = [
            hit for hit in assignment_hits if hit["receiver"] in acquired_vars
        ]
        if not sys_session_assignments:
            screened.append(
                {
                    "path": rel,
                    "reason": "assignment_receiver_not_proven_sys_session",
                    "assignments": assignment_hits[:4],
                    "acquires": acquire_hits[:4],
                }
            )
            continue

        path_slug = slug(rel.removesuffix(".go"), 64)
        target_key = f"target.source.pooled-session-state.{path_slug}.v1"
        legacy_state_ingress_key = f"target.source.dynamic-state-ingress.{path_slug}.v1"
        if target_key in target_status:
            skipped.append(
                {
                    "path": rel,
                    "reason": "target_exists",
                    "status": target_status[target_key],
                    "target_key": target_key,
                }
            )
            continue
        if legacy_state_ingress_key in target_status:
            skipped.append(
                {
                    "path": rel,
                    "reason": "covered_by_existing_state_ingress_pivot",
                    "status": target_status[legacy_state_ingress_key],
                    "target_key": legacy_state_ingress_key,
                }
            )
            continue

        module = rel.removesuffix(".go")
        candidates.append(
            {
                "record_type": "target",
                "target_key": target_key,
                "title": f"SOURCE_TARGETS: {module} must isolate mutable sys-session state",
                "module": module,
                "selector": "SYS_SESSION_POOLED_STATE_ISOLATION",
                "status": "candidate",
                "discoverability": "SOURCE_ONLY",
                "obligation_class": "S-SYS-SESSION-STATE-ISOLATION",
                "priority": 72,
                "consequence": 1,
                "effort": 4,
                "uncertainty": 6,
                "payload": {
                    "source_rule": "pooled sys-session mutable state",
                    "source_paths": [rel],
                    "evidence": [
                        {
                            "path": rel,
                            "sys_session_acquire_lines": acquire_hits[:6],
                            "session_state_assignment_lines": sys_session_assignments[:6],
                            "same_file_reader_lines": reader_hits[:8],
                        }
                    ],
                    "expected_shape": (
                        "code borrows a pooled sys session, mutates SessionVars, then releases it "
                        "without proving the state is restored before another product path reuses it"
                    ),
                    "expected_next_step": (
                        "prove the release/reset contract, find a later reader that writes user-visible "
                        "metadata or changes behavior, then build a two-actor or two-statement oracle"
                    ),
                    "first_oracle_to_try": (
                        "metadata/action owner should reflect the current user or current statement state, "
                        "not stale mutable state from a previously borrowed sys session"
                    ),
                    "stop_rule": (
                        "do not claim a bug from mutation alone; require a product-feasible reuse schedule "
                        "and a SQL-visible metadata or behavior oracle"
                    ),
                },
                "provenance": {
                    "source_kind": "source_target_candidate",
                    "source": "store.py source-targets pooled-session-state",
                    "introduced_for": "source-targets",
                },
            }
        )
        if len(candidates) >= limit:
            break

    if jsonl_output is not None:
        jsonl_output.parent.mkdir(parents=True, exist_ok=True)
        jsonl_output.write_text("".join(canonical(candidate) + "\n" for candidate in candidates))

    return {
        "rule": "pooled-session-state",
        "repo": str(repo),
        "candidates": candidates,
        "skipped": skipped,
        "raw_hits": {
            "session_state_assignments_sample": raw_hits[:40],
            "screened_out_sample": screened[:40],
        },
        "jsonl_output": str(jsonl_output) if jsonl_output else None,
    }


def source_targets_user_session_state_restore(path: Path, repo: Path, limit: int, jsonl_output: Path | None) -> dict[str, Any]:
    init_db(path)
    repo = repo.resolve()
    if not repo.exists():
        raise SystemExit(f"repo does not exist: {repo}")

    with connect(path) as conn:
        target_status = {
            row["target_key"]: row["status"]
            for row in conn.execute("SELECT target_key, status FROM target_queue").fetchall()
        }

    assignment_pattern = re.compile(
        r"GetSessionVars\(\)\.(?P<field>OptimizerUseInvisibleIndexes|TimeZone|SQLMode|CurrentDB|TxnReadTS|SnapshotTS|Enable[A-Za-z0-9_]*)\s*=\s*(?P<value>[^/\n]+)"
    )
    helper_path_fragments = ("/mock/", "/coretestsdk/", "pkg/util/mock/")
    permanent_state_changes = {
        ("pkg/executor/simple.go", "CurrentDB"): "USE statement intentionally changes the session current database",
    }

    candidates: list[dict[str, Any]] = []
    skipped: list[dict[str, Any]] = []
    raw_hits: list[dict[str, Any]] = []
    screened: list[dict[str, Any]] = []

    for source in iter_go_files(repo):
        rel = str(source.relative_to(repo))
        if rel.endswith("_test.go"):
            continue
        try:
            lines = source.read_text(errors="ignore").splitlines()
        except OSError:
            continue

        hits: list[dict[str, Any]] = []
        for lineno, line in enumerate(lines, 1):
            match = assignment_pattern.search(line)
            if match and "==" not in line:
                hits.append(
                    {
                        "path": rel,
                        "line": lineno,
                        "text": line.strip(),
                        "field": match.group("field"),
                        "value": match.group("value").strip(),
                    }
                )
        if not hits:
            continue
        raw_hits.extend(hits[:4])

        if any(fragment in rel for fragment in helper_path_fragments):
            screened.append({"path": rel, "reason": "test_or_mock_helper", "hits": hits[:6]})
            continue

        fields = {hit["field"] for hit in hits}
        if any((rel, field) in permanent_state_changes for field in fields):
            screened.append(
                {
                    "path": rel,
                    "reason": "intended_permanent_session_state_change",
                    "details": [permanent_state_changes[(rel, field)] for field in fields if (rel, field) in permanent_state_changes],
                    "hits": hits[:6],
                }
            )
            continue

        text = "\n".join(lines)
        restores_original = bool(
            re.search(r"(origin|original|old)[A-Za-z0-9_]*\s*:=.*GetSessionVars\(\)\.", text)
            and re.search(r"GetSessionVars\(\)\.[A-Za-z0-9_]+\s*=\s*(origin|original|old)[A-Za-z0-9_]*", text)
        )
        hard_reset = any(
            hit["value"] in {"false", "0", "\"\""} or hit["value"].startswith("false")
            for hit in hits
        )
        enables_state = any(
            hit["value"] == "true" or hit["value"].startswith("&") or hit["value"].startswith("time.")
            for hit in hits
        )

        if restores_original:
            screened.append({"path": rel, "reason": "restores_original_value", "hits": hits[:6]})
            continue
        if not (hard_reset and enables_state):
            screened.append({"path": rel, "reason": "no_temporary_hard_reset_shape", "hits": hits[:6]})
            continue
        if not rel.startswith("pkg/executor/"):
            screened.append({"path": rel, "reason": "no_user_executor_wrapper_signal", "hits": hits[:6]})
            continue

        path_slug = slug(rel.removesuffix(".go"), 64)
        target_key = f"target.source.user-session-state-restore.{path_slug}.v1"
        legacy_state_ingress_key = f"target.source.dynamic-state-ingress.{path_slug}.v1"
        if target_key in target_status:
            skipped.append(
                {
                    "path": rel,
                    "reason": "target_exists",
                    "status": target_status[target_key],
                    "target_key": target_key,
                }
            )
            continue
        if legacy_state_ingress_key in target_status:
            skipped.append(
                {
                    "path": rel,
                    "reason": "covered_by_existing_state_ingress_pivot",
                    "status": target_status[legacy_state_ingress_key],
                    "target_key": legacy_state_ingress_key,
                }
            )
            continue

        module = rel.removesuffix(".go")
        candidates.append(
            {
                "record_type": "target",
                "target_key": target_key,
                "title": f"SOURCE_TARGETS: {module} must restore user session state exactly",
                "module": module,
                "selector": "USER_SESSION_STATE_RESTORE",
                "status": "candidate",
                "discoverability": "SOURCE_ONLY",
                "obligation_class": "S-USER-SESSION-STATE-RESTORE",
                "priority": 70,
                "consequence": 1,
                "effort": 4,
                "uncertainty": 6,
                "payload": {
                    "source_rule": "user session temporary state hard reset",
                    "source_paths": [rel],
                    "evidence": [{"path": rel, "session_state_assignment_lines": hits[:8]}],
                    "expected_shape": (
                        "a user-visible executor temporarily mutates SessionVars and exits by writing "
                        "a default value instead of restoring the caller's prior value"
                    ),
                    "expected_next_step": (
                        "prove the variable is user-configurable or behavior-visible, then compare "
                        "pre/post behavior rather than trusting @@ display alone"
                    ),
                    "first_oracle_to_try": (
                        "same-session behavior before and after the statement should match when the "
                        "user had already enabled the state"
                    ),
                    "stop_rule": (
                        "do not claim a bug from assignment shape alone; require a visible behavior "
                        "oracle and a control path that restores the original value"
                    ),
                },
                "provenance": {
                    "source_kind": "source_target_candidate",
                    "source": "store.py source-targets user-session-state-restore",
                    "introduced_for": "source-targets",
                },
            }
        )
        if len(candidates) >= limit:
            break

    if jsonl_output is not None:
        jsonl_output.parent.mkdir(parents=True, exist_ok=True)
        jsonl_output.write_text("".join(canonical(candidate) + "\n" for candidate in candidates))

    return {
        "rule": "user-session-state-restore",
        "repo": str(repo),
        "candidates": candidates,
        "skipped": skipped,
        "raw_hits": {
            "session_state_assignments_sample": raw_hits[:40],
            "screened_out_sample": screened[:40],
        },
        "jsonl_output": str(jsonl_output) if jsonl_output else None,
    }


def source_targets_terminal_action_error(path: Path, repo: Path, limit: int, jsonl_output: Path | None) -> dict[str, Any]:
    init_db(path)
    repo = repo.resolve()
    if not repo.exists():
        raise SystemExit(f"repo does not exist: {repo}")

    with connect(path) as conn:
        target_status = {
            row["target_key"]: row["status"]
            for row in conn.execute("SELECT target_key, status FROM target_queue").fetchall()
        }

    terminal_pattern = re.compile(
        r"(?P<receiver>[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*)"
        r"\.(?P<method>" + "|".join(TERMINAL_ACTION_METHODS) + r")\s*\("
    )
    high_signal_path_fragments = (
        "dxf/",
        "import",
        "ingestor/",
        "lightning/",
        "objstore/",
        "storage",
        "backup",
        "restore",
        "ddl/",
    )
    high_signal_function_names = (
        "Close",
        "close",
        "Finished",
        "Import",
        "flush",
        "preprocess",
        "cleanup",
    )

    candidates_scored: list[tuple[int, dict[str, Any]]] = []
    skipped: list[dict[str, Any]] = []
    raw_hits: list[dict[str, Any]] = []
    screened: list[dict[str, Any]] = []

    for fn in iter_go_functions(repo):
        rel = fn["path"]
        if rel in TERMINAL_ACTION_KNOWN_PATHS:
            skipped.append(
                {
                    "path": rel,
                    "function": fn["name"],
                    "reason": TERMINAL_ACTION_KNOWN_PATHS[rel],
                }
            )
            continue
        if rel.startswith(TERMINAL_ACTION_SCREENED_PREFIXES) or any(
            fragment in rel for fragment in TERMINAL_ACTION_SCREENED_FRAGMENTS
        ):
            continue

        terminal_hits: list[dict[str, Any]] = []
        defer_block_depth = 0
        for offset, line in enumerate(fn["lines"]):
            stripped = line.strip()
            in_defer_block = defer_block_depth > 0 or stripped.startswith("defer ")
            for match in terminal_pattern.finditer(line):
                hit = {
                    "path": rel,
                    "function": fn["name"],
                    "line": fn["start_line"] + offset,
                    "text": stripped,
                    "receiver": match.group("receiver"),
                    "method": match.group("method"),
                    "ignored_result": stripped.startswith("_ ="),
                    "deferred": in_defer_block,
                }
                terminal_hits.append(hit)
            if defer_block_depth > 0:
                defer_block_depth += line.count("{") - line.count("}")
                defer_block_depth = max(defer_block_depth, 0)
            elif stripped.startswith("defer func"):
                defer_block_depth = max(line.count("{") - line.count("}"), 0)
        if len(terminal_hits) < 2:
            continue
        raw_hits.extend(terminal_hits[:4])

        if fn["name"].startswith(("New", "open", "Open", "create", "Create")):
            screened.append(
                {
                    "path": rel,
                    "function": fn["name"],
                    "reason": "constructor_or_setup_cleanup_not_terminal_lane",
                    "terminal_hits": terminal_hits[:8],
                }
            )
            continue

        active_hits = [
            hit for hit in terminal_hits if not hit["deferred"] and not hit["ignored_result"]
        ]
        receivers = {hit["receiver"].split(".")[-1] for hit in active_hits}
        if len(active_hits) < 2 or len(receivers) < 2:
            screened.append(
                {
                    "path": rel,
                    "function": fn["name"],
                    "reason": "no_two_active_sibling_terminal_owners",
                    "terminal_hits": terminal_hits[:8],
                }
            )
            continue

        early_return_gaps: list[dict[str, Any]] = []
        for hit in active_hits:
            if "err" not in hit["text"] and not hit["text"].startswith("return "):
                continue
            local_index = hit["line"] - fn["start_line"]
            window_lines = fn["lines"][local_index : min(local_index + 5, len(fn["lines"]))]
            return_line = None
            for window_offset, window_line in enumerate(window_lines):
                if "return " in window_line and "err" in window_line:
                    return_line = hit["line"] + window_offset
                    break
            if return_line is None:
                continue
            later_sibling_hits = [
                later
                for later in active_hits
                if later["line"] > return_line
                and later["receiver"].split(".")[-1] != hit["receiver"].split(".")[-1]
            ]
            if not later_sibling_hits:
                continue
            early_return_gaps.append(
                {
                    "terminal_line": hit,
                    "later_sibling_terminal_lines": later_sibling_hits[:6],
                    "return_window": [line.strip() for line in window_lines],
                }
            )
        if not early_return_gaps:
            screened.append(
                {
                    "path": rel,
                    "function": fn["name"],
                    "reason": "no_error_return_before_later_sibling_terminal_action",
                    "terminal_hits": terminal_hits[:8],
                }
            )
            continue

        path_slug = slug(rel.removesuffix(".go"), 52)
        fn_slug = slug(fn["name"], 24)
        target_key = f"target.source.terminal-action-error.{path_slug}.{fn_slug}.v1"
        if target_key in target_status:
            skipped.append(
                {
                    "path": rel,
                    "function": fn["name"],
                    "reason": "target_exists",
                    "status": target_status[target_key],
                    "target_key": target_key,
                }
            )
            continue

        score = 40 + min(len(active_hits), 8) * 3 + len(early_return_gaps) * 12 + len(receivers) * 4
        if any(fragment in rel for fragment in high_signal_path_fragments):
            score += 18
        if any(name in fn["name"] for name in high_signal_function_names):
            score += 12
        if fn["name"].startswith(("New", "open", "Open", "create", "Create")):
            score -= 18
        if any(hit["method"] in {"Commit", "Abort", "Rollback"} for hit in active_hits):
            score += 8

        module = rel.removesuffix(".go")
        candidate = {
            "record_type": "target",
            "target_key": target_key,
            "title": f"SOURCE_TARGETS: {module}.{fn['name']} must not skip sibling terminal actions after root error",
            "module": module,
            "selector": "ERROR_IDENTITY_PRESERVATION",
            "status": "candidate",
            "discoverability": "SOURCE_ONLY",
            "obligation_class": "S-SOURCE-ERR-TERMINAL-ACTION",
            "priority": min(score, 92),
            "consequence": 2 if any(fragment in rel for fragment in ("dxf/", "ingestor/", "importer/", "lightning/")) else 1,
            "effort": 5,
            "uncertainty": 6,
            "payload": {
                "source_rule": "terminal-action error gap",
                "source_paths": [rel],
                "function": fn["name"],
                "function_range": {"start_line": fn["start_line"], "end_line": fn["end_line"]},
                "evidence": [
                    {
                        "path": rel,
                        "function": fn["name"],
                        "terminal_lines": terminal_hits[:10],
                        "early_return_gaps": early_return_gaps[:4],
                    }
                ],
                "expected_shape": (
                    "one terminal owner returns a root error, and the same higher-level operation "
                    "still has later sibling terminal owners that may be skipped by an early return"
                ),
                "expected_next_step": (
                    "derive target-specific P/Q/F, then add a minimal observer/fault point that proves "
                    "both returned root error identity and sibling terminal action reachability"
                ),
                "first_oracle_to_try": (
                    "root error must survive, and every sibling Close/Flush/Commit/Abort owner must "
                    "reach the expected terminal action or explicit abort path"
                ),
                "stop_rule": (
                    "do not execute from source shape alone; retire if a defer/cleanup path already "
                    "closes the sibling, if the later terminal action must not run after the first "
                    "error, or if no strong state/action observer can be added"
                ),
            },
            "provenance": {
                "source_kind": "source_target_candidate",
                "source": "store.py source-targets terminal-action-error",
                "introduced_for": "error-identity-terminal-action-lane",
            },
        }
        candidates_scored.append((score, candidate))

    candidates_scored.sort(
        key=lambda item: (
            -item[0],
            item[1]["module"],
            item[1]["payload"]["function"],
        )
    )
    candidates = [candidate for _, candidate in candidates_scored[:limit]]

    if jsonl_output is not None:
        jsonl_output.parent.mkdir(parents=True, exist_ok=True)
        jsonl_output.write_text("".join(canonical(candidate) + "\n" for candidate in candidates))

    return {
        "rule": "terminal-action-error",
        "repo": str(repo),
        "candidates": candidates,
        "skipped": skipped[:80],
        "raw_hits": {
            "terminal_action_sites_sample": raw_hits[:60],
            "screened_out_sample": screened[:60],
        },
        "jsonl_output": str(jsonl_output) if jsonl_output else None,
    }


def stats(path: Path) -> dict[str, Any]:
    init_db(path)
    with connect(path) as conn:
        assets = conn.execute(
            "SELECT asset_type, COUNT(*) AS n FROM asset GROUP BY asset_type ORDER BY asset_type"
        ).fetchall()
        runs = conn.execute(
            "SELECT verdict, COUNT(*) AS n FROM run_result GROUP BY verdict ORDER BY verdict"
        ).fetchall()
        revisions = conn.execute("SELECT COUNT(*) AS n FROM asset_revision").fetchone()["n"]
        targets = conn.execute(
            "SELECT status, COUNT(*) AS n FROM target_queue GROUP BY status ORDER BY status"
        ).fetchall()
    return {
        "assets": {row["asset_type"]: row["n"] for row in assets},
        "asset_revisions": revisions,
        "runs": {row["verdict"]: row["n"] for row in runs},
        "targets": {row["status"]: row["n"] for row in targets},
    }


def health(path: Path) -> dict[str, Any]:
    init_db(path)
    base = stats(path)
    queued = queue(path, include_done=True)["targets"]
    state_counts = Counter(item["state"]["next_state"] for item in queued)
    admission_counts = Counter(item["state"]["admission"] for item in queued)
    admitted_active = sum(
        1
        for item in queued
        if item["state"]["next_state"] not in {"validated", "blocked", "retired"}
        and item["state"]["eligible_for_mine_bug"]
    )
    with connect(path) as conn:
        oracle_debt_rows = conn.execute(
            """
            SELECT asset_key, trust_level FROM asset
            WHERE asset_type = 'oracle'
              AND lifecycle_status != 'retired'
              AND trust_level NOT IN ('execution_verified', 'trusted')
            ORDER BY asset_key
            """
        ).fetchall()
    return {
        **base,
        "queue_states": dict(state_counts),
        "severity_admission": {
            "by_admission": dict(admission_counts),
            "admitted_active_targets": admitted_active,
        },
        "oracle_debt": [dict(row) for row in oracle_debt_rows],
    }


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--db", type=Path, default=DEFAULT_DB)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("init")
    importer = sub.add_parser("import")
    importer.add_argument("source", type=Path)
    pack = sub.add_parser("pack")
    pack.add_argument("--module", required=True)
    pack.add_argument("--selector", required=True)
    sub.add_parser("stats")
    queue_parser = sub.add_parser("queue")
    queue_parser.add_argument("--include-done", action="store_true")
    refill_parser = sub.add_parser("refill")
    refill_parser.add_argument("--limit", type=int, default=5)
    refill_parser.add_argument("--include-covered", action="store_true")
    refill_parser.add_argument("--jsonl-output", type=Path)
    source_targets_parser = sub.add_parser("source-targets")
    source_targets_parser.add_argument(
        "--rule",
        required=True,
        choices=[
            "identity-token",
            "state-ingress",
            "pooled-session-state",
            "user-session-state-restore",
            "terminal-action-error",
        ],
    )
    source_targets_parser.add_argument("--repo", type=Path, required=True)
    source_targets_parser.add_argument("--limit", type=int, default=5)
    source_targets_parser.add_argument("--jsonl-output", type=Path)
    sub.add_parser("next")
    sub.add_parser("health")
    args = parser.parse_args()

    if args.command == "init":
        init_db(args.db)
        output = {"db": str(args.db), "initialized": True}
    elif args.command == "import":
        output = {"db": str(args.db), "imported": dict(import_jsonl(args.db, args.source))}
    elif args.command == "pack":
        output = build_pack(args.db, args.module, args.selector)
    elif args.command == "queue":
        output = queue(args.db, args.include_done)
    elif args.command == "refill":
        output = refill(args.db, args.limit, args.include_covered, args.jsonl_output)
    elif args.command == "source-targets":
        if args.rule == "identity-token":
            output = source_targets_identity_token(args.db, args.repo, args.limit, args.jsonl_output)
        elif args.rule == "state-ingress":
            output = source_targets_state_ingress(args.db, args.repo, args.limit, args.jsonl_output)
        elif args.rule == "pooled-session-state":
            output = source_targets_pooled_session_state(args.db, args.repo, args.limit, args.jsonl_output)
        elif args.rule == "user-session-state-restore":
            output = source_targets_user_session_state_restore(args.db, args.repo, args.limit, args.jsonl_output)
        elif args.rule == "terminal-action-error":
            output = source_targets_terminal_action_error(args.db, args.repo, args.limit, args.jsonl_output)
        else:
            raise SystemExit(f"unsupported source-targets rule: {args.rule}")
    elif args.command == "next":
        output = next_target(args.db)
    elif args.command == "health":
        output = health(args.db)
    else:
        output = stats(args.db)
    print(json.dumps(output, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
