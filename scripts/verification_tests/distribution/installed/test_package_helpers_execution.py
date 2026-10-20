"""Sealed helper startup and desktop loading require actual bounded probe outcomes."""
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release.installed_cases import package_helpers as owner


class App:
    def __init__(self, binding):
        self.observations = {"capture_complete": True, "output_limit": False}
        self.stopped = False
    def __enter__(self):
        return self
    def __exit__(self, *_):
        self.stop()
    def request(self, *_):
        return {"status": 200}
    def stop(self):
        self.stopped = True
    def streams(self):
        return {"app-stdout": b"owned desktop", "app-stderr": b""}


class PackageHelperTests(unittest.TestCase):
    def fixture(self):
        return {"source_commit": "a" * 40, "artifact": {"id": "app", "installation": "deb",
                "architecture": "amd64", "source_commit": "a" * 40,
                "identity": {"kind": "file", "sha256": "b" * 64}},
                "payload": {"status": "passed", "files_and_links": 42}}

    def execute(self, root, *, mutation=None):
        def run(binding, role, arguments):
            if mutation == "missing-image":
                raise owner.CaseUnavailable("selected helper image is unavailable")
            probe = next(item for item in owner.PROBES if item[1] == role and list(item[2]) == arguments)
            return {"exit_code": 127 if mutation == "failed-helper" and role == "git-observer" else probe[3],
                    "timed_out": mutation == "timeout", "output_limit": False,
                    "launch_identity": {"pid": 22, "start_time_ticks": 33},
                    "stdout": b"x" * ((4 << 20) + 1) if mutation == "joined-output-limit" and role == "pw" else role.encode(), "stderr": b""}
        with patch.object(owner, "require_owned_guest"), patch.object(owner, "run", side_effect=run) as probes, \
                patch.object(owner, "InstalledApp", side_effect=App) as application, \
                patch.object(owner, "interactive_window", return_value={"owned": True}):
            result = owner.execute(self.fixture(), "installation", "all-helpers-and-resources", root)
        return result, probes, application

    def test_complete_fixed_probe_suite_and_owned_desktop_loading_pass(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            result, probes, application = self.execute(root)
            self.assertEqual(result["status"], "passed")
            self.assertEqual(probes.call_count, len(owner.PROBES))
            application.assert_called_once()
            journal = json.loads((root / "installation/all-helpers-and-resources/execution.json").read_text())
            self.assertEqual(len(journal["observations"]["probes"]), len(owner.PROBES))
            self.assertEqual(len(result["evidence"]), 5)
            self.assertTrue(all(item["passed"] for item in journal["observations"]["probes"]))

    def test_failed_or_timed_out_helper_is_not_replaced_by_healthy_app(self):
        for mutation in ("failed-helper", "timeout"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                result, _, application = self.execute(Path(directory), mutation=mutation)
                self.assertEqual(result["status"], "failed")
                application.assert_not_called()

    def test_missing_selected_helper_retains_unverified_observation(self):
        with tempfile.TemporaryDirectory() as directory:
            result, _, application = self.execute(Path(directory), mutation="missing-image")
            self.assertEqual(result["status"], "unverified")
            self.assertTrue(result["evidence"])
            application.assert_not_called()

    def test_probe_catalog_uses_only_fixed_literal_roles_and_no_sandbox_bypass(self):
        self.assertEqual({item[1] for item in owner.PROBES},
                         {"pw", "pw-logs", "pw-vault-identity", "bialy", "git-observer",
                          "git-lfs-observer", "browser-observer", "ocr-observer"})
        self.assertFalse(any("--no-sandbox" in item[2] for item in owner.PROBES))
        with self.assertRaisesRegex(ValueError, "different fixed case"):
            owner.execute(self.fixture(), "updates", "all-helpers-and-resources", Path("/unused"))

    def test_joined_helper_channel_limit_retains_failed_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            result, _, application = self.execute(root, mutation="joined-output-limit")
            self.assertEqual(result["status"], "failed")
            self.assertLessEqual((root / "installation/all-helpers-and-resources/helpers-stdout.bin").stat().st_size, 8 << 20)
            journal = json.loads((root / "installation/all-helpers-and-resources/execution.json").read_text())
            self.assertTrue(journal["observations"]["output_limit"])
            application.assert_not_called()
