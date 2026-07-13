"""Executable high-consumer transaction oracles.

The inputs are deliberately evidence-shaped. A schedule that cannot prove its
critical ordering is INVALID, even when the final database state looks odd.
"""

from __future__ import annotations

from typing import Any, Callable


VERDICTS = {"RED", "GREEN", "INVALID"}


def _result(verdict: str, reasons: list[str], facts: dict[str, Any]) -> dict[str, Any]:
    if verdict not in VERDICTS:
        raise ValueError(f"unsupported verdict: {verdict}")
    return {"verdict": verdict, "reasons": reasons, "facts": facts}


def _terminal_kind(payload: dict[str, Any]) -> str:
    terminal = payload.get("terminal", {})
    return str(terminal.get("kind", "")).lower()


def terminal_mvcc_truth(payload: dict[str, Any]) -> dict[str, Any]:
    """O56: close the public terminal result against durable MVCC truth."""

    keys = list(payload.get("keys", []))
    terminal = _terminal_kind(payload)
    facts = {
        "terminal": terminal,
        "key_count": len(keys),
        "apply_witness": bool(payload.get("apply_witness")),
        "response_suppressed": bool(payload.get("response_suppressed")),
        "duplicate_count": int(payload.get("duplicate_count", 0)),
    }
    invalid: list[str] = []
    if terminal not in {"success", "failure", "undetermined"}:
        invalid.append("terminal.kind must be success, failure, or undetermined")
    if not keys:
        invalid.append("fresh-session key observations are missing")
    if payload.get("response_suppressed") and not payload.get("apply_witness"):
        invalid.append("response-loss schedule lacks an after-apply witness")
    if any("visible" not in key or not key.get("txn_status") for key in keys):
        invalid.append("each key needs visibility and durable transaction status")
    if invalid:
        return _result("INVALID", invalid, facts)

    visible = [bool(key["visible"]) for key in keys]
    statuses = {str(key["txn_status"]).lower() for key in keys}
    commit_ts = {str(key["commit_ts"]) for key in keys if key.get("commit_ts") is not None}
    table_index_mismatch = any(
        key.get("table_value") != key.get("index_value")
        for key in keys
        if "table_value" in key and "index_value" in key
    )
    all_visible = all(visible)
    none_visible = not any(visible)
    all_committed = statuses == {"committed"}
    all_absent = statuses.issubset({"absent", "rolled_back"})
    red: list[str] = []
    if not all_visible and not none_visible:
        red.append("transaction key set is partially visible")
    if "committed" in statuses and statuses & {"absent", "rolled_back", "locked"}:
        red.append("durable statuses disagree across the transaction key set")
    if len(commit_ts) > 1:
        red.append("committed keys have divergent commit timestamps")
    if table_index_mismatch:
        red.append("table and index observations disagree")
    if facts["duplicate_count"] > 1:
        red.append("one logical operation produced duplicate durable effects")
    if terminal == "success" and not (all_visible and all_committed):
        red.append("definite success is not backed by one complete committed key set")
    if terminal == "failure" and (all_visible or all_committed):
        red.append("definite failure contradicts a durable commit")
    facts.update(
        {
            "visible": visible,
            "statuses": sorted(statuses),
            "commit_ts": sorted(commit_ts),
            "table_index_mismatch": table_index_mismatch,
        }
    )
    if red:
        return _result("RED", red, facts)
    if not ((all_visible and all_committed) or (none_visible and all_absent)):
        return _result(
            "INVALID",
            ["durable state is not yet terminal; wait for status recovery before judging"],
            facts,
        )
    return _result("GREEN", ["terminal result and durable transaction truth are coherent"], facts)


def _lock_identity(value: dict[str, Any]) -> tuple[Any, ...]:
    return tuple(
        value.get(field)
        for field in ("key", "start_ts", "for_update_ts", "lock_kind", "primary", "mode")
    )


def lock_generation_survival(payload: dict[str, Any]) -> dict[str, Any]:
    """O57: prove a delayed cleanup did not destroy a newer lock owner."""

    old_lock = dict(payload.get("old_lock", {}))
    new_lock = dict(payload.get("new_lock_before_cleanup", {}))
    after = dict(payload.get("lock_after_cleanup", {}))
    consumer = dict(payload.get("later_consumer", {}))
    facts = {
        "old_identity": _lock_identity(old_lock),
        "new_identity": _lock_identity(new_lock),
        "after_identity": _lock_identity(after) if after else None,
        "cleanup_applied": bool(payload.get("cleanup_applied")),
        "consumer_committed": consumer.get("committed"),
    }
    invalid: list[str] = []
    if not old_lock or not new_lock:
        invalid.append("old and new lock identities are required")
    if old_lock and new_lock and _lock_identity(old_lock) == _lock_identity(new_lock):
        invalid.append("schedule did not create a distinguishable lock generation or owner")
    if not payload.get("new_lock_witness_before_cleanup"):
        invalid.append("no witness proves the new lock existed before delayed cleanup")
    if not payload.get("cleanup_applied"):
        invalid.append("no witness proves the delayed cleanup reached TiKV")
    if "committed" not in consumer:
        invalid.append("later correctness consumer is missing")
    if invalid:
        return _result("INVALID", invalid, facts)

    red: list[str] = []
    if not after and not consumer["committed"]:
        red.append("new lock disappeared and its transaction could not commit")
    if after and _lock_identity(after) != _lock_identity(new_lock):
        red.append("post-cleanup lock identity no longer matches the newer owner")
    if consumer.get("error_class") in {"rolled_back", "write_conflict", "lock_not_found"}:
        red.append("newer owner was poisoned by cleanup for the old generation")
    if red:
        return _result("RED", red, facts)
    if not consumer["committed"]:
        return _result("INVALID", ["later consumer failed for an unclassified reason"], facts)
    return _result("GREEN", ["delayed cleanup preserved the newer lock owner"], facts)


def protocol_atomic_keyset(payload: dict[str, Any]) -> dict[str, Any]:
    """O58: validate all-or-none key ownership across protocol fallback."""

    keys = list(payload.get("keys", []))
    region_count = int(payload.get("region_count", 0))
    accepted_prefix = list(payload.get("accepted_prewrite_prefix", []))
    facts = {
        "region_count": region_count,
        "accepted_prefix_count": len(accepted_prefix),
        "key_count": len(keys),
        "fallback_witness": bool(payload.get("fallback_witness")),
        "duplicate_count": int(payload.get("duplicate_count", 0)),
    }
    invalid: list[str] = []
    if region_count < 2:
        invalid.append("multi-region setup is required")
    if len(keys) < 2:
        invalid.append("multi-key observation is required")
    if not accepted_prefix:
        invalid.append("no nonempty accepted prewrite prefix was witnessed")
    if not payload.get("fallback_witness"):
        invalid.append("protocol fallback transition was not witnessed")
    if any("visible" not in key or not key.get("txn_status") for key in keys):
        invalid.append("each key needs visibility and durable transaction status")
    if not payload.get("resolved_mode") and any(not key.get("mode") for key in keys):
        invalid.append("protocol mode evidence is required for every key or as resolved_mode")
    if invalid:
        return _result("INVALID", invalid, facts)

    visible = [bool(key["visible"]) for key in keys]
    statuses = {str(key["txn_status"]).lower() for key in keys}
    modes = {str(key["mode"]).lower() for key in keys if key.get("mode")}
    if payload.get("resolved_mode"):
        modes.add(str(payload["resolved_mode"]).lower())
    commit_ts = {str(key["commit_ts"]) for key in keys if key.get("commit_ts") is not None}
    table_index_mismatch = any(
        key.get("table_value") != key.get("index_value")
        for key in keys
        if "table_value" in key and "index_value" in key
    )
    all_visible = all(visible)
    none_visible = not any(visible)
    all_committed = statuses == {"committed"}
    all_absent = statuses.issubset({"absent", "rolled_back"})
    red: list[str] = []
    if not all_visible and not none_visible:
        red.append("fallback produced a partially visible mutation key set")
    if "committed" in statuses and statuses & {"absent", "rolled_back", "locked"}:
        red.append("fallback left mixed durable transaction statuses")
    if len(modes) > 1:
        red.append("keys retain incompatible protocol ownership")
    if len(commit_ts) > 1:
        red.append("committed keys have divergent commit timestamps")
    if table_index_mismatch:
        red.append("table and index observations disagree")
    if facts["duplicate_count"] > 1:
        red.append("fallback or replay duplicated the logical operation")
    facts.update(
        {
            "visible": visible,
            "statuses": sorted(statuses),
            "modes": sorted(modes),
            "commit_ts": sorted(commit_ts),
            "table_index_mismatch": table_index_mismatch,
        }
    )
    if red:
        return _result("RED", red, facts)
    if not ((all_visible and all_committed) or (none_visible and all_absent)):
        return _result("INVALID", ["key set has not reached one terminal outcome"], facts)
    return _result("GREEN", ["fallback preserved one atomic transaction key set"], facts)


ORACLES: dict[str, Callable[[dict[str, Any]], dict[str, Any]]] = {
    "terminal_mvcc_truth": terminal_mvcc_truth,
    "lock_generation_survival": lock_generation_survival,
    "protocol_atomic_keyset": protocol_atomic_keyset,
}


def evaluate(name: str, payload: dict[str, Any]) -> dict[str, Any]:
    try:
        oracle = ORACLES[name]
    except KeyError as exc:
        raise ValueError(f"unknown oracle {name!r}; choose from {sorted(ORACLES)}") from exc
    return {"oracle": name, **oracle(payload)}
