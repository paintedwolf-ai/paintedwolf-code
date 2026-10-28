"""Owned provider projection is bounded, readonly and independent of private API tokens."""
from contextlib import ExitStack
import json
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_cases import profile_observer, profile_providers as observer
from linux_release.installed_cases.provider import ProviderFixture, GUI_LABEL, GUI_KIND, MODEL, gui_endpoint

ENDPOINT = gui_endpoint(41234)
from linux_release.installed_cases.records import CaseUnavailable


class ProfileProviderTests(unittest.TestCase):
    def setUp(self):
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.home = Path(self.stack.enter_context(tempfile.TemporaryDirectory())).resolve()
        self.directory = self.home / '.config/paintedwolf'
        self.directory.mkdir(parents=True, mode=0o700)
        self.directory.parent.chmod(0o700)
        self.path = self.directory / 'providers.local.yaml'
        self.app = SimpleNamespace(binding={'install_root': '/owned', 'images': {
            '/owned/bin/pw': {'device': 1, 'inode': 2, 'bytes': 3}}},
            observations={'engine_identity': {'pid': 123, 'start_time_ticks': 9}},
            require_running=Mock(), require_engine=Mock())
        self.stack.enter_context(patch.object(Path, 'home', return_value=self.home))
        self.stack.enter_context(patch.object(profile_observer, 'require_owned_guest'))
        self.stack.enter_context(patch.object(profile_observer, 'engine_image', return_value=(1, 2, 3)))
        self.row = {'id': 'owned-fixture-1', 'kind': GUI_KIND, 'label': GUI_LABEL,
            'base_url': ENDPOINT, 'requires_api_key': True, 'api_key_env': '', 'models': [{'id': MODEL}]}

    def write(self, rows):
        self.path.write_text(json.dumps({'providers': rows}))
        self.path.chmod(0o600)

    def test_missing_catalog_is_not_created_and_fixed_projection_retains_no_other_provider_data(self):
        self.assertIsNone(observer.find_gui_provider(self.app, ENDPOINT))
        self.assertFalse(self.path.exists())
        self.write([{'id': 'other', 'label': 'SECRET OTHER PROVIDER', 'api_key_env': 'SECRET_ENV'}, self.row])
        before = self.path.read_bytes()
        with patch.object(observer.os, 'open', wraps=os.open) as opened:
            observed = observer.find_gui_provider(self.app, ENDPOINT)
        self.assertEqual(observed, {'provider_id': 'owned-fixture-1', 'kind': GUI_KIND,
            'requires_api_key': True, 'models': [MODEL], 'observer': 'owned-provider-YAML'})
        self.assertNotIn('SECRET', json.dumps(observed))
        self.assertEqual(self.path.read_bytes(), before)
        self.assertFalse(opened.call_args.args[1] & (os.O_WRONLY | os.O_RDWR | os.O_CREAT | os.O_TRUNC))
        self.assertFalse((self.directory / 'store.db').exists())

    def test_yaml_duplicates_aliases_excess_and_diagnostic_prose_are_refused(self):
        for raw in [b'providers: []\nproviders: []', b'providers: &a [*a]', b'unknown: []',
                    b'providers: !!python/object/apply:SECRET []', b'providers: [SECRET', b'x' * (observer.LIMIT + 1)]:
            with self.subTest(length=len(raw)), self.assertRaises(ValueError) as refusal:
                observer.document(raw)
            self.assertNotIn('SECRET', str(refusal.exception))

    def test_changed_endpoint_authentication_or_ambiguous_identity_never_matches_canary(self):
        changes = [{'base_url': 'http://127.0.0.1:1/v1'}, {'requires_api_key': False},
                   {'requires_api_key': 0}, {'api_key_env': 'SECRET_ENV'}, {'ambient_auth': 'ambient'}]
        for changed in changes:
            with self.subTest(changed=changed), self.assertRaises(ValueError):
                observer.selected([{**self.row, **changed}], ENDPOINT)
        for rows in [[self.row, self.row], [self.row, {**self.row, 'id': 'other-fixture'}]]:
            with self.assertRaises(ValueError):
                observer.selected(rows, ENDPOINT)

    def test_nonprivate_or_symlink_catalog_is_refused_without_mutation(self):
        self.write([self.row])
        before = self.path.read_bytes()
        self.path.chmod(0o644)
        with self.assertRaises(ValueError):
            observer.find_gui_provider(self.app, ENDPOINT)
        self.assertEqual(self.path.read_bytes(), before)
        self.path.chmod(0o600)
        actual = self.path.with_suffix('.original')
        self.path.rename(actual)
        self.path.symlink_to(actual)
        with self.assertRaises(OSError):
            observer.find_gui_provider(self.app, ENDPOINT)
        self.assertEqual(actual.read_bytes(), before)

    def test_engine_exec_or_identity_change_refuses_even_valid_catalog(self):
        self.write([self.row])
        with patch.object(profile_observer, 'engine_image', side_effect=[(1, 2, 3), (1, 2, 9)]):
            with self.assertRaises(ValueError):
                observer.find_gui_provider(self.app, ENDPOINT)
        selected = observer.selected
        def changed(rows, endpoint):
            self.app.observations['engine_identity'] = {'pid': 456, 'start_time_ticks': 10}
            return selected(rows, endpoint)
        with patch.object(observer, 'selected', side_effect=changed):
            with self.assertRaises(ValueError):
                observer.find_gui_provider(self.app, ENDPOINT)

    def test_foreign_model_or_unbounded_inventory_never_returns_user_payload(self):
        for models in [[{'id': 'SECRET UNKNOWN MODEL'}], [{'id': MODEL}, {'id': MODEL}], 'SECRET']:
            with self.assertRaises(ValueError):
                observer.selected([{**self.row, 'models': models}], ENDPOINT)
        with self.assertRaises(ValueError):
            observer.document(json.dumps({'providers': [self.row] * 257}).encode())

    def test_gui_provider_binds_a_free_port_and_serves_that_endpoint(self):
        fixture = ProviderFixture.gui([{'name': 'command', 'arguments': {'command': '/usr/bin/true'}}])
        with fixture:
            port = fixture.server.server_port
            self.assertNotEqual(port, 0)
            self.assertEqual(fixture.base_url, gui_endpoint(port))
            from urllib.request import Request, urlopen
            request = Request(fixture.base_url + '/model/info', headers={'Authorization': 'Bearer installed-local-fixture-key'})
            with urlopen(request, timeout=5) as response:
                self.assertEqual(json.loads(response.read())['data'][0]['model_name'], MODEL)
        for port in (0, 80, 1023, 65536, True):
            with self.subTest(port=port), self.assertRaises(ValueError):
                gui_endpoint(port)

    def test_a_registration_for_another_endpoint_is_refused(self):
        with self.assertRaisesRegex(ValueError, 'fixed local credential transport'):
            observer.selected([self.row], gui_endpoint(41235))
