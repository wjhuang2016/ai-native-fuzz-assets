"""TOML configuration model for transaction experiments."""

from __future__ import annotations

import re
import tomllib
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any


PHASES = ("prepare", "arm", "workload", "observe", "cleanup")
ACTION_KINDS = {
    "command",
    "sql",
    "failpoint_arm",
    "failpoint_disarm",
    "chaos_apply",
    "chaos_delete",
    "image_switch",
    "wait_log",
    "collect",
    "sleep",
}
COMPONENTS = {"tidb", "tikv", "pd"}


class ConfigError(ValueError):
    """Raised when an experiment configuration is incomplete or unsafe."""


@dataclass(frozen=True)
class SourcePin:
    name: str
    repo: Path
    commit: str
    git_url: str = ""
    git_ref: str = "master"


@dataclass(frozen=True)
class ImagePin:
    component: str
    base: str
    tag: str
    expected_commit: str = ""
    profile: str = "failpoint"

    @property
    def reference(self) -> str:
        return f"{self.base}:{self.tag}"


@dataclass(frozen=True)
class ClusterConfig:
    kubeconfig: Path
    namespace: str
    tc_name: str = "tc"
    mysql_service: str = "tc-tidb"
    namespace_pattern: str = r"^testbed-[a-z0-9-]+$"
    rollout_timeout_seconds: int = 900
    enable_tidb_test_api: bool = False


@dataclass(frozen=True)
class AssetConfig:
    store_py: Path
    sqlite_db: Path
    auto_import: bool = False


@dataclass(frozen=True)
class BuildConfig:
    artifacts_repo: Path
    artifacts_commit: str = ""
    release_version: str = "v9.0.0-alpha"
    registry: str = "us-docker.pkg.dev/pingcap-testing-account/hub"
    os: str = "linux"
    arch: str = "amd64"
    profile: str = "failpoint"


@dataclass
class ExperimentConfig:
    path: Path
    schema_version: int
    run_key: str
    obligation_key: str
    selector: str
    module: str
    environment: str
    evidence_root: Path
    workspace_root: Path
    allow_mutation: bool
    auto_cleanup: bool
    restore_images_on_cleanup: bool
    collect_logs: bool
    sources: dict[str, SourcePin] = field(default_factory=dict)
    images: dict[str, ImagePin] = field(default_factory=dict)
    cluster: ClusterConfig | None = None
    assets: AssetConfig | None = None
    build: BuildConfig | None = None
    actions: list[dict[str, Any]] = field(default_factory=list)
    oracle: dict[str, Any] = field(default_factory=dict)
    metadata: dict[str, Any] = field(default_factory=dict)

    @property
    def base_dir(self) -> Path:
        return self.path.parent

    def actions_for(self, phase: str) -> list[dict[str, Any]]:
        return [
            action
            for action in self.actions
            if action["phase"] == phase and action.get("enabled", True)
        ]


def _resolve(base: Path, value: str) -> Path:
    path = Path(value).expanduser()
    return path if path.is_absolute() else (base / path).resolve()


def _required(data: dict[str, Any], key: str, where: str) -> Any:
    value = data.get(key)
    if value is None or value == "":
        raise ConfigError(f"{where}.{key} is required")
    return value


def load_config(path: Path) -> ExperimentConfig:
    path = path.expanduser().resolve()
    with path.open("rb") as fh:
        raw = tomllib.load(fh)
    base = path.parent

    schema_version = int(raw.get("schema_version", 0))
    if schema_version != 1:
        raise ConfigError(f"unsupported schema_version {schema_version}; expected 1")

    sources: dict[str, SourcePin] = {}
    for name, item in raw.get("sources", {}).items():
        sources[name] = SourcePin(
            name=name,
            repo=_resolve(base, _required(item, "repo", f"sources.{name}")),
            commit=str(_required(item, "commit", f"sources.{name}")),
            git_url=str(item.get("git_url", "")),
            git_ref=str(item.get("git_ref", "master")),
        )

    images: dict[str, ImagePin] = {}
    for name, item in raw.get("images", {}).items():
        if name not in COMPONENTS:
            raise ConfigError(f"images.{name}: unsupported component")
        images[name] = ImagePin(
            component=name,
            base=str(_required(item, "base", f"images.{name}")),
            tag=str(_required(item, "tag", f"images.{name}")),
            expected_commit=str(item.get("expected_commit", "")),
            profile=str(item.get("profile", "failpoint")),
        )

    cluster = None
    if "cluster" in raw:
        item = raw["cluster"]
        cluster = ClusterConfig(
            kubeconfig=_resolve(base, _required(item, "kubeconfig", "cluster")),
            namespace=str(_required(item, "namespace", "cluster")),
            tc_name=str(item.get("tc_name", "tc")),
            mysql_service=str(item.get("mysql_service", "tc-tidb")),
            namespace_pattern=str(item.get("namespace_pattern", r"^testbed-[a-z0-9-]+$")),
            rollout_timeout_seconds=int(item.get("rollout_timeout_seconds", 900)),
            enable_tidb_test_api=bool(item.get("enable_tidb_test_api", False)),
        )

    assets = None
    if "assets" in raw:
        item = raw["assets"]
        assets = AssetConfig(
            store_py=_resolve(base, _required(item, "store_py", "assets")),
            sqlite_db=_resolve(base, _required(item, "sqlite_db", "assets")),
            auto_import=bool(item.get("auto_import", False)),
        )

    build = None
    if "build" in raw:
        item = raw["build"]
        build = BuildConfig(
            artifacts_repo=_resolve(base, _required(item, "artifacts_repo", "build")),
            artifacts_commit=str(item.get("artifacts_commit", "")),
            release_version=str(item.get("release_version", "v9.0.0-alpha")),
            registry=str(item.get("registry", "us-docker.pkg.dev/pingcap-testing-account/hub")),
            os=str(item.get("os", "linux")),
            arch=str(item.get("arch", "amd64")),
            profile=str(item.get("profile", "failpoint")),
        )

    actions = list(raw.get("actions", []))
    for index, action in enumerate(actions):
        where = f"actions[{index}]"
        phase = str(_required(action, "phase", where))
        kind = str(_required(action, "kind", where))
        if phase not in PHASES:
            raise ConfigError(f"{where}.phase must be one of {PHASES}")
        if kind not in ACTION_KINDS:
            raise ConfigError(f"{where}.kind unsupported: {kind}")
        action.setdefault("name", f"{phase}-{index:02d}-{kind}")
        action.setdefault("enabled", True)
        if not re.fullmatch(r"[A-Za-z0-9](?:[A-Za-z0-9._-]{0,61}[A-Za-z0-9])?", action["name"]):
            raise ConfigError(
                f"{where}.name must be a Kubernetes-safe value no longer than 63 characters"
            )

    config = ExperimentConfig(
        path=path,
        schema_version=schema_version,
        run_key=str(_required(raw, "run_key", "root")),
        obligation_key=str(_required(raw, "obligation_key", "root")),
        selector=str(_required(raw, "selector", "root")),
        module=str(raw.get("module", "txn/cross-layer")),
        environment=str(raw.get("environment", "local")),
        evidence_root=_resolve(base, str(raw.get("evidence_root", "runs"))),
        workspace_root=_resolve(base, str(raw.get("workspace_root", ".txnlab"))),
        allow_mutation=bool(raw.get("allow_mutation", False)),
        auto_cleanup=bool(raw.get("auto_cleanup", True)),
        restore_images_on_cleanup=bool(raw.get("restore_images_on_cleanup", True)),
        collect_logs=bool(raw.get("collect_logs", True)),
        sources=sources,
        images=images,
        cluster=cluster,
        assets=assets,
        build=build,
        actions=actions,
        oracle=dict(raw.get("oracle", {})),
        metadata=dict(raw.get("metadata", {})),
    )
    validate_config(config)
    return config


def validate_config(config: ExperimentConfig) -> None:
    if not re.fullmatch(r"[A-Za-z0-9._-]+", config.run_key):
        raise ConfigError("run_key may contain only letters, digits, dot, underscore, and dash")
    if config.environment not in {"local", "testbed"}:
        raise ConfigError("environment must be local or testbed")
    if config.environment == "testbed" and config.cluster is None:
        raise ConfigError("testbed environment requires [cluster]")
    if config.environment == "testbed" and len(config.run_key) > 63:
        raise ConfigError("testbed run_key must be no longer than 63 characters")
    if config.cluster is not None:
        if not re.match(config.cluster.namespace_pattern, config.cluster.namespace):
            raise ConfigError(
                f"namespace {config.cluster.namespace!r} does not match safety pattern "
                f"{config.cluster.namespace_pattern!r}"
            )
    for name, pin in config.sources.items():
        if not re.fullmatch(r"[0-9a-fA-F]{7,40}", pin.commit):
            raise ConfigError(f"sources.{name}.commit is not a Git commit hash")
    if config.build:
        if config.build.artifacts_commit and not re.fullmatch(
            r"[0-9a-fA-F]{7,40}", config.build.artifacts_commit
        ):
            raise ConfigError("build.artifacts_commit is not a Git commit hash")
        if config.build.os != "linux":
            raise ConfigError("official image builds require build.os=linux")
        if config.build.arch not in {"amd64", "arm64"}:
            raise ConfigError("build.arch must be amd64 or arm64")
        if config.build.profile != "failpoint":
            raise ConfigError("txnlab build.profile must be failpoint")
    if any(
        action.get("enabled", True)
        and (
            action["kind"].startswith("failpoint")
            or action["kind"].startswith("chaos")
            or action["kind"] == "image_switch"
        )
        for action in config.actions
    ):
        if config.cluster is None:
            raise ConfigError("cluster mutation actions require [cluster]")
    mutating_testbed = config.environment == "testbed" and any(
        action.get("enabled", True)
        and (
            action["kind"] in {
                "sql",
                "failpoint_arm",
                "failpoint_disarm",
                "chaos_apply",
                "chaos_delete",
                "image_switch",
            }
            or (action["kind"] == "command" and not action.get("read_only", False))
            or bool(action.get("mutates_cluster"))
        )
        for action in config.actions
    )
    if mutating_testbed and not config.auto_cleanup:
        raise ConfigError("mutating testbed experiments require auto_cleanup=true")
    for index, action in enumerate(config.actions):
        if action.get("enabled", True) and action["kind"].startswith("failpoint"):
            if not action.get("pod") and not action.get("pods"):
                raise ConfigError(f"actions[{index}] must select pod or pods explicitly")


def config_summary(config: ExperimentConfig) -> dict[str, Any]:
    return {
        "run_key": config.run_key,
        "obligation_key": config.obligation_key,
        "selector": config.selector,
        "module": config.module,
        "environment": config.environment,
        "allow_mutation": config.allow_mutation,
        "auto_cleanup": config.auto_cleanup,
        "sources": {
            name: {"repo": str(pin.repo), "commit": pin.commit, "git_ref": pin.git_ref}
            for name, pin in sorted(config.sources.items())
        },
        "images": {name: pin.reference for name, pin in sorted(config.images.items())},
        "cluster": None
        if config.cluster is None
        else {
            "kubeconfig": str(config.cluster.kubeconfig),
            "namespace": config.cluster.namespace,
            "tc_name": config.cluster.tc_name,
        },
        "build": None
        if config.build is None
        else {
            "artifacts_repo": str(config.build.artifacts_repo),
            "artifacts_commit": config.build.artifacts_commit,
            "release_version": config.build.release_version,
            "registry": config.build.registry,
            "os": config.build.os,
            "arch": config.build.arch,
            "profile": config.build.profile,
        },
        "actions": [
            {
                "phase": action["phase"],
                "kind": action["kind"],
                "name": action["name"],
                "enabled": action.get("enabled", True),
            }
            for action in config.actions
        ],
        "oracle": config.oracle,
    }
