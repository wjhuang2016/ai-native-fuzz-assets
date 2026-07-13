from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path

from assets.store import store


class StoreWritebackTest(unittest.TestCase):
    def test_run_and_used_asset_are_imported(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            db = root / "assets.sqlite3"
            source = root / "records.jsonl"
            records = [
                {
                    "record_type": "asset",
                    "asset_key": "obligation.test.v1",
                    "asset_type": "obligation",
                    "name": "test obligation",
                    "module": "txn/test",
                },
                {
                    "record_type": "run",
                    "run_key": "run.test.INFO",
                    "obligation_key": "obligation.test.v1",
                    "verdict": "INFO",
                    "used_assets": [
                        {"asset_key": "obligation.test.v1", "role": "obligation"}
                    ],
                },
            ]
            source.write_text("".join(json.dumps(item) + "\n" for item in records))
            counts = store.import_jsonl(db, source)
            self.assertEqual(1, counts["run"])
            with store.connect(db) as conn:
                run = conn.execute(
                    "SELECT verdict FROM run_result WHERE run_key = ?", ("run.test.INFO",)
                ).fetchone()
                link = conn.execute(
                    "SELECT role FROM run_asset WHERE run_key = ?", ("run.test.INFO",)
                ).fetchone()
            self.assertEqual("INFO", run["verdict"])
            self.assertEqual("obligation", link["role"])


if __name__ == "__main__":
    unittest.main()
