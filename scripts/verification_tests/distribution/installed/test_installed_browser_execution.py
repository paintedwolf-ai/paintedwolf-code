"""A real browser fixture requires pinned live descendants and kernel isolation."""
import copy
import json
from pathlib import Path
import unittest
from unittest.mock import MagicMock, patch

from linux_release.installed_cases import browser, browser_boundary

LAUNCH = {'pid': 5, 'parent_pid': 1, 'start_time_ticks': 20}
ENGINE = {'pid': 10, 'parent_pid': 5, 'start_time_ticks': 30}
HOST = {'user': 1, 'mnt': 2, 'pid': 3, 'net': 4}


def witness():
    identity = {'pid': 12, 'parent_pid': 5, 'start_time_ticks': 40}
    return {'launch': LAUNCH, 'engine': ENGINE, 'engine_ancestors': [ENGINE], 'browsers': [{'identity': identity, 'ancestors': [identity],
        'namespaces': {name: value + 100 for name, value in HOST.items()}, 'uids': [1000] * 4,
        'no_new_privs': 1, 'seccomp': 2, 'capabilities': {name: 0 for name in browser_boundary.CAPS}}]}


class InstalledBrowserTests(unittest.TestCase):
    def test_pinned_browser_requires_actual_inherited_kernel_boundaries(self):
        self.assertEqual(browser_boundary.validate(witness(), ENGINE, LAUNCH, HOST, 1000), [{'pid': 12, 'start_ticks': '40'}])
        for field, value in [('no_new_privs', 0), ('no_new_privs', True), ('seccomp', 0), ('uids', [0] * 4)]:
            data = witness()
            data['browsers'][0][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                browser_boundary.validate(data, ENGINE, LAUNCH, HOST, 1000)
        for name in browser_boundary.CAPS:
            data = witness()
            data['browsers'][0]['capabilities'][name] = 1
            with self.assertRaises(ValueError):
                browser_boundary.validate(data, ENGINE, LAUNCH, HOST, 1000)
        for name in HOST:
            data = witness()
            data['browsers'][0]['namespaces'][name] = HOST[name]
            with self.assertRaises(ValueError):
                browser_boundary.validate(data, ENGINE, LAUNCH, HOST, 1000)

    def test_no_missing_reused_or_foreign_ancestry_can_establish_ownership(self):
        mutations = [lambda data: data.update(browsers=[]),
                     lambda data: data.update(launch=dict(LAUNCH, start_time_ticks=21)),
                     lambda data: data.update(engine_ancestors=[]),
                     lambda data: data.update(engine_ancestors=[dict(ENGINE, parent_pid=99)]),
                     lambda data: data.update(engine=dict(ENGINE, start_time_ticks=31)),
                     lambda data: data['browsers'].append(copy.deepcopy(data['browsers'][0])),
                     lambda data: data['browsers'][0].update(ancestors=[]),
                     lambda data: data['browsers'][0]['identity'].update(pid=True),
                     lambda data: data['browsers'][0]['identity'].update(parent_pid=99),
                     lambda data: data['browsers'][0]['ancestors'].append(copy.deepcopy(data['browsers'][0]['identity']))]
        for mutate in mutations:
            data = witness()
            mutate(data)
            with self.assertRaises(ValueError):
                browser_boundary.validate(data, ENGINE, LAUNCH, HOST, 1000)
        with self.assertRaises(ValueError):
            browser_boundary.validate(witness(), ENGINE, LAUNCH, {}, 1000)

    def test_observer_uses_captured_launch_tree_for_broker_sibling_browser(self):
        path = Path('/selected/engine-root/browser/chrome-headless-shell')
        binding = {'images': {str(path): {'device': 1, 'inode': 2, 'bytes': 3}}}
        browser_row = witness()['browsers'][0]
        row = {**browser_row, 'image': [1, 2, 3],
               'namespaces': {key: [1, value] for key, value in browser_row['namespaces'].items()},
               'capabilities': {key: 0 for key in browser_boundary.process_tree.CAPABILITIES},
               'capability_bounding_set': 0}
        tree = {'processes': [{'identity': LAUNCH, 'image': [3, 4, 5]},
                              {'identity': ENGINE, 'image': [4, 5, 6]}, row]}
        def identity(pid):
            return {5: LAUNCH, 10: ENGINE}[pid]
        with patch.object(browser_boundary, 'probe_path', return_value=path) as probe, \
                patch.object(browser_boundary, 'process_identity', side_effect=identity), \
                patch.object(browser_boundary.process_tree, 'observe', return_value=tree) as observe:
            self.assertEqual(browser_boundary.observe(binding, ENGINE, LAUNCH), witness())
            observe.assert_called_once_with(LAUNCH)
            self.assertEqual(probe.call_count, 2)
            for mutate in (lambda: tree['processes'][1].update(identity=dict(ENGINE, parent_pid=99)),
                           lambda: tree['processes'].pop(1)):
                with self.subTest(mutate=mutate):
                    mutate()
                    with self.assertRaises(ValueError):
                        browser_boundary.observe(binding, ENGINE, LAUNCH)
        with patch.object(browser_boundary, 'probe_path', return_value=path), \
                patch.object(browser_boundary, 'process_identity', return_value=dict(ENGINE, start_time_ticks=31)), \
                patch.object(browser_boundary.process_tree, 'observe') as observe, self.assertRaises(ValueError):
            browser_boundary.observe(binding, ENGINE, LAUNCH)
        observe.assert_not_called()

    def test_observer_failure_retains_page_receipt_and_bounded_app_streams(self):
        row = {'tool': 'page_open', 'outcome': 'completed', 'invocation': {
            'tool': 'page_open', 'invoked': True, 'status': 'completed'},
            'content': json.dumps({'id': 'page_host_123', 'live_pages': ['page_host_123']})}
        app, provider, turn = MagicMock(), MagicMock(), MagicMock()
        app.observations = {'engine_identity': ENGINE, 'launch_identity': LAUNCH}
        app.streams.return_value = {'app-stderr': b'bounded fixture diagnostic'}
        provider.observations, turn.observations = {}, {}
        turn.wait.return_value = [row]
        with patch.object(browser, 'InstalledApp') as installed, \
                patch.object(browser, 'ProviderFixture') as fixture, \
                patch.object(browser, 'ToolTurn') as tool_turn, \
                patch.object(browser, 'wait_settled'), \
                patch.object(browser, 'namespace_observer', return_value=HOST), \
                patch.object(browser, 'observe', return_value={'browsers': []}), \
                patch.object(browser, 'cleanup'):
            installed.return_value.__enter__.return_value = app
            fixture.return_value.__enter__.return_value = provider
            tool_turn.return_value.__enter__.return_value = turn
            with self.assertRaises(ValueError) as refused:
                browser.browser_sandbox({})
        facts = refused.exception.observations
        self.assertEqual(facts['tool_results'], [row])
        self.assertEqual(facts['opened']['page']['id'], 'page_host_123')
        self.assertEqual(facts['browser_observation'], {'browsers': []})
        self.assertEqual(refused.exception.streams, {'app-stderr': b'bounded fixture diagnostic'})

    def test_browser_tool_receipt_cannot_be_replaced_by_success_prose(self):
        row = {'tool': 'page_open', 'outcome': 'completed', 'invocation': {
            'tool': 'page_open', 'invoked': True, 'status': 'completed'},
            'content': json.dumps({'id': 'page_host_123', 'live_pages': ['page_host_123']})}
        self.assertEqual(browser.opened([row])['page']['id'], 'page_host_123')
        for changed in [dict(row, invocation={}), dict(row, outcome='rejected'),
                        dict(row, content='browser is confined and open'),
                        dict(row, content=json.dumps({'id': 'page_host_123', 'live_pages': []}))]:
            with self.assertRaises(ValueError):
                browser.opened([changed])
        with self.assertRaises(ValueError):
            browser.opened([row, row])
