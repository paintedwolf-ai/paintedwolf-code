"""AUR availability needs explicit operation, fresh admission and exact recipe bytes."""

import copy
import hashlib
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release.aur_delivery import plan, production_publish, protected_publisher
from linux_release.aur_delivery_workflow import verify_provenance, selected_directory
from linux_release.aur_release_authority import authorize
from linux_release.repository_operations import identity


class AurDeliveryTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        (self.root / 'PKGBUILD').write_text('pkgname=painted-wolf-code-bin\n')
        (self.root / '.SRCINFO').write_text('pkgname = painted-wolf-code-bin\n')
        self.manifest = {'family': 'pacman', 'source_commit': 'f' * 40, 'version': '1.0.0',
                         'channel': 'stable', 'rehearsal': False, 'architecture': 'x86_64',
                         'inventory_sha256': 'b' * 64, 'signing_generation': 1, 'key_fingerprint': 'A' * 40}
        self.metadata = {**self.manifest, 'package': 'painted-wolf-code-bin'}
        self.artifacts = b'{"final":"selected"}\n'
        self.selected = plan(self.metadata, [self.manifest], self.root, hashlib.sha256(self.artifacts).hexdigest())
        self.report = {'accepted': True, 'verified_repositories': [
            {'family': 'pacman', 'generation': identity(self.manifest), 'inventory_sha256': 'b' * 64}]}

    def admit(self, selected=None, report=None):
        calls = []
        def verify(*args, **kwargs):
            calls.append((args, kwargs))
            return self.report if report is None else report
        with patch.dict('sys.modules', {'linux_release.release_admission': SimpleNamespace(verify_release=verify)}):
            result = authorize(self.selected if selected is None else selected, self.root,
                               catalog={}, artifacts_bytes=self.artifacts, campaign={},
                               evidence_root=self.root, file_root=self.root,
                               repositories={'generations': [{'manifest': self.manifest}]},
                               source_commit='f' * 40, version='1.0.0', channel='stable',
                               signing_generation=1, key_fingerprint='A' * 40, release_build=14)
        return result, calls

    def test_fresh_campaign_reevaluation_precedes_recipe_authority(self):
        result, calls = self.admit()
        self.assertTrue(result['accepted'])
        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0][0][1], json.loads(self.artifacts))
        self.assertEqual(calls[0][1]['signing_generation'], 1)
        self.assertEqual(calls[0][1]['release_build'], 14)
        with self.assertRaises(ValueError):
            self.admit(report={'accepted': False})
        with self.assertRaises(ValueError):
            production_publish({'accepted': True})

    def test_selected_source_signer_final_files_and_exact_recipes_are_bound(self):
        for mutation in [{'source_commit': 'e' * 40}, {'operation': 'publish'}, {'rehearsal': 0},
                         {'final_artifacts_sha256': 'c' * 64}, {'recipe_sha256': 'd' * 64}]:
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                self.admit({**self.selected, **mutation})
        changed = copy.deepcopy(self.selected)
        changed['generations'][0]['signing_generation'] = True
        with self.assertRaises(ValueError):
            self.admit(changed)
        changed = copy.deepcopy(self.selected)
        changed['generations'][0]['architecture'] = 'aarch64'
        with self.assertRaises(ValueError):
            self.admit(changed)
        (self.root / 'PKGBUILD').write_text('changed after review\n')
        with self.assertRaises(ValueError):
            self.admit()

    def test_recipe_plan_refuses_wrong_source_and_symlink_declarations(self):
        with self.assertRaises(ValueError):
            plan({**self.metadata, 'source_commit': 'e' * 40}, [self.manifest], self.root, 'a' * 64)
        with self.assertRaises(ValueError):
            plan(self.metadata, [self.manifest], self.root, 'short')
        (self.root / 'PKGBUILD').unlink()
        (self.root / 'PKGBUILD').symlink_to(self.root / '.SRCINFO')
        with self.assertRaises(ValueError):
            plan(self.metadata, [self.manifest], self.root, 'a' * 64)

    def test_only_successful_main_release_controller_provenance_is_selected(self):
        run = {'repository': {'id': 1}, 'path': '.github/workflows/release.yml', 'head_branch': 'main',
               'event': 'workflow_dispatch', 'status': 'completed', 'conclusion': 'success'}
        verify_provenance(run, 1)
        for mutation in [{'head_branch': 'feature'}, {'conclusion': 'failure'}, {'event': 'pull_request'},
                         {'path': '.github/workflows/other.yml'}, {'repository': {'id': 2}}]:
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                verify_provenance({**run, **mutation}, 1)
        for path in ['../outside', '/absolute', './same', 'a//b']:
            with self.subTest(path=path), self.assertRaises(ValueError):
                selected_directory(self.root, path)

    def test_public_transport_requires_private_files_and_never_disables_host_verification(self):
        key, hosts = self.root / 'key', self.root / 'known-hosts'
        for path in [key, hosts]:
            path.write_text('fixture only\n')
            path.chmod(0o600)
        publisher = protected_publisher(self.root, 'painted-wolf-code-bin', key, hosts, lambda *_: None)
        self.assertEqual(publisher.origin, 'ssh://aur@aur.archlinux.org/painted-wolf-code-bin.git')
        self.assertIn('StrictHostKeyChecking=yes', publisher.environment['GIT_SSH_COMMAND'])
        key.chmod(0o644)
        with self.assertRaises(ValueError):
            protected_publisher(self.root, 'painted-wolf-code-bin', key, hosts, lambda *_: None)
        with self.assertRaises(ValueError):
            protected_publisher(self.root, 'unreviewed-name', key, hosts, lambda *_: None)
