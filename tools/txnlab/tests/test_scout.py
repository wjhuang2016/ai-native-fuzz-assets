from __future__ import annotations

import json
import tempfile
import textwrap
import unittest
from pathlib import Path

from tools.txnlab.config import load_config
from tools.txnlab.scout import (
    MAX_REGION_LINES,
    ScoutError,
    SourceRegion,
    build_source_packet,
    render_scout_prompt,
    run_source_packet_scout,
    validate_scout_result,
)


class SourcePacketScoutTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        self.repo = root / "client-go"
        self.repo.mkdir()
        (self.repo / "sample.go").write_text("one\ntwo\nthree\n")
        config_path = root / "experiment.toml"
        config_path.write_text(
            textwrap.dedent(
                f"""
                schema_version = 1
                run_key = "scout.test"
                obligation_key = "obligation.test.v1"
                selector = "TEST"
                environment = "local"
                evidence_root = "runs"
                workspace_root = ".txnlab"

                [sources.client-go]
                repo = "{self.repo}"
                commit = "661db4f5f4e85d1efe3a0f189fc80c564b7b573a"
                """
            )
        )
        self.config = load_config(config_path)

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def test_packet_contains_only_selected_numbered_lines(self) -> None:
        packet = build_source_packet(
            self.config,
            "find the owner mismatch",
            [SourceRegion.parse("client-go:sample.go:2:3")],
        )
        self.assertIn("     2 two", packet["regions"][0]["content"])
        self.assertNotIn("one", packet["regions"][0]["content"])

    def test_region_limits_and_path_escape_are_rejected(self) -> None:
        with self.assertRaisesRegex(ScoutError, "at most"):
            SourceRegion.parse(f"client-go:sample.go:1:{MAX_REGION_LINES + 1}")
        with self.assertRaisesRegex(ScoutError, "escapes"):
            build_source_packet(
                self.config,
                "question",
                [SourceRegion.parse("client-go:../outside.go:1:1")],
            )

    def test_runner_uses_json_prompt_without_output_schema(self) -> None:
        root = Path(self.tmp.name)
        fake = root / "fake-codex"
        fake.write_text(
            "#!/bin/sh\n"
            "for arg in \"$@\"; do\n"
            "  [ \"$arg\" = \"--output-schema\" ] && exit 91\n"
            "done\n"
            "while [ \"$1\" != \"--output-last-message\" ]; do shift; done\n"
            "shift\n"
            "out=$1\n"
            "cat >/dev/null\n"
            "printf '%s\\n' '{\"scope\":\"test\",\"candidates\":[],\"retired\":[]}' >\"$out\"\n"
        )
        fake.chmod(0o755)
        output = root / "output"
        result = run_source_packet_scout(
            self.config,
            "question",
            [SourceRegion.parse("client-go:sample.go:1:3")],
            output,
            timeout_seconds=5,
            codex_binary=str(fake),
        )
        self.assertTrue(result["ok"], json.dumps(result))
        self.assertNotIn("--output-schema", result["command"])
        prompt = (output / "prompt.txt").read_text()
        self.assertIn("Do not use tools", prompt)
        self.assertIn("SOURCE_PACKET=", prompt)

    def test_candidate_requires_concrete_production_trigger_card(self) -> None:
        packet = build_source_packet(
            self.config,
            "find the owner mismatch",
            [SourceRegion.parse("client-go:sample.go:1:3")],
        )
        prompt = render_scout_prompt(packet)
        for field in (
            "production_workload",
            "natural_producer",
            "ordering",
            "defaults",
            "topology",
            "production_outcome",
            "control",
        ):
            self.assertIn(field, prompt)

        incomplete = {
            "scope": "test",
            "candidates": [
                {
                    "P": "P",
                    "Q": "Q",
                    "F": "F",
                    "owners": "owner",
                    "highest_consumer": "data loss",
                    "oracle": "fresh read",
                    "confidence": "medium",
                    "anchors": ["client-go/sample.go:1"],
                }
            ],
            "retired": [],
        }
        with self.assertRaisesRegex(ScoutError, "production_workload"):
            validate_scout_result(incomplete)

        generic = incomplete["candidates"][0] | {
            "production_workload": "UPDATE an order row in a pessimistic transaction",
            "natural_producer": "network error",
            "ordering": "request A starts before request B and finishes after B commits",
            "defaults": "MDL ON and otherwise default settings",
            "topology": "two TiDB nodes and three healthy TiKV nodes",
            "production_outcome": "COMMIT succeeds but a fresh session reads a missing row",
            "control": "without request B the durable row remains present",
        }
        with self.assertRaisesRegex(ScoutError, "natural_producer is generic"):
            validate_scout_result({"scope": "test", "candidates": [generic], "retired": []})

        concrete = generic | {
            "natural_producer": (
                "a TiKV leader transfer after the old leader applies the write but before its "
                "response reaches TiDB causes the Region request sender to relocate the batch"
            )
        }
        validate_scout_result({"scope": "test", "candidates": [concrete], "retired": []})


if __name__ == "__main__":
    unittest.main()
