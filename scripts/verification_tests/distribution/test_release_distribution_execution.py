"""Release activation, authenticated storage and feed-plan preparation share durable controls."""
import json
import unittest
from unittest.mock import patch

import release_distribution as distribution
from scripts.release_distribution_contract import ledger
from scripts.verification_tests.distribution.repository_fixture_support import halt_distribution


class ReleaseControlTests(unittest.TestCase):
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
        with patch.dict(distribution.os.environ, env), patch.object(distribution, "storage_json", return_value=(b"", found)), patch.object(distribution, "storage_bytes", side_effect=[missing, raw]), patch.object(distribution.time, "sleep"):
            self.assertEqual(distribution.read_storage("release"), {"version": "1.0.0"})
        with patch.dict(distribution.os.environ, env), patch.object(distribution, "storage_json", return_value=(b"", {"success": True, "result": []})):
            self.assertIsNone(distribution.read_storage("release"))
        with patch.dict(distribution.os.environ, env), patch.object(distribution, "storage_json", return_value=(b"", found)), patch.object(distribution, "storage_bytes", side_effect=[missing] * 12), patch.object(distribution.time, "sleep"):
            with self.assertRaisesRegex(ValueError, "converge"):
                distribution.read_storage("release")

    def test_present_nonobject_storage_controls_refuse_ledger_without_writes(self):
        import hashlib
        env = {'CLOUDFLARE_ACCOUNT_ID': 'account', 'R2_BUCKET': 'bucket', 'CLOUDFLARE_API_TOKEN': 'fixture',
               'RELEASE_R2_TRANSPORT': ''}
        key = 'release-metadata/1.2.3/withdrawn.json'
        for raw in (b'null', b'[]', b'42', b'"value"'):
            found = {'success': True, 'result': [{'key': key, 'etag': hashlib.md5(raw).hexdigest()}]}
            with self.subTest(raw=raw), patch.dict(distribution.os.environ, env), \
                 patch.object(distribution, 'storage_json', return_value=(b'', found)), \
                 patch.object(distribution, 'storage_bytes', return_value=raw), patch.object(ledger.subprocess, 'run') as write:
                for operation in (lambda: distribution.read_storage(key), lambda: ledger.require_publishable('1.2.3'),
                                  lambda: ledger.prepare(['1.2.3']),
                                  lambda: ledger.record([{'version': '1.2.3', 'action': 'withdraw'}], ['1.2.3'])):
                    with self.assertRaisesRegex(ValueError, 'JSON object'):
                        operation()
                write.assert_not_called()
        with patch.dict(distribution.os.environ, env), patch.object(distribution, 'storage_json',
                return_value=(b'', {'success': True, 'result': []})):
            ledger.require_publishable('1.2.3')
            self.assertEqual(ledger.prepare(['1.2.3']), [{'version': '1.2.3', 'action': 'withdraw'}])

    def test_unknown_marker_objects_refuse_real_read_preparation_and_complete_barrier(self):
        import hashlib
        import tempfile
        from pathlib import Path
        from urllib.parse import parse_qs, urlsplit
        from scripts.release_distribution_contract import halt_effects
        versions = ['1.2.3', '1.2.4']
        selected = halt_distribution('1.2.4', None, channels=())
        selected['bad_versions'] = versions
        env = {'CLOUDFLARE_ACCOUNT_ID': 'account', 'R2_BUCKET': 'bucket', 'CLOUDFLARE_API_TOKEN': 'fixture',
               'RELEASE_R2_TRANSPORT': ''}
        cases = [{'version': '1.2.4', 'withdrawn': value} for value in (1, 0, 1.0, False, 'true')]
        cases.extend([{}, {'version': '1.2.4', 'withdrawn': True, 'extra': None},
                      {'version': True, 'withdrawn': True}, {'version': '1.2.3', 'withdrawn': True}])
        for marker in cases:
            raw = json.dumps(marker).encode()
            key = 'release-metadata/1.2.4/withdrawn.json'
            def listing(url, token):
                prefix = parse_qs(urlsplit(url).query)['prefix'][0]
                return b'', {'success': True, 'result': [{'key': key, 'etag': hashlib.md5(raw).hexdigest()}] if prefix == key else []}
            with self.subTest(marker=marker), tempfile.TemporaryDirectory() as temporary, \
                 patch.dict(distribution.os.environ, env), patch.object(distribution, 'storage_json', side_effect=listing), \
                 patch.object(distribution, 'storage_bytes', return_value=raw), patch.object(ledger.subprocess, 'run') as effect:
                root = Path(temporary)
                (root / halt_effects.FILES['selection']).write_text(json.dumps(selected))
                outcomes = {owner: 'skipped' for owner in halt_effects.FILES}
                outcomes['selection'] = 'success'
                try:
                    ledger.prepare(versions)
                except ValueError as error:
                    outcomes['ledger'] = 'failure'
                    (root / 'halt-ledger-refusal.log').write_text(str(error))
                else:
                    self.fail('unknown marker passed real authenticated ledger preparation')
                evidence = halt_effects.admit(root, outcomes)
                self.assertFalse(evidence['complete'])
                self.assertEqual(evidence['effects']['ledger']['actions'][0]['action'], 'refuse')
                self.assertIn('unexpected content', evidence['effects']['ledger']['actions'][0]['diagnostic'])
                with self.assertRaisesRegex(ValueError, 'changed after preparation'):
                    ledger.record([{'version': versions[0], 'action': 'withdraw'},
                                   {'version': versions[1], 'action': 'keep'}], versions)
                effect.assert_not_called()

    def test_authenticated_storage_preserves_the_signed_bytes(self):
        import hashlib
        raw = b'{ "version": "1.0.0" }\n'
        env = {"CLOUDFLARE_ACCOUNT_ID": "account", "R2_BUCKET": "bucket", "CLOUDFLARE_API_TOKEN": "token"}
        found = {"success": True, "result": [{"key": "release", "etag": hashlib.md5(raw).hexdigest()}]}
        with patch.dict(distribution.os.environ, env), patch.object(distribution, "storage_json", return_value=(b"", found)), patch.object(distribution, "storage_bytes", return_value=raw):
            self.assertEqual(distribution.read_storage_bytes("release"), raw)

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
        root = Path(__file__).resolve().parents[3]
        result = subprocess.run(["bash", str(root / "scripts/release-halt.sh"), "--bad", "0.2.0", "--channel", "stable", "--current-file", str(root / "lycaon/testdata/release-halt/current-bad.json"), "--dry-run"], env={**os.environ, "DOWNLOAD_BASE_URL": "https://downloads.paintedwolf.dev"}, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("withdrawn", result.stderr)


class HaltPlanTests(unittest.TestCase):
    def setUp(self):
        import importlib.util
        from pathlib import Path
        spec = importlib.util.spec_from_file_location("halt_plan_tests", Path(__file__).resolve().parents[2].joinpath("release-halt-plan.py"))
        self.plan = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.plan)

    def test_selection_rejects_effect_options_before_reading_release_state(self):
        import sys
        common = ['halt', '--bad', '1.2.3', '--select-only']
        for options in [[], ['--write-plan', 'selected.json'],
                        ['--write-plan', 'selected.json', '--distribution-output', 'distribution.json',
                         '--prepare-output', 'prepared'],
                        ['--write-plan', 'selected.json', '--distribution-output', 'distribution.json',
                         '--apply-prepared', 'prepared']]:
            with self.subTest(options=options), patch.object(sys, 'argv', common + options), \
                 patch.object(self.plan, 'load_registry') as read, \
                 patch.object(self.plan, 'prepare') as prepare, patch.object(self.plan, 'apply') as apply:
                with self.assertRaisesRegex(ValueError, 'selection requires'):
                    self.plan.main()
                read.assert_not_called()
                prepare.assert_not_called()
                apply.assert_not_called()

    def test_cli_validates_prepared_authority_before_apply_and_honors_dry_run(self):
        import sys
        import tempfile
        from pathlib import Path
        reviewed = {'source_generation': 1, 'feeds': [], 'distribution': halt_distribution('1.2.3', None)}
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            plan, ledger = root / 'plan.json', root / 'ledger.json'
            plan.write_text(json.dumps(reviewed))
            retained = [{'version': '1.2.3', 'action': 'withdraw'}]
            ledger.write_text(json.dumps(retained))
            common = ['halt', '--plan', str(plan)]
            modes = [(['--prepare-output', str(root / 'prepared'), '--ledger-plan', str(ledger), '--dry-run'],
                      ['prepare', 'validate']),
                     (['--apply-prepared', str(root / 'prepared')], ['validate', 'apply']),
                     ([], ['prepare', 'validate', 'apply'])]
            for options, expected in modes:
                with self.subTest(options=options), patch.object(sys, 'argv', common + options), \
                     patch.object(self.plan, 'read_storage', return_value=None), \
                     patch.object(self.plan, 'prepare') as prepare, \
                     patch.object(self.plan, 'validate_prepared') as validate, \
                     patch.object(self.plan, 'apply') as apply:
                    order, prepared = [], {'reviewed': 'prepared authority'}
                    prepare.side_effect = lambda *args, **kwargs: order.append('prepare')
                    validate.side_effect = lambda *args: order.append('validate') or prepared
                    apply.side_effect = lambda *args: order.append('apply')
                    self.plan.main()
                    self.assertEqual(order, expected)
                    self.assertEqual(validate.call_args.args[1], reviewed)
                    if '--ledger-plan' in options:
                        self.assertEqual(prepare.call_args.kwargs['ledger_rows'], retained)
                    if 'apply' in expected:
                        self.assertIs(apply.call_args.args[0], prepared)
            with patch.object(sys, 'argv', common + ['--apply-prepared', str(root / 'prepared')]), \
                 patch.object(self.plan, 'read_storage', return_value=None), \
                 patch.object(self.plan, 'validate_prepared', side_effect=ValueError('stale authority')), \
                 patch.object(self.plan, 'apply') as apply:
                with self.assertRaisesRegex(ValueError, 'stale authority'):
                    self.plan.main()
                apply.assert_not_called()
            with patch.object(sys, 'argv', common + ['--prepare-output', 'a', '--apply-prepared', 'b']), \
                 patch.object(self.plan, 'read_storage', return_value=None), \
                 patch.object(self.plan, 'prepare') as prepare, patch.object(self.plan, 'apply') as apply:
                with self.assertRaisesRegex(ValueError, 'choose prepare-output or apply-prepared'):
                    self.plan.main()
                prepare.assert_not_called()
                apply.assert_not_called()

    def test_halt_selection_requires_typed_generation_and_channel_before_storage(self):
        from update_keys import load_registry
        registry = load_registry()
        malformed = [{'source_generation': True, 'feeds': []},
                     {'source_generation': 1, 'feeds': False}]
        malformed += [{'source_generation': 1, 'feeds': [row]} for row in
                      [None, {'generation': True, 'channel': 'stable'},
                       {'generation': 0, 'channel': 'stable'}, {'generation': 1, 'channel': 'nightly'}]]
        for plan in malformed:
            with self.subTest(plan=plan), patch.object(self.plan, 'read_storage') as read:
                with self.assertRaises(ValueError):
                    self.plan.validate_selection(plan, registry)
                read.assert_not_called()

    def test_withdrawal_attempts_other_feed_after_a_write_failure(self):
        import subprocess
        import sys
        import tempfile
        from pathlib import Path
        index = {"distribution": {}, "ledger": [], "feeds": [
            {"generation": number, "channel": channel, "bad": "1.0.0", "pointer_sha256": "a", "signature_sha256": "b"}
            for number, channel in [(1, "stable"), (2, "preview")]]}
        with tempfile.TemporaryDirectory() as tmp:
            with patch.object(self.plan.subprocess, "run", side_effect=[subprocess.CompletedProcess([], code) for code in [0, 1, 0]]) as run:
                with self.assertRaisesRegex(ValueError, "some feeds"):
                    self.plan.apply(index, Path(tmp))
                self.assertEqual(run.call_count, 3)
                self.assertIn("preview", run.call_args.args[0])
                self.assertIn("--signature", run.call_args.args[0])
                receipt = json.loads((Path(tmp) / "application.json").read_text())
                self.assertEqual([row["exit_code"] for row in receipt["feeds"]], [1, 0])

    def test_preparation_failure_never_writes_a_complete_receipt_or_withdraws(self):
        import subprocess
        import tempfile
        from pathlib import Path
        rows = {(n, "stable"): {"bad": "2.0.0", "last_good": "1.0.0"} for n in [1, 2]}
        with tempfile.TemporaryDirectory() as tmp:
            directory = Path(tmp)
            def run(command, **kwargs):
                if command[command.index("--generation") + 1] == "2":
                    raise subprocess.CalledProcessError(1, command)
                (directory / "latest-stable-key-1.json").write_text("{}")
                (directory / "latest-stable-key-1.json.sig").write_text("signature")
                return subprocess.CompletedProcess(command, 0)
            with patch.object(self.plan, "read_storage_bytes", return_value=b'{"version":"2.0.0"}'), patch.object(self.plan.subprocess, "run", side_effect=run) as calls:
                with self.assertRaises(subprocess.CalledProcessError):
                    self.plan.prepare(rows, {}, {"bad_versions": []}, directory, ledger_rows=[])
                self.assertFalse((directory / "prepared.json").exists())
                self.assertTrue(all("--prepare-output" in call.args[0] for call in calls.call_args_list))

    def test_prepared_halt_refuses_changed_bytes_or_feeds_and_accepts_a_completed_retry(self):
        import tempfile
        import hashlib
        from pathlib import Path
        from update_keys import load_registry, release_binding
        fixture = json.loads(Path(__file__).resolve().parents[3].joinpath("lycaon-den/src-tauri/src/update_service/fixtures/signed-feed.json").read_text())
        registry = load_registry()
        registry["generations"][0]["feed_public_key"] = fixture["feed_public_key"]
        rows = {(1, "stable"): {"generation": 1, "channel": "stable", "bad": "1.3.0", "last_good": "1.2.3"}}
        plan = {"source_generation": 1, "feeds": list(rows.values())}
        manifest = {"version": "1.2.3", "update_keys": release_binding(registry, "1.2.3")}
        before = {"version": "1.3.0"}
        with tempfile.TemporaryDirectory() as tmp:
            directory = Path(tmp)
            pointer = directory / "latest-stable-key-1.json"
            signature = directory / "latest-stable-key-1.json.sig"
            pointer.write_text(json.dumps(manifest))
            signature.write_text(fixture["signature"])
            item = {"generation": 1, "channel": "stable", "bad": "1.3.0", "before": before,
                    "pointer_sha256": self.plan.digest(pointer), "signature_sha256": self.plan.digest(signature),
                    "before_pointer_sha256": hashlib.sha256(json.dumps(before).encode()).hexdigest(), "before_signature_sha256": None}
            index = {"format_version": 1, "plan": plan, "distribution": self.plan.distribution_plan(list(rows.values())), "feeds": [item], "ledger": [{"version": "1.3.0", "action": "withdraw"}], "actions": [{"generation": 1, "channel": "stable", "action": "withdraw", "version": "1.3.0"}]}
            (directory / "prepared.json").write_text(json.dumps(index))
            with patch.object(self.plan, "read_storage", return_value=None), patch("feed_signing.verify") as verify, patch.object(self.plan, "read_storage_bytes", side_effect=lambda key: None if key.endswith(".sig") else json.dumps(before).encode()) as read:
                self.plan.validate_prepared(rows, plan, directory, registry)
                read.side_effect = lambda key: signature.read_bytes() if key.endswith(".sig") else pointer.read_bytes()
                self.plan.validate_prepared(rows, plan, directory, registry)
                read.side_effect = lambda key: None if key.endswith(".sig") else b'{"version":"1.4.0"}'
                with self.assertRaisesRegex(ValueError, "feed changed"):
                    self.plan.validate_prepared(rows, plan, directory, registry)
                read.side_effect = lambda key: None if key.endswith(".sig") else json.dumps(before).encode()
                verify.side_effect = ValueError("feed signature verification failed")
                read.reset_mock()
                with self.assertRaisesRegex(ValueError, "verification failed"):
                    self.plan.validate_prepared(rows, plan, directory, registry)
                read.assert_not_called()
                verify.assert_called_with(pointer, fixture["signature"], fixture["feed_public_key"])
                verify.side_effect = None
                pointer.write_text(json.dumps(manifest) + "\n")
                with self.assertRaisesRegex(ValueError, "bytes changed"):
                    self.plan.validate_prepared(rows, plan, directory, registry)

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
            plan = self.plan.plan_withdrawal(registry, 1, "1.0.0", "0.9.0")
        self.assertEqual([row["channel"] for row in plan["feeds"] if "bad" in row], ["stable", "preview"])
        self.assertEqual(plan["feeds"][-1]["keep_version"], "1.1.0-rc.1")
        self.assertEqual([row["channel"] for row in self.plan.distribution_plan(plan["feeds"])["channels"]], ["stable"])

    def test_selection_only_writes_valid_common_facts_without_effect_preparation(self):
        import tempfile
        from pathlib import Path
        import sys
        with tempfile.TemporaryDirectory() as temporary:
            selected = Path(temporary) / 'selected.json'
            distribution = Path(temporary) / 'distribution.json'
            arguments = ['halt', '--bad', '1.2.3', '--select-only',
                         '--write-plan', str(selected), '--distribution-output', str(distribution)]
            with patch.object(sys, 'argv', arguments), patch.object(self.plan, 'read_storage', return_value=None), \
                 patch.object(self.plan, 'prepare') as prepare, patch.object(self.plan, 'apply') as apply:
                self.plan.main()
            prepare.assert_not_called()
            apply.assert_not_called()
            self.assertEqual(json.loads(distribution.read_bytes())['channels'], [
                halt_distribution('1.2.3', None)['channels'][index] for index, channel in enumerate(('stable', 'preview'))])
            requested = Path(temporary) / 'requested.json'
            malformed = json.loads(selected.read_bytes())
            malformed['distribution']['channels'][0]['last_good'] = '1.1.0'
            requested.write_text(json.dumps(malformed))
            selected.unlink()
            distribution.unlink()
            arguments = ['halt', '--plan', str(requested), '--select-only', '--write-plan', str(selected), '--distribution-output', str(distribution)]
            with patch.object(sys, 'argv', arguments), patch.object(self.plan, 'read_storage', return_value=None), \
                 self.assertRaises(ValueError):
                self.plan.main()
            self.assertFalse(selected.exists())
            self.assertFalse(distribution.exists())


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


class PermanentWithdrawalTests(unittest.TestCase):
    def test_unactivated_withdrawal_is_recorded_once_and_blocks_publication(self):
        captured = []
        from pathlib import Path
        def put(command, **kwargs):
            captured.append(json.loads(Path(command[command.index("--file") + 1]).read_text()))
        plan = {"bad_versions": ["1.0.0"]}
        with patch.object(distribution, "read_storage", return_value=None), patch.object(distribution.subprocess, "run", side_effect=put):
            ledger.record(ledger.prepare(plan["bad_versions"]), plan["bad_versions"])
        self.assertEqual(captured, [{"version": "1.0.0", "withdrawn": True}])
        with patch.object(distribution, "read_storage", return_value=captured[0]) as read, patch.object(distribution.subprocess, "run") as put_again:
            ledger.record(ledger.prepare(plan["bad_versions"]), plan["bad_versions"])
            put_again.assert_not_called()
            with self.assertRaisesRegex(ValueError, "cannot be published"):
                ledger.require_publishable("1.0.0")
            read.assert_called_with("release-metadata/1.0.0/withdrawn.json")
