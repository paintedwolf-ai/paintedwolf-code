from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import analysis_tools as tools


class AnalysisToolsTests(unittest.TestCase):
    def test_pins_are_exact_and_match_the_existing_inventory_sources(self):
        for name in tools.TOOLS:
            package, module, version = tools.pin(name)
            self.assertTrue(package.startswith(module + "/"))
            self.assertRegex(version, r"^v\d+\.\d+\.\d+$")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / tools.TOOLS["deadcode"][0]
            source.parent.mkdir(parents=True)
            for declaration in ['', 'DEADCODE_VERSION="latest"', 'DEADCODE_VERSION="v1.2.3"\nDEADCODE_VERSION="v1.2.4"']:
                source.write_text(declaration)
                with self.assertRaises(ValueError):
                    tools.pin("deadcode", root)

    def test_cached_binary_requires_exact_module_version_and_compiler(self):
        with tempfile.TemporaryDirectory() as directory:
            binary = Path(directory) / "deadcode"
            binary.write_text("fixture")
            binary.chmod(0o755)
            for module, version, compiler, expected in [
                ("golang.org/x/tools", "v0.33.0", "go1.26.8", True),
                ("wrong/module", "v0.33.0", "go1.26.8", False),
                ("golang.org/x/tools", "v0.32.0", "go1.26.8", False),
                ("golang.org/x/tools", "v0.33.0", "go1.26.7", False),
            ]:
                output = f"{binary}: {compiler}\n\tpath\tgolang.org/x/tools/cmd/deadcode\n\tmod\t{module}\t{version}\th1:fixture\n"
                with patch.object(tools.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, output)):
                    self.assertEqual(tools.matches(binary, "golang.org/x/tools", "v0.33.0", "go1.26.8"), expected)
            binary.chmod(0o644)
            self.assertFalse(tools.matches(binary, "golang.org/x/tools", "v0.33.0", "go1.26.8"))

    def test_install_validates_before_replacing_a_previous_tool(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            destination = root / "bin"
            destination.mkdir()
            binary = destination / "deadcode"
            binary.write_text("old tool")

            def install(command, **kwargs):
                self.assertEqual(command, ["go", "install", "golang.org/x/tools/cmd/deadcode@v0.33.0"])
                self.assertEqual(kwargs['env']['GOTOOLCHAIN'], "go1.26.8")
                (Path(kwargs['env']['GOBIN']) / "deadcode").write_text("new tool")

            for validated in [False, True]:
                with patch.object(tools, "pin", return_value=("golang.org/x/tools/cmd/deadcode", "golang.org/x/tools", "v0.33.0")), \
                        patch.object(tools, "matches", side_effect=[False, validated]), \
                        patch.object(tools.subprocess, "run", side_effect=install):
                    if validated:
                        self.assertEqual(tools.ensure("deadcode", root, destination, "go1.26.8"), binary)
                    else:
                        with self.assertRaises(ValueError):
                            tools.ensure("deadcode", root, destination, "go1.26.8")
                        self.assertEqual(binary.read_text(), "old tool")
            self.assertEqual(binary.read_text(), "new tool")
            self.assertEqual(list(destination.iterdir()), [binary])

    def test_matching_cache_does_not_install(self):
        with patch.object(tools, "matches", return_value=True), patch.object(tools.subprocess, "run") as install:
            tools.ensure("govulncheck")
            install.assert_not_called()
