"""Native lifecycle admission and restoration are exercised without host mutations."""
import hashlib
from contextlib import nullcontext
from pathlib import Path
import tempfile
import sys
import unittest
from unittest.mock import patch

from scripts.linux_release.installed_cases import packages
from scripts.linux_release.installed_cases.package_native import command, current_package, operations
from scripts.linux_release.installed_cases.records import CaseUnavailable


class InstalledPackagesTests(unittest.TestCase):
    def binding(self, path):
        path.chmod(0o600)
        return {"source_commit": "a" * 40, "artifact": {"id": path.name, "installation": "deb",
                "architecture": "amd64", "source_commit": "a" * 40,
                "identity": {"kind": "file", "sha256": hashlib.sha256(path.read_bytes()).hexdigest()}},
                "launcher": {"path": "/usr/bin/painted-wolf-code"}, "install_root": "/opt/painted-wolf-code",
                "case_inputs": {"native_packages": {"format_version": 1, "current_package": str(path)}}}

    def test_native_command_retains_exit_status_and_separate_streams(self):
        result = command([sys.executable, "-c", "import sys;print('out');print('err',file=sys.stderr);sys.exit(3)"])
        self.assertEqual(result, (3, b"out\n", b"err\n"))

    def test_selected_fixture_rejects_changed_bytes_symlinks_and_unknown_fields(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "selected.deb"
            path.write_bytes(b"package")
            binding = self.binding(path)
            self.assertEqual(current_package(binding), path)
            path.write_bytes(b"changed")
            with self.assertRaises(ValueError):
                current_package(binding)
            binding = self.binding(path)
            binding["case_inputs"]["native_packages"]["command"] = "arbitrary"
            with self.assertRaises(CaseUnavailable):
                current_package(binding)

    def test_mutations_have_fixed_noninteractive_commands_and_owner_grammar(self):
        for family in ("deb", "rpm", "pacman"):
            for command in operations(family, Path("/selected"), "painted-wolf-code"):
                self.assertEqual(command[:2], ["sudo", "-n"])
        with self.assertRaises(ValueError):
            operations("deb", Path("/selected"), "--all")

    def test_failed_removal_restores_and_rebinds_before_reporting(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "selected.deb"
            path.write_bytes(b"package")
            binding = self.binding(path)
            replies = [(0, b"painted-wolf-code: /usr/bin/painted-wolf-code\n", b""),
                       (0, b"painted-wolf-code\n", b""), (1, b"", b"remove failed"), (0, b"", b"")]
            with patch.object(packages, "native_lock", return_value=nullcontext()), patch.object(packages, "require_owned_guest"), patch.object(packages, "command", side_effect=replies) as run, \
                    patch.object(packages, "inspect", return_value={}), patch.object(packages, "bind", return_value={"payload": {"status": "passed"}}) as rebind:
                result = packages.execute(binding, "installation", "package-ownership-and-removal", Path(temporary)/"evidence")
            self.assertEqual(result["status"], "failed")
            self.assertEqual(run.call_args.args[0][:4], ["sudo", "-n", "dpkg", "--install"])
            rebind.assert_called_once()
            self.assertEqual(binding["payload"]["status"], "passed")
            self.assertIn("native_packages", binding["case_inputs"])

    def test_missing_driver_never_claims_an_interaction(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary)/"selected.deb"
            path.write_bytes(b"package")
            result = packages.execute(self.binding(path), "updates", "interrupted-upgrade", Path(temporary)/"evidence")
            self.assertEqual(result["status"], "unverified")
