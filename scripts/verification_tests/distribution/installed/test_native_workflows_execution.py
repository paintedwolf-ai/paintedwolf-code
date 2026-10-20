"""Fresh installation requires real absence, selected snapshots and honest recovery."""
from contextlib import ExitStack, nullcontext
from pathlib import Path
from types import SimpleNamespace
import json
import stat
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release.installed_cases import native_workflows as owner


class NativeWorkflowTests(unittest.TestCase):
    def fixture(self, directory):
        root = Path(directory)
        packages, output = root / "packages", root / "evidence"
        packages.mkdir(); output.mkdir()
        package = packages / "app.deb"
        package.write_bytes(b"selected")
        artifact = {"id": "app.deb", "installation": "deb", "architecture": "amd64",
                    "source_commit": "a" * 40, "identity": {"kind": "file", "sha256": "b" * 64}}
        final = {"format_version": 1, "source_commit": "a" * 40, "artifacts": [artifact]}
        inputs = {"native_packages": {"format_version": 1, "current_package": str(package),
                  "installation": {"operation": "fresh-install", "expected_absent": True}}}
        return final, packages, output, package, inputs

    def execute(self, directory, *, mutation=None):
        final, packages, output, package, inputs = self.fixture(directory)
        calls = []
        def command(arguments):
            calls.append(arguments)
            if arguments[0] == "dpkg-query":
                if arguments[-1] == "dpkg":
                    return (1 if mutation == "database-unavailable" else 0), b"dpkg\n", b""
                return (0 if mutation == "already-installed" else 1), b"", b""
            if arguments[0] == "dpkg-deb":
                return 0, b"painted-wolf-code\n", b""
            if "mktemp" in arguments:
                return 0, b"/var/tmp/paintedwolf-native-1234567890\n", b""
            if "sha256sum" in arguments:
                digest = "d" * 64 if mutation == "snapshot-changed" else "b" * 64
                return 0, (digest + "  /var/tmp/paintedwolf-native-1234567890/package.deb\n").encode(), b""
            if "--purge" in arguments and mutation == "rollback-failed":
                return 1, b"", b"fixture rollback failure"
            return 0, b"", b""
        root_identity = SimpleNamespace(st_mode=stat.S_IFDIR | 0o700, st_uid=0, st_dev=1, st_ino=2)
        with ExitStack() as stack:
            stack.enter_context(patch.object(owner.platform, "system", return_value="Linux"))
            stack.enter_context(patch.object(owner.platform, "machine", return_value="x86_64"))
            stack.enter_context(patch.object(owner, "native_lock", return_value=nullcontext()))
            guest = stack.enter_context(patch.object(owner, "require_owned_guest", return_value={}))
            stack.enter_context(patch.object(owner, "current_package", return_value=package))
            stack.enter_context(patch.object(owner, "inspect", return_value={"payload": SimpleNamespace(kind="file", name="opt/painted-wolf-code/bin/pw")}))
            stack.enter_context(patch.object(owner.os.path, "lexists", return_value=False))
            stack.enter_context(patch.object(owner, "command", side_effect=command))
            stack.enter_context(patch.object(Path, "lstat", return_value=root_identity))
            rebound = stack.enter_context(patch.object(owner, "bind", return_value={"payload": {"status": "passed"}}))
            if mutation in {"post-install-changed", "rollback-failed"}:
                rebound.side_effect = ValueError("selected installed payload changed")
            result = owner.prepare_install(final, "a" * 40, "app.deb", packages,
                                           Path("/opt/painted-wolf-code"), inputs, output)
            guest.assert_called_once()
        return result, calls, output

    def test_actual_absent_to_installed_sequence_retains_snapshot_and_postbind(self):
        with tempfile.TemporaryDirectory() as directory:
            result, calls, output = self.execute(directory)
            self.assertEqual(result["status"], "passed")
            self.assertTrue(result["observations"]["installed"])
            install = next(call for call in calls if "apt-get" in call)
            self.assertEqual(install[-1], "/var/tmp/paintedwolf-native-1234567890/package.deb")
            self.assertFalse(any("--purge" in call for call in calls))
            retained = json.loads((output / "installation/preinstallation-transaction/execution.json").read_text())
            self.assertEqual(retained["observations"]["snapshot_sha256"], "b" * 64)
            self.assertTrue(result["evidence"])

    def test_existing_package_or_changed_snapshot_never_calls_installer(self):
        for mutation, status in (("already-installed", "unverified"), ("database-unavailable", "unverified"), ("snapshot-changed", "failed")):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                result, calls, _ = self.execute(directory, mutation=mutation)
                self.assertEqual(result["status"], status)
                self.assertFalse(any("apt-get" in call or "--purge" in call for call in calls))

    def test_postinstall_failure_restores_absence_without_claiming_success(self):
        with tempfile.TemporaryDirectory() as directory:
            result, calls, _ = self.execute(directory, mutation="post-install-changed")
            self.assertEqual(result["status"], "failed")
            self.assertTrue(result["observations"]["restored_absence"])
            self.assertFalse(result["observations"]["recovery_required"])
            self.assertTrue(any("--purge" in call for call in calls))

    def test_failed_rollback_retains_recovery_requirement(self):
        with tempfile.TemporaryDirectory() as directory:
            result, _, _ = self.execute(directory, mutation="rollback-failed")
            self.assertEqual(result["status"], "failed")
            self.assertFalse(result["observations"]["restored_absence"])
            self.assertTrue(result["observations"]["recovery_required"])

    def test_no_operation_is_not_installation_evidence_and_boolean_alias_refuses(self):
        with tempfile.TemporaryDirectory() as directory:
            final, packages, output, _, inputs = self.fixture(directory)
            with patch.object(owner, "command") as command:
                self.assertIsNone(owner.prepare_install(final, "a" * 40, "app.deb", packages,
                                                       Path("/opt/painted-wolf-code"), {}, output))
                inputs["native_packages"]["installation"]["expected_absent"] = 1
                with self.assertRaisesRegex(ValueError, "exact fresh"):
                    owner.prepare_install(final, "a" * 40, "app.deb", packages,
                                          Path("/opt/painted-wolf-code"), inputs, output)
                command.assert_not_called()

    def test_owned_lock_refuses_concurrent_transaction_and_releases_normally(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(Path, "home", return_value=Path(directory)):
            with owner.native_lock():
                with self.assertRaisesRegex(owner.CaseUnavailable, "another owned"):
                    with owner.native_lock():
                        self.fail("concurrent native transaction admitted")
            with owner.native_lock():
                pass

    def test_foreign_native_cpu_refuses_before_guest_or_package_effects(self):
        with tempfile.TemporaryDirectory() as directory:
            final, packages, output, _, inputs = self.fixture(directory)
            with patch.object(owner.platform, "system", return_value="Linux"), \
                    patch.object(owner.platform, "machine", return_value="aarch64"), \
                    patch.object(owner, "require_owned_guest") as guest, patch.object(owner, "command") as command:
                with self.assertRaisesRegex(ValueError, "actual native Linux CPU"):
                    owner.prepare_install(final, "a" * 40, "app.deb", packages,
                                          Path("/opt/painted-wolf-code"), inputs, output)
                guest.assert_not_called()
                command.assert_not_called()
