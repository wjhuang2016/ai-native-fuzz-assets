"""Command line interface for the transaction experiment control plane."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from .build import generate_build_script
from .config import config_summary, load_config
from .local import (
    build_local_component,
    local_binary_path,
    refresh_tiup_nightly,
    run_realtikvtest,
    verify_pinned_binary,
)
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

    local_build = sub.add_parser("local-build", help="build an exact-SHA local component binary")
    local_build.add_argument("config", type=Path)
    local_build.add_argument("component", choices=["tikv"])
    local_build.add_argument("--profile", choices=["debug", "release"], default="debug")

    local_verify = sub.add_parser(
        "local-verify", help="verify that a local binary matches the configured source SHA"
    )
    local_verify.add_argument("config", type=Path)
    local_verify.add_argument("component", choices=["tikv"])
    local_verify.add_argument("--profile", choices=["debug", "release"], default="debug")
    local_verify.add_argument("--binary", type=Path)

    refresh_nightly = sub.add_parser(
        "refresh-nightly", help="remove a cached TiUP nightly component and download it again"
    )
    refresh_nightly.add_argument("component", choices=["tikv"])

    realtikvtest = sub.add_parser(
        "realtikvtest", help="run one test against exact-SHA or recorded-nightly TiKV and clean up"
    )
    realtikvtest.add_argument("config", type=Path)
    realtikvtest.add_argument("test_name")
    realtikvtest.add_argument(
        "--package", default="./tests/realtikvtest/txntest/...", help="Go package pattern"
    )
    realtikvtest.add_argument("--profile", choices=["debug", "release"], default="debug")
    realtikvtest.add_argument("--tikv-binary", type=Path)
    realtikvtest.add_argument(
        "--nightly",
        action="store_true",
        help="use the installed nightly and record its commit without claiming exact-SHA coverage",
    )
    realtikvtest.add_argument("--timeout", type=int, default=300)

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

    if args.command == "refresh-nightly":
        _print(refresh_tiup_nightly(args.component, CommandRunner()))
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
    elif args.command == "local-build":
        _print(build_local_component(config, args.component, args.profile, CommandRunner()))
    elif args.command == "local-verify":
        binary = args.binary or local_binary_path(config, args.component, args.profile)
        _print(verify_pinned_binary(config, args.component, binary, CommandRunner()))
    elif args.command == "realtikvtest":
        if args.nightly and args.tikv_binary:
            parser.error("--nightly and --tikv-binary are mutually exclusive")
        binary = None if args.nightly else (
            args.tikv_binary or local_binary_path(config, "tikv", args.profile)
        )
        result = run_realtikvtest(
            config,
            args.test_name,
            args.package,
            binary,
            CommandRunner(),
            timeout_seconds=args.timeout,
        )
        _print(result)
        if not result["passed"]:
            sys.exit(2)
    elif args.command == "run":
        result = TxnLab(config).run(allow_mutation=args.allow_mutation)
        _print(result)
        if not result["ok"]:
            sys.exit(2)
    elif args.command == "cleanup":
        _print(TxnLab(config).emergency_cleanup())


if __name__ == "__main__":
    main()
