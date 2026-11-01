"""Exercise generation packaging through its isolated fixture suite."""

from pathlib import Path
import subprocess
import sys
import unittest


class GenerationStagingTests(unittest.TestCase):
    def test_authenticated_runtime_and_seed_staging(self):
        result = subprocess.run([sys.executable, "-m", "unittest", "test_staging"],
                                cwd=Path(__file__).parent / "generation",
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
