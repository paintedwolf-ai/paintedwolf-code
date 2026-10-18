"""Native failures and exact ticket evidence survive Tauri's captured output."""
import contextlib
import io
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import Mock

SPEC = importlib.util.spec_from_file_location("macos_staple", Path(__file__).resolve().parents[2] / "macos-notary-tools/notary.py")
macos_staple = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(macos_staple)


def result(code=0, stdout="", stderr=""):
    return subprocess.CompletedProcess([], code, stdout, stderr)


class StapleTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.arguments = ["stapler", "staple", "-v", "/build/Painted Wolf Code.app"]

    def test_validates_the_same_bundle_before_allowing_packaging(self):
        execute = Mock(side_effect=[result(), result()])
        self.assertEqual(macos_staple.staple(self.arguments, self.directory, execute), 0)
        self.assertEqual(execute.call_args_list[1].args[0],
                         ["/usr/bin/xcrun", "stapler", "validate", "-v", self.arguments[-1]])
        macos_staple.check(self.directory)

    def test_retains_initial_failure_and_requires_validated_retry(self):
        execute = Mock(side_effect=[result(65, "CloudKit record unavailable"), result(),
                                    result(65, stderr="ticket absent"), result(), result()])
        sleep = Mock()
        self.assertEqual(macos_staple.staple(self.arguments, self.directory, execute, sleep), 0)
        evidence = json.loads(next(self.directory.glob("staple-*.json")).read_text())
        self.assertEqual(len(evidence["attempts"]), 3)
        self.assertEqual(evidence["attempts"][0]["commands"][0]["exit_code"], 65)
        self.assertEqual(evidence["attempts"][1]["commands"][1]["stderr"], "ticket absent")
        self.assertEqual([call.args[0] for call in sleep.call_args_list], [10, 30])
        macos_staple.check(self.directory)

    def test_swallowed_native_failure_cannot_confirm_a_build(self):
        execute = Mock(return_value=result(65, stderr="permanent failure"))
        self.assertEqual(macos_staple.staple(self.arguments, self.directory, execute, Mock()), 1)
        self.assertEqual(execute.call_count, 3)
        with self.assertRaisesRegex(ValueError, "did not validate"):
            macos_staple.check(self.directory)

    def test_missing_interrupted_or_malformed_receipts_refuse_confirmation(self):
        for body in (None, '{"success": false}', '{'):
            with self.subTest(body=body):
                if body is not None:
                    (self.directory / "staple-interrupted.json").write_text(body)
                with self.assertRaises(ValueError):
                    macos_staple.check(self.directory)

    def test_native_timeout_is_bounded_and_retained(self):
        execute = Mock(side_effect=subprocess.TimeoutExpired("xcrun", 120))
        self.assertEqual(macos_staple.staple(self.arguments, self.directory, execute, Mock()), 1)
        self.assertEqual(execute.call_count, 3)
        with self.assertRaises(ValueError):
            macos_staple.check(self.directory)

    def test_notary_log_preserves_ticket_hashes_without_credentials(self):
        identifier = "80b38d9e-4f98-455c-8744-4fc98fbc2bd6"
        response = json.dumps({"id": identifier, "status": "Accepted"})
        ticket = {"ticketContents": [{"cdhash": "abc", "path": "App.zip/App.app"}]}
        execute = Mock(side_effect=[result(stdout=response), result(stdout=json.dumps(ticket))])
        args = ["notarytool", "submit", "App.zip", "--output-format", "json",
                "--key", "/private/key", "--key-id", "secret-id", "--issuer", "secret-issuer"]
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            self.assertEqual(macos_staple.notarize(args, self.directory, execute), 0)
        self.assertEqual(output.getvalue(), response)
        evidence = next(self.directory.glob("notary-*.json")).read_text()
        self.assertIn('"cdhash": "abc"', evidence)
        self.assertNotIn("secret", evidence)
        self.assertNotIn("/private/key", evidence)
        self.assertEqual(execute.call_args_list[1].args[0],
                         ["/usr/bin/xcrun", "notarytool", "log", identifier, *args[5:]])

    def test_notary_failure_keeps_its_status_and_does_not_query_a_ticket(self):
        execute = Mock(return_value=result(7))
        self.assertEqual(macos_staple.notarize(["notarytool", "submit", "App.zip"], self.directory, execute), 7)
        self.assertEqual(execute.call_count, 1)

    def test_ticket_log_unavailability_does_not_resubmit_notarization(self):
        identifier = "80b38d9e-4f98-455c-8744-4fc98fbc2bd6"
        execute = Mock(side_effect=[result(stdout=json.dumps({"id": identifier})), result(1)])
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(macos_staple.notarize(["notarytool", "submit", "App.zip"], self.directory, execute), 0)
        self.assertEqual(execute.call_count, 2)

    def test_release_controller_retains_receipts_even_when_packaging_fails(self):
        root = Path(__file__).resolve().parents[3]
        workflow = (root / ".github/workflows/release.yml").read_text()
        self.assertIn('ref: refs/heads/main\n          path: .release-controller', workflow)
        self.assertIn('scripts/macos-notary-tools:${PATH}', workflow)
        self.assertIn('macos-notary-tools/notary.py --check "${PW_STAPLE_LOG_DIR}"', workflow)
        self.assertIn("if: ${{ always() && matrix.goos == 'darwin' }}", workflow)
        self.assertIn("if: ${{ failure() && matrix.goos == 'darwin' }}", workflow)
        self.assertIn("macos-failed-updater-${{ matrix.updater_key }}", workflow)
        self.assertIn("bundle/macos/*.app.tar.gz", workflow)
