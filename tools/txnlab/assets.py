"""Bridge txnlab evidence into the incremental JSONL/SQLite asset store."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from .config import ExperimentConfig
from .process import CommandRunner


def _used_assets(config: ExperimentConfig) -> list[dict[str, str]]:
    configured = list(config.metadata.get("used_assets", []))
    if configured:
        return configured
    return [{"asset_key": config.obligation_key, "role": "obligation"}]


def make_run_record(
    config: ExperimentConfig,
    *,
    verdict: str,
    evidence_dir: Path,
    oracle_result: dict[str, Any] | None,
    source_state: dict[str, Any],
    cluster_state: dict[str, Any] | None,
    error: str | None = None,
) -> dict[str, Any]:
    return {
        "record_type": "run",
        "run_key": config.run_key,
        "obligation_key": config.obligation_key,
        "verdict": verdict,
        "code_ref": {
            name: {
                "repo": str(pin.repo),
                "requested_commit": pin.commit,
                "resolved_commit": (
                    source_state.get(name, {}).get("resolved_commit")
                    or source_state.get(name, {}).get("commit")
                ),
            }
            for name, pin in sorted(config.sources.items())
        },
        "environment": {
            "runner": "tools.txnlab",
            "kind": config.environment,
            "namespace": config.cluster.namespace if config.cluster else None,
            "tidbcluster": config.cluster.tc_name if config.cluster else None,
            "cluster_state": cluster_state or {},
        },
        "evidence": {
            "bundle": str(evidence_dir),
            "manifest": str(evidence_dir / "manifest.json"),
            "index": str(evidence_dir / "evidence-index.json"),
            "oracle": oracle_result or {},
            "error": error,
        },
        "lessons": {
            "selector": config.selector,
            "experiment_metadata": config.metadata.get("lessons", {}),
        },
        "used_assets": _used_assets(config),
    }


def write_run_record(path: Path, record: dict[str, Any]) -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(record, sort_keys=True, ensure_ascii=True) + "\n")
    return path


def import_run_record(config: ExperimentConfig, path: Path, runner: CommandRunner) -> dict[str, Any]:
    if config.assets is None:
        return {"imported": False, "reason": "no assets configuration"}
    result = runner.run(
        [
            "python3",
            config.assets.store_py,
            "--db",
            config.assets.sqlite_db,
            "import",
            path,
        ]
    )
    return {"imported": True, "stdout": result.stdout.strip()}


def promotion_candidate(record: dict[str, Any]) -> dict[str, Any]:
    return {
        "status": "needs-independent-confirmation",
        "run_key": record["run_key"],
        "obligation_key": record["obligation_key"],
        "verdict": record["verdict"],
        "next_gates": [
            "repeat the RED with the same pinned commits",
            "run the nearest altitude and no-fault controls",
            "prove exact source owner and minimal counterfactual",
            "only then promote a bug asset or file an issue",
        ],
    }
