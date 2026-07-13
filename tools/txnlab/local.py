"""Pinned local TiKV runtime for realtikvtest experiments."""

from __future__ import annotations

import os
import re
import signal
import socket
import subprocess
import time
from pathlib import Path
from typing import Any

from .config import ExperimentConfig
from .process import CommandRunner


def pinned_worktree(config: ExperimentConfig, component: str) -> Path:
    if component not in config.sources:
        raise ValueError(f"source pin missing for component {component}")
    return config.workspace_root / "worktrees" / config.run_key / component


def local_binary_path(config: ExperimentConfig, component: str, profile: str = "debug") -> Path:
    if component != "tikv":
        raise ValueError("local binary builds currently support only tikv")
    if profile not in {"debug", "release"}:
        raise ValueError("profile must be debug or release")
    return pinned_worktree(config, component) / "target" / profile / "tikv-server"


def render_local_build_plan(
    config: ExperimentConfig, component: str, profile: str = "debug"
) -> dict[str, Any]:
    worktree = pinned_worktree(config, component)
    target = "build" if profile == "debug" else "release"
    return {
        "component": component,
        "profile": profile,
        "source_commit": config.sources[component].commit,
        "worktree": str(worktree),
        "command": ["make", target],
        "binary": str(local_binary_path(config, component, profile)),
    }


def _parse_version_output(output: str, source: str) -> dict[str, str]:
    commit_match = re.search(r"^Git Commit Hash:\s*([0-9a-fA-F]{7,40})\s*$", output, re.MULTILINE)
    if not commit_match:
        raise RuntimeError(f"cannot read Git Commit Hash from {source}")
    return {
        "git_commit": commit_match.group(1).lower(),
        "version_output": output.strip(),
    }


def inspect_binary(binary: Path, runner: CommandRunner) -> dict[str, str]:
    binary = binary.expanduser().resolve()
    if not binary.is_file():
        raise RuntimeError(f"binary does not exist: {binary}")
    result = runner.run([binary, "--version"])
    output = f"{result.stdout}\n{result.stderr}"
    return {"binary": str(binary), **_parse_version_output(output, f"{binary} --version")}


def inspect_tiup_nightly(component: str, runner: CommandRunner) -> dict[str, str]:
    result = runner.run(["tiup", f"{component}:nightly", "--version"])
    output = f"{result.stdout}\n{result.stderr}"
    return {
        "component": component,
        "channel": "nightly",
        **_parse_version_output(output, f"tiup {component}:nightly --version"),
    }


def refresh_tiup_nightly(component: str, runner: CommandRunner) -> dict[str, str]:
    runner.run(["tiup", "uninstall", f"{component}:nightly"], check=False)
    runner.run(["tiup", "install", f"{component}:nightly"])
    return inspect_tiup_nightly(component, runner)


def verify_pinned_binary(
    config: ExperimentConfig,
    component: str,
    binary: Path,
    runner: CommandRunner,
) -> dict[str, Any]:
    expected = config.sources[component].commit.lower()
    actual = inspect_binary(binary, runner)
    actual["expected_commit"] = expected
    actual["commit_verified"] = actual["git_commit"] == expected
    if not actual["commit_verified"]:
        raise RuntimeError(
            f"{component} binary commit mismatch: expected {expected}, got {actual['git_commit']}"
        )
    return actual


def build_local_component(
    config: ExperimentConfig,
    component: str,
    profile: str,
    runner: CommandRunner,
) -> dict[str, Any]:
    plan = render_local_build_plan(config, component, profile)
    worktree = Path(plan["worktree"])
    expected = config.sources[component].commit.lower()
    actual = runner.run(["git", "-C", worktree, "rev-parse", "HEAD"]).stdout.strip().lower()
    if actual != expected:
        raise RuntimeError(f"worktree commit mismatch: expected {expected}, got {actual}")
    runner.run(plan["command"], cwd=worktree)
    return {**plan, **verify_pinned_binary(config, component, Path(plan["binary"]), runner)}


def render_realtikvtest_plan(
    config: ExperimentConfig,
    test_name: str,
    package: str,
    tikv_binary: Path | None,
) -> dict[str, Any]:
    if not re.fullmatch(r"[A-Za-z0-9_]+", test_name):
        raise ValueError("test_name must be one Go test identifier")
    tidb_worktree = pinned_worktree(config, "tidb")
    playground_command = [
        "tiup",
        "playground",
        "nightly",
        "--db=0",
        "--kv=1",
        "--tiflash=0",
        "--without-monitor",
    ]
    if tikv_binary is not None:
        playground_command.append(f"--kv.binpath={tikv_binary.expanduser().resolve()}")
    return {
        "tidb_worktree": str(tidb_worktree),
        "tikv_binary": None
        if tikv_binary is None
        else str(tikv_binary.expanduser().resolve()),
        "runtime_mode": "refreshed_nightly" if tikv_binary is None else "exact_sha_binary",
        "playground_command": playground_command,
        "test_command": [
            "go",
            "test",
            "-v",
            "-count=1",
            "-run",
            f"^{test_name}$",
            "--tags=intest",
            package,
        ],
    }


def _port_open(host: str, port: int) -> bool:
    with socket.socket() as sock:
        sock.settimeout(0.2)
        return sock.connect_ex((host, port)) == 0


def _stop_process_group(proc: subprocess.Popen[Any]) -> None:
    if proc.poll() is not None:
        return
    try:
        os.killpg(proc.pid, signal.SIGTERM)
    except ProcessLookupError:
        return
    try:
        proc.wait(timeout=20)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(proc.pid, signal.SIGKILL)
        except ProcessLookupError:
            return
        proc.wait(timeout=5)


def run_realtikvtest(
    config: ExperimentConfig,
    test_name: str,
    package: str,
    tikv_binary: Path | None,
    runner: CommandRunner,
    timeout_seconds: int = 300,
) -> dict[str, Any]:
    binary: dict[str, Any]
    if tikv_binary is None:
        binary = inspect_tiup_nightly("tikv", runner)
        binary["expected_commit"] = config.sources["tikv"].commit.lower()
        binary["commit_verified"] = binary["git_commit"] == binary["expected_commit"]
        binary["verdict_scope"] = "capability_and_current_nightly_only"
    else:
        binary = verify_pinned_binary(config, "tikv", tikv_binary, runner)
        binary["verdict_scope"] = "exact_source_pin"
    plan = render_realtikvtest_plan(config, test_name, package, tikv_binary)
    if _port_open("127.0.0.1", 2379):
        raise RuntimeError("local PD port 2379 is already in use; refusing to reuse an unpinned cluster")

    stamp = time.strftime("%Y%m%d-%H%M%S")
    run_dir = config.workspace_root / "local-runs" / config.run_key / f"{stamp}-{test_name}"
    run_dir.mkdir(parents=True, exist_ok=False)
    playground_log = run_dir / "playground.log"
    test_log = run_dir / "test.log"
    started = time.monotonic()

    with playground_log.open("w") as pg_out:
        playground = subprocess.Popen(
            plan["playground_command"],
            cwd=plan["tidb_worktree"],
            stdout=pg_out,
            stderr=subprocess.STDOUT,
            text=True,
            start_new_session=True,
        )
        try:
            deadline = time.monotonic() + 90
            while time.monotonic() < deadline:
                if playground.poll() is not None:
                    raise RuntimeError(
                        f"playground exited before PD became ready; see {playground_log}"
                    )
                if _port_open("127.0.0.1", 2379):
                    break
                time.sleep(0.25)
            else:
                raise RuntimeError(f"timed out waiting for local PD; see {playground_log}")

            with test_log.open("w") as test_out:
                test = subprocess.run(
                    plan["test_command"],
                    cwd=plan["tidb_worktree"],
                    stdout=test_out,
                    stderr=subprocess.STDOUT,
                    text=True,
                    timeout=timeout_seconds,
                    check=False,
                )
        finally:
            _stop_process_group(playground)

    return {
        **plan,
        "binary": binary,
        "run_dir": str(run_dir),
        "playground_log": str(playground_log),
        "test_log": str(test_log),
        "test_returncode": test.returncode,
        "passed": test.returncode == 0,
        "duration_seconds": time.monotonic() - started,
    }
