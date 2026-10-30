"""The same signed-document owner verifies new confirmation and legacy-aware halt identities."""
import hashlib
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from release_distribution_contract import confirmation

from scripts.release_distribution_contract import halt_plan, remote_document, withdrawal
from scripts.release_distribution_contract.platforms import LEGACY_PLATFORMS
from scripts.release_distribution_contract.document_schema import canonical
from scripts.verification_tests.distribution.distribution_document_fixtures import ORIGIN, prepared_document, signing_identity


class DistributionConfirmationTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.registry, self.sign = signing_identity(self.root)
        self.objects, self.receipt = prepared_document(self.root, self.registry, self.sign)
        self.options = {'rehearsal': True, 'registry': self.registry, 'origin': ORIGIN}

    def fetch(self, url, maximum):
        raw = self.objects[url.removeprefix(ORIGIN + '/')]
        self.assertLessEqual(len(raw), maximum)
        return raw

    def test_real_signature_and_digest_confirm_all_public_packages(self):
        document = remote_document.confirm_public(self.receipt, fetch=self.fetch, **self.options)
        self.assertEqual(sum(len(row['packages']) for row in document['platforms'].values()), 7)
        changed = {**self.receipt, 'distribution_sha256': 'f' * 64}
        with self.assertRaisesRegex(ValueError, 'selected digest'):
            remote_document.confirm_public(changed, fetch=self.fetch, **self.options)
        key = 'releases/v1.2.3/distribution.v1.json'
        altered = json.loads(self.objects[key])
        altered['notes'] = 'Changed bytes with the original signature'
        self.objects[key] = canonical(altered)
        with self.assertRaisesRegex(ValueError, 'signature verification failed'):
            remote_document.confirm_public({**self.receipt, 'distribution_sha256': hashlib.sha256(self.objects[key]).hexdigest()},
                                           fetch=self.fetch, **self.options)

    def test_production_rejects_fixture_trust_and_mode_confusion_before_fetch(self):
        calls = []
        for options in ({'registry': self.registry}, {'origin': ORIGIN}, {'rehearsal': 1}, {'rehearsal': True}):
            with self.subTest(options=options), self.assertRaises(ValueError):
                remote_document.confirm_public(self.receipt, fetch=lambda *args: calls.append(args), **options)
        self.assertEqual(calls, [])
        raw = self.objects['releases/v1.2.3/distribution.v1.json']
        signature = self.objects['releases/v1.2.3/distribution.v1.json.sig'].decode()
        with self.assertRaisesRegex(ValueError, 'selected release'):
            remote_document.verify_metadata(raw, signature, '1.2.3', self.receipt['manifest_sha256'])

    def legacy_release(self):
        number = '1.0.2'
        fixture = Path(__file__).parent / 'testdata/legacy-releases'
        self.objects[f'updates/releases/{number}.json'] = (fixture / (number + '.json')).read_bytes()
        self.objects[f'releases/v{number}/painted-wolf-code_v{number}_darwin-aarch64.dmg.sha256'] = (fixture / (number + '.dmg.sha256')).read_bytes()
        return number

    def selected(self, good):
        return halt_plan.classify({'bad_versions': ['1.2.3'], 'channels': [
            {'channel': 'stable', 'bad': '1.2.3', 'last_good': good}]}, read=self.objects.get, **self.options)['channels'][0]

    def test_real_signed_bad_document_restores_only_sealed_legacy(self):
        good = self.legacy_release()
        row = self.selected(good)
        result = withdrawal.identities(row, self.receipt, read=self.objects.get, **self.options)
        self.assertIsNone(result['distribution_sha256'])
        self.assertEqual(result['bad_distribution_sha256'], self.receipt['distribution_sha256'])
        self.assertTrue(result['last_good_legacy'])
        self.objects['releases/v1.2.3/distribution.v1.json'] = b'{malformed'
        with self.assertRaises(ValueError):
            withdrawal.identities(row, self.receipt, read=self.objects.get, **self.options)

    def test_unknown_bad_missing_new_document_and_invalid_identifiers_never_fall_back(self):
        calls = []
        with self.assertRaises(ValueError):
            remote_document.halt_digests('../unknown', None, None, read=lambda key: calls.append(key), **self.options)
        self.assertEqual(calls, [])
        with self.assertRaisesRegex(ValueError, 'complete immutable'):
            remote_document.halt_digests('1.2.3', None, None, read=lambda key: None, **self.options)
        for good, digest in [('1.2.3', 'a' * 64), ('1.2.4', 'a' * 64), ('../unknown', 'a' * 64), ('1.2.2', 'bad')]:
            with self.subTest(good=good), self.assertRaises(ValueError):
                remote_document.halt_digests('1.2.3', good, digest, read=lambda key: calls.append(key), **self.options)
        self.assertEqual(calls, [])

    def page_confirmation(self, expected, *, legacy_catalog=False, page=None):
        class Response(io.BytesIO):
            headers = {'Cache-Control': 'no-store'}
        if page is None:
            from scripts.release_distribution_contract.platforms import package_urls
            catalog = LEGACY_PLATFORMS if legacy_catalog else confirmation.ROOT / "packaging/release-platforms.json"
            urls = package_urls(expected['version'], catalog, origin='https://downloads.paintedwolf.dev' if legacy_catalog else ORIGIN) if expected['version'] else []
            page = ''.join(f'<a data-release-channel="{expected["channel"]}" download href="{url}">Download</a>' for url in urls)
        receipt = json.dumps({'schema_version': 2, **expected}).encode()
        with patch.object(confirmation.urllib.request, 'urlopen', side_effect=[Response(receipt), Response(page.encode())]):
            confirmation.wait_for_website('https://website.example.invalid', expected, seconds=0,
                legacy_catalog=legacy_catalog, fetch=self.fetch, **self.options)

    def test_new_documents_require_all_seven_links_and_matching_channel_receipt(self):
        self.page_confirmation(self.receipt)
        with self.assertRaisesRegex(ValueError, 'download links'):
            self.page_confirmation(self.receipt, page='<p>Missing public packages</p>')
        with self.assertRaisesRegex(ValueError, 'selected digest'):
            self.page_confirmation({**self.receipt, 'distribution_sha256': 'f' * 64})
        # A withdrawn channel can confirm only its empty page and null identities.
        empty = {**self.receipt, 'version': None, 'manifest_sha256': None,
                 'distribution_sha256': None, 'withdrawn_version': '1.2.3'}
        self.page_confirmation(empty)
        with self.assertRaisesRegex(ValueError, 'download links'):
            self.page_confirmation(empty, page='<a data-release-channel="stable" download href="stale">Old</a>')

    def test_prepared_legacy_halt_confirms_exactly_one_historical_link(self):
        good = self.legacy_release()
        row = self.selected(good)
        selected = withdrawal.identities(row, self.receipt, read=self.objects.get, **self.options)
        expected = {**self.receipt, 'version': good, 'manifest_sha256': row['last_good_manifest_sha256'],
                    'distribution_sha256': selected['distribution_sha256'], 'withdrawn_version': '1.2.3'}
        from scripts.release_distribution_contract.platforms import package_urls
        self.assertEqual(package_urls(good, LEGACY_PLATFORMS), [
            'https://downloads.paintedwolf.dev/releases/v1.0.2/painted-wolf-code_v1.0.2_darwin-aarch64.dmg'])
        self.page_confirmation(expected, legacy_catalog=selected['last_good_legacy'])
        with self.assertRaisesRegex(ValueError, 'selected distribution digest'):
            self.page_confirmation(expected)

    def test_signed_replacement_binds_both_immutable_documents_and_updater(self):
        good = self.root / 'good'
        good.mkdir()
        objects, receipt = prepared_document(good, self.registry, self.sign, version='1.2.2')
        self.objects.update(objects)
        row = self.selected('1.2.2')
        result = withdrawal.identities(row, self.receipt, read=self.objects.get, **self.options)
        self.assertEqual(result['distribution_sha256'], receipt['distribution_sha256'])
        self.assertEqual(result['bad_distribution_sha256'], self.receipt['distribution_sha256'])
        self.assertFalse(result['last_good_legacy'])
        self.page_confirmation(receipt)
        with self.assertRaises(ValueError):
            withdrawal.identities({**row, 'last_good_manifest_sha256': 'f' * 64}, self.receipt,
                                  read=self.objects.get, **self.options)
        del self.objects['releases/v1.2.2/distribution.v1.json.sig']
        with self.assertRaisesRegex(ValueError, 'complete immutable'):
            self.selected('1.2.2')

    def test_production_page_gate_refuses_origin_override_before_any_transport(self):
        with patch.object(confirmation.urllib.request, 'urlopen') as page:
            with self.assertRaisesRegex(ValueError, 'maintained origin and trust'):
                confirmation.wait_for_website('https://website.example.invalid', self.receipt, seconds=0,
                    origin=ORIGIN, registry=self.registry, fetch=self.fetch)
            page.assert_not_called()


class ClosedPublicHistoryTests(unittest.TestCase):
    def test_sealed_enumeration_matches_every_captured_public_byte_and_predates_document_floor(self):
        from scripts.release_distribution_contract import public_history
        from release_semver import compare, parse
        baseline = public_history.load()
        self.assertEqual(set(baseline['releases']), {'1.0.0-rc.1', '1.0.0-rc.2', '1.0.0-rc.3', '1.0.0', '1.0.1', '1.0.2-rc1',
            '1.0.2-rc3', '1.0.2-rc4', '1.0.2-rc5', '1.0.2-rc6', '1.0.2-rc7', '1.0.2-rc8', '1.0.2'})
        self.assertEqual(hashlib.sha256(LEGACY_PLATFORMS.read_bytes()).hexdigest(),
                         '789b4d6207230d8f82e6e533dac6b2d87d05af07f22fcd44843f3c80b27a5ba3')
        fixture = Path(__file__).parent / 'testdata/legacy-releases'
        self.assertEqual(set(baseline['releases']), {path.stem for path in fixture.glob('*.json')})
        for version, row in baseline['releases'].items():
            raw = (fixture / (version + '.json')).read_bytes()
            self.assertEqual(json.loads(raw)['version'], version)
            self.assertEqual(hashlib.sha256(raw).hexdigest(), row['updater_manifest_sha256'])
            self.assertEqual((fixture / (version + '.dmg.sha256')).read_text().strip(), row['dmg_sha256'])
            self.assertLess(compare(parse(version), parse(baseline['document_release_floor'])), 0)
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'altered.json'
            document = json.loads(public_history.LEGACY.read_bytes())
            document['releases'].append({'version': '1.2.3', 'updater_manifest_sha256': 'a' * 64, 'dmg_sha256': 'b' * 64})
            path.write_text(json.dumps(document))
            with self.assertRaisesRegex(ValueError, 'sealed baseline'):
                public_history.load(path)

    def test_sealed_enumeration_equals_the_website_migrated_legacy_history(self):
        from scripts.release_distribution_contract import public_history
        website = json.loads((Path(__file__).parent / 'testdata/website-legacy-history.json').read_text())
        sealed = {version: {'manifest_sha256': row['updater_manifest_sha256'], 'sha256': row['dmg_sha256']}
                  for version, row in public_history.load()['releases'].items()}
        self.assertEqual(sealed, website['releases'])

    def test_unknown_missing_malformed_and_forged_legacy_never_classify(self):
        from scripts.verification_tests.distribution.repository_fixture_support import halt_distribution
        for raw in (None, b'{malformed'):
            def read(key):
                if key.endswith('/withdrawn.json'):
                    return None
                return b'{"version":"1.2.2"}' if key.startswith('updates/') else raw
            with self.assertRaises(ValueError):
                halt_plan.classify({'bad_versions': ['1.2.3'], 'channels': [
                    {'channel': 'stable', 'bad': '1.2.3', 'last_good': '1.2.2'}]}, read=read)
        forged = halt_distribution('1.2.3', '1.2.2')
        forged['channels'][0]['last_good_legacy'] = True
        with self.assertRaisesRegex(ValueError, 'closed publication baseline'):
            halt_plan.validate(forged)

    def test_common_selection_refuses_withdrawn_good_before_immutable_reads_but_allows_bad_retry(self):
        for good in ('1.0.2', '1.2.2'):
            for marker in (json.dumps({'version': good, 'withdrawn': True}).encode(), b'null', b'[]', b'42'):
                calls = []
                def read(key):
                    calls.append(key)
                    return marker
                with self.subTest(good=good, marker=marker), self.assertRaisesRegex(ValueError, 'withdrawn release'):
                    halt_plan.classify({'bad_versions': ['1.2.3'], 'channels': [
                        {'channel': 'stable', 'bad': '1.2.3', 'last_good': good}]}, read=read)
                self.assertEqual(calls, [f'release-metadata/{good}/withdrawn.json'])
        calls.clear()
        selected = halt_plan.classify({'bad_versions': ['1.2.3'], 'channels': [
            {'channel': 'stable', 'bad': '1.2.3', 'last_good': None}]}, read=read)
        self.assertIsNone(selected['channels'][0]['last_good'])
        self.assertEqual(calls, [])
