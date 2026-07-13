"""Kubernetes, failpoint, and Chaos Mesh adapters for authorized testbeds."""

from __future__ import annotations

import contextlib
import json
import os
import queue
import re
import socket
import subprocess
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Iterator

from .config import ClusterConfig, ImagePin
from .process import CommandRunner


COMPONENT_PORTS = {"tidb": 10080, "tikv": 20180, "pd": 2379}


@dataclass
class PortForward:
    process: subprocess.Popen[str]
    local_port: int
    output: list[str]


class Kubectl:
    def __init__(self, config: ClusterConfig, runner: CommandRunner):
        self.config = config
        self.runner = runner
        self.env = {"KUBECONFIG": str(config.kubeconfig)}

    def command(self, *args: str) -> list[str]:
        return ["kubectl", "-n", self.config.namespace, *args]

    def run(self, *args: str, **kwargs: Any):
        env = dict(self.env)
        env.update(kwargs.pop("env", {}) or {})
        return self.runner.run(self.command(*args), env=env, **kwargs)

    def json(self, *args: str) -> Any:
        return json.loads(self.run(*args, "-o", "json").stdout)

    def auth_can_i(self, verb: str, resource: str) -> bool:
        result = self.run("auth", "can-i", verb, resource, check=False)
        return result.returncode == 0 and result.stdout.strip() == "yes"

    def tc(self) -> dict[str, Any]:
        return self.json("get", "tidbcluster", self.config.tc_name)

    def pods(self, component: str | None = None) -> list[dict[str, Any]]:
        args = ["get", "pods"]
        if component:
            args += ["-l", f"app.kubernetes.io/instance={self.config.tc_name},app.kubernetes.io/component={component}"]
        return self.json(*args).get("items", [])

    def pod_names(self, component: str) -> list[str]:
        return sorted(pod["metadata"]["name"] for pod in self.pods(component))

    def snapshot(self) -> dict[str, Any]:
        tc = self.tc()
        return {
            "tidbcluster": tc,
            "pods": self.pods(),
            "services": self.json("get", "services"),
        }

    def preflight(self) -> dict[str, Any]:
        if not self.config.kubeconfig.is_file():
            raise RuntimeError(f"kubeconfig not found: {self.config.kubeconfig}")
        tc = self.tc()
        condition = next(
            (item for item in tc.get("status", {}).get("conditions", []) if item.get("type") == "Ready"),
            {},
        )
        access_checks = [
            ("get", "pods"),
            ("get", "pods/log"),
            ("create", "pods/exec"),
            ("create", "pods/portforward"),
            ("patch", "tidbclusters.pingcap.com"),
            ("create", "jobs.batch"),
            ("create", "podchaos.chaos-mesh.org"),
            ("create", "networkchaos.chaos-mesh.org"),
            ("create", "iochaos.chaos-mesh.org"),
            ("create", "timechaos.chaos-mesh.org"),
            ("create", "stresschaos.chaos-mesh.org"),
            ("create", "httpchaos.chaos-mesh.org"),
            ("create", "workflows.chaos-mesh.org"),
        ]
        access = {
            f"{verb} {resource}": self.auth_can_i(verb, resource)
            for verb, resource in access_checks
        }
        return {
            "cluster_id": tc.get("status", {}).get("clusterID"),
            "ready": condition.get("status") == "True",
            "condition": condition,
            "images": {
                component: tc.get("status", {}).get(component, {}).get("image")
                for component in ("tidb", "tikv", "pd")
            },
            "replicas": {
                component: {
                    "ready": tc.get("status", {}).get(component, {}).get("statefulSet", {}).get("readyReplicas", 0),
                    "wanted": tc.get("spec", {}).get(component, {}).get("replicas", 0),
                }
                for component in ("tidb", "tikv", "pd")
            },
            "access": access,
        }

    @contextlib.contextmanager
    def port_forward(self, resource: str, remote_port: int) -> Iterator[PortForward]:
        sock = socket.socket()
        sock.bind(("127.0.0.1", 0))
        local_port = int(sock.getsockname()[1])
        sock.close()
        argv = self.command("port-forward", resource, f"{local_port}:{remote_port}", "--address", "127.0.0.1")
        env = os.environ.copy()
        env.update(self.env)
        proc = subprocess.Popen(
            argv,
            env=env,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            bufsize=1,
        )
        lines: list[str] = []
        output_queue: queue.Queue[str] = queue.Queue()

        def read_output() -> None:
            assert proc.stdout is not None
            for line in proc.stdout:
                lines.append(line)
                output_queue.put(line)

        thread = threading.Thread(target=read_output, daemon=True)
        thread.start()
        deadline = time.monotonic() + 20
        ready = False
        while time.monotonic() < deadline:
            if proc.poll() is not None:
                break
            try:
                line = output_queue.get(timeout=0.2)
            except queue.Empty:
                continue
            if "Forwarding from 127.0.0.1" in line:
                ready = True
                break
        if not ready:
            proc.terminate()
            proc.wait(timeout=5)
            raise RuntimeError(f"port-forward failed for {resource}: {''.join(lines)}")
        self.runner.emit(
            {"type": "port_forward_start", "resource": resource, "local_port": local_port, "remote_port": remote_port}
        )
        try:
            yield PortForward(process=proc, local_port=local_port, output=lines)
        finally:
            if proc.poll() is None:
                proc.terminate()
                try:
                    proc.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait(timeout=5)
            self.runner.emit({"type": "port_forward_stop", "resource": resource})

    def http_request(
        self,
        pod: str,
        port: int,
        path: str,
        *,
        method: str = "GET",
        body: str | None = None,
    ) -> tuple[int, str]:
        with self.port_forward(f"pod/{pod}", port) as forward:
            url = f"http://127.0.0.1:{forward.local_port}{path}"
            request = urllib.request.Request(
                url,
                data=body.encode() if body is not None else None,
                method=method,
            )
            try:
                with urllib.request.urlopen(request, timeout=10) as response:
                    return int(response.status), response.read().decode(errors="replace")
            except urllib.error.HTTPError as exc:
                return int(exc.code), exc.read().decode(errors="replace")

    def set_failpoint(self, component: str, pod: str, name: str, action: str) -> dict[str, Any]:
        if component not in {"tidb", "tikv"}:
            raise ValueError("HTTP failpoints support tidb and tikv")
        encoded = urllib.parse.quote(name, safe="/")
        status, response = self.http_request(
            pod,
            COMPONENT_PORTS[component],
            f"/fail/{encoded}",
            method="PUT",
            body=action,
        )
        if status < 200 or status >= 300:
            raise RuntimeError(f"failed to set {component} failpoint {name} on {pod}: {status} {response}")
        return {"component": component, "pod": pod, "name": name, "action": action, "response": response}

    def delete_failpoint(self, component: str, pod: str, name: str) -> dict[str, Any]:
        encoded = urllib.parse.quote(name, safe="/")
        status, response = self.http_request(
            pod,
            COMPONENT_PORTS[component],
            f"/fail/{encoded}",
            method="DELETE",
        )
        if status < 200 or status >= 300:
            raise RuntimeError(f"failed to delete {component} failpoint {name} on {pod}: {status} {response}")
        return {"component": component, "pod": pod, "name": name, "response": response}

    def list_failpoints(self, component: str, pod: str) -> str:
        path = "/fail/" if component == "tidb" else "/fail"
        status, response = self.http_request(pod, COMPONENT_PORTS[component], path)
        if status < 200 or status >= 300:
            raise RuntimeError(f"failed to list {component} failpoints on {pod}: {status} {response}")
        return response

    def apply_object(self, obj: dict[str, Any]) -> dict[str, Any]:
        obj = confine_chaos_object(obj, self.config.namespace)
        result = self.run("apply", "-f", "-", input_text=json.dumps(obj))
        return {"stdout": result.stdout.strip(), "object": obj}

    def delete_chaos_for_run(self, run_key: str) -> dict[str, Any]:
        selector = f"ai-native.pingcap.net/run-key={run_key}"
        kinds = ["podchaos", "networkchaos", "iochaos", "timechaos", "stresschaos", "httpchaos", "workflow"]
        result = self.run(
            "delete",
            ",".join(kinds),
            "-l",
            selector,
            "--ignore-not-found=true",
            check=False,
        )
        return {"returncode": result.returncode, "stdout": result.stdout, "stderr": result.stderr}

    def image_state(self) -> dict[str, Any]:
        tc = self.tc()
        state: dict[str, Any] = {}
        for component in ("tidb", "tikv", "pd"):
            spec = tc.get("spec", {}).get(component, {})
            state[component] = {
                "baseImage": spec.get("baseImage"),
                "version": spec.get("version"),
                "env": spec.get("env", []),
            }
        return state

    def patch_images(self, images: dict[str, ImagePin], enable_tidb_test_api: bool = False) -> dict[str, Any]:
        before = self.image_state()
        spec_patch: dict[str, Any] = {}
        for component, image in images.items():
            component_patch: dict[str, Any] = {"baseImage": image.base, "version": image.tag}
            if component == "tidb" and enable_tidb_test_api:
                env = [
                    item
                    for item in (before["tidb"].get("env") or [])
                    if item.get("name") != "GO_FAILPOINTS"
                ]
                env.append(
                    {
                        "name": "GO_FAILPOINTS",
                        "value": "github.com/pingcap/tidb/pkg/server/enableTestAPI=return",
                    }
                )
                component_patch["env"] = env
            spec_patch[component] = component_patch
        self.run(
            "patch",
            "tidbcluster",
            self.config.tc_name,
            "--type=merge",
            "-p",
            json.dumps({"spec": spec_patch}),
        )
        self.wait_for_images(images)
        return before

    def restore_images(self, state: dict[str, Any]) -> None:
        patch = {"spec": {component: values for component, values in state.items() if values.get("baseImage")}}
        self.run(
            "patch",
            "tidbcluster",
            self.config.tc_name,
            "--type=merge",
            "-p",
            json.dumps(patch),
        )
        expected = {
            component: ImagePin(component, values["baseImage"], values["version"])
            for component, values in state.items()
            if values.get("baseImage") and values.get("version")
        }
        self.wait_for_images(expected)

    def wait_for_images(self, images: dict[str, ImagePin]) -> None:
        deadline = time.monotonic() + self.config.rollout_timeout_seconds
        last: dict[str, Any] = {}
        while time.monotonic() < deadline:
            tc = self.tc()
            last = {}
            complete = True
            for component, image in images.items():
                status = tc.get("status", {}).get(component, {})
                stateful = status.get("statefulSet", {})
                wanted = tc.get("spec", {}).get(component, {}).get("replicas", 0)
                current = status.get("image")
                ready = stateful.get("readyReplicas", 0)
                last[component] = {"image": current, "ready": ready, "wanted": wanted}
                if current != image.reference or ready != wanted:
                    complete = False
            if complete:
                return
            time.sleep(5)
        raise TimeoutError(f"image rollout did not converge: {last}")

    def collect_logs(self, since_time: str, destination: Path) -> dict[str, Any]:
        destination.mkdir(parents=True, exist_ok=True)
        collected = []
        for component in ("tidb", "tikv", "pd"):
            for pod in self.pod_names(component):
                result = self.run("logs", pod, f"--since-time={since_time}", check=False)
                path = destination / f"{pod}.log"
                path.write_text(result.stdout + ("\nSTDERR:\n" + result.stderr if result.stderr else ""))
                collected.append({"pod": pod, "path": str(path), "returncode": result.returncode})
        return {"logs": collected}

    def wait_for_log(self, component: str, pattern: str, since_time: str, timeout_seconds: int) -> dict[str, Any]:
        regex = re.compile(pattern)
        deadline = time.monotonic() + timeout_seconds
        while time.monotonic() < deadline:
            for pod in self.pod_names(component):
                result = self.run("logs", pod, f"--since-time={since_time}", check=False)
                match = regex.search(result.stdout)
                if match:
                    return {"pod": pod, "match": match.group(0)}
            time.sleep(2)
        raise TimeoutError(f"log pattern not found for {component}: {pattern}")


def label_chaos_object(obj: dict[str, Any], run_key: str, action_name: str) -> dict[str, Any]:
    obj = json.loads(json.dumps(obj))
    metadata = obj.setdefault("metadata", {})
    labels = metadata.setdefault("labels", {})
    labels["app.kubernetes.io/managed-by"] = "txnlab"
    labels["ai-native.pingcap.net/run-key"] = run_key
    labels["ai-native.pingcap.net/action"] = action_name
    return obj


def confine_chaos_object(obj: dict[str, Any], namespace: str) -> dict[str, Any]:
    """Reject cross-namespace selectors and force object ownership to the test namespace."""

    obj = json.loads(json.dumps(obj))
    metadata = obj.setdefault("metadata", {})
    object_namespace = metadata.get("namespace")
    if object_namespace and object_namespace != namespace:
        raise ValueError(
            f"Chaos object namespace {object_namespace!r} is outside configured namespace {namespace!r}"
        )
    metadata["namespace"] = namespace

    def walk(value: Any) -> None:
        if isinstance(value, dict):
            for key, child in value.items():
                if key == "namespaces":
                    if not isinstance(child, list) or set(child) != {namespace}:
                        raise ValueError(
                            f"Chaos selector namespaces must be exactly [{namespace!r}]"
                        )
                else:
                    walk(child)
        elif isinstance(value, list):
            for child in value:
                walk(child)

    walk(obj.get("spec", {}))
    selector = obj.get("spec", {}).get("selector")
    if isinstance(selector, dict) and "namespaces" not in selector:
        selector["namespaces"] = [namespace]
    return obj
