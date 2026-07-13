"""Pinned source checkout preparation using isolated Git worktrees."""

from __future__ import annotations

import shutil
from typing import Any

from .config import ExperimentConfig
from .process import CommandRunner


def validate_source_pins(config: ExperimentConfig, runner: CommandRunner) -> dict[str, Any]:
    results: dict[str, Any] = {}
    for name, pin in sorted(config.sources.items()):
        if not (pin.repo / ".git").exists():
            raise RuntimeError(f"source repo is not a Git checkout: {pin.repo}")
        runner.run(["git", "-C", pin.repo, "cat-file", "-e", f"{pin.commit}^{{commit}}"])
        resolved = runner.run(
            ["git", "-C", pin.repo, "rev-parse", pin.commit]
        ).stdout.strip()
        status = runner.run(
            ["git", "-C", pin.repo, "status", "--porcelain"], check=True
        ).stdout.splitlines()
        results[name] = {
            "repo": str(pin.repo),
            "requested_commit": pin.commit,
            "resolved_commit": resolved,
            "dirty_paths": len(status),
        }
    return results


def prepare_worktrees(config: ExperimentConfig, runner: CommandRunner) -> dict[str, Any]:
    validate_source_pins(config, runner)
    root = config.workspace_root / "worktrees" / config.run_key
    root.mkdir(parents=True, exist_ok=True)
    output: dict[str, Any] = {}
    for name, pin in sorted(config.sources.items()):
        target = root / name
        if target.exists():
            current = runner.run(["git", "-C", target, "rev-parse", "HEAD"]).stdout.strip()
            wanted = runner.run(
                ["git", "-C", pin.repo, "rev-parse", pin.commit]
            ).stdout.strip()
            if current != wanted:
                raise RuntimeError(f"existing worktree {target} is at {current}, expected {wanted}")
        else:
            runner.run(["git", "-C", pin.repo, "worktree", "add", "--detach", target, pin.commit])
        output[name] = {
            "path": str(target),
            "commit": runner.run(["git", "-C", target, "rev-parse", "HEAD"]).stdout.strip(),
        }
    return output


def remove_worktrees(config: ExperimentConfig, runner: CommandRunner) -> None:
    root = config.workspace_root / "worktrees" / config.run_key
    if not root.exists():
        return
    for name, pin in sorted(config.sources.items()):
        target = root / name
        if target.exists():
            runner.run(["git", "-C", pin.repo, "worktree", "remove", "--force", target])
    shutil.rmtree(root, ignore_errors=True)
