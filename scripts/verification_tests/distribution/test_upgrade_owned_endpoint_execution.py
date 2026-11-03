"""Upgrade probes use the listener published by their own captured child."""
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[3]


class UpgradeOwnedEndpointTests(unittest.TestCase):
    def test_only_the_owned_loopback_listener_is_accepted(self):
        cases = [(123, "127.0.0.1", 49123, True), (124, "127.0.0.1", 49123, False),
                 (123, "0.0.0.0", 49123, False), (123, "127.0.0.1", 0, False),
                 (123, "127.0.0.1", 65536, False), (123, "127.0.0.1", "49123", False),
                 (123, "127.0.0.1", 49123.5, False)]
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            manifest = root / "daemon.json"
            command = ["bash", "-c", 'source "$1"; upgrade_owned_base "$2" 123', "_",
                       str(ROOT / "scripts/upgrade-store-contract.sh"), str(root)]
            self.assertNotEqual(subprocess.run(command, capture_output=True).returncode, 0)
            for pid, host, port, accepted in cases:
                with self.subTest(pid=pid, host=host, port=port):
                    manifest.write_text(json.dumps({"pid": pid, "host": host, "port": port}))
                    result = subprocess.run(command, capture_output=True, text=True, timeout=10)
                    self.assertEqual(result.returncode == 0, accepted, result.stderr)
                    self.assertEqual(result.stdout, "http://127.0.0.1:49123\n" if accepted else "")
            manifest.unlink()
            target = root / "other.json"
            target.write_text(json.dumps({"pid": 123, "host": "127.0.0.1", "port": 49123}))
            manifest.symlink_to(target)
            self.assertNotEqual(subprocess.run(command, capture_output=True).returncode, 0)
