from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from tools.txnlab.assets import make_run_record
from tools.txnlab.build import render_build_plan
from tools.txnlab.config import load_config


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
