"""Publication confirmation must agree with the served download page."""
import io
import json
import unittest
from unittest.mock import patch

import release_distribution as distribution


class Response(io.BytesIO):
    def __init__(self, body, cache="no-store"):
        super().__init__(body)
        self.headers = {"Cache-Control": cache}


class WebsitePublicationTests(unittest.TestCase):
    def setUp(self):
        self.expected = {"channel": "stable", "version": "1.0.0", "manifest_sha256": "a" * 64, "withdrawn_version": None}
        self.receipt = json.dumps({"schema_version": 1, **self.expected}).encode()
        self.page = b'<a data-release-channel="stable" download href="https://downloads.paintedwolf.dev/releases/v1.0.0/painted-wolf-code_v1.0.0_darwin-aarch64.dmg">Download</a>'

    def test_awaits_actual_download_page_with_matching_confirmation(self):
        with patch.object(distribution.urllib.request, "urlopen", side_effect=[Response(self.receipt), Response(self.page)]) as get:
            distribution.wait_for_website("https://paintedwolf.ai", self.expected, seconds=0)
        self.assertEqual(get.call_count, 2)
        self.assertEqual(get.call_args_list[1].args[0].full_url, "https://paintedwolf.ai/download/")

    def test_preview_waits_on_the_unlisted_preview_page(self):
        expected = {**self.expected, "channel": "preview", "version": "1.0.0-rc.3"}
        receipt = json.dumps({"schema_version": 1, **expected}).encode()
        page = self.page.replace(b"stable", b"preview").replace(b"1.0.0", b"1.0.0-rc.3")
        with patch.object(distribution.urllib.request, "urlopen", side_effect=[Response(receipt), Response(page)]) as get:
            distribution.wait_for_website("https://paintedwolf.ai", expected, seconds=0)
        self.assertEqual(get.call_args_list[1].args[0].full_url, "https://paintedwolf.ai/download/preview/")

    def test_stale_page_or_cache_headers_cannot_confirm_a_release(self):
        for page, headers in [(self.page.replace(b"1.0.0", b"0.9.0"), "no-store"), (self.page, "public, max-age=3600")]:
            with self.subTest(headers=headers), patch.object(distribution.urllib.request, "urlopen", side_effect=[Response(self.receipt), Response(page, headers)]):
                with self.assertRaisesRegex(ValueError, "not verified"):
                    distribution.wait_for_website("https://paintedwolf.ai", self.expected, seconds=0)

    def test_unpublished_channel_has_no_download_link(self):
        expected = {**self.expected, "version": None, "manifest_sha256": None, "withdrawn_version": "1.0.0"}
        distribution.validate_website_downloads('<p>Downloads unavailable</p>', expected)
        with self.assertRaises(ValueError):
            distribution.validate_website_downloads(self.page.decode(), expected)

    def test_stable_verifies_preview_when_it_advances(self):
        for previous, expected in [(None, ["stable", "preview"]), ({"version": "1.0.0-rc.1"}, ["stable", "preview"]),
                                   ({"version": "1.0.0"}, ["stable", "preview"]), ({"version": "1.1.0-rc.1"}, ["stable"])]:
            with patch.object(distribution, "read_storage", return_value=previous):
                self.assertEqual(distribution.publication_channels("stable", "1.0.0", 1), expected)
        with patch.object(distribution, "read_storage") as read:
            self.assertEqual(distribution.publication_channels("preview", "1.0.0-rc.1", 1), ["preview"])
            read.assert_not_called()

    def test_preview_storage_failure_cannot_be_treated_as_first_publication(self):
        with patch.object(distribution, "read_storage", side_effect=ValueError("storage unavailable")) as read:
            with self.assertRaisesRegex(ValueError, "storage unavailable"):
                distribution.publication_channels("stable", "1.0.0", 2)
            read.assert_called_once_with("updates/preview/key-2/latest.json")

    def test_stable_confirmation_preserves_preview_from_interrupted_activation(self):
        expected = {**self.expected, "channel": "preview"}
        newer = {**expected, "version": "1.1.0-rc.1", "manifest_sha256": "b" * 64}
        page = self.page.replace(b"stable", b"preview").replace(b"1.0.0", b"1.1.0-rc.1")
        responses = [Response(json.dumps({"schema_version": 1, **newer}).encode()), Response(page)]
        with patch.object(distribution.urllib.request, "urlopen", side_effect=responses):
            distribution.wait_for_website("https://paintedwolf.ai", expected, seconds=0, allow_newer=True)
        wrong_channel = {"schema_version": 1, **newer, "channel": "stable"}
        with patch.object(distribution.urllib.request, "urlopen", return_value=Response(json.dumps(wrong_channel).encode())):
            with self.assertRaisesRegex(ValueError, "not verified"):
                distribution.wait_for_website("https://paintedwolf.ai", expected, seconds=0, allow_newer=True)

    def test_incorrect_confirmation_never_accepts_a_valid_page(self):
        with patch.object(distribution.urllib.request, "urlopen", return_value=Response(b'{}')) as get:
            with self.assertRaisesRegex(ValueError, "not verified"):
                distribution.wait_for_website("https://paintedwolf.ai", self.expected, seconds=0)
        self.assertEqual(get.call_count, 1)


class WebsiteWithdrawalTests(unittest.TestCase):
    def test_first_release_withdrawal_sends_null_replacement_and_checks_live_page(self):
        import release_withdrawal as withdrawal
        prepared = {"bad_versions": [], "channels": [{"channel": "stable", "bad": "1.0.0", "last_good": None,
            "path": "repos/owner/tap/contents/Casks/painted-wolf-code.rb", "sha": "current", "change_tap": True, "content": None, "manifest_sha256": None}]}
        with patch.dict(withdrawal.os.environ, {"HOMEBREW_TAP_TOKEN": "tap", "WWW_DISPATCH_TOKEN": "site"}), \
             patch.object(withdrawal, "api") as api, patch.object(withdrawal, "wait_for_withdrawal") as wait:
            withdrawal.apply(prepared)
        self.assertEqual(api.call_args_list[0].args[2], "DELETE")
        event = api.call_args_list[1].args[3]
        self.assertEqual(event, {"event_type": "lycaon-release-halt", "client_payload": {
            "channel": "stable", "bad_version": "1.0.0", "last_good_version": None, "manifest_sha256": None}})
        self.assertEqual(wait.call_args.args[0], prepared["channels"][0])

    def test_package_failure_does_not_prevent_website_withdrawal(self):
        import release_withdrawal as withdrawal
        prepared = {"bad_versions": [], "channels": [{"channel": "preview", "bad": "1.0.0-rc.2", "last_good": "1.0.0-rc.1",
            "path": "repos/owner/tap/contents/Casks/painted-wolf-code@preview.rb", "sha": "current", "change_tap": True, "content": "replacement", "manifest_sha256": "a" * 64}]}
        with patch.dict(withdrawal.os.environ, {"HOMEBREW_TAP_TOKEN": "tap", "WWW_DISPATCH_TOKEN": "site"}), \
             patch.object(withdrawal, "api", side_effect=[ValueError("tap unavailable"), None]) as api, \
             patch.object(withdrawal, "wait_for_withdrawal") as wait:
            with self.assertRaisesRegex(ValueError, "tap unavailable"):
                withdrawal.apply(prepared)
        self.assertEqual(api.call_args_list[1].args[3]["client_payload"]["last_good_version"], "1.0.0-rc.1")
        self.assertEqual(wait.call_count, 1)

    def test_missing_github_object_is_distinct_from_permission_failure(self):
        import release_withdrawal as withdrawal
        import urllib.error
        for status in (404, 403):
            error = urllib.error.HTTPError("https://api.github.com", status, "failure", {}, None)
            with patch.object(withdrawal.urllib.request, "urlopen", side_effect=error):
                if status == 404:
                    self.assertIsNone(withdrawal.api("token", "repos/owner/site"))
                else:
                    with self.assertRaisesRegex(ValueError, "HTTP 403"):
                        withdrawal.api("token", "repos/owner/site")


class ReleaseControlTests(unittest.TestCase):
    def test_manual_tag_rehearsal_never_publishes(self):
        from release_control import publishes
        self.assertFalse(publishes("workflow_dispatch", "tag"))
        self.assertFalse(publishes("workflow_dispatch", "branch"))
        self.assertTrue(publishes("push", "tag"))
        with self.assertRaises(ValueError):
            publishes("push", "branch")

    def test_first_stable_checks_the_published_preview_and_bootstrap_has_no_prior(self):
        from release_control import prior_versions
        self.assertEqual(prior_versions({"stable": None, "preview": None}, "1.0.0"), [])
        self.assertEqual(prior_versions({"stable": None, "preview": {"version": "1.0.0-rc.2"}}, "1.0.0"), [])
        self.assertEqual(prior_versions({"stable": None, "preview": {"version": "1.0.0-rc.2", "withdrawn": True}}, "1.0.0-rc.3"), [])
        self.assertEqual(prior_versions({"stable": {"version": "1.0.0"}, "preview": {"version": "1.1.0-rc.1", "withdrawn": True}}, "1.1.0"), ["1.0.0", "1.1.0-rc.1"])

    def test_activation_preserves_start_time_and_binds_original_manifest(self):
        import hashlib
        manifest = {"version": "1.0.0", "pub_date": "2020-01-01T00:00:00Z"}
        record = {"version": "1.0.0", "manifest_sha256": hashlib.sha256(distribution.encoded(manifest)).hexdigest(), "started_at": "2026-09-14T00:00:00Z"}
        active = distribution.activation_manifest(manifest, record)
        self.assertEqual(active["pub_date"], record["started_at"])
        self.assertEqual(manifest["pub_date"], "2020-01-01T00:00:00Z")
        with self.assertRaises(ValueError):
            distribution.activation_manifest({**manifest, "notes": "changed"}, record)
        self.assertTrue(distribution.validate_advance(manifest, active))
        with self.assertRaises(ValueError):
            distribution.validate_advance({**manifest, "notes": "changed"}, active)
        with self.assertRaises(ValueError):
            distribution.validate_advance(manifest, {**active, "withdrawn": True})
        self.assertFalse(distribution.validate_advance(manifest, {**active, "version": "1.1.0"}, True))

    def test_storage_distinguishes_absence_from_cached_miss(self):
        import hashlib
        import urllib.error
        raw = b'{"version":"1.0.0"}'
        found = {"success": True, "result": [{"key": "release", "etag": hashlib.md5(raw).hexdigest()}]}
        missing = urllib.error.HTTPError("https://example.test", 404, "missing", {}, None)
        env = {"CLOUDFLARE_ACCOUNT_ID": "account", "R2_BUCKET": "bucket", "CLOUDFLARE_API_TOKEN": "secret"}
        with patch.dict(distribution.os.environ, env), patch.object(distribution, "storage_json", side_effect=[(b"", found), missing, (raw, json.loads(raw))]), patch.object(distribution.time, "sleep"):
            self.assertEqual(distribution.read_storage("release"), {"version": "1.0.0"})
        with patch.dict(distribution.os.environ, env), patch.object(distribution, "storage_json", return_value=(b"", {"success": True, "result": []})):
            self.assertIsNone(distribution.read_storage("release"))
        with patch.dict(distribution.os.environ, env), patch.object(distribution, "storage_json", side_effect=[(b"", found)] + [missing] * 12), patch.object(distribution.time, "sleep"):
            with self.assertRaisesRegex(ValueError, "converge"):
                distribution.read_storage("release")

    def test_canonical_pointer_requires_final_response_no_store(self):
        from release_pointer_headers import validate
        validate("HTTP/1.1 200 OK\r\nCache-Control: no-cache, no-store, must-revalidate\r\n\r\n")
        with self.assertRaises(ValueError):
            validate("HTTP/2 200\r\nCache-Control: no-store\r\nCF-Cache-Status: HIT\r\n\r\n")
        with self.assertRaises(ValueError):
            validate("HTTP/1.1 302 Found\r\nCache-Control: no-store\r\n\r\nHTTP/2 200\r\nCache-Control: max-age=3600\r\n\r\n")

    def test_first_release_halt_accepts_no_replacement_locally(self):
        import os
        import subprocess
        from pathlib import Path
        root = Path(__file__).resolve().parents[2]
        result = subprocess.run(["bash", str(root / "scripts/release-halt.sh"), "--bad", "0.2.0", "--channel", "stable", "--current-file", str(root / "lycaon/testdata/release-halt/current-bad.json"), "--dry-run"], env={**os.environ, "DOWNLOAD_BASE_URL": "https://downloads.paintedwolf.dev"}, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("withdrawn", result.stderr)


class HaltPlanTests(unittest.TestCase):
    def setUp(self):
        import importlib.util
        from pathlib import Path
        spec = importlib.util.spec_from_file_location("halt_plan_tests", Path(__file__).resolve().parents[1].joinpath("release-halt-plan.py"))
        self.plan = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.plan)

    def test_withdrawal_attempts_other_feed_after_a_write_failure(self):
        import subprocess
        import sys
        import tempfile
        from pathlib import Path
        plan = {"source_generation": 1, "feeds": [{"generation": 1, "channel": channel, "bad": "1.0.0", "last_good": None} for channel in ("stable", "preview")]}
        with tempfile.TemporaryDirectory() as tmp:
            source = Path(tmp) / "plan.json"
            source.write_text(json.dumps(plan))
            with patch.object(sys, "argv", ["halt-plan", "--plan", str(source)]), \
                 patch.object(self.plan, "load_registry", return_value={"generations": [{}]}), \
                 patch.object(self.plan, "generation"), patch.object(self.plan, "validate_binding"), \
                 patch.object(self.plan, "read_storage", return_value={"version": "1.0.0"}), \
                 patch.object(self.plan.subprocess, "run", side_effect=[subprocess.CompletedProcess([], code) for code in [0, 0, 1, 0]]) as run:
                with self.assertRaisesRegex(ValueError, "some feeds"):
                    self.plan.main()
                self.assertEqual(run.call_count, 4)
                self.assertIn("preview", run.call_args.args[0])
                self.assertNotIn("--dry-run", run.call_args.args[0])

    def test_unactivated_release_still_has_a_distribution_withdrawal(self):
        with patch.object(self.plan, "generation"), patch.object(self.plan, "read_storage", return_value=None):
            plan = self.plan.plan_withdrawal({"generations": [{}]}, 1, "1.0.0", None)
        self.assertEqual(plan["feeds"], [])
        distribution = self.plan.distribution_plan([], "1.0.0", None)
        self.assertEqual(distribution["bad_versions"], ["1.0.0"])
        self.assertEqual([row["channel"] for row in distribution["channels"]], ["stable", "preview"])

    def test_inventory_withdraws_both_channels_and_keeps_newer_preview(self):
        registry = {"generations": [{}, {}]}
        values = [{"version": "1.0.0"}, {"version": "1.0.0"}, None, {"version": "1.1.0-rc.1"}]
        with patch.object(self.plan, "generation"), patch.object(self.plan, "validate_binding"), \
             patch.object(self.plan, "read_storage", side_effect=values):
            plan = self.plan.plan_withdrawal(registry, 1, "1.0.0", None)
        self.assertEqual([row["channel"] for row in plan["feeds"] if "bad" in row], ["stable", "preview"])
        self.assertEqual(plan["feeds"][-1]["keep_version"], "1.1.0-rc.1")
        self.assertEqual([row["channel"] for row in self.plan.distribution_plan(plan["feeds"])["channels"]], ["stable"])


class ActivationRetryTests(unittest.TestCase):
    def test_retry_reuses_original_start_time_and_never_rewrites_record(self):
        import tempfile
        from pathlib import Path
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source, output = root / "manifest.json", root / "active.json"
            source.write_text(json.dumps({"version": "1.0.0", "pub_date": "2020-01-01T00:00:00Z"}))
            created = []
            def put(command, **kwargs):
                created.append(json.loads(Path(command[command.index("--file") + 1]).read_text()))
            with patch.object(distribution, "read_storage", return_value=None), patch.object(distribution.subprocess, "run", side_effect=put):
                distribution.activate(source, output)
            first = output.read_bytes()
            with patch.object(distribution, "read_storage", side_effect=lambda key: None if key.endswith("/withdrawn.json") else created[0]), patch.object(distribution.subprocess, "run") as put_again:
                distribution.activate(source, output)
                put_again.assert_not_called()
            self.assertEqual(output.read_bytes(), first)
            self.assertNotEqual(json.loads(first)["pub_date"], "2020-01-01T00:00:00Z")


class PackagePublicationTests(unittest.TestCase):
    def test_newer_preview_is_preserved_even_if_its_updater_was_never_activated(self):
        import tempfile
        from pathlib import Path
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source, tap = root / "source", root / "tap"
            source.mkdir()
            (tap / "Casks").mkdir(parents=True)
            for name in ("painted-wolf-code.rb", "painted-wolf-code@preview.rb"):
                (source / name).write_text('  version "1.0.0"\n')
            preview = tap / "Casks/painted-wolf-code@preview.rb"
            preview.write_text('  version "1.1.0-rc.1"\n')
            distribution.stage_casks(source, tap, "stable")
            self.assertIn("1.1.0-rc.1", preview.read_text())
            self.assertIn("1.0.0", (tap / "Casks/painted-wolf-code.rb").read_text())
            (source / "painted-wolf-code.rb").write_text('  version "1.0.0"\nchanged')
            with self.assertRaisesRegex(ValueError, "same version"):
                distribution.stage_casks(source, tap, "stable")


class PartialWithdrawalTests(unittest.TestCase):
    def test_package_preparation_preserves_an_unrelated_advertised_version(self):
        import base64
        import release_withdrawal as withdrawal
        current = {"sha": "current", "content": base64.b64encode(b'  version "1.1.0"\n').decode()}
        env = {"HOMEBREW_TAP_REPO": "owner/tap", "HOMEBREW_TAP_TOKEN": "tap", "WWW_DISPATCH_TOKEN": "site"}
        plan = {"bad_versions": ["1.0.0"], "channels": [{"channel": "stable", "bad": "1.0.0", "last_good": None}]}
        with patch.dict(withdrawal.os.environ, env), patch.object(withdrawal, "api", side_effect=[{}, {}, current]), patch.object(withdrawal, "download") as download:
            result = withdrawal.prepare(plan)
        self.assertFalse(result["channels"][0]["change_tap"])
        self.assertIsNone(result["channels"][0]["sha"])
        download.assert_not_called()

    def test_website_confirmation_requires_recorded_withdrawal_and_real_download_state(self):
        import base64
        import release_withdrawal as withdrawal
        row = {"channel": "stable", "bad": "1.0.0", "last_good": None}
        channel = {"release": {"version": "1.1.0", "manifest_sha256": "b" * 64}, "withdrawn_versions": ["1.0.0"], "withdrawn_version": None}
        stored = {"content": base64.b64encode(json.dumps({"channels": {"stable": channel}}).encode()).decode()}
        with patch.dict(withdrawal.os.environ, {"WWW_DISPATCH_TOKEN": "site"}), patch.object(withdrawal, "api", return_value=stored), patch.object(withdrawal, "wait_for_website") as wait:
            withdrawal.wait_for_withdrawal(row, seconds=0)
        self.assertEqual(wait.call_args.args[1], {"channel": "stable", "version": "1.1.0", "manifest_sha256": "b" * 64, "withdrawn_version": None})
        with patch.dict(withdrawal.os.environ, {"WWW_DISPATCH_TOKEN": "site"}), patch.object(withdrawal, "api", return_value=None):
            with self.assertRaisesRegex(ValueError, "recorded"):
                withdrawal.wait_for_withdrawal(row, seconds=0)


class PermanentWithdrawalTests(unittest.TestCase):
    def test_unactivated_withdrawal_is_recorded_once_and_blocks_publication(self):
        captured = []
        from pathlib import Path
        def put(command, **kwargs):
            captured.append(json.loads(Path(command[command.index("--file") + 1]).read_text()))
        plan = {"bad_versions": ["1.0.0"]}
        with patch.object(distribution, "read_storage", return_value=None), patch.object(distribution.subprocess, "run", side_effect=put):
            distribution.record_withdrawals(plan)
        self.assertEqual(captured, [{"version": "1.0.0", "withdrawn": True}])
        with patch.object(distribution, "read_storage", return_value=captured[0]) as read, patch.object(distribution.subprocess, "run") as put_again:
            distribution.record_withdrawals(plan)
            put_again.assert_not_called()
            with self.assertRaisesRegex(ValueError, "cannot be published"):
                distribution.require_publishable("1.0.0")
            read.assert_called_with("release-metadata/1.0.0/withdrawn.json")
