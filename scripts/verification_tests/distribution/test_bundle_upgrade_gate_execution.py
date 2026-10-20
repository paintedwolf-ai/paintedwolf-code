"""Gate receipts distinguish failure, excluded coverage, and confirmed activation."""
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from release_upgrade.case import confirmed
from release_upgrade.prepare import file_digest
import release_upgrade.rehearse as gate
import release_upgrade.host as host


class GateTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.fixtures = self.root / "fixtures"
        self.fixtures.mkdir()
        self.plan = {"format_version": 1, "source_sha": "a" * 40, "candidate": {"version": "1.1.0"},
                     "cases": [], "excluded_sources": [{"source_version": "1.0.1"}],
                     "skip_reason": "no_automatic_upgrade_source"}
        self.evidence = self.root / "evidence"
        self.case_id = "not_applicable"

    def inventory(self):
        (self.fixtures / "plan.json").write_text(json.dumps(self.plan))
        (self.fixtures / "inventory.json").write_text(json.dumps({"plan.json": file_digest(self.fixtures / "plan.json")}))

    def test_no_automatic_source_records_not_applicable_without_mutating_the_host(self):
        self.inventory()
        with patch.object(gate, "require_disposable_host") as guard:
            gate.rehearse(self.fixtures, self.evidence, self.case_id)
        receipt = json.loads((self.evidence / "receipt.json").read_text())
        self.assertEqual(receipt["result"], "not_applicable")
        self.assertEqual(receipt["reason"], self.plan["skip_reason"])
        self.assertEqual(receipt["excluded_sources"], self.plan["excluded_sources"])
        guard.assert_not_called()

    def test_corruption_or_extra_files_fail_before_host_mutation(self):
        for extra in (False, True):
            with self.subTest(extra=extra):
                self.inventory()
                if extra:
                    (self.fixtures / "extra").touch()
                else:
                    (self.fixtures / "plan.json").write_text("changed")
                with self.assertRaises(ValueError):
                    gate.verify_inventory(self.fixtures)

    def test_failed_host_admission_retains_a_failed_source_bound_receipt(self):
        self.plan["cases"] = [{"id": "1.0.2-preview", "source_version": "1.0.2", "channel": "preview"}]
        self.case_id = "1.0.2-preview"
        self.plan["skip_reason"] = None
        self.inventory()
        with patch.object(gate, "require_disposable_host", side_effect=ValueError("not disposable")):
            with self.assertRaisesRegex(ValueError, "not disposable"):
                gate.rehearse(self.fixtures, self.evidence, self.case_id)
        receipt = json.loads((self.evidence / "receipt.json").read_text())
        self.assertEqual(receipt["result"], "failed")
        self.assertEqual(receipt["source_sha"], self.plan["source_sha"])

    def test_confirmation_requires_cleanup_and_the_expected_release_and_installation(self):
        app = self.root / "Product.app"
        previous = self.root / ".paintedwolf-update-prior.app"
        previous.mkdir()
        transaction = {"phase": "startup_confirmed", "candidate": {"version": "1.1.0"},
                       "target": str(app), "prepared_bundle": str(previous)}
        self.assertFalse(confirmed(transaction, "1.1.0", app))
        previous.rmdir()
        self.assertTrue(confirmed(transaction, "1.1.0", app))
        for modified in ({"target": "/other.app"}, {"candidate": {"version": "1.0.2"}},
                         {"prepared_bundle": str(self.root / "unrelated.app")}, {"phase": "failed"}):
            with self.subTest(modified=modified), self.assertRaises(ValueError):
                confirmed({**transaction, **modified}, "1.1.0", app)
        self.assertFalse(confirmed(None, "1.1.0", app))
        self.assertFalse(confirmed({"phase": "activated"}, "1.1.0", app))

    def test_local_and_self_hosted_hosts_are_refused(self):
        with patch.dict(host.os.environ, {"GITHUB_ACTIONS": "false"}, clear=True):
            with self.assertRaisesRegex(ValueError, "disposable"):
                host.require_disposable_host()
        with patch.object(host.platform, "system", return_value="Darwin"), \
             patch.object(host.platform, "machine", return_value="arm64"), \
             patch.dict(host.os.environ, {"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "self-hosted"}, clear=True):
            with self.assertRaisesRegex(ValueError, "disposable"):
                host.require_disposable_host()

    def test_staged_identity_must_match_the_signed_offer(self):
        from release_upgrade.case import candidate_matches
        from verification_tests.distribution.test_bundle_upgrade_prepare_execution import manifest
        step = manifest("1.1.0")
        candidate = {"version": "1.1.0", "channel": "preview", "platform": "darwin-aarch64", "signing_generation": 1,
                     "artifact_url": step["platforms"]["darwin-aarch64"]["url"],
                     "artifact_signature": step["platforms"]["darwin-aarch64"]["signature"]}
        self.assertTrue(candidate_matches(candidate, step, "preview"))
        for key in candidate:
            with self.subTest(key=key):
                self.assertFalse(candidate_matches({**candidate, key: "different"}, step, "preview"))

    def test_each_case_must_observe_its_own_feed_signature_and_archive_requests(self):
        from verification_tests.distribution.test_bundle_upgrade_prepare_execution import manifest
        from release_upgrade.prepare import archive_path
        step = manifest("1.1.0")
        case = {"channel": "preview", "steps": [step]}
        routes = ["/updates/preview/key-1/latest.json", "/updates/preview/key-1/latest.json.sig", "/" + archive_path(step)]
        log = self.root / "requests.jsonl"
        log.write_text("\n".join(json.dumps({"path": route, "status": 200}) for route in routes))
        gate.verify_requests(log, case, 0)
        with self.assertRaises(ValueError):
            gate.verify_requests(log, case, 1)
        log.write_text("\n".join(json.dumps({"path": route, "status": 404}) for route in routes))
        with self.assertRaises(ValueError):
            gate.verify_requests(log, case, 0)

    def test_native_rejections_fail_staging_without_waiting_for_a_timeout(self):
        from release_upgrade.case import staged
        records = self.root / "records"
        records.mkdir()
        self.assertIsNone(staged(records))
        (records / "rejected.json").write_text('{"format_version":1,"release_id":"rejected"}')
        with self.assertRaisesRegex(ValueError, "rejected"):
            staged(records)
        (records / "rejected.json").unlink()
        (records / "transaction.json").write_text('{"phase":"failed"}')
        with self.assertRaisesRegex(ValueError, "transaction failed"):
            staged(records)

    def test_case_diagnostics_survive_a_separate_host_cleanup_failure(self):
        from contextlib import contextmanager
        self.plan["cases"] = [{"id": "1.0.2-preview", "source_version": "1.0.2", "channel": "preview"}]
        self.case_id = "1.0.2-preview"
        self.plan["skip_reason"] = None
        self.inventory()
        @contextmanager
        def origin(*_):
            try:
                yield
            finally:
                raise ValueError("host cleanup failed")
        with patch.object(gate, "require_disposable_host", return_value=self.root / "config"), \
             patch.object(gate, "local_origin", origin), \
             patch.object(gate, "run_case", side_effect=ValueError("candidate rejected")):
            with self.assertRaisesRegex(ValueError, "host cleanup failed"):
                gate.rehearse(self.fixtures, self.evidence, self.case_id)
        receipt = json.loads((self.evidence / "receipt.json").read_text())
        self.assertEqual(receipt["cases"][0]["error"], "candidate rejected")
        self.assertEqual(receipt["error"], "host cleanup failed")

    def test_activation_preserves_channel_automatic_updates_and_rollout_bucket(self):
        from release_upgrade.case import verify_preferences
        path = self.root / "preferences.json"
        original = {"format_version": 2, "channel": "preview", "automatic_updates_enabled": True, "rollout_bucket": 0}
        path.write_text(json.dumps(original))
        verify_preferences(path, "preview")
        for change in ({"channel": "stable"}, {"automatic_updates_enabled": False}, {"rollout_bucket": 1},
                       {"rollout_bucket": False}, {"rollout_bucket": 0.0}, {"format_version": 99}):
            with self.subTest(change=change):
                path.write_text(json.dumps({**original, **change}))
                with self.assertRaisesRegex(ValueError, "preferences changed"):
                    verify_preferences(path, "preview")

    def test_interruption_retains_cancelled_evidence_and_exits_the_host_context(self):
        from contextlib import contextmanager
        self.plan["cases"] = [{"id": "1.0.2-preview", "source_version": "1.0.2", "channel": "preview"}]
        self.case_id = "1.0.2-preview"
        self.plan["skip_reason"] = None
        self.inventory()
        cleaned = []
        @contextmanager
        def origin(*_):
            try:
                yield
            finally:
                cleaned.append(True)
        with patch.object(gate, "require_disposable_host", return_value=self.root / "config"), \
             patch.object(gate, "local_origin", origin), \
             patch.object(gate, "run_case", side_effect=KeyboardInterrupt("cancelled")):
            with self.assertRaises(KeyboardInterrupt):
                gate.rehearse(self.fixtures, self.evidence, self.case_id)
        receipt = json.loads((self.evidence / "receipt.json").read_text())
        self.assertEqual(receipt["result"], "cancelled")
        self.assertEqual(receipt["cases"][0]["error"], "cancelled")
        self.assertEqual(cleaned, [True])

    def test_unknown_fixture_formats_and_unexplained_empty_matrices_are_refused(self):
        for change in ({"format_version": 99}, {"format_version": True}, {"cases": {}}, {"cases": [{}]}, {"skip_reason": None}):
            with self.subTest(change=change):
                original = dict(self.plan)
                self.plan.update(change)
                self.inventory()
                with self.assertRaises(ValueError):
                    gate.verify_inventory(self.fixtures)
                self.plan = original

    def test_new_structured_native_errors_fail_staging_and_prior_diagnostics_are_ignored(self):
        from release_upgrade.case import staged
        records = self.root / "records"
        records.mkdir()
        diagnostics = self.root / "updates.log"
        diagnostics.write_text('{"error":{"code":"prior_failure"}}\n')
        offset = diagnostics.stat().st_size
        self.assertIsNone(staged(records, diagnostics, offset))
        with diagnostics.open("a") as output:
            output.write('diagnostic prose is not a failure code\n')
        self.assertIsNone(staged(records, diagnostics, offset))
        with diagnostics.open("a") as output:
            output.write('{"error":{"code":"feed_rejected"}}\n')
        with self.assertRaisesRegex(ValueError, "feed_rejected"):
            staged(records, diagnostics, offset)

    def test_retained_diagnostics_exclude_the_api_token(self):
        from release_upgrade.case import retain_state
        config, records, evidence = self.root / "config", self.root / "records", self.root / "logs"
        for path in (config, records, evidence):
            path.mkdir()
        for name in ("engine.log", "updates.log", "api.token"):
            (config / name).write_text("fixture")
        (records / "transaction.json").write_text('{"phase":"failed"}')
        retain_state(config, records, evidence)
        self.assertEqual({path.name for path in evidence.iterdir()}, {"engine.log", "updates.log", "final-transaction.json"})
