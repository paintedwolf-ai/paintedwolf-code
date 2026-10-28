"""A first Linux release withdraws to its signed empty generation through the real publishers."""

import json
from pathlib import Path
import shutil
import sys
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release import (native_repositories, pacman_r2, pacman_repository, release_repositories,
                                   repository_r2, repository_withdrawal)
from scripts.release_distribution_contract.channels import select
from scripts.linux_release.openpgp import ephemeral
from scripts.linux_release.repository_objects import encoded
from scripts.linux_release.repository_operations import identity
from scripts.verification_tests.distribution.repository_fixture_support import native_tools, halt_distribution, generation_prefixes

SOURCE = "a" * 40
BASE = "https://downloads.paintedwolf.dev"


class FirstReleaseWithdrawalTests(unittest.TestCase):
    """Generations are real signed assembly output; only the object store is in memory."""

    @classmethod
    def setUpClass(cls):
        if shutil.which("gpg") is None:
            if sys.platform == "linux":
                raise AssertionError("Linux distribution tests require gpg")
            raise unittest.SkipTest("signing requires gpg")
        cls.temporary = tempfile.TemporaryDirectory()
        work = Path(cls.temporary.name)
        cls.catalog = ephemeral(work / "key")
        cls.private = cls.catalog.parent / "rehearsal-secret.asc"
        # The empty generation is production trust: the release lane's key, not rehearsal trust.
        cls.catalog.write_text(json.dumps({**json.loads(cls.catalog.read_text()), "rehearsal": False}))

    @classmethod
    def tearDownClass(cls):
        cls.temporary.cleanup()

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)
        self.objects = {}
        self.base = BASE
        for target in [release_repositories, repository_withdrawal]:
            patcher = patch.object(target, "CATALOG", self.catalog)
            patcher.start()
            self.addCleanup(patcher.stop)
        for module in [repository_r2, pacman_r2]:
            for name, replacement in [("current", self.current), ("put", self.put), ("public_bytes", self.public_bytes)]:
                patcher = patch.object(module, name, side_effect=replacement)
                patcher.start()
                self.addCleanup(patcher.stop)

    def current(self, key, directory):
        return json.loads(self.objects[key]) if key in self.objects else None

    def put(self, key, source, mutable=False):
        if not mutable and key in self.objects and self.objects[key] != source.read_bytes():
            raise ValueError("immutable collision")
        self.objects[key] = source.read_bytes()

    def public_bytes(self, url, source, mutable=False):
        if self.objects.get(url.removeprefix(self.base + "/")) != source.read_bytes():
            raise ValueError("public bytes differ")

    def prefixes(self, prefix):
        return sorted(key.removesuffix('generation.json') for key in self.objects
                      if key.startswith(prefix) and key.endswith('/generation.json'))

    def fetch(self, base_url, row, directory):
        for item in row["receipt"]["objects"]:
            target = directory / item["path"]
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(self.objects[f"{row.get('storage_prefix', 'linux')}/generations/{row['replacement']}/{item['path']}"])
        (directory / "generation.json").write_bytes(encoded(row["receipt"]))

    def empty(self, family: str, channel="stable", *, catalog=None, rehearsal=False) -> tuple[dict, Path]:
        catalog = catalog or self.catalog
        destination = self.work / (family + "-" + channel + "-withdrawal")
        manifest = {"format_version": 1, "source_commit": SOURCE, "version": "1.2.3", "packages": []}
        if family == "pacman":
            receipt = pacman_repository.assemble({**manifest, "architecture": "x86_64"}, self.work, destination,
                                                 channel, catalog, self.private, rehearsal)
        else:
            receipt = native_repositories.assemble(family, manifest, self.work, destination, channel, catalog,
                                                   self.private, rehearsal=rehearsal, architectures=["amd64", "arm64"])
        return receipt, destination

    def offer_first_release(self, family: str, channel="stable"):
        empty, directory = self.empty(family, channel)
        self.assertEqual(empty["packages"], [])
        offer = {**empty, "packages": [{"file": "painted-wolf-code.pkg", "sha256": "0" * 64}],
                 "inventory_sha256": "0" * 64}
        qualified = []
        release_repositories.retain_withdrawal(empty, offer, directory, BASE, qualified.append,
                                               upload=self.put, verify=self.public_bytes)
        self.assertEqual(qualified, [SOURCE])
        # The halted first release is the active offer, with no earlier Linux generation.
        halted = identity(offer)
        active = {"format_version": 1, "operation": "publish", "generation": halted, "previous": None,
                  **{key: offer[key] for key in ["family", "channel", "version", "source_commit", "inventory_sha256",
                                                 "packages", "signing_generation", "key_fingerprint", "rehearsal"]},
                  **({"architecture": "x86_64"} if family == "pacman" else {})}
        state = release_repositories.channel_key(offer)
        self.objects[state] = encoded(active)
        return empty, state, halted

    def withdraw_first_release(self, family: str, channel="stable", good=None, admit=None):
        empty, state, halted = self.offer_first_release(family, channel)
        rows = repository_withdrawal.prepare(halt_distribution("1.2.3", good), read=self.current)
        self.assertEqual([row["replacement"] for row in rows if row["action"] == "withdraw"], [identity(empty)])
        if admit is not None:
            admit(rows)
        for _ in range(2):  # an interrupted halt retries the same plan
            repository_withdrawal.apply(rows, BASE, fetch=self.fetch)
            record = json.loads(self.objects[state])
            self.assertEqual((record["operation"], record["generation"], record["previous"], record["packages"]),
                             ("withdraw", identity(empty), halted, []))
        self.assertIn("linux/withdrawn/" + halted + ".json", self.objects)
        marker = "linux/withdrawn-versions/" + family + "/" + channel + "/" + ("x86_64" if family == "pacman" else "all")
        self.assertIn(marker + "/1.2.3.json", self.objects)
        self.assertTrue(all(row["action"] == "keep" for row in repository_withdrawal.prepare(halt_distribution("1.2.3", None), read=self.current)))

    def test_pacman_first_release_withdraws_to_signed_empty_databases(self):
        self.withdraw_first_release("pacman")
        self.assertEqual(json.loads(self.objects["linux/channels/pacman/stable/x86_64/current.json"])["version"], "1.2.3")

    def test_apt_and_dnf_first_release_withdraws_to_signed_empty_indexes(self):
        native_tools("apt-ftparchive", "createrepo_c", "gpg", "gpgv")
        for family in ["deb", "rpm"]:
            with self.subTest(family=family):
                self.withdraw_first_release(family)

    def test_shared_promoted_stable_plan_withdraws_both_signed_channel_generations(self):
        selected = select("stable", "1.2.3", 1, SOURCE, read=lambda key: None)
        for channel in selected["channels"]:
            self.withdraw_first_release("pacman", channel, good="1.0.2")
        for channel in selected["channels"]:
            record = json.loads(self.objects[f"linux/channels/pacman/{channel}/x86_64/current.json"])
            self.assertEqual((record["operation"], record["channel"], record["packages"]), ("withdraw", channel, []))

    def test_explicit_rehearsal_uses_the_same_signed_empty_retention_and_halt_owners(self):
        selected = json.loads(self.catalog.read_bytes())
        selected['rehearsal'] = True
        isolated = self.work / 'disposable-trust'
        isolated.mkdir()
        for row in selected['generations']:
            shutil.copyfile(self.catalog.parent / row['public_key'], isolated / row['public_key'])
        catalog = isolated / 'keys.json'
        catalog.write_text(json.dumps(selected))
        self.base = 'https://rehearsal.example.invalid'
        prefix = 'release-system-tests/native-halt'
        empty, directory = self.empty('pacman', 'preview', catalog=catalog, rehearsal=True)
        offer = {**empty, 'packages': [{'file': 'selected-package.pkg.tar.zst', 'sha256': '0' * 64}]}
        observed = []
        release_repositories.retain_withdrawal(empty, offer, directory, self.base, observed.append,
            rehearsal=True, catalog=catalog, storage_prefix=prefix, transport=self)
        state = release_repositories.channel_key(offer, storage_prefix=prefix)
        self.objects[state] = encoded({'format_version': 1, 'operation': 'publish', 'generation': identity(offer),
            'previous': None, **{key: offer[key] for key in ('family', 'channel', 'architecture', 'version',
                'source_commit', 'packages', 'inventory_sha256', 'signing_generation', 'key_fingerprint', 'rehearsal')}})
        plan = halt_distribution('1.2.3', None, channels=('preview',))
        rows = repository_withdrawal.prepare(plan, rehearsal=True, catalog=catalog, storage_prefix=prefix,
                                             base_url=self.base, transport=self)
        self.assertEqual(rows[0]['replacement'], identity(empty))
        repository_withdrawal.apply(rows, self.base, rehearsal=True, catalog=catalog, storage_prefix=prefix,
                                   transport=self, fetch=self.fetch)
        self.assertEqual(json.loads(self.objects[state])['operation'], 'withdraw')
        self.assertEqual(observed, [SOURCE])
        with patch.object(self, 'current', wraps=self.current) as read:
            for mode, namespace in [(False, prefix), (True, 'linux'), (1, prefix)]:
                with self.subTest(mode=mode), self.assertRaises(ValueError):
                    repository_withdrawal.prepare(plan, rehearsal=mode, catalog=catalog, storage_prefix=namespace,
                                                  base_url=self.base, transport=self)
            read.assert_not_called()

    def test_real_retained_manifests_refuse_foreign_signing_identity_before_preparation(self):
        from scripts.verification_tests.distribution.native_repository_fixtures import generation, trust
        catalog = trust(self.work / 'retained-trust', rehearsal=False)
        for family in ('deb', 'rpm'):
            receipt, _ = generation(family + '-0.0.1', self.work)
            receipt['rehearsal'] = False
            version = identity(receipt)
            self.objects['linux/generations/' + version + '/generation.json'] = encoded(receipt)
            self.objects[f'linux/channels/{family}/preview/current.json'] = encoded({
                'version': '0.0.2', 'generation': 'a' * 64, 'packages': receipt['packages']})
        plan = halt_distribution('0.0.2', '0.0.1', channels=('preview',))
        with patch.object(release_repositories, 'CATALOG', catalog), patch.object(repository_withdrawal, 'CATALOG', catalog):
            rows = repository_withdrawal.prepare(plan, read=self.current, enumerate_prefixes=self.prefixes)
            self.assertEqual({row['family'] for row in rows if row['action'] == 'withdraw'}, {'deb', 'rpm'})
            key = next(key for key in self.objects if '/generations/' in key)
            bad = json.loads(self.objects.pop(key))
            bad['key_fingerprint'] = '0' * 40
            self.objects['linux/generations/' + identity(bad) + '/generation.json'] = encoded(bad)
            before = dict(self.objects)
            with self.assertRaisesRegex(ValueError, 'selected signing identity'):
                repository_withdrawal.prepare(plan, read=self.current, enumerate_prefixes=self.prefixes)
            self.assertEqual(self.objects, before)

    def test_complete_barrier_keeps_prior_discovery_but_restores_actual_native_publication(self):
        self.effect_inputs(bad_offered=False)
        self.withdraw_first_release('pacman', admit=self.complete_effect_barrier)
        evidence = json.loads((self.effect_root / 'halt-effects.json').read_bytes())
        self.assertTrue(evidence['complete'])
        self.assertEqual({row['action'] for row in evidence['effects']['native']['actions']}, {'withdraw', 'keep'})
        self.assertEqual({row['action'] for row in evidence['effects']['feeds']['actions']}, {'keep'})
        self.assertEqual({row['action'] for row in evidence['effects']['distribution']['actions']}, {'keep'})
        self.assertEqual(evidence['effects']['ledger']['actions'], [{'version': '1.2.3', 'action': 'withdraw'}])
        self.assertEqual({row['action'] for row in evidence['effects']['nix']['actions']}, {'withdraw', 'keep'})

    def test_legacy_plan_restores_actually_bad_feeds_website_and_both_native_channels(self):
        self.effect_inputs(bad_offered=True, good='1.0.2')
        for row in self.selected['channels']:
            self.offer_first_release('pacman', row['channel'])
        native = repository_withdrawal.prepare(self.selected, read=self.current)
        self.complete_effect_barrier(native)
        repository_withdrawal.apply(native, BASE, fetch=self.fetch)
        for row in native:
            if row['action'] == 'withdraw':
                active = self.current(repository_withdrawal.state_key(row['family'], row['channel'], row['architecture']), self.work)
                self.assertEqual((active['operation'], active['packages']), ('withdraw', []))
        for channel in ('stable', 'preview'):
            restored = json.loads(self.providers.get(f'updates/{channel}/key-1/latest.json'))
            self.assertEqual(restored['version'], '1.0.2')
        self.assertEqual(len(self.dispatches), 2)
        for event in self.dispatches:
            payload = event['client_payload']
            self.assertEqual(set(payload), {'bad_version', 'last_good_version', 'channel', 'manifest_sha256',
                'distribution_sha256', 'bad_distribution_sha256', 'last_good_legacy', 'bad_legacy'})
            self.assertEqual((payload['bad_version'], payload['last_good_version']), ('1.2.3', '1.0.2'))
            self.assertIs(payload['last_good_legacy'], True)
            self.assertIs(payload['bad_legacy'], False)
            self.assertIsNone(payload['distribution_sha256'])
            self.assertEqual(payload['bad_distribution_sha256'], self.bad_receipt['distribution_sha256'])
        evidence = json.loads((self.effect_root / 'halt-effects.json').read_bytes())
        self.assertEqual({row['action'] for row in evidence['effects']['feeds']['actions']}, {'withdraw'})
        website = [row for row in evidence['effects']['distribution']['actions'] if row['effect'] == 'website']
        self.assertEqual([row['action'] for row in website], ['withdraw', 'withdraw'])
        self.advertised = self.observed_sites['stable']
        before = (self.providers.mutations(), list(self.dispatches), list(self.cache_applies), dict(self.objects))
        rerun = repository_withdrawal.prepare(self.selected, read=self.current)
        self.complete_effect_barrier(rerun)
        repository_withdrawal.apply(rerun, BASE, fetch=self.fetch)
        self.assertEqual((self.providers.mutations(), self.dispatches, self.cache_applies, self.objects), before)
        repeated = json.loads((self.effect_root / 'halt-effects.json').read_bytes())
        for owner in ('native', 'ledger', 'feeds', 'nix', 'distribution'):
            self.assertEqual({row['action'] for row in repeated['effects'][owner]['actions']}, {'keep'})

    def test_fully_published_bad_missing_document_refuses_all_effects_before_any_apply(self):
        self.effect_inputs(bad_offered=True, good='1.0.2', missing=True)
        for row in self.selected['channels']:
            self.offer_first_release('pacman', row['channel'])
        native = repository_withdrawal.prepare(self.selected, read=self.current)
        before = dict(self.objects)
        stored = {path.relative_to(self.providers.store).as_posix(): path.read_bytes()
                  for path in self.providers.store.rglob('*') if path.is_file()}
        with self.assertRaisesRegex(ValueError, 'complete halt barrier'):
            self.complete_effect_barrier(native)
        self.assertEqual(self.objects, before)
        self.assertEqual({path.relative_to(self.providers.store).as_posix(): path.read_bytes()
                          for path in self.providers.store.rglob('*') if path.is_file()}, stored)
        self.assertEqual((self.providers.mutations(), self.dispatches, self.cache_applies), ([], [], []))
        evidence = json.loads((self.effect_root / 'halt-effects.json').read_bytes())
        self.assertFalse(evidence['complete'])
        self.assertIn('complete immutable distribution', self.distribution_error)
        self.assertEqual(evidence['effects']['distribution']['actions'][0]['action'], 'refuse')
        self.assertEqual({row['action'] for row in evidence['effects']['feeds']['actions']}, {'withdraw'})
        self.assertEqual(set(evidence['effects']), {'selection', 'native', 'feeds', 'ledger', 'nix', 'distribution'})

    def effect_inputs(self, *, bad_offered, good=None, missing=False):
        import base64
        from scripts.verification_tests.distribution.distribution_document_fixtures import signing_identity, prepared_document
        from scripts.verification_tests.distribution.release_provider_fixtures import Providers
        from scripts.release_distribution_contract.halt_plan import classify
        from update_keys import load_registry, release_binding
        from updater_signature import rehearsal_signature
        self.effect_root = self.work / 'effects'
        self.effect_root.mkdir()
        self.registry, signer = signing_identity(self.effect_root)
        self.registry['generations'][0]['public_key'] = load_registry()['generations'][0]['public_key']
        updater = {'version': '1.2.3', 'notes': 'Noninstalled release fixture', 'pub_date': '2026-10-28T00:00:00Z',
            'update_keys': release_binding(self.registry, '1.2.3'), 'platforms': {'darwin-aarch64': {
                'url': BASE + '/releases/v1.2.3/painted-wolf-code_v1.2.3_darwin-aarch64.app.tar.gz',
                'signature': rehearsal_signature('1.2.3')}}}
        objects, self.bad_receipt = prepared_document(self.effect_root, self.registry, signer,
                                                      updater_bytes=json.dumps(updater).encode())
        if good is not None:
            fixtures = Path(__file__).parent / 'testdata/legacy-releases'
            objects[f'updates/releases/{good}.json'] = (fixtures / (good + '.json')).read_bytes()
            objects[f'releases/v{good}/painted-wolf-code_v{good}_darwin-aarch64.dmg.sha256'] = (fixtures / (good + '.dmg.sha256')).read_bytes()
        if missing:
            del objects['releases/v1.2.3/distribution.v1.json']
        from scripts.verification_tests.distribution.distribution_document_fixtures import ORIGIN
        self.selected = classify({'bad_versions': ['1.2.3'], 'channels': [
            {'channel': channel, 'bad': '1.2.3', 'last_good': good} for channel in ('stable', 'preview')]},
            read=objects.get, rehearsal=True, registry=self.registry, origin=ORIGIN)
        source = Path(__file__).resolve().parents[3]
        self.providers = Providers(self.effect_root / 'providers', source, self.registry, self.effect_root / 'feed.key')
        self.tap, self.dispatches, self.cache_applies, self.observed_sites = {}, [], [], {}
        if bad_offered:
            advertised = updater
            self.advertised = self.bad_receipt
        else:
            fixture = Path(__file__).parent / 'testdata/legacy-releases/1.0.2.json'
            raw = fixture.read_bytes()
            advertised = json.loads(raw)
            import hashlib
            self.advertised = {**self.bad_receipt, 'version': advertised['version'],
                               'manifest_sha256': hashlib.sha256(raw).hexdigest(), 'distribution_sha256': None}
        for channel in ('stable', 'preview'):
            token = 'painted-wolf-code' + ('@preview' if channel == 'preview' else '')
            current = f'  version "{advertised["version"]}"\n'.encode()
            self.tap[token] = current
            objects[f'release-metadata/1.2.3/{token}.rb'] = current
            if good is not None:
                checksum = objects[f'releases/v{good}/painted-wolf-code_v{good}_darwin-aarch64.dmg.sha256'].decode().strip()
                objects[f'release-metadata/{good}/{token}.rb'] = f'  version "{good}"\n  sha256 "{checksum}"\n'.encode()
            raw = json.dumps(advertised).encode()
            self.providers.put(f'updates/{channel}/key-1/latest.json', raw)
            pointer = self.effect_root / f'latest-{channel}-key-1.json'
            pointer.write_bytes(raw)
            self.providers.put(f'updates/{channel}/key-1/latest.json.sig', signer(pointer, 1, self.registry).encode())
        for key, raw in objects.items():
            self.providers.put(key, raw)

    def complete_effect_barrier(self, native):
        import base64
        import importlib.util
        import io
        import release_withdrawal
        from scripts.release_distribution_contract import halt_effects, ledger
        from scripts.release_distribution_contract.withdrawal import identities
        from scripts.nix_cache import withdrawal as cache
        from scripts.nix_cache.release_lane import trust_policy
        from scripts.verification_tests.distribution.distribution_document_fixtures import cache_receipt, ORIGIN
        from scripts.release_distribution_contract.halt_plan import classify
        self.selected = classify(self.selected, read=self.providers.get, rehearsal=True, registry=self.registry, origin=ORIGIN)
        selected, root = self.selected, self.effect_root
        env = {**self.providers.environment, 'HOMEBREW_TAP_REPO': 'owner/tap', 'HOMEBREW_TAP_TOKEN': 'fixture',
               'WWW_DISPATCH_TOKEN': 'fixture', 'GITHUB_REPOSITORY': 'owner/product', 'GH_TOKEN': 'fixture'}
        def api(token, endpoint, method='GET', body=None):
            if endpoint in ('repos/owner/tap', 'repos/paintedwolf-ai/paintedwolf-www'):
                return {}
            if '/contents/Casks/' in endpoint:
                name = endpoint.rsplit('/', 1)[1].removesuffix('.rb')
                if method == 'GET':
                    return {'sha': 'fixture', 'content': base64.b64encode(self.tap[name]).decode()}
                self.tap[name] = base64.b64decode(body['content']) if method == 'PUT' else b''
                return None
            if endpoint.endswith('/dispatches') and method == 'POST':
                self.dispatches.append(body)
                return None
            if '/releases/tags/' in endpoint:
                return None
            if '/contents/state.json?' in endpoint:
                row = selected['channels'][0]
                release = {'version': row['last_good'], 'manifest_sha256': row['last_good_manifest_sha256'], 'distribution_sha256': row['distribution_sha256']}
                state = {'channels': {channel: {'release': release, 'withdrawn_versions': ['1.2.3'],
                    'withdrawn_version': '1.2.3'} for channel in ('stable', 'preview')}}
                return {'content': base64.b64encode(json.dumps(state).encode()).decode()}
            raise AssertionError('unexpected fixture GitHub request: ' + endpoint)
        class Response(io.BytesIO):
            headers = {'Cache-Control': 'no-store'}
        def page(request, **kwargs):
            url = request.full_url
            channel = 'preview' if '/preview' in url else 'stable'
            if '/.well-known/releases/' in url:
                row = next(row for row in selected['channels'] if row['channel'] == channel)
                receipt = {'schema_version': 2, 'channel': channel, 'version': row['last_good'],
                    'manifest_sha256': row['last_good_manifest_sha256'], 'distribution_sha256': row['distribution_sha256'],
                    'withdrawn_version': row['bad']}
                self.observed_sites[channel] = receipt
                return Response(json.dumps(receipt).encode())
            if url in ('https://paintedwolf.ai/download/', 'https://paintedwolf.ai/download/preview/'):
                return Response(f'<a data-release-channel="{channel}" download href="https://downloads.paintedwolf.dev/releases/v1.0.2/painted-wolf-code_v1.0.2_darwin-aarch64.dmg">Download</a>'.encode())
            raise AssertionError('unexpected fixture website request: ' + url)
        bind = lambda row, published: identities(row, published, read=self.providers.get,
                                                rehearsal=True, registry=self.registry, origin=ORIGIN)
        spec = importlib.util.spec_from_file_location('shared_halt_plan', Path(__file__).resolve().parents[2] / 'release-halt-plan.py')
        feed = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(feed)
        outcomes = {owner: 'success' for owner in halt_effects.FILES}
        def stored(key):
            raw = self.providers.get(key)
            return json.loads(raw) if raw is not None else None
        with patch.dict(release_withdrawal.os.environ, env, clear=True), \
             patch.object(release_withdrawal, 'api', side_effect=api), \
             patch.object(release_withdrawal, 'download', side_effect=self.providers.get), \
             patch.object(release_withdrawal, 'identities', side_effect=bind), \
             patch.object(release_withdrawal.urllib.request, 'urlopen', side_effect=page), \
             patch.object(feed, 'read_storage', side_effect=stored), \
             patch.object(feed, 'read_storage_bytes', side_effect=self.providers.get), \
             patch.object(feed.subprocess, 'run', side_effect=self.providers.run):
            negative = ledger.prepare(selected['bad_versions'], read=stored)
            planned = feed.plan_withdrawal(self.registry, 1, '1.2.3', selected['channels'][0]['last_good'])
            planned['distribution'] = selected
            rows = {(row['generation'], row['channel']): row for row in planned['feeds']}
            feed.prepare(rows, planned, selected, root / 'prepared-halt', ledger_rows=negative)
            prepared = feed.validate_prepared(rows, planned, root / 'prepared-halt', self.registry)
            try:
                distribution = release_withdrawal.prepare(selected,
                    receipt=lambda channel: {**self.advertised, 'channel': channel}, binding=bind)
                (root / halt_effects.FILES['distribution']).write_text(json.dumps(distribution))
            except ValueError as error:
                self.distribution_error = str(error)
                (root / 'halt-distribution-refusal.log').write_text(str(error))
                outcomes['distribution'] = 'failure'
            request = {'schema_version': 1, 'mode': 'production', 'cache_prefix': 'nix-cache', 'version': '1.2.3',
                'application_commit': SOURCE, 'flake_lock_sha256': 'b' * 64, 'system': 'x86_64-linux',
                'outputs': ['/nix/store/' + 'a' * 32 + '-painted-wolf-code']}
            if not hasattr(self, 'cache_records'):
                self.cache_records = {'release-records/1.2.3/x86_64-linux.json': json.dumps(cache_receipt(request, trust_policy()['public_keys'])).encode()}
            records = self.cache_records
            from contextlib import contextmanager
            applied = self.cache_applies
            class Backend:
                def __init__(self, request): self.request = request
                def read(self, name): return records.get(name)
                @contextmanager
                def serialized(self, operation): yield self
                def put_bytes(self, name, raw, content_type, cache_control):
                    if name in records:
                        if records[name] != raw:
                            raise ValueError('immutable fixture cache control changed')
                        return
                    records[name] = raw
                    applied.append(self.request)
            nix = cache.prepare(selected, backend_factory=Backend)
            for owner, value in (('selection', selected), ('native', native), ('ledger', negative), ('nix', nix)):
                (root / halt_effects.FILES[owner]).write_text(json.dumps(value))
            evidence = halt_effects.admit(root, outcomes)
            (root / 'halt-effects.json').write_text(json.dumps(evidence))
            if not evidence['complete']:
                raise ValueError('complete halt barrier refused; no effect may apply: ' + json.dumps(evidence))
            feed.apply(prepared, root / 'prepared-halt')
            release_withdrawal.apply(distribution)
            cache.apply(nix, backend_factory=Backend)
            self.assertEqual(self.cache_applies, [request])

    def test_withdrawal_generation_must_match_its_offer(self):
        empty, directory = self.empty("pacman")
        for change in [{"version": "1.2.4"}, {"architecture": "aarch64"}, {"key_fingerprint": "0" * 40}]:
            with self.subTest(change=change), self.assertRaisesRegex(ValueError, "differs from the offer"):
                release_repositories.retain_withdrawal(empty, {**empty, **change}, directory, BASE, lambda _s: None,
                                                       upload=self.put, verify=self.public_bytes)
        with self.assertRaisesRegex(ValueError, "differs from the offer"):
            release_repositories.retain_withdrawal({**empty, "packages": [{"file": "x"}]}, empty, directory, BASE,
                                                   lambda _s: None, upload=self.put, verify=self.public_bytes)
        self.assertEqual(self.objects, {})


if __name__ == "__main__":
    unittest.main()
