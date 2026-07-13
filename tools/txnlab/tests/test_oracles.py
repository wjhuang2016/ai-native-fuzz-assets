from __future__ import annotations

import unittest

from tools.txnlab.oracles import (
    lock_generation_survival,
    protocol_atomic_keyset,
    terminal_mvcc_truth,
)


class TerminalTruthTest(unittest.TestCase):
    def test_red_when_definite_failure_contradicts_commit(self) -> None:
        result = terminal_mvcc_truth(
            {
                "apply_witness": True,
                "response_suppressed": True,
                "terminal": {"kind": "failure"},
                "keys": [
                    {"visible": True, "txn_status": "committed", "commit_ts": 10},
                    {"visible": True, "txn_status": "committed", "commit_ts": 10},
                ],
            }
        )
        self.assertEqual("RED", result["verdict"])

    def test_green_when_unknown_result_resolves_to_complete_commit(self) -> None:
        result = terminal_mvcc_truth(
            {
                "apply_witness": True,
                "response_suppressed": True,
                "terminal": {"kind": "undetermined"},
                "keys": [
                    {"visible": True, "txn_status": "committed", "commit_ts": 10},
                    {"visible": True, "txn_status": "committed", "commit_ts": 10},
                ],
            }
        )
        self.assertEqual("GREEN", result["verdict"])

    def test_invalid_without_apply_altitude_witness(self) -> None:
        result = terminal_mvcc_truth(
            {
                "response_suppressed": True,
                "terminal": {"kind": "failure"},
                "keys": [{"visible": False, "txn_status": "absent"}],
            }
        )
        self.assertEqual("INVALID", result["verdict"])


class LockGenerationTest(unittest.TestCase):
    def test_red_when_new_owner_is_removed(self) -> None:
        result = lock_generation_survival(
            {
                "old_lock": {"key": "k", "start_ts": 1},
                "new_lock_before_cleanup": {"key": "k", "start_ts": 2},
                "new_lock_witness_before_cleanup": True,
                "cleanup_applied": True,
                "lock_after_cleanup": {},
                "later_consumer": {"committed": False, "error_class": "rolled_back"},
            }
        )
        self.assertEqual("RED", result["verdict"])

    def test_invalid_without_order_witness(self) -> None:
        result = lock_generation_survival(
            {
                "old_lock": {"key": "k", "start_ts": 1},
                "new_lock_before_cleanup": {"key": "k", "start_ts": 2},
                "cleanup_applied": True,
                "later_consumer": {"committed": True},
            }
        )
        self.assertEqual("INVALID", result["verdict"])


class ProtocolAtomicityTest(unittest.TestCase):
    def test_red_on_partial_keyset(self) -> None:
        result = protocol_atomic_keyset(
            {
                "region_count": 2,
                "accepted_prewrite_prefix": ["r1"],
                "fallback_witness": True,
                "keys": [
                    {"visible": True, "txn_status": "committed", "mode": "2pc"},
                    {"visible": False, "txn_status": "rolled_back", "mode": "2pc"},
                ],
            }
        )
        self.assertEqual("RED", result["verdict"])

    def test_invalid_on_single_region(self) -> None:
        result = protocol_atomic_keyset(
            {
                "region_count": 1,
                "accepted_prewrite_prefix": ["r1"],
                "fallback_witness": True,
                "keys": [
                    {"visible": True, "txn_status": "committed", "mode": "2pc"},
                    {"visible": True, "txn_status": "committed", "mode": "2pc"},
                ],
            }
        )
        self.assertEqual("INVALID", result["verdict"])


if __name__ == "__main__":
    unittest.main()
