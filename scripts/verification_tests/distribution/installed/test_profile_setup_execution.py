"""Configured profiles are selected by bounded typed persisted facts, never missing UI controls."""
from contextlib import nullcontext
import hashlib
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_cases import profile_setup as owner, provider_setup
from linux_release.installed_cases.provider import MODEL
from linux_release.installed_cases.records import CaseUnavailable

REF = {'provider_id': 'fixed', 'model': MODEL}


class ProfileSetupTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name).resolve()
        self.slices = self.directory / 'app-state-v1'
        self.slices.mkdir(mode=0o700)
        self.onboarding = self.slices / owner.ONBOARDING
        self.policy = self.directory / 'model-policy.yaml'
        context = patch.object(owner, 'profile', side_effect=lambda app: nullcontext(self.directory))
        context.start()
        self.addCleanup(context.stop)

    def write(self, path, value):
        path.write_text(json.dumps(value))
        path.chmod(0o600)

    def completed(self):
        self.write(self.onboarding, {'key': 'onboarding', 'value': {'firstRunSetupCompleted': True}})
        self.write(self.policy, {'coordinator': REF, 'lite': {'provider_id': '', 'model': ''},
                                'agent_pool': {'selection': 'first', 'models': [REF]}})

    def test_missing_files_are_incomplete_and_never_created(self):
        facts = owner.state(None)
        self.assertIs(facts['onboarding_completed'], False)
        self.assertIsNone(facts['coordinator'])
        self.assertFalse(self.onboarding.exists())
        self.assertFalse(self.policy.exists())

    def test_actual_slice_envelope_and_coordinator_are_readonly_with_exact_digests(self):
        self.completed()
        before = self.onboarding.read_bytes(), self.policy.read_bytes()
        facts = owner.state(None)
        self.assertIs(facts['onboarding_completed'], True)
        self.assertEqual(facts['coordinator'], REF)
        self.assertEqual(facts['onboarding_sha256'], hashlib.sha256(before[0]).hexdigest())
        self.assertEqual(facts['model_policy_sha256'], hashlib.sha256(before[1]).hexdigest())
        self.assertEqual(before, (self.onboarding.read_bytes(), self.policy.read_bytes()))
        self.assertFalse((self.directory / 'store.db').exists())

    def test_ambiguous_unknown_or_nonboolean_onboarding_refuses(self):
        for value in ({'key': 'other', 'value': {}}, {'key': 'onboarding', 'value': {'other': True}},
                      {'key': 'onboarding', 'value': {'firstRunSetupCompleted': 1}}, [],
                      {'key': 'onboarding', 'value': []}):
            self.write(self.onboarding, value)
            with self.subTest(value=value), self.assertRaises(ValueError):
                owner.state(None)
        for raw in ('{"key":"onboarding","key":"other","value":{}}', 'x' * 4097):
            self.onboarding.write_text(raw)
            with self.assertRaises(ValueError):
                owner.state(None)

    def test_unknown_duplicate_or_wrong_coordinator_policy_refuses(self):
        for value in ({'coordinator': REF, 'other': {}}, {'coordinator': {'provider_id': 'fixed'}},
                      {'coordinator': {'provider_id': 1, 'model': MODEL}}, {'coordinator': None}):
            self.write(self.policy, value)
            with self.subTest(value=value), self.assertRaises(ValueError):
                owner.state(None)
        self.policy.write_text('coordinator: {}\ncoordinator: {}')
        with self.assertRaises(ValueError):
            owner.state(None)

    def test_nonprivate_or_symlink_state_never_becomes_setup_proof(self):
        self.completed()
        self.onboarding.chmod(0o644)
        with self.assertRaises(ValueError):
            owner.state(None)
        self.onboarding.chmod(0o600)
        actual = self.onboarding.with_suffix('.retained')
        self.onboarding.rename(actual)
        self.onboarding.symlink_to(actual)
        with self.assertRaises(OSError):
            owner.state(None)
        self.assertTrue(actual.exists())

    def test_explicit_completed_matching_profile_skips_setup_and_requires_fresh_catalog(self):
        app = SimpleNamespace(observations={})
        witness = {'provider_id': 'fixed'}
        state = {'onboarding_completed': True, 'coordinator': REF}
        with patch.object(provider_setup, 'find_gui_provider', return_value=witness), patch.object(
                provider_setup, 'setup_state', return_value=state), patch.object(
                provider_setup, 'await_discovery', return_value=witness) as discovery, patch.object(
                provider_setup, 'DesktopUI') as desktop:
            self.assertEqual(provider_setup.configure_provider(app, None), witness)
        discovery.assert_called_once_with(app, None)
        desktop.assert_not_called()
        self.assertEqual(app.observations['provider_setup_state'], state)

    def test_completed_foreign_default_is_not_reconfigured_or_treated_as_ready(self):
        for existing, coordinator in ((None, REF), ({'provider_id': 'fixed'}, dict(REF, model='foreign'))):
            with patch.object(provider_setup, 'find_gui_provider', return_value=existing), patch.object(
                    provider_setup, 'setup_state', return_value={'onboarding_completed': True, 'coordinator': coordinator}), patch.object(
                    provider_setup, 'DesktopUI') as desktop, patch.object(provider_setup, 'await_discovery') as discovery:
                with self.assertRaises(CaseUnavailable):
                    provider_setup.configure_provider(SimpleNamespace(observations={}), None)
            desktop.assert_not_called()
            discovery.assert_not_called()
