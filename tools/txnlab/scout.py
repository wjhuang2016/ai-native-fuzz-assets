"""Bounded, packet-only AI source scouting."""

from __future__ import annotations

import hashlib
import json
import os
import re
import signal
import subprocess
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Sequence

from .config import ExperimentConfig


MAX_REGIONS = 12
MAX_REGION_LINES = 240
MAX_PACKET_LINES = 1200
MAX_PACKET_BYTES = 32 * 1024
MAX_QUESTION_CHARS = 4000
MAX_CANDIDATES = 3


class ScoutError(ValueError):
    """Raised when a source packet or scout result violates its contract."""


@dataclass(frozen=True)
class SourceRegion:
    component: str
    path: Path
    start: int
    end: int

    @classmethod
    def parse(cls, value: str) -> SourceRegion:
        try:
            component, path, start, end = value.rsplit(":", 3)
        except ValueError as exc:
            raise ScoutError(
                "region must use COMPONENT:PATH:START:END"
            ) from exc
        if not component or not path:
            raise ScoutError("region component and path are required")
        try:
            start_line = int(start)
            end_line = int(end)
        except ValueError as exc:
            raise ScoutError("region line numbers must be integers") from exc
        if start_line < 1 or end_line < start_line:
            raise ScoutError("region requires 1 <= START <= END")
        if end_line - start_line + 1 > MAX_REGION_LINES:
            raise ScoutError(f"one region may contain at most {MAX_REGION_LINES} lines")
        return cls(component, Path(path), start_line, end_line)


def _source_root(config: ExperimentConfig, component: str) -> tuple[Path, str]:
    if component not in config.sources:
        raise ScoutError(f"unknown source component: {component}")
    pin = config.sources[component]
    worktree = config.workspace_root / "worktrees" / config.run_key / component
    root = worktree if worktree.is_dir() else pin.repo
    return root.resolve(), pin.commit


def build_source_packet(
    config: ExperimentConfig,
    question: str,
    regions: Sequence[SourceRegion],
) -> dict[str, Any]:
    question = question.strip()
    if not question:
        raise ScoutError("question is required")
    if len(question) > MAX_QUESTION_CHARS:
        raise ScoutError(f"question exceeds {MAX_QUESTION_CHARS} characters")
    if not regions:
        raise ScoutError("at least one source region is required")
    if len(regions) > MAX_REGIONS:
        raise ScoutError(f"source packet may contain at most {MAX_REGIONS} regions")

    packet_regions: list[dict[str, Any]] = []
    total_lines = 0
    for region in regions:
        root, commit = _source_root(config, region.component)
        source = (root / region.path).resolve()
        if not source.is_relative_to(root):
            raise ScoutError(f"source path escapes {region.component} root: {region.path}")
        if not source.is_file():
            raise ScoutError(f"source file does not exist: {source}")
        lines = source.read_text(errors="replace").splitlines()
        if region.end > len(lines):
            raise ScoutError(
                f"region {region.component}:{region.path} ends at {region.end}, "
                f"but file has {len(lines)} lines"
            )
        selected = lines[region.start - 1 : region.end]
        total_lines += len(selected)
        if total_lines > MAX_PACKET_LINES:
            raise ScoutError(f"source packet may contain at most {MAX_PACKET_LINES} lines")
        numbered = "\n".join(
            f"{line_no:6d} {line}"
            for line_no, line in enumerate(selected, start=region.start)
        )
        packet_regions.append(
            {
                "component": region.component,
                "commit": commit,
                "path": region.path.as_posix(),
                "start": region.start,
                "end": region.end,
                "sha256": hashlib.sha256(numbered.encode()).hexdigest(),
                "content": numbered,
            }
        )

    packet = {
        "schema_version": 1,
        "question": question,
        "limits": {
            "regions": MAX_REGIONS,
            "lines": MAX_PACKET_LINES,
            "bytes": MAX_PACKET_BYTES,
            "candidates": MAX_CANDIDATES,
        },
        "regions": packet_regions,
    }
    encoded = json.dumps(packet, ensure_ascii=True, sort_keys=True).encode()
    if len(encoded) > MAX_PACKET_BYTES:
        raise ScoutError(f"source packet exceeds {MAX_PACKET_BYTES} bytes")
    packet["packet_bytes"] = len(encoded)
    packet["packet_sha256"] = hashlib.sha256(encoded).hexdigest()
    return packet


def render_scout_prompt(packet: dict[str, Any]) -> str:
    contract = {
        "scope": "short string",
        "candidates": [
            {
                "P": "checked fact",
                "Q": "inference made by code",
                "F": "user-visible failure",
                "owners": "durable owner and finalizer graph",
                "highest_consumer": "data loss, atomicity, false terminal truth, or serious liveness",
                "reachability": "supported TiDB SQL/config route",
                "schedule": "small deterministic schedule",
                "oracle": "strong public plus durable-state oracle",
                "confidence": "high|medium|low",
                "anchors": ["component/path:line"],
            }
        ],
        "retired": [],
    }
    return (
        "You are the reasoning stage of an authorized database quality workflow. "
        "Analyze only the source packet embedded below. Do not use tools, shell commands, "
        "the internet, issues, PRs, git history, or any file outside this prompt. Retire a shape "
        "when its highest consumer is only transient lock leakage, extra retry, logging, metrics, "
        "TiCDC-only impact, or eventual self-healing. Return JSON only, with no markdown. "
        f"Return at most {MAX_CANDIDATES} candidates. A candidate must contain every field in "
        f"this contract: {json.dumps(contract, ensure_ascii=True, sort_keys=True)}\n\n"
        f"SOURCE_PACKET={json.dumps(packet, ensure_ascii=True, sort_keys=True)}"
    )


def _extract_json(text: str) -> dict[str, Any]:
    try:
        value = json.loads(text)
    except json.JSONDecodeError:
        start = text.find("{")
        end = text.rfind("}")
        if start < 0 or end <= start:
            raise ScoutError("scout did not return a JSON object") from None
        try:
            value = json.loads(text[start : end + 1])
        except json.JSONDecodeError as exc:
            raise ScoutError(f"scout returned invalid JSON: {exc}") from exc
    if not isinstance(value, dict):
        raise ScoutError("scout result must be a JSON object")
    return value


def validate_scout_result(value: dict[str, Any]) -> None:
    for key in ("scope", "candidates", "retired"):
        if key not in value:
            raise ScoutError(f"scout result is missing {key}")
    candidates = value["candidates"]
    if not isinstance(candidates, list):
        raise ScoutError("scout candidates must be a list")
    if len(candidates) > MAX_CANDIDATES:
        raise ScoutError(f"scout returned more than {MAX_CANDIDATES} candidates")
    required = {
        "P",
        "Q",
        "F",
        "owners",
        "highest_consumer",
        "reachability",
        "schedule",
        "oracle",
        "confidence",
        "anchors",
    }
    for index, candidate in enumerate(candidates):
        if not isinstance(candidate, dict):
            raise ScoutError(f"candidate {index} must be an object")
        missing = sorted(required - candidate.keys())
        if missing:
            raise ScoutError(f"candidate {index} is missing fields: {', '.join(missing)}")
        if not isinstance(candidate["anchors"], list) or not candidate["anchors"]:
            raise ScoutError(f"candidate {index} requires at least one source anchor")


def _terminate_process_group(proc: subprocess.Popen[str]) -> None:
    try:
        os.killpg(proc.pid, signal.SIGTERM)
        proc.wait(timeout=3)
    except (ProcessLookupError, subprocess.TimeoutExpired):
        try:
            os.killpg(proc.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        proc.wait()


def run_source_packet_scout(
    config: ExperimentConfig,
    question: str,
    regions: Sequence[SourceRegion],
    output_dir: Path,
    *,
    timeout_seconds: int = 90,
    codex_binary: str = "codex",
) -> dict[str, Any]:
    if timeout_seconds < 5 or timeout_seconds > 600:
        raise ScoutError("timeout must be between 5 and 600 seconds")
    packet = build_source_packet(config, question, regions)
    prompt = render_scout_prompt(packet)
    output_dir = output_dir.expanduser().resolve()
    output_dir.mkdir(parents=True, exist_ok=True)
    packet_path = output_dir / "source-packet.json"
    prompt_path = output_dir / "prompt.txt"
    final_path = output_dir / "last-message.json"
    log_path = output_dir / "codex.log"
    result_path = output_dir / "result.json"
    packet_path.write_text(json.dumps(packet, indent=2, sort_keys=True) + "\n")
    prompt_path.write_text(prompt + "\n")

    argv = [
        codex_binary,
        "exec",
        "--ephemeral",
        "--ignore-user-config",
        "--skip-git-repo-check",
        "--sandbox",
        "read-only",
        "--cd",
        str(output_dir),
        "--color",
        "never",
        "--output-last-message",
        str(final_path),
        "-",
    ]
    # Deliberately no --output-schema: the configured provider does not support it.
    started = time.monotonic()
    timed_out = False
    with log_path.open("w") as log:
        proc = subprocess.Popen(
            argv,
            cwd=output_dir,
            stdin=subprocess.PIPE,
            stdout=log,
            stderr=subprocess.STDOUT,
            text=True,
            start_new_session=True,
        )
        try:
            proc.communicate(prompt, timeout=timeout_seconds)
        except subprocess.TimeoutExpired:
            timed_out = True
            _terminate_process_group(proc)

    result: dict[str, Any] = {
        "ok": False,
        "reason": "",
        "returncode": proc.returncode,
        "timed_out": timed_out,
        "duration_seconds": round(time.monotonic() - started, 3),
        "packet_sha256": packet["packet_sha256"],
        "packet_bytes": packet["packet_bytes"],
        "region_count": len(packet["regions"]),
        "log_bytes": log_path.stat().st_size,
        "output_dir": str(output_dir),
        "command": argv,
    }
    if timed_out:
        result["reason"] = "wall_clock_budget_exceeded"
    elif proc.returncode != 0:
        result["reason"] = "codex_exec_failed"
    elif not final_path.is_file():
        result["reason"] = "missing_last_message"
    else:
        try:
            value = _extract_json(final_path.read_text())
            validate_scout_result(value)
            result["ok"] = True
            result["result"] = value
            result["candidate_count"] = len(value["candidates"])
        except ScoutError as exc:
            result["reason"] = str(exc)

    match = re.search(r"tokens used\s+([0-9,]+)", log_path.read_text(errors="replace"))
    if match:
        result["reported_tokens"] = int(match.group(1).replace(",", ""))
    result_path.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n")
    return result
