"""Coverage reports reuse one test profile."""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from coverage_policy import main, percentage

ROOT = Path(__file__).resolve().parents[2]


class CoverageExecutionTests(unittest.TestCase):
    def test_fractional_floors_do_not_truncate(self):
        self.assertEqual(main(["62.1", "62.9"]), 1)
        self.assertEqual(main(["62.9", "62.9"]), 0)
        self.assertEqual(main(["63.0", "62.9"]), 0)

    def test_invalid_percentage_is_rejected(self):
        for value in ("", "nan", "inf", "-1", "101", "Unknown"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                percentage(value)

    def test_aggregate_and_package_reports_share_one_test_run(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = Path(directory)
            fake_go = fixture / "go"
            fake_go.write_text("""#!/usr/bin/env python3
import os
from pathlib import Path
import sys
if sys.argv[1] == 'test':
    with Path(os.environ['COVERAGE_CALLS']).open('a') as calls:
        calls.write('test\\n')
    profile = next(arg.split('=', 1)[1] for arg in sys.argv if arg.startswith('-coverprofile='))
    Path(profile).write_text('mode: set\\ngithub.com/lycaon/lycaon/internal/fixture/a.go:1.1,2.1 10 1\\n')
    print('{"Package":"github.com/lycaon/lycaon/internal/fixture","Action":"pass"}')
elif sys.argv[1:3] == ['tool', 'cover']:
    print('total: (statements) 100.0%')
else:
    raise SystemExit('unexpected invocation: ' + repr(sys.argv))
""")
            fake_go.chmod(0o755)
            calls = fixture / "calls"
            env = {**os.environ, "PATH": str(fixture) + os.pathsep + os.environ["PATH"],
                   # The script takes real admission; an isolated queue keeps it off the shared one.
                   "PW_TEST_ARTIFACT_ROOT": str(fixture), "PW_TEST_EXECUTION_ROOT": str(fixture / "queue"),
                   "COVERAGE_CALLS": str(calls), "COVERAGE_MIN": "62",
                   "COVERAGE_PKG_MIN": "40", "COVERAGE_PKG_ENFORCE": "1"}
            result = subprocess.run(["bash", str(ROOT / "scripts/coverage-check.sh")],
                                    env=env, text=True, capture_output=True, timeout=60)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(calls.read_text(), "test\n")
            self.assertIn("1 packages, none below 40%", result.stdout)
            self.assertIn("100.0%", result.stdout)


if __name__ == "__main__":
    unittest.main()
