"""Host inputs a test run keeps after its home is isolated."""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ISOLATION = Path(__file__).resolve().parents[2] / "test-run-isolation.sh"

# Resolves its toolchain from HOME, as a version wrapper does.
WRAPPER = """#!/bin/sh
case "$1 $2" in
  "env GOROOT") echo "$HOME/sdk/go-fixture" ;;
  *) echo "$HOME/go-fixture-$2" ;;
esac
"""

# Answers only what a fixture scripted, as a fake toolchain does.
FAKE = """#!/bin/sh
case "$1 $2" in
  "env GOROOT") echo "unexpected invocation" >&2; exit 1 ;;
  *) echo "/fake-$2" ;;
esac
"""


@unittest.skipIf(os.name == "nt", "test isolation is a POSIX shell library")
class RunIsolationTests(unittest.TestCase):
    def selected_go(self, front, sdk):
        """The go a run resolves after isolation, with front first on PATH and sdk under the original home."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            home, wrappers, run = root / "home", root / "wrappers", root / "run"
            selected = home / "sdk" / "go-fixture" / "bin" / "go"
            for path, body in ((wrappers / "go", front), (selected, sdk)):
                path.parent.mkdir(parents=True)
                path.write_text(body)
                path.chmod(0o755)
            run.mkdir()
            environment = {"HOME": str(home), "PATH": f"{wrappers}:/usr/bin:/bin"}
            result = subprocess.run(
                ["bash", "-c", 'source "$1"; test_run_export_isolation "$2"; command -v go',
                 "isolation", str(ISOLATION), str(run)],
                env=environment, capture_output=True, text=True, check=True)
            return Path(result.stdout.strip()).relative_to(root)

    def test_the_toolchain_selected_before_isolation_stays_selected(self):
        self.assertEqual(self.selected_go(WRAPPER, "#!/bin/sh\n"), Path("home/sdk/go-fixture/bin/go"))

    def test_a_go_that_names_no_toolchain_root_stays_first(self):
        self.assertEqual(self.selected_go(FAKE, "#!/bin/sh\n"), Path("wrappers/go"))


if __name__ == "__main__":
    unittest.main()
