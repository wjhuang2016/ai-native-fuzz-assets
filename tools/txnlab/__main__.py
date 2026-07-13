"""Command line interface for the transaction experiment control plane."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from .build import generate_build_script
from .config import config_summary, load_config
from .oracles import ORACLES, evaluate
from .process import CommandRunner
from .runner import TxnLab
from .workspace import prepare_worktrees


def _print(value: object) -> None:
    print(json.dumps(value, indent=2, sort_keys=True, ensure_ascii=True))


def main() -> None:
    parser = argparse.ArgumentParser(prog="python3 -m tools.txnlab")
    sub = parser.add_subparsers(dest="command", required=True)

    validate = sub.add_parser("validate", help="validate TOML without touching a cluster")
    validate.add_argument("config", type=Path)

    preflight = sub.add_parser("preflight", help="verify source, images, RBAC, and cluster health")
    preflight.add_argument("config", type=Path)
    preflight.add_argument("--prepare-worktrees", action="store_true")

    worktrees = sub.add_parser("prepare-worktrees", help="create isolated pinned source worktrees")
    worktrees.add_argument("config", type=Path)

    build = sub.add_parser("render-build", help="generate the official failpoint image build script")
    build.add_argument("config", type=Path)
    build.add_argument("component")
    build.add_argument("--output", "-o", type=Path)

    oracle = sub.add_parser("oracle", help="evaluate one evidence payload")
    oracle.add_argument("name", choices=sorted(ORACLES))
    oracle.add_argument("input", type=Path)

    run = sub.add_parser("run", help="run an experiment and always collect/clean evidence")
    run.add_argument("config", type=Path)
    run.add_argument("--allow-mutation", action="store_true")

    cleanup = sub.add_parser("cleanup", help="remove failpoints and Chaos objects named by a config")
    cleanup.add_argument("config", type=Path)

    args = parser.parse_args()
    if args.command == "validate":
        _print(config_summary(load_config(args.config)))
        return
    if args.command == "oracle":
        _print(evaluate(args.name, json.loads(args.input.read_text())))
        return

    config = load_config(args.config)
    if args.command == "preflight":
        _print(TxnLab(config).preflight(prepare=args.prepare_worktrees))
    elif args.command == "prepare-worktrees":
        _print(prepare_worktrees(config, CommandRunner()))
    elif args.command == "render-build":
        output = args.output or (
            config.workspace_root
            / "build"
            / config.run_key
            / args.component
            / "build-package-images.sh"
        )
        _print(generate_build_script(config, args.component, output, CommandRunner()))
    elif args.command == "run":
        result = TxnLab(config).run(allow_mutation=args.allow_mutation)
        _print(result)
        if not result["ok"]:
            sys.exit(2)
    elif args.command == "cleanup":
        _print(TxnLab(config).emergency_cleanup())


if __name__ == "__main__":
    main()
