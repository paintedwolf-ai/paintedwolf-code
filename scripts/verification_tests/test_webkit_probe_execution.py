import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]


class WebKitProbeTests(unittest.TestCase):
    def probe(self, status, hosted=True):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name, body in {"uname": "echo Darwin", "cargo": f"exit {status}"}.items():
                executable = root / name
                executable.write_text(f"#!/bin/sh\n{body}\n")
                executable.chmod(0o755)
            summary = root / "summary.md"
            environment = {**os.environ, "PATH": f"{root}:{os.environ['PATH']}",
                           "GITHUB_ACTIONS": "true" if hosted else "false",
                           "GITHUB_STEP_SUMMARY": str(summary)}
            result = subprocess.run(["bash", str(ROOT / "scripts/den-webkit-scroll.sh"), "--supervised"],
                                    env=environment, capture_output=True, text=True, timeout=10)
            return result, summary.read_text() if summary.exists() else ""

    def test_absent_scrolling_thread_reports_unexecuted_scenarios(self):
        result, summary = self.probe(3)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("::notice", result.stdout)
        self.assertIn("**skipped**", summary)
        self.assertIn("./task den:webkit:scroll", summary)

    def test_probe_failure_does_not_become_a_supported_skip(self):
        for status in [1, 2, 7]:
            with self.subTest(status=status):
                result, summary = self.probe(status)
                self.assertEqual(result.returncode, status, result.stderr)
                self.assertNotIn("::notice", result.stdout)
                self.assertEqual(summary, "")

    def test_local_skip_does_not_write_hosted_evidence(self):
        result, summary = self.probe(3, hosted=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(summary, "")
