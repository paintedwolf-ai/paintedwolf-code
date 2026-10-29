"""Typed failures and verified terminal artifacts for unattended launchers."""
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest

from checkpoint import atomic_json
from common import digest, inventory
from terminal import completed, execute, failure


class Terminal(unittest.TestCase):
    def test_oom_is_permanent_and_transport_is_retryable(self):
        self.assertEqual(failure(MemoryError())["code"], "out_of_memory")
        self.assertFalse(failure(MemoryError())["retryable"])
        self.assertTrue(failure(ConnectionResetError())["retryable"])
        self.assertFalse(failure(RuntimeError("connection reset"))["retryable"])

    def test_failure_replaces_running_receipt(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            def trainer(args):
                self.assertEqual(json.loads((root / "terminal.json").read_text())["status"], "running")
                raise MemoryError("fixture")
            with self.assertRaises(MemoryError):
                execute(SimpleNamespace(out=root, resume=False), trainer)
            result = json.loads((root / "terminal.json").read_text())
            self.assertEqual(result["status"], "failed")
            self.assertEqual(result["failure"], {"code": "out_of_memory", "retryable": False})

    def test_completed_requires_current_adapter_and_input_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "adapter").mkdir()
            (root / "adapter/weights").write_bytes(b"fixture")
            atomic_json(root / "inputs.json", {"fixture": True})
            atomic_json(root / "result.json", {"inputs_sha256": digest({"fixture": True}),
                        "adapter": inventory(root / "adapter"), "resources": {"status": "complete"}})
            self.assertEqual(completed(root, {})["status"], "complete")
            (root / "adapter/weights").write_bytes(b"changed")
            with self.assertRaisesRegex(ValueError, "does not match"):
                completed(root, {})


if __name__ == "__main__":
    unittest.main()
