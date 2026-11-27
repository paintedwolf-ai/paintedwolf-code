"""Host inputs a test run keeps after its home is isolated."""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ISOLATION = Path(__file__).resolve().parents[2] / "test-run-isolation.sh"

# Resolves its toolchain and caches from HOME, as a version wrapper does.
WRAPPER = """#!/bin/sh
case "$1 $2" in
  "env GOROOT") echo "$HOME/sdk/go-fixture" ;;
  "env GOCACHE") echo "$HOME/go-cache" ;;
  "env GOMODCACHE") echo "$HOME/go-mod" ;;
  "env GOPATH") echo "$HOME/go-path" ;;
  *) exit 2 ;;
esac
"""

# Names a toolchain root that holds no go.
MISSING_ROOT = """#!/bin/sh
case "$1 $2" in
  "env GOROOT") echo "$HOME/nowhere" ;;
  "env "*) echo "$HOME/go-$2" ;;
  *) exit 2 ;;
esac
"""

# Cannot report its environment.
BROKEN = """#!/bin/sh
echo "go: cannot find GOROOT directory" >&2
exit 1
"""


@unittest.skipIf(os.name == "nt", "test isolation is a POSIX shell library")
class RunIsolationTests(unittest.TestCase):
    def isolate(self, front):
        """Isolates a run with front first on PATH and a toolchain under the original home."""
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        root = Path(directory.name)
        home, wrappers, run = root / "home", root / "wrappers", root / "run"
        for path, body in ((wrappers / "go", front), (home / "sdk" / "go-fixture" / "bin" / "go", "#!/bin/sh\n")):
            path.parent.mkdir(parents=True)
            path.write_text(body)
            path.chmod(0o755)
        run.mkdir()
        result = subprocess.run(
            ["bash", "-c", 'set -e; source "$1"; test_run_export_isolation "$2"; command -v go; echo "$GOCACHE"; echo "$GOMODCACHE"; echo "$GOPATH"',
             "isolation", str(ISOLATION), str(run)],
            env={"HOME": str(home), "PATH": f"{wrappers}:/usr/bin:/bin"}, capture_output=True, text=True)
        return root, result

    def test_the_toolchain_and_caches_resolved_before_isolation_stay_selected(self):
        root, result = self.isolate(WRAPPER)
        self.assertEqual(result.returncode, 0, result.stderr)
        selected = [Path(line).relative_to(root) for line in result.stdout.split()]
        self.assertEqual(selected, [Path("home/sdk/go-fixture/bin/go"), Path("home/go-cache"),
                                    Path("home/go-mod"), Path("home/go-path")])

    def test_a_go_that_cannot_report_its_environment_stops_isolation(self):
        _, result = self.isolate(BROKEN)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")

    def test_a_toolchain_root_without_go_stops_isolation(self):
        _, result = self.isolate(MISSING_ROOT)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("has no bin/go", result.stderr)


if __name__ == "__main__":
    unittest.main()
