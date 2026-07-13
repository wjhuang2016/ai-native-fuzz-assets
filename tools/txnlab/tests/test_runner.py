from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path

from tools.txnlab.config import load_config
from tools.txnlab.runner import TxnLab


class RunnerTest(unittest.TestCase):
    def test_testbed_mutation_needs_config_and_cli_gate(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            common = f'''schema_version = 1
run_key = "run.gate"
obligation_key = "obligation.test.v1"
selector = "TEST"
environment = "testbed"
evidence_root = "{root / 'runs'}"
workspace_root = "{root / '.txnlab'}"
allow_mutation = {{allow}}

[cluster]
kubeconfig = "{root / 'kubeconfig'}"
namespace = "testbed-gate"

[[actions]]
phase = "workload"
kind = "sql"
sql = "SELECT 1"
'''
            path = root / "experiment.toml"
            path.write_text(common.format(allow="false"))
            lab = TxnLab(load_config(path))
            with self.assertRaisesRegex(RuntimeError, "both allow_mutation=true"):
                lab._check_mutation_gate(True)

            path.write_text(common.format(allow="true"))
            lab = TxnLab(load_config(path))
            with self.assertRaisesRegex(RuntimeError, "both allow_mutation=true"):
                lab._check_mutation_gate(False)
            lab._check_mutation_gate(True)

    def test_local_red_writes_evidence_and_promotion_gate(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            payload = {
                "apply_witness": True,
                "response_suppressed": True,
                "terminal": {"kind": "failure"},
                "keys": [
                    {"visible": True, "txn_status": "committed", "commit_ts": 10},
                    {"visible": True, "txn_status": "committed", "commit_ts": 10},
                ],
            }
            code = (
                "import json,os,pathlib; "
                f"p={json.dumps(payload)!r}; "
                "pathlib.Path(os.environ['TXNLAB_RUN_DIR'], 'oracle-input.json').write_text(p)"
            )
            config_path = root / "experiment.toml"
            config_path.write_text(
                f'''schema_version = 1
run_key = "run.local.red"
obligation_key = "obligation.txn-terminal-result-matches-durable-status.v1"
selector = "TXN_COMMIT_OUTCOME_TERMINAL_TRUTH"
environment = "local"
evidence_root = "{root / 'runs'}"
workspace_root = "{root / '.txnlab'}"

[[actions]]
phase = "workload"
kind = "command"
argv = ["python3", "-c", {json.dumps(code)}]

[oracle]
name = "terminal_mvcc_truth"
input_file = "oracle-input.json"
'''
            )
            result = TxnLab(load_config(config_path)).run()
            run_dir = Path(result["evidence_dir"])
            self.assertTrue(result["ok"])
            self.assertEqual("RED", result["verdict"])
            self.assertTrue((run_dir / "manifest.json").is_file())
            self.assertTrue((run_dir / "promotion-candidate.json").is_file())
            record = json.loads((run_dir / "run-record.jsonl").read_text())
            self.assertEqual("RED", record["verdict"])


if __name__ == "__main__":
    unittest.main()
