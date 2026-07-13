"""Use PingCAP-QE/artifacts as the source of truth for failpoint images."""

from __future__ import annotations

import shutil
from pathlib import Path
from typing import Any

from .config import ExperimentConfig
from .process import CommandRunner


IMAGE_REPOS = {
    "tidb": "pingcap/tidb/images/tidb-server",
    "tikv": "tikv/tikv/image",
}


def _exact_tag(git_ref: str, commit: str, profile: str) -> str:
    ref = git_ref.replace("/", "-").lower()
    short = commit[:7]
    if not (ref == short or ref.endswith(f"-{short}") or ref.endswith(f"-g{short}")):
        ref = f"{ref}-{short}"
    return ref if profile == "release" else f"{ref}-{profile}"


def render_build_plan(config: ExperimentConfig, component: str, output: Path) -> dict[str, Any]:
    if config.build is None:
        raise ValueError("[build] is required to render an image build")
    if component not in IMAGE_REPOS:
        raise ValueError(f"official image build supports {sorted(IMAGE_REPOS)}")
    if component not in config.sources:
        raise ValueError(f"source pin missing for component {component}")
    source = config.sources[component]
    if not source.git_url:
        raise ValueError(f"sources.{component}.git_url is required for image builds")
    build = config.build
    generator = build.artifacts_repo / "packages/scripts/gen-package-images-with-config.sh"
    template = build.artifacts_repo / "packages/packages.yaml.tmpl"
    command = [
        str(generator),
        component,
        build.os,
        build.arch,
        build.release_version,
        build.profile,
        source.git_ref,
        source.commit,
        str(template),
        str(output.resolve()),
        build.registry,
        source.git_url,
    ]
    tag = _exact_tag(source.git_ref, source.commit, build.profile)
    return {
        "component": component,
        "generator": str(generator),
        "template": str(template),
        "command": command,
        "expected_multiarch_image": f"{build.registry}/{IMAGE_REPOS[component]}:{tag}",
        "expected_platform_image": f"{build.registry}/{IMAGE_REPOS[component]}:{tag}_{build.os}_{build.arch}",
        "source_commit": source.commit,
        "artifacts_commit": build.artifacts_commit,
        "profile": build.profile,
        "output": str(output.resolve()),
    }


def generate_build_script(
    config: ExperimentConfig,
    component: str,
    output: Path,
    runner: CommandRunner,
) -> dict[str, Any]:
    plan = render_build_plan(config, component, output)
    for command in ("yq", "gomplate"):
        if not shutil.which(command):
            raise RuntimeError(f"{command} is required by PingCAP-QE/artifacts")
    generator = Path(plan["generator"])
    template = Path(plan["template"])
    if not generator.is_file() or not template.is_file():
        raise RuntimeError(
            f"artifacts checkout is incomplete: {config.build.artifacts_repo if config.build else ''}"
        )
    actual_artifacts_commit = runner.run(
        ["git", "-C", config.build.artifacts_repo, "rev-parse", "HEAD"]
    ).stdout.strip()
    if config.build.artifacts_commit and actual_artifacts_commit != config.build.artifacts_commit:
        raise RuntimeError(
            "PingCAP-QE/artifacts checkout mismatch: "
            f"expected {config.build.artifacts_commit}, got {actual_artifacts_commit}"
        )
    output = output.expanduser().resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    workdir = output.parent / "render-context"
    workdir.mkdir(parents=True, exist_ok=True)
    result = runner.run(plan["command"], cwd=workdir)
    if not output.is_file():
        raise RuntimeError(f"official generator did not create {output}")
    return {
        **plan,
        "actual_artifacts_commit": actual_artifacts_commit,
        "generated": True,
        "stdout": result.stdout.strip(),
        "next_step": (
            "Run the generated script in the PingCAP image-builder environment with registry "
            "credentials; generation alone does not build or push an image."
        ),
    }
