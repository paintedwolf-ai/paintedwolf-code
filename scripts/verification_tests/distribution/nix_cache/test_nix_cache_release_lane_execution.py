"""The release lane publishes Nix closures only with provisioned trust and fresh admission."""

import base64
import hashlib
import json
import os
from pathlib import Path
import stat
import sys
import tempfile
import unittest
from unittest.mock import patch

from scripts.nix_cache import release_lane, withdrawal
from scripts.verification_tests.distribution.repository_fixture_support import halt_distribution
from scripts.verification_tests.distribution.distribution_document_fixtures import cache_receipt

ROOT = Path(__file__).resolve().parents[4]
SOURCE = "a" * 40
SECRET = base64.b64decode('MSsARpZiQF/tzyvH6dthUbU8qX6ojohVwQqBgV/+3rZ7tl6jymCcYf3lGTUuzhsy2lDVQ12pe+0cA4wH4klHIQ==')
OTHER_SECRET = 'rtsn7TU+rDYlvuYFrvHWraYdNKkezF5gM3o5OrzRlyNKAhN4PGCt2Ank5vyM5zNhJ4jdcz1R+SFwY1Am3KfOTA=='
PUBLIC = "paintedwolf-cache-1:" + base64.b64encode(SECRET[32:]).decode()


def policy(directory: Path, keys) -> Path:
    path = directory / "nix-cache-trust.json"
    path.write_text(json.dumps({"schema_version": 1, "mode": "production", "public_keys": keys}))
    return path


class NixCacheReleaseLaneTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)

    def test_committed_trust_selects_the_maintained_cache_key(self):
        committed = json.loads((ROOT / "packaging/linux/nix-cache-trust.json").read_text())
        self.assertEqual((committed["schema_version"], committed["mode"]), (1, "production"))
        self.assertEqual(len(committed["public_keys"]), 1)
        self.assertEqual(release_lane.trust_policy(), committed)
        self.assertEqual(release_lane.public_key(committed["public_keys"][0]), release_lane.KEY_NAME)
        with self.assertRaisesRegex(ValueError, "not provisioned"):
            release_lane.trust_policy(policy(self.work, []))
        with self.assertRaisesRegex(ValueError, "only the paintedwolf-cache-1"):
            release_lane.trust_policy(policy(self.work, ["other-1:" + base64.b64encode(bytes(32)).decode()]))
        self.assertEqual(release_lane.trust_policy(policy(self.work, [PUBLIC]))["public_keys"], [PUBLIC])

    def test_credential_must_be_the_committed_public_key(self):
        trusted = policy(self.work, [PUBLIC])
        secret = "paintedwolf-cache-1:" + base64.b64encode(SECRET).decode()
        self.assertEqual(release_lane.credential(secret, trusted), PUBLIC)
        for wrong in [None, "", "other-1:" + base64.b64encode(SECRET).decode(), "paintedwolf-cache-1:not-base64",
                      "paintedwolf-cache-1:" + OTHER_SECRET]:
            with self.subTest(wrong=wrong), self.assertRaises(ValueError):
                release_lane.credential(wrong, trusted)

    def test_signing_key_is_owner_only_and_consumed(self):
        with patch.dict(os.environ, {"NIX_CACHE_SIGNING_KEY": "secret\n"}):
            with release_lane.signing_key() as path:
                self.assertEqual((path.read_text(), stat.S_IMODE(path.stat().st_mode)), ("secret", 0o600))
                self.assertNotIn("NIX_CACHE_SIGNING_KEY", os.environ)
        with patch.dict(os.environ, {}, clear=True), self.assertRaisesRegex(ValueError, "release-publication"):
            with release_lane.signing_key():
                pass

    def test_publication_covers_both_systems_with_fresh_admission(self):
        lock = self.work / "flake.lock"
        lock.write_text("{}")
        outputs = {system: ["/nix/store/" + "a" * 32 + "-painted-wolf-code"] for system in release_lane.SYSTEMS}
        qualified, delivered = [], []

        def construct(request, trust, key, cache):
            self.assertEqual((request["mode"], request["cache_prefix"], request["application_commit"]),
                             ("production", "nix-cache", SOURCE))
            self.assertEqual(request["flake_lock_sha256"], hashlib.sha256(b"{}").hexdigest())
            return {"request": request, "public_keys": trust["public_keys"], "status": "passed"}

        def deliver(cache, report, *, authorize, backend_factory):
            self.assertIs(authorize(report["request"], report), True)
            delivered.append(report["request"]["system"])
            return {"system": report["request"]["system"]}
        trust = release_lane.trust_policy()
        receipts = release_lane.publish(outputs, SOURCE, "1.2.3", self.work, self.work / "key", qualified.append,
                                        lock=lock, policy=trust, construct=construct, deliver=deliver,
                                        backend_factory=object)
        self.assertEqual(delivered, list(release_lane.SYSTEMS))
        self.assertEqual((len(receipts), qualified), (2, [SOURCE, SOURCE]))
        with self.assertRaisesRegex(ValueError, "every native Linux system"):
            release_lane.publish({"x86_64-linux": []}, SOURCE, "1.2.3", self.work, self.work / "key", qualified.append,
                                 lock=lock, policy=trust, construct=construct, deliver=deliver)
        with patch.object(release_lane, "trust_policy", side_effect=ValueError("not provisioned")):
            with self.assertRaisesRegex(ValueError, "not provisioned"):
                release_lane.publish(outputs, SOURCE, "1.2.3", self.work, self.work / "key", qualified.append, lock=lock)

    def test_same_publisher_explicit_rehearsal_binds_mode_namespace_and_fresh_authority(self):
        from scripts.nix_cache import release_authority
        lock = self.work / 'flake.lock'
        lock.write_text('{}')
        outputs = {system: ['/nix/store/' + 'a' * 32 + '-painted-wolf-code'] for system in release_lane.SYSTEMS}
        trust = {'schema_version': 1, 'mode': 'rehearsal', 'public_keys': [
            'paintedwolf-rehearsal-fixture:' + base64.b64encode(SECRET[32:]).decode()]}
        observed, selected = [], []
        def construct(request, policy, key, cache):
            selected.append(request)
            self.assertEqual((request['mode'], request['cache_prefix']), ('rehearsal', 'release-system-tests/fixture/nix-cache'))
            return {'request': request, 'status': 'passed', 'public_keys': policy['public_keys']}
        def deliver(cache, report, *, authorize, backend_factory):
            self.assertTrue(authorize(report['request'], report))
            with self.assertRaises(ValueError):
                authorize({**report['request'], 'application_commit': 'b' * 40}, report)
            return report['request']
        options = dict(policy=trust, construct=construct, deliver=deliver, backend_factory=object,
                       rehearsal=True, cache_prefix='release-system-tests/fixture/nix-cache', lock=lock)
        result = release_lane.publish(outputs, SOURCE, '1.2.3', self.work, self.work / 'key', observed.append, **options)
        self.assertEqual((len(result), observed), (2, [SOURCE, SOURCE]))
        for change in ({'rehearsal': 1}, {'rehearsal': False}, {'cache_prefix': 'nix-cache'}, {'backend_factory': release_lane.R2Transport}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                release_lane.publish(outputs, SOURCE, '1.2.3', self.work, self.work / 'key', observed.append,
                                     **{**options, **change})
        self.assertEqual(len(selected), 2)
        with self.assertRaisesRegex(ValueError, 'selected release'):
            release_authority.authorize({'source_commit': SOURCE, 'version': '1.2.3'}, result[0],
                {'request': result[0], 'public_keys': trust['public_keys'], 'status': 'passed'}, trust_policy=trust)

    def test_halt_withdraws_availability_of_each_published_system(self):
        request = release_lane.request(SOURCE, '1.2.3', 'x86_64-linux',
            ['/nix/store/' + 'a' * 32 + '-painted-wolf-code'], ROOT / 'flake.lock')
        keys = release_lane.trust_policy()['public_keys']
        records = {'release-records/1.2.3/x86_64-linux.json': json.dumps(cache_receipt(request, keys)).encode()}

        class Backend:
            def __init__(self, request):
                self.request = request

            def read(self, name):
                return records.get(name)
        withdrawn = []

        def retract(request, *, authorize, backend_factory):
            self.assertTrue(authorize(request))
            self.assertFalse(authorize({**request, "version": "1.2.4"}))
            withdrawn.append(request["system"])
        prepared = withdrawal.prepare(halt_distribution("1.2.3", None), backend_factory=Backend)
        self.assertEqual([row["action"] for row in prepared["rows"]], ["withdraw", "keep"])
        withdrawal.apply(prepared, backend_factory=Backend, withdraw=retract)
        self.assertEqual(withdrawn, ["x86_64-linux"])
        records['release-records/1.2.4/aarch64-linux.json'] = json.dumps(cache_receipt(
            {**request, 'system': 'aarch64-linux'}, keys)).encode()
        with self.assertRaisesRegex(ValueError, "differs from the halted release"):
            withdrawal.prepare(halt_distribution("1.2.4", None), backend_factory=Backend)

    def test_closed_availability_metadata_refuses_before_any_withdrawal(self):
        from copy import deepcopy
        from scripts.nix_cache.publication import validate_availability
        request = release_lane.request(SOURCE, '1.2.3', 'x86_64-linux',
            ['/nix/store/' + 'a' * 32 + '-painted-wolf-code'], ROOT / 'flake.lock')
        policy = release_lane.trust_policy()
        original = cache_receipt(request, policy['public_keys'])
        self.assertEqual(validate_availability(original, policy), original)
        for mutation in ('schema', 'closure', 'object', 'unknown', 'coverage'):
            row = deepcopy(original)
            if mutation == 'schema': row['schema_version'] = 2
            elif mutation == 'closure': row['closure_sha256'] = 'f' * 64
            elif mutation == 'object': row['objects'][0]['bytes'] = True
            elif mutation == 'unknown': row['future'] = True
            else: row['objects'] = row['objects'][:1]
            class Backend:
                def __init__(self, request): pass
                def read(self, name):
                    return json.dumps(row).encode() if name == 'release-records/1.2.3/x86_64-linux.json' else None
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                withdrawal.prepare(halt_distribution('1.2.3', None), backend_factory=Backend)

    def test_cli_rerun_refuses_common_withdrawal_after_cache_keep_without_tombstone(self):
        import release_distribution
        from scripts.linux_release import release_qualification
        class EmptyBackend:
            def __init__(self, request): pass
            def read(self, name): return None
        halted = withdrawal.prepare(halt_distribution('1.2.3', None), backend_factory=EmptyBackend)
        self.assertEqual({row['action'] for row in halted['rows']}, {'keep'})
        outputs = self.work / 'rerun-outputs'
        outputs.mkdir()
        for system in release_lane.SYSTEMS:
            (outputs / (system + '.txt')).write_text('/nix/store/' + 'a' * 32 + '-painted-wolf-code')
        actual, writes = release_lane.publish, []
        def construct(request, policy, key, cache):
            return {'request': request, 'status': 'passed', 'public_keys': policy['public_keys']}
        def deliver(cache, report, *, authorize, backend_factory):
            authorize(report['request'], report)
            writes.append(report['request'])
        def exercise(*args):
            return actual(*args, construct=construct, deliver=deliver)
        with patch.object(release_lane, 'publish', side_effect=exercise), \
             patch.object(release_distribution, 'read_storage', return_value={'version': '1.2.3', 'withdrawn': True}), \
             patch.object(release_qualification, 'require') as qualify:
            code = self.run_main(['publish', '--outputs', str(outputs), '--source-commit', SOURCE, '--version', '1.2.3',
                                  '--output', str(self.work / 'forbidden.json')], {'NIX_CACHE_SIGNING_KEY': 'fixture'})
        self.assertEqual((code, writes), (1, []))
        qualify.assert_not_called()
        self.assertFalse((self.work / 'forbidden.json').exists())

    def run_main(self, arguments, environment=None):
        with patch.object(sys, "argv", ["release_lane", *arguments]), patch.dict(os.environ, environment or {}):
            try:
                release_lane.main()
            except SystemExit as exit:
                return exit.code
        return 0

    def test_commands_fail_closed_only_once_linux_is_public(self):
        self.assertEqual(self.run_main(["check-trust"]), 0)
        with patch.object(release_lane, "linux_public", return_value=False):
            self.assertEqual(self.run_main(["check-credential"]), 0)
        with patch.object(release_lane, "linux_public", return_value=True):
            self.assertEqual(self.run_main(["check-credential"]), 1)
        with patch.object(release_lane, "credential", return_value=PUBLIC):
            self.assertEqual(self.run_main(["check-credential"], {"NIX_CACHE_SIGNING_KEY": "x"}), 0)
        self.assertIsInstance(release_lane.linux_public(), bool)
        outputs = self.work / "outputs"
        outputs.mkdir()
        for system in release_lane.SYSTEMS:
            (outputs / (system + ".txt")).write_text("/nix/store/" + "a" * 32 + "-x\n")
        with patch.object(release_lane, "publish", return_value=[]) as published:
            code = self.run_main(["publish", "--outputs", str(outputs), "--source-commit", SOURCE, "--version", "1.2.3", "--output", str(self.work / "publication.json")],
                                 {"NIX_CACHE_SIGNING_KEY": "x"})
        self.assertEqual(code, 0)
        self.assertEqual(sorted(published.call_args.args[0]), sorted(release_lane.SYSTEMS))
        source = self.work / "prepared.json"
        source.write_text("{}")
        with patch.object(withdrawal, "apply", return_value=[]) as withdrawn:
            self.assertEqual(self.run_main(["withdraw", "--file", str(source)]), 0)
        withdrawn.assert_called_once_with({})


if __name__ == "__main__":
    unittest.main()
