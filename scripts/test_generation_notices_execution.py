"""Generation notices retain crate attribution and reject unknown licenses."""

import json
from pathlib import Path
import subprocess
import tempfile
import unittest


class GenerationNoticesTests(unittest.TestCase):
    def test_crate_attribution_and_unknown_license_refusal(self):
        module = Path(__file__).with_name("licenses-assemble.ts").resolve()
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            crates = root / "crates.json"
            program = root / "render.ts"
            program.write_text(
                "import {renderCratesFromJson} from " + json.dumps(str(module)) + ";\n"
                "console.log(renderCratesFromJson(process.argv[2], 'Generation engine', 'lycaon/internal/generation/native'));\n")
            for identifier, expected in [("MIT", 0), ("unknown", 1)]:
                crates.write_text(json.dumps({"licenses": [{"id": identifier, "text": "fixture license", "used_by": [
                    {"crate": {"name": "fixture-kernel", "version": "1.2.3"}}]}]}))
                result = subprocess.run(["bun", str(program), str(crates)], capture_output=True, text=True)
                self.assertEqual(result.returncode, expected, result.stderr)
                if expected == 0:
                    self.assertIn("Generation engine", result.stdout)
                    self.assertIn("fixture-kernel 1.2.3", result.stdout)
                    self.assertIn("fixture license", result.stdout)
                else:
                    self.assertIn("unclassifiable crate license", result.stderr)
