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
        self.plan = {"source_sha": "a" * 40, "candidate": {"version": "1.1.0"},
                     "cases": [], "excluded_sources": [{"source_version": "1.0.1"}],
                     "skip_reason": "no_automatic_upgrade_source"}
        self.evidence = self.root / "evidence"

    def inventory(self):
        (self.fixtures / "plan.json").write_text(json.dumps(self.plan))
        (self.fixtures / "inventory.json").write_text(json.dumps({"plan.json": file_digest(self.fixtures / "plan.json")}))

    def test_no_automatic_source_records_not_applicable_without_mutating_the_host(self):
        self.inventory()
        with patch.object(gate, "require_disposable_host") as guard:
            gate.rehearse(self.fixtures, self.evidence)
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
        self.plan["cases"] = [{"id": "prior-preview"}]
        self.inventory()
        with patch.object(gate, "require_disposable_host", side_effect=ValueError("not disposable")):
            with self.assertRaisesRegex(ValueError, "not disposable"):
                gate.rehearse(self.fixtures, self.evidence)
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
