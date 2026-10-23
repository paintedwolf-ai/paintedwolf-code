"""A preserved endpoint is recovery input, never proof of a new fixture or model turn."""
import copy
import hashlib
import json
import os
from pathlib import Path
import socket
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, patch

from linux_release.installed_cases import provider_recovery as recovery, provider_setup
from linux_release.installed_cases.provider import MODEL, ProviderFixture
from linux_release.installed_input import controller, protocol


class ProviderRecoveryTests(unittest.TestCase):
    def fixture(self):
        execution = {'format_version': 1, 'execution_nonce': 'a' * 32, 'source_commit': 'b' * 40,
            'driver_tree': 'c' * 40, 'artifact_id': 'owned.deb', 'environment_id': 'noble', 'variant_id': 'wayland'}
        current = dict(execution, execution_nonce='d' * 32, driver_tree='e' * 40)
        identity = {'pid': 100, 'parent_pid': 50, 'start_time_ticks': 42}
        focus = {'launch': identity, 'entry': identity, 'frame': identity, 'frame_lineage': [identity],
            'entry_lineage': [identity], 'accessible_chain': [{'bus_name': ':1.2', 'path': '/frame', 'pid': 100}],
            'entry_focused': True, 'frame_active': True, 'frame_visible': True}
        record = {'format_version': 1, 'vm_id': 'owned', 'transaction_id': 'f' * 32,
            'operation': 'provider-endpoint-v1-41234', 'completed': True, 'events': [], 'text_readback': False}
        for ordinal, _ in enumerate(protocol.steps(record['operation'])):
            pending = {'format_version': 1, 'binding': execution, 'transaction_id': record['transaction_id'],
                       'operation': record['operation'], 'ordinal': ordinal, 'focus': focus}
            delivery = {'format_version': 1, 'execution_nonce': execution['execution_nonce'], 'vm_id': 'owned',
                'transaction_id': record['transaction_id'], 'operation': record['operation'], 'ordinal': ordinal,
                'event_sha256': protocol.event_sha256(record['operation'], ordinal),
                'lease': {'vm_id': 'owned'}, 'qmp_ack': {'id': ordinal + 1, 'return': {}}}
            record['events'].append({'request': pending, 'delivery': delivery, 'focus_after': focus})
        artifact = {'id': 'owned.deb', 'identity': {'sha256': '1' * 64}}
        document = {'format_version': 1, 'source_commit': execution['source_commit'], 'artifact': artifact,
            'capability': 'conversation', 'case': 'restart-and-reconnect', 'status': 'unverified',
            'observations': {'launch_before': {'session': {'User': str(os.getuid())}, 'fixed_input': [record]}}}
        binding = {'source_commit': execution['source_commit'], 'artifact': artifact,
                   'native_input': {'vm_id': 'owned', 'binding': current}}
        return document, binding

    def test_same_product_and_vm_accept_distinct_old_and_current_driver_identities(self):
        document, binding = self.fixture()
        self.assertEqual(recovery.registered(document, binding, str(os.getuid()))['endpoint'], 'http://127.0.0.1:41234/v1')
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'execution.json'
            raw = json.dumps(document).encode();path.write_bytes(raw)
            binding['case_inputs'] = {'desktop': {'format_version': 1, 'provider_recovery': {
                'prior_execution': str(path), 'sha256': hashlib.sha256(raw).hexdigest()}}}
            self.assertEqual(recovery.load(binding)[0]['endpoint'], 'http://127.0.0.1:41234/v1')
            path.write_bytes(raw + b' ')
            with self.assertRaisesRegex(ValueError, 'digest differs'):
                recovery.load(binding)
        self.assertIsNone(recovery.load({}))

    def test_wrong_bytes_vm_account_incomplete_or_changed_delivery_refuse(self):
        original, binding = self.fixture()
        for change in ('source', 'artifact', 'vm', 'account', 'incomplete', 'ordinal', 'ack', 'focus', 'replay'):
            document = copy.deepcopy(original)
            record = document['observations']['launch_before']['fixed_input'][0]
            if change == 'source': document['source_commit'] = '9' * 40
            if change == 'artifact': document['artifact']['identity']['sha256'] = '9' * 64
            if change == 'vm': record['vm_id'] = 'foreign'
            if change == 'account': document['observations']['launch_before']['session']['User'] = 'foreign'
            if change == 'incomplete': record['events'].pop()
            if change == 'ordinal': record['events'][0]['request']['ordinal'] = 1
            if change == 'ack': record['events'][0]['delivery']['qmp_ack']['return'] = {'not-empty': True}
            if change == 'focus': record['events'][0]['focus_after'] = {}
            if change == 'replay':
                for event in record['events']: event['request']['binding']['execution_nonce'] = 'd' * 32
            with self.subTest(change=change), self.assertRaises(ValueError):
                recovery.registered(document, binding, str(os.getuid()))

    def test_controller_pins_retained_registration_and_selects_only_current_prompt(self):
        document, binding = self.fixture()
        raw = json.dumps(document).encode()
        witness = {**recovery.registered(document, binding, str(os.getuid())),
                   'prior_execution_sha256': hashlib.sha256(raw).hexdigest()}
        current = {'operation': 'reply-fixture-prompt-v1', 'completed': False, 'events': []}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            leaf = root / 'leaf/evidence/conversation/restart-and-reconnect'
            leaf.mkdir(parents=True)
            (leaf / 'provider-registration.bin').write_bytes(raw)
            (leaf / 'execution.json').write_text(json.dumps({'artifact': binding['artifact'], 'observations': {
                'provider_recovery': witness, 'launch_before': {'session': {'User': str(os.getuid())},
                                                               'fixed_input': [current]}}}))
            records, operations = controller.delivery_records(root, binding['native_input']['binding'], 'owned')
            self.assertEqual((records, operations), ([current], ['reply-fixture-prompt-v1']))
            (leaf / 'provider-registration.bin').write_bytes(raw + b' ')
            with self.assertRaises(ValueError):
                controller.delivery_records(root, binding['native_input']['binding'], 'owned')

    def test_recovery_server_exclusively_binds_only_canonical_loopback(self):
        calls = [{'name': 'command', 'arguments': {'command': '/usr/bin/true'}}]
        for endpoint in ('http://user@127.0.0.1:41234/v1', 'http://127.0.0.1:41234/v1?q=1',
                         'http://localhost:41234/v1', 'http://127.0.0.1:80/v1'):
            with self.assertRaises(ValueError): ProviderFixture.gui(calls, endpoint=endpoint)
        with socket.socket() as occupied:
            occupied.bind(('127.0.0.1', 0));occupied.listen()
            endpoint = f'http://127.0.0.1:{occupied.getsockname()[1]}/v1'
            with self.assertRaises(OSError):
                with ProviderFixture.gui(calls, endpoint=endpoint): pass
            self.assertGreater(occupied.fileno(), 0)

    def test_existing_provider_is_not_created_or_retyped_and_needs_current_discovery(self):
        app, ui = SimpleNamespace(observations={}, require_running=lambda: None), MagicMock()
        ui.__enter__.return_value = ui
        witness = {'provider_id': 'fixture', 'models': [MODEL]}
        fixture = SimpleNamespace(base_url='http://127.0.0.1:41234/v1', discovery_observations=[{
            'path': '/v1/model/info', 'model': MODEL, 'fixture_credential_observed': True}])
        with patch.object(provider_setup, 'DesktopUI', return_value=ui), patch.object(
                provider_setup, 'find_gui_provider', return_value=witness), patch.object(provider_setup, 'physical_input') as physical:
            self.assertEqual(provider_setup.configure_provider(app, fixture), witness)
        physical.assert_not_called()
        self.assertEqual([call.args[0] for call in ui.click.call_args_list], ['Default model', MODEL, 'Skip'])
        fixture.discovery_observations = []
        with patch.object(provider_setup, 'find_gui_provider', return_value=witness), patch.object(
                provider_setup.time, 'monotonic', side_effect=[0, 1, 91]), patch.object(provider_setup.time, 'sleep'):
            with self.assertRaises(provider_setup.CaseUnavailable): provider_setup.await_catalog(app, fixture)
