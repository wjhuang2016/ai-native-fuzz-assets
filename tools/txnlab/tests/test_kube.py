from __future__ import annotations

import unittest

from tools.txnlab.kube import confine_chaos_object, label_chaos_object


class ChaosLabelTest(unittest.TestCase):
    def test_labels_copy_without_mutating_template(self) -> None:
        template = {"apiVersion": "chaos-mesh.org/v1alpha1", "kind": "NetworkChaos"}
        labeled = label_chaos_object(template, "run-1", "delay")
        self.assertNotIn("metadata", template)
        labels = labeled["metadata"]["labels"]
        self.assertEqual("run-1", labels["ai-native.pingcap.net/run-key"])
        self.assertEqual("delay", labels["ai-native.pingcap.net/action"])

    def test_cross_namespace_selector_is_rejected(self) -> None:
        template = {
            "kind": "NetworkChaos",
            "metadata": {"namespace": "testbed-a"},
            "spec": {"selector": {"namespaces": ["testbed-b"]}},
        }
        with self.assertRaisesRegex(ValueError, "selector namespaces"):
            confine_chaos_object(template, "testbed-a")

    def test_missing_selector_namespace_is_scoped(self) -> None:
        template = {"kind": "NetworkChaos", "spec": {"selector": {}}}
        confined = confine_chaos_object(template, "testbed-a")
        self.assertEqual("testbed-a", confined["metadata"]["namespace"])
        self.assertEqual(["testbed-a"], confined["spec"]["selector"]["namespaces"])


if __name__ == "__main__":
    unittest.main()
