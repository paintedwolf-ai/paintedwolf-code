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


@unittest.skipIf(os.name == "nt", "test isolation is a POSIX shell library")
class RunIsolationTests(unittest.TestCase):
    def test_the_toolchain_selected_before_isolation_stays_selected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            home, wrappers, run = root / "home", root / "wrappers", root / "run"
            selected = home / "sdk" / "go-fixture" / "bin" / "go"
            for path, body in ((wrappers / "go", WRAPPER), (selected, "#!/bin/sh\n")):
                path.parent.mkdir(parents=True)
                path.write_text(body)
                path.chmod(0o755)
            run.mkdir()
            environment = {"HOME": str(home), "PATH": f"{wrappers}:/usr/bin:/bin"}
            result = subprocess.run(
                ["bash", "-c", 'source "$1"; test_run_export_isolation "$2"; command -v go',
                 "isolation", str(ISOLATION), str(run)],
                env=environment, capture_output=True, text=True, check=True)
        self.assertEqual(result.stdout.strip(), str(selected))


if __name__ == "__main__":
    unittest.main()
