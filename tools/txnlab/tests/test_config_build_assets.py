from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from tools.txnlab.assets import make_run_record
from tools.txnlab.build import render_build_plan
from tools.txnlab.config import load_config
from tools.txnlab.local import render_local_build_plan, render_realtikvtest_plan


TOML = """
schema_version = 1
run_key = "run.test"
obligation_key = "obligation.test.v1"
selector = "TEST_SELECTOR"
environment = "local"
evidence_root = "runs"
workspace_root = ".txnlab"

[sources.tidb]
repo = "/tmp/tidb"
commit = "5c9198e9484db852b8477ce0014e0422ff9ec6a9"
git_url = "https://github.com/pingcap/tidb.git"

[build]
artifacts_repo = "/tmp/artifacts"
artifacts_commit = "5958dc44f5f39ad4cb90b711d98571aff1bf06b6"
registry = "example.invalid/repo"

[[actions]]
phase = "prepare"
kind = "command"
argv = ["true"]

[[actions]]
phase = "arm"
kind = "sleep"
seconds = 1
enabled = false
"""


class ConfigBuildAssetTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.path = Path(self.tmp.name) / "experiment.toml"
        self.path.write_text(TOML)
        self.config = load_config(self.path)

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def test_disabled_actions_do_not_execute(self) -> None:
        self.assertEqual(1, len(self.config.actions_for("prepare")))
        self.assertEqual([], self.config.actions_for("arm"))

    def test_build_is_pinned_and_failpoint_enabled(self) -> None:
        rendered = render_build_plan(self.config, "tidb", Path("/tmp/build.sh"))
        self.assertEqual(self.config.sources["tidb"].commit, rendered["source_commit"])
        self.assertEqual("failpoint", rendered["profile"])
        self.assertIn("master-5c9198e-failpoint", rendered["expected_multiarch_image"])

    def test_local_tikv_build_uses_pinned_worktree(self) -> None:
        raw = TOML + """

[sources.tikv]
repo = "/tmp/tikv"
commit = "bf73df27b9675a2f8e3cc0b4b921f3128c37d8a5"
git_url = "https://github.com/tikv/tikv.git"
"""
        self.path.write_text(raw)
        config = load_config(self.path)
        plan = render_local_build_plan(config, "tikv")
        self.assertEqual(["make", "build"], plan["command"])
        self.assertTrue(plan["worktree"].endswith("worktrees/run.test/tikv"))
        self.assertTrue(plan["binary"].endswith("target/debug/tikv-server"))

    def test_realtikvtest_plan_injects_exact_tikv_binary(self) -> None:
        raw = TOML + """

[sources.tikv]
repo = "/tmp/tikv"
commit = "bf73df27b9675a2f8e3cc0b4b921f3128c37d8a5"
git_url = "https://github.com/tikv/tikv.git"
"""
        self.path.write_text(raw)
        config = load_config(self.path)
        plan = render_realtikvtest_plan(
            config,
            "TestSharedLockBlockExclusiveLock",
            "./tests/realtikvtest/txntest/...",
            Path("/tmp/tikv-server"),
        )
        expected = f"--kv.binpath={Path('/tmp/tikv-server').resolve()}"
        self.assertIn(expected, plan["playground_command"])
        self.assertIn("^TestSharedLockBlockExclusiveLock$", plan["test_command"])

    def test_realtikvtest_nightly_does_not_claim_exact_binary(self) -> None:
        raw = TOML + """

[sources.tikv]
repo = "/tmp/tikv"
commit = "bf73df27b9675a2f8e3cc0b4b921f3128c37d8a5"
git_url = "https://github.com/tikv/tikv.git"
"""
        self.path.write_text(raw)
        config = load_config(self.path)
        plan = render_realtikvtest_plan(
            config,
            "TestSharedLockBlockExclusiveLock",
            "./tests/realtikvtest/txntest/...",
            None,
        )
        self.assertEqual("refreshed_nightly", plan["runtime_mode"])
        self.assertFalse(any(arg.startswith("--kv.binpath=") for arg in plan["playground_command"]))

    def test_run_record_links_obligation(self) -> None:
        record = make_run_record(
            self.config,
            verdict="INVALID",
            evidence_dir=Path("/tmp/run"),
            oracle_result={"verdict": "INVALID"},
            source_state={},
            cluster_state=None,
        )
        self.assertEqual("run", record["record_type"])
        self.assertEqual(self.config.obligation_key, record["used_assets"][0]["asset_key"])


if __name__ == "__main__":
    unittest.main()
