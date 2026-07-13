"""Transaction experiment runner with evidence-first cleanup semantics."""

from __future__ import annotations

import json
import os
import hashlib
import shutil
import time
import traceback
from pathlib import Path
from typing import Any

from .assets import import_run_record, make_run_record, promotion_candidate, write_run_record
from .config import PHASES, ExperimentConfig, config_summary
from .evidence import EvidenceBundle, utc_now
from .kube import Kubectl, label_chaos_object
from .oracles import evaluate
from .process import CommandRunner
from .workspace import prepare_worktrees, validate_source_pins


MUTATING_ACTIONS = {
    "sql",
    "failpoint_arm",
    "failpoint_disarm",
    "chaos_apply",
    "chaos_delete",
    "image_switch",
}


class TxnLab:
    def __init__(self, config: ExperimentConfig):
        self.config = config
        self.bundle: EvidenceBundle | None = None
        self.runner = CommandRunner()
        self.kube = Kubectl(config.cluster, self.runner) if config.cluster else None
        self.worktrees: dict[str, dict[str, Any]] = {}
        self.active_failpoints: list[dict[str, str]] = []
        self.original_image_state: dict[str, Any] | None = None
        self.started_at = utc_now()

    def _connect_bundle(self, bundle: EvidenceBundle) -> None:
        self.bundle = bundle
        self.runner.event_sink = bundle.event

    def _require_tools(self) -> dict[str, str]:
        required = {"git", "python3"}
        if self.config.cluster:
            required.add("kubectl")
        if self.config.images:
            required.add("skopeo")
        if any(action.get("enabled", True) and action["kind"] == "sql" for action in self.config.actions):
            if not (shutil.which("mysql") or shutil.which("mariadb")):
                raise RuntimeError("mysql or mariadb client is required for SQL actions")
        found: dict[str, str] = {}
        for command in sorted(required):
            path = shutil.which(command)
            if not path:
                raise RuntimeError(f"required command is missing: {command}")
            found[command] = path
        return found

    def _check_images(self) -> dict[str, Any]:
        checked: dict[str, Any] = {}
        for name, image in sorted(self.config.images.items()):
            result = self.runner.run(
                [
                    "skopeo",
                    "inspect",
                    "--override-os",
                    "linux",
                    "--override-arch",
                    "amd64",
                    f"docker://{image.reference}",
                ],
                timeout=120,
                check=False,
            )
            metadata = json.loads(result.stdout) if result.returncode == 0 else {}
            labels = metadata.get("Labels") or {}
            actual_commit = (
                labels.get("net.pingcap.tibuild.git-sha")
                or labels.get("org.opencontainers.image.revision")
            )
            checked[name] = {
                "reference": image.reference,
                "expected_commit": image.expected_commit,
                "available": result.returncode == 0,
                "digest": metadata.get("Digest"),
                "architecture": metadata.get("Architecture"),
                "actual_commit": actual_commit,
                "commit_verified": bool(image.expected_commit and actual_commit == image.expected_commit),
                "error": result.stderr.strip() if result.returncode else None,
            }
            if result.returncode != 0:
                raise RuntimeError(f"image is unavailable: {image.reference}: {result.stderr.strip()}")
            if image.expected_commit and actual_commit != image.expected_commit:
                raise RuntimeError(
                    f"image commit mismatch for {image.reference}: "
                    f"expected {image.expected_commit}, got {actual_commit or 'no revision label'}"
                )
        return checked

    def preflight(self, *, prepare: bool = False) -> dict[str, Any]:
        tools = self._require_tools()
        source_state = (
            prepare_worktrees(self.config, self.runner)
            if prepare
            else validate_source_pins(self.config, self.runner)
        )
        images = self._check_images() if self.config.images else {}
        cluster_state = self.kube.preflight() if self.kube else None
        if cluster_state and not cluster_state["ready"]:
            raise RuntimeError("TidbCluster is not Ready")
        denied = [key for key, value in (cluster_state or {}).get("access", {}).items() if not value]
        return {
            "config": config_summary(self.config),
            "tools": tools,
            "sources": source_state,
            "images": images,
            "cluster": cluster_state,
            "access_warnings": denied,
        }

    def _mutation_required(self) -> bool:
        return self.config.environment == "testbed" and any(
            action.get("enabled", True)
            and (
                action["kind"] in MUTATING_ACTIONS
                or (action["kind"] == "command" and not action.get("read_only", False))
                or bool(action.get("mutates_cluster"))
            )
            for action in self.config.actions
        )

    def _check_mutation_gate(self, cli_allow_mutation: bool) -> None:
        if not self._mutation_required():
            return
        if not self.config.allow_mutation or not cli_allow_mutation:
            raise RuntimeError(
                "testbed mutation requires both allow_mutation=true in TOML and --allow-mutation"
            )

    def _environment(self) -> dict[str, str]:
        assert self.bundle is not None
        env = {
            "TXNLAB_RUN_KEY": self.config.run_key,
            "TXNLAB_RUN_DIR": str(self.bundle.root),
        }
        for name, item in self.worktrees.items():
            env[f"TXNLAB_WORKTREE_{name.upper().replace('-', '_')}"] = str(item["path"])
        if self.config.cluster:
            env["KUBECONFIG"] = str(self.config.cluster.kubeconfig)
            env["TXNLAB_NAMESPACE"] = self.config.cluster.namespace
        return env

    def _resolve_path(self, value: str, *, prefer_run: bool = False) -> Path:
        assert self.bundle is not None
        path = Path(value).expanduser()
        if path.is_absolute():
            return path
        run_path = self.bundle.root / path
        if prefer_run and run_path.exists():
            return run_path
        return (self.config.base_dir / path).resolve()

    def _write_action_output(self, action: dict[str, Any], result: dict[str, Any]) -> None:
        assert self.bundle is not None
        safe_name = action["name"].replace("/", "_")
        self.bundle.write_json(f"actions/{safe_name}.json", result)

    def _run_command(self, action: dict[str, Any]) -> dict[str, Any]:
        argv = action.get("argv")
        if not isinstance(argv, list) or not argv:
            raise ValueError(f"{action['name']}: command action requires argv array")
        cwd = self._resolve_path(str(action["cwd"])) if action.get("cwd") else None
        env = self._environment()
        env.update({str(key): str(value) for key, value in action.get("env", {}).items()})
        result = self.runner.run(
            argv,
            cwd=cwd,
            env=env,
            timeout=float(action.get("timeout_seconds", 900)),
            check=bool(action.get("check", True)),
        )
        return {
            "argv": result.argv,
            "returncode": result.returncode,
            "stdout": result.stdout,
            "stderr": result.stderr,
            "duration_seconds": result.duration_seconds,
        }

    def _run_sql(self, action: dict[str, Any]) -> dict[str, Any]:
        if self.kube is None or self.config.cluster is None:
            raise RuntimeError("SQL action currently requires a testbed cluster")
        sql = action.get("sql")
        if action.get("file"):
            sql = self._resolve_path(str(action["file"]), prefer_run=True).read_text()
        if not isinstance(sql, str) or not sql.strip():
            raise ValueError(f"{action['name']}: SQL action requires sql or file")
        mysql = shutil.which("mysql") or shutil.which("mariadb")
        assert mysql is not None
        with self.kube.port_forward(f"service/{self.config.cluster.mysql_service}", 4000) as forward:
            argv = [
                mysql,
                "--host=127.0.0.1",
                f"--port={forward.local_port}",
                f"--user={action.get('user', 'root')}",
                "--batch",
                "--raw",
            ]
            if action.get("database"):
                argv.append(str(action["database"]))
            env = self._environment()
            password_env = action.get("password_env")
            if password_env and os.environ.get(str(password_env)):
                env["MYSQL_PWD"] = os.environ[str(password_env)]
            result = self.runner.run(
                argv,
                env=env,
                input_text=sql,
                timeout=float(action.get("timeout_seconds", 300)),
                check=bool(action.get("check", True)),
            )
        return {
            "returncode": result.returncode,
            "stdout": result.stdout,
            "stderr": result.stderr,
            "sql_sha256": hashlib.sha256(sql.encode()).hexdigest(),
        }

    def _selected_pods(self, action: dict[str, Any], component: str) -> list[str]:
        assert self.kube is not None
        if action.get("pod"):
            return [str(action["pod"])]
        if action.get("pods"):
            return [str(item) for item in action["pods"]]
        return self.kube.pod_names(component)

    def _arm_failpoint(self, action: dict[str, Any]) -> dict[str, Any]:
        assert self.kube is not None
        component = str(action["component"])
        name = str(action["failpoint"])
        value = str(action.get("value", "return"))
        armed = []
        for pod in self._selected_pods(action, component):
            item = self.kube.set_failpoint(component, pod, name, value)
            self.active_failpoints.append({"component": component, "pod": pod, "name": name})
            armed.append(item)
        return {"armed": armed}

    def _disarm_failpoint(self, action: dict[str, Any]) -> dict[str, Any]:
        assert self.kube is not None
        component = str(action["component"])
        name = str(action["failpoint"])
        cleared = []
        for pod in self._selected_pods(action, component):
            cleared.append(self.kube.delete_failpoint(component, pod, name))
            self.active_failpoints = [
                item
                for item in self.active_failpoints
                if not (item["component"] == component and item["pod"] == pod and item["name"] == name)
            ]
        return {"cleared": cleared}

    def _execute_action(self, action: dict[str, Any]) -> dict[str, Any]:
        kind = action["kind"]
        if kind == "command":
            return self._run_command(action)
        if kind == "sql":
            return self._run_sql(action)
        if kind == "failpoint_arm":
            return self._arm_failpoint(action)
        if kind == "failpoint_disarm":
            return self._disarm_failpoint(action)
        if kind == "chaos_apply":
            assert self.kube is not None
            obj = json.loads(self._resolve_path(str(action["manifest"])).read_text())
            obj = label_chaos_object(obj, self.config.run_key, action["name"])
            return self.kube.apply_object(obj)
        if kind == "chaos_delete":
            assert self.kube is not None
            return self.kube.delete_chaos_for_run(self.config.run_key)
        if kind == "image_switch":
            assert self.kube is not None
            components = list(action.get("components", self.config.images))
            selected = {name: self.config.images[name] for name in components}
            if self.original_image_state is None:
                self.original_image_state = self.kube.image_state()
            before = self.kube.patch_images(
                selected,
                enable_tidb_test_api=self.config.cluster.enable_tidb_test_api,
            )
            return {"before": before, "selected": {key: value.reference for key, value in selected.items()}}
        if kind == "wait_log":
            assert self.kube is not None
            return self.kube.wait_for_log(
                str(action["component"]),
                str(action["pattern"]),
                self.started_at,
                int(action.get("timeout_seconds", 60)),
            )
        if kind == "collect":
            assert self.bundle is not None
            if self.kube is None:
                return {"collected": "local action outputs only"}
            snapshot = self.kube.snapshot()
            self.bundle.write_json(f"snapshots/{action['name']}.json", snapshot)
            return {"snapshot": f"snapshots/{action['name']}.json"}
        if kind == "sleep":
            seconds = float(action.get("seconds", 1))
            time.sleep(seconds)
            return {"slept_seconds": seconds}
        raise ValueError(f"unsupported action kind: {kind}")

    def _run_phase(self, phase: str) -> None:
        assert self.bundle is not None
        self.bundle.event({"type": "phase_start", "phase": phase})
        for action in self.config.actions_for(phase):
            self.bundle.event(
                {"type": "action_start", "phase": phase, "name": action["name"], "kind": action["kind"]}
            )
            result = self._execute_action(action)
            self._write_action_output(action, result)
            self.bundle.event({"type": "action_finish", "phase": phase, "name": action["name"]})
        self.bundle.event({"type": "phase_finish", "phase": phase})

    def _evaluate_oracle(self) -> dict[str, Any] | None:
        if not self.config.oracle or not self.config.oracle.get("name"):
            return None
        name = str(self.config.oracle["name"])
        input_file = str(self.config.oracle.get("input_file", "oracle-input.json"))
        path = self._resolve_path(input_file, prefer_run=True)
        payload = json.loads(path.read_text())
        result = evaluate(name, payload)
        assert self.bundle is not None
        self.bundle.write_json("oracle-input.json", payload)
        self.bundle.write_json("oracle-result.json", result)
        return result

    def _automatic_cleanup(self) -> list[dict[str, Any]]:
        cleaned: list[dict[str, Any]] = []
        if self.kube is None:
            return cleaned
        for item in reversed(self.active_failpoints):
            try:
                cleaned.append({"failpoint": self.kube.delete_failpoint(**item)})
            except Exception as exc:  # cleanup must continue through independent resources
                cleaned.append({"failpoint_error": str(exc), "target": item})
        self.active_failpoints.clear()
        try:
            cleaned.append({"chaos": self.kube.delete_chaos_for_run(self.config.run_key)})
        except Exception as exc:
            cleaned.append({"chaos_error": str(exc)})
        if self.original_image_state and self.config.restore_images_on_cleanup:
            try:
                self.kube.restore_images(self.original_image_state)
                cleaned.append({"images_restored": True})
            except Exception as exc:
                cleaned.append({"image_restore_error": str(exc)})
        return cleaned

    def run(self, *, allow_mutation: bool = False) -> dict[str, Any]:
        self._check_mutation_gate(allow_mutation)
        bundle = EvidenceBundle(self.config.evidence_root, self.config.run_key)
        self._connect_bundle(bundle)
        source_state: dict[str, Any] = {}
        cluster_state: dict[str, Any] | None = None
        oracle_result: dict[str, Any] | None = None
        error: str | None = None
        cleanup: list[dict[str, Any]] = []
        verdict = "INFO"
        try:
            bundle.write_json("config-summary.json", config_summary(self.config))
            preflight = self.preflight(prepare=True)
            bundle.write_json("preflight.json", preflight)
            source_state = preflight["sources"]
            self.worktrees = {
                name: item for name, item in source_state.items() if "path" in item
            }
            cluster_state = preflight.get("cluster")
            if self.kube:
                bundle.write_json("snapshots/before.json", self.kube.snapshot())
            for phase in PHASES:
                if phase != "cleanup":
                    self._run_phase(phase)
            oracle_result = self._evaluate_oracle()
            verdict = oracle_result["verdict"] if oracle_result else "INFO"
        except Exception as exc:
            error = f"{type(exc).__name__}: {exc}"
            verdict = "INVALID"
            bundle.write_text("error.txt", traceback.format_exc())
            bundle.event({"type": "run_error", "error": error})
        finally:
            if self.kube:
                try:
                    bundle.write_json("snapshots/pre-cleanup.json", self.kube.snapshot())
                except Exception as exc:
                    cleanup.append({"pre_cleanup_snapshot_error": str(exc)})
                if self.config.collect_logs:
                    try:
                        log_result = self.kube.collect_logs(
                            self.started_at, bundle.root / "logs/pre-cleanup"
                        )
                        bundle.write_json("logs/pre-cleanup/collection.json", log_result)
                    except Exception as exc:
                        cleanup.append({"pre_cleanup_log_collection_error": str(exc)})
            if self.config.auto_cleanup:
                try:
                    self._run_phase("cleanup")
                except Exception as exc:
                    cleanup.append({"configured_cleanup_error": str(exc)})
                cleanup.extend(self._automatic_cleanup())
            if self.kube:
                try:
                    bundle.write_json("snapshots/after.json", self.kube.snapshot())
                except Exception as exc:
                    cleanup.append({"after_snapshot_error": str(exc)})
                if self.config.collect_logs:
                    try:
                        log_result = self.kube.collect_logs(
                            self.started_at, bundle.root / "logs/post-cleanup"
                        )
                        bundle.write_json("logs/post-cleanup/collection.json", log_result)
                    except Exception as exc:
                        cleanup.append({"post_cleanup_log_collection_error": str(exc)})

        cleanup_errors = [
            item
            for item in cleanup
            if any(key.endswith("error") for key in item)
        ]
        if cleanup_errors and error is None:
            error = f"CleanupError: {cleanup_errors}"

        record = make_run_record(
            self.config,
            verdict=verdict,
            evidence_dir=bundle.root,
            oracle_result=oracle_result,
            source_state=source_state,
            cluster_state=cluster_state,
            error=error,
        )
        record_path = write_run_record(bundle.root / "run-record.jsonl", record)
        if verdict == "RED":
            bundle.write_json("promotion-candidate.json", promotion_candidate(record))
        asset_import: dict[str, Any] = {"imported": False, "reason": "auto_import disabled"}
        if self.config.assets and self.config.assets.auto_import:
            try:
                asset_import = import_run_record(self.config, record_path, self.runner)
            except Exception as exc:
                asset_import = {"imported": False, "error": str(exc)}
        bundle.write_json("asset-import.json", asset_import)
        bundle.write_json("cleanup.json", cleanup)
        bundle.finalize(
            {
                "schema_version": 1,
                "run_key": self.config.run_key,
                "selector": self.config.selector,
                "obligation_key": self.config.obligation_key,
                "verdict": verdict,
                "error": error,
                "cleanup": cleanup,
                "asset_import": asset_import,
            }
        )
        return {
            "ok": error is None,
            "run_key": self.config.run_key,
            "verdict": verdict,
            "evidence_dir": str(bundle.root),
            "error": error,
            "asset_import": asset_import,
        }

    def emergency_cleanup(self) -> dict[str, Any]:
        if self.kube is None:
            return {"cleaned": False, "reason": "no cluster configuration"}
        self._require_tools()
        cleared: list[dict[str, Any]] = []
        for action in self.config.actions:
            if not action.get("enabled", True) or action["kind"] != "failpoint_arm":
                continue
            component = str(action["component"])
            name = str(action["failpoint"])
            for pod in self._selected_pods(action, component):
                try:
                    cleared.append(self.kube.delete_failpoint(component, pod, name))
                except Exception as exc:
                    cleared.append({"component": component, "pod": pod, "name": name, "error": str(exc)})
        return {"failpoints": cleared, "chaos": self.kube.delete_chaos_for_run(self.config.run_key)}
