"""Current public availability requires exact activated bytes and authenticated state."""
import copy
import hashlib
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

from scripts.linux_release.publication import repository_observation as owner
from scripts.linux_release.repository_operations import identity, transition
from scripts.linux_release.repository_pending import activation_plan, marker


class RepositoryObservationTests(unittest.TestCase):
    def fixture(self, root):
        directory = root / 'generation'; directory.mkdir()
        package = directory / 'pool/pkg.deb'; package.parent.mkdir(); package.write_bytes(b'abc')
        manifest = {'family': 'deb', 'channel': 'stable', 'version': '1.2.3', 'source_commit': 'a' * 40,
            'inventory_sha256': 'b' * 64, 'signing_generation': 1, 'key_fingerprint': 'C' * 40,
            'rehearsal': False, 'packages': [], 'objects': [{'path': 'pool/pkg.deb', 'size': 3,
                'sha256': hashlib.sha256(b'abc').hexdigest()}]}
        operation = transition(manifest, None, None)
        current = {**operation, 'configuration': f'generations/{identity(manifest)}/client.conf'}
        backend = Mock(origin='https://downloads.paintedwolf.dev', prefix=owner.PREFIX)
        backend.public = backend.origin + '/' + backend.prefix
        pending = marker(activation_plan(current, None, backend.prefix, backend.origin, 'activate', None, None), 'completed')
        states = {'channels/deb/stable/current.json': current, 'channels/deb/stable/pending.json': pending}
        backend.read.side_effect = lambda name: copy.deepcopy(states.get(name))
        key = root / 'fixture-key.asc'; key.write_bytes(b'explicit unit fixture public key')
        return manifest, directory, operation, backend, states, key

    def invoke(self, root, *, mutation=None):
        manifest, directory, operation, backend, states, key = self.fixture(root)
        if mutation == 'withdrawn':
            states['withdrawn/' + identity(manifest) + '.json'] = {'withdrawn': True}
        if mutation == 'pending':
            states['channels/deb/stable/pending.json']['state'] = 'pending'
        if mutation == 'wrong-origin':
            backend.origin = 'https://foreign.example'
            backend.public = backend.origin + '/' + backend.prefix
        def public(name, path, mutable=False):
            self.assertTrue(path.is_file())
            if mutation == 'tampered':
                raise ValueError('fixture public byte mismatch')
            if mutation == 'changed-during-read':
                states['channels/deb/stable/current.json']['version'] = '1.2.4'
        backend.verify_public.side_effect = public
        with patch('scripts.linux_release.repository_validation.admit') as admit, \
                patch.object(owner, 'selection', return_value=({'generation': 1, 'fingerprint': 'C' * 40}, key)):
            try:
                result = owner.observe(manifest, directory, operation, backend=backend)
            except ValueError:
                return None, backend, admit.call_count
        return result, backend, admit.call_count

    def test_success_reads_actual_current_pending_generation_and_all_signed_payload_bytes(self):
        with tempfile.TemporaryDirectory() as temporary:
            result, backend, admissions = self.invoke(Path(temporary))
            self.assertEqual(admissions, 2)
            self.assertEqual(result['facts']['source_commit'], 'a' * 40)
            self.assertEqual(len(result['admission_sha256']), 64)
            names = [call.args[0] for call in backend.verify_public.call_args_list]
            self.assertIn('channels/deb/stable/current.json', names)
            self.assertIn('channels/deb/stable/pending.json', names)
            self.assertIn('channels/deb/stable/pool/pkg.deb', names)
            self.assertTrue(any(name.endswith('/package-key.asc') for name in names))

    def test_withdrawal_incomplete_activation_and_foreign_origin_never_reach_public_claim(self):
        for mutation in ['withdrawn', 'pending', 'wrong-origin']:
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as temporary:
                result, backend, _ = self.invoke(Path(temporary), mutation=mutation)
                self.assertIsNone(result)
                backend.verify_public.assert_not_called()

    def test_public_tampering_or_mid_observation_activation_change_cannot_return_facts(self):
        for mutation in ['tampered', 'changed-during-read']:
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as temporary:
                result, _, _ = self.invoke(Path(temporary), mutation=mutation)
                self.assertIsNone(result)
