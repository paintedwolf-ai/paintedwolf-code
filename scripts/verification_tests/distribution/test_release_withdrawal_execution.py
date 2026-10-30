"""Package, website and mirror withdrawal prepare and re-admit their actual controls."""
import io
import json
import unittest
from unittest.mock import patch

from scripts.verification_tests.distribution.repository_fixture_support import halt_distribution


class WebsiteWithdrawalTests(unittest.TestCase):
    def prepared(self, *, channel='stable', bad='2.0.0', good=None, content=None):
        selected = halt_distribution(bad, good, channels=(channel,))
        row = selected['channels'][0]
        row.update(path='repos/owner/tap/contents/Casks/painted-wolf-code.rb', sha='current',
            tap_action='withdraw', website_action='withdraw', advertised={}, content=content,
            manifest_sha256=row['last_good_manifest_sha256'], bad_distribution_sha256='b' * 64)
        return {**selected, 'mirrors': []}

    def test_first_release_withdrawal_sends_all_eight_fields_and_checks_live_page(self):
        import release_withdrawal as withdrawal
        prepared = self.prepared()
        with patch.dict(withdrawal.os.environ, {'HOMEBREW_TAP_TOKEN': 'tap', 'WWW_DISPATCH_TOKEN': 'site'}), \
             patch.object(withdrawal, 'api') as api, patch.object(withdrawal, 'identities', return_value={
                'distribution_sha256': None, 'bad_distribution_sha256': 'b' * 64,
                'last_good_legacy': False, 'bad_legacy': False}), patch.object(withdrawal, 'wait_for_withdrawal') as wait:
            withdrawal.apply(prepared)
        self.assertEqual(api.call_args_list[0].args[2], 'DELETE')
        self.assertEqual(api.call_args_list[1].args[3], {'event_type': 'painted-wolf-release-halt', 'client_payload': {
            'channel': 'stable', 'bad_version': '2.0.0', 'last_good_version': None, 'manifest_sha256': None,
            'distribution_sha256': None, 'bad_distribution_sha256': 'b' * 64,
            'last_good_legacy': False, 'bad_legacy': False}})
        self.assertEqual(wait.call_args.args[0], prepared['channels'][0])

    def test_package_write_failure_still_attempts_prepared_website_withdrawal(self):
        import release_withdrawal as withdrawal
        prepared = self.prepared(channel='preview', bad='2.0.0-rc.2', good='2.0.0-rc.1', content='replacement')
        with patch.dict(withdrawal.os.environ, {'HOMEBREW_TAP_TOKEN': 'tap', 'WWW_DISPATCH_TOKEN': 'site'}), \
             patch.object(withdrawal, 'api', side_effect=[ValueError('tap unavailable'), None]) as api, \
             patch.object(withdrawal, 'identities', return_value={'distribution_sha256': 'c' * 64,
                'bad_distribution_sha256': 'b' * 64, 'last_good_legacy': False, 'bad_legacy': False}), \
             patch.object(withdrawal, 'wait_for_withdrawal') as wait:
            with self.assertRaisesRegex(ValueError, 'tap unavailable'):
                withdrawal.apply(prepared)
        self.assertEqual(api.call_args_list[1].args[3]['client_payload']['last_good_version'], '2.0.0-rc.1')
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


class PartialWithdrawalTests(unittest.TestCase):
    def test_package_preparation_preserves_an_unrelated_advertised_version(self):
        import base64
        import release_withdrawal as withdrawal
        current = {"sha": "current", "content": base64.b64encode(b'  version "1.1.0"\n').decode()}
        env = {"HOMEBREW_TAP_REPO": "owner/tap", "HOMEBREW_TAP_TOKEN": "tap", "WWW_DISPATCH_TOKEN": "site", "GITHUB_REPOSITORY": "owner/product", "GH_TOKEN": "fixture"}
        plan = halt_distribution("1.2.3", None, channels=("stable",))
        with patch.dict(withdrawal.os.environ, env), patch.object(withdrawal, "api", side_effect=[{}, {}, current, None]), patch.object(withdrawal, "download") as download, patch.object(withdrawal, "identities", return_value={"distribution_sha256": None, "bad_distribution_sha256": "b" * 64}):
            result = withdrawal.prepare(plan, receipt=lambda channel: {"channel": channel, "version": "1.1.0"})
        self.assertEqual(result["channels"][0]["tap_action"], "keep")
        self.assertEqual(result["channels"][0]["website_action"], "keep")
        self.assertIsNone(result["channels"][0]["sha"])
        download.assert_not_called()

    def test_website_confirmation_requires_recorded_withdrawal_and_real_download_state(self):
        import base64
        import release_withdrawal as withdrawal
        row = {"channel": "stable", "bad": "1.0.0", "last_good": None}
        channel = {"release": {"version": "1.1.0", "manifest_sha256": "b" * 64, "distribution_sha256": "c" * 64}, "withdrawn_versions": ["1.0.0"], "withdrawn_version": None}
        stored = {"content": base64.b64encode(json.dumps({"channels": {"stable": channel}}).encode()).decode()}
        with patch.dict(withdrawal.os.environ, {"WWW_DISPATCH_TOKEN": "site"}), patch.object(withdrawal, "api", return_value=stored), patch.object(withdrawal.confirmation, "wait_for_website") as wait:
            withdrawal.wait_for_withdrawal(row, seconds=0)
        self.assertEqual(wait.call_args.args[1], {"channel": "stable", "version": "1.1.0", "manifest_sha256": "b" * 64, "distribution_sha256": "c" * 64, "withdrawn_version": None})
        with patch.dict(withdrawal.os.environ, {"WWW_DISPATCH_TOKEN": "site"}), patch.object(withdrawal, "api", return_value=None):
            with self.assertRaisesRegex(ValueError, "recorded"):
                withdrawal.wait_for_withdrawal(row, seconds=0)



class MirrorControlTests(unittest.TestCase):
    env = {'HOMEBREW_TAP_REPO': 'owner/tap', 'HOMEBREW_TAP_TOKEN': 'tap',
           'WWW_DISPATCH_TOKEN': 'site', 'GITHUB_REPOSITORY': 'owner/product', 'GH_TOKEN': 'fixture'}
    release = {'id': 7, 'tag_name': 'v1.2.3', 'draft': False, 'body': 'Existing release notes'}

    def test_present_get_controls_refuse_nonobjects_but_mutation_may_be_empty(self):
        import release_withdrawal as withdrawal
        for raw in (b'null', b'[]', b'42', b'"value"', b''):
            with self.subTest(raw=raw), patch.object(withdrawal.urllib.request, 'urlopen', return_value=io.BytesIO(raw)):
                with self.assertRaises(ValueError):
                    withdrawal.api('fixture', 'repos/owner/product')
        with patch.object(withdrawal.urllib.request, 'urlopen', return_value=io.BytesIO(b'')):
            self.assertIsNone(withdrawal.api('fixture', 'repos/owner/product/dispatches', 'POST', {}))

    def test_malformed_mirror_refuses_during_prepare_before_mutations(self):
        import release_withdrawal as withdrawal
        plan = halt_distribution('1.2.3', None, channels=())
        cases = [{}, *[{**self.release, key: value} for key, value in
            [('id', True), ('id', 0), ('tag_name', 'v1.2.2'), ('draft', 0), ('body', []), ('immutable', 1)]]]
        for release in cases:
            with self.subTest(release=release), patch.dict(withdrawal.os.environ, self.env), \
                 patch.object(withdrawal, 'api', side_effect=[{}, {}, release]) as api:
                with self.assertRaisesRegex(ValueError, 'typed release identity'):
                    withdrawal.prepare(plan)
                self.assertTrue(all(len(call.args) == 2 for call in api.call_args_list))

    def test_mirror_ignores_download_counters_and_preserves_notes(self):
        import release_withdrawal as withdrawal
        selected = withdrawal.mirror_control(self.release, '1.2.3')
        prepared = {'channels': [], 'mirrors': [{'version': '1.2.3', 'action': 'withdraw', 'release': selected}]}
        actual = {**self.release, 'assets': [{'download_count': 99}], 'updated_at': 'later'}
        with patch.dict(withdrawal.os.environ, self.env), patch.object(withdrawal, 'api', side_effect=[actual, actual, None]) as api:
            withdrawal.apply(prepared)
        self.assertEqual(api.call_args.args[2], 'PATCH')
        self.assertTrue(api.call_args.args[3]['body'].endswith(self.release['body']))
        self.assertTrue(api.call_args.args[3]['draft'])
        for key, value in [('id', 8), ('tag_name', 'v1.2.2'), ('draft', True), ('body', 'Changed notes'), ('immutable', True)]:
            with self.subTest(key=key), patch.dict(withdrawal.os.environ, self.env), \
                 patch.object(withdrawal, 'api', return_value={**actual, key: value}) as api:
                with self.assertRaises(ValueError):
                    withdrawal.apply(prepared)
                self.assertEqual(api.call_count, 1)
