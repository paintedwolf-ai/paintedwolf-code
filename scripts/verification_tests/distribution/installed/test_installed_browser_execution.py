"""A real browser fixture requires pinned live descendants and kernel isolation."""
import copy
import json
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release.installed_cases import browser, browser_boundary

ENGINE = {'pid': 10, 'parent_pid': 5, 'start_time_ticks': 30}
HOST = {'user': 1, 'mnt': 2, 'pid': 3, 'net': 4}


def witness():
    identity = {'pid': 12, 'parent_pid': 10, 'start_time_ticks': 40}
    return {'engine': ENGINE, 'browsers': [{'identity': identity, 'ancestors': [identity],
        'namespaces': {name: value + 100 for name, value in HOST.items()}, 'uids': [1000] * 4,
        'no_new_privs': 1, 'seccomp': 2, 'capabilities': {name: 0 for name in browser_boundary.CAPS}}]}


class InstalledBrowserTests(unittest.TestCase):
    def test_pinned_browser_requires_actual_inherited_kernel_boundaries(self):
        self.assertEqual(browser_boundary.validate(witness(), ENGINE, HOST, 1000), [{'pid': 12, 'start_ticks': '40'}])
        for field, value in [('no_new_privs', 0), ('no_new_privs', True), ('seccomp', 0), ('uids', [0] * 4)]:
            data = witness()
            data['browsers'][0][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                browser_boundary.validate(data, ENGINE, HOST, 1000)
        for name in browser_boundary.CAPS:
            data = witness()
            data['browsers'][0]['capabilities'][name] = 1
            with self.assertRaises(ValueError):
                browser_boundary.validate(data, ENGINE, HOST, 1000)
        for name in HOST:
            data = witness()
            data['browsers'][0]['namespaces'][name] = HOST[name]
            with self.assertRaises(ValueError):
                browser_boundary.validate(data, ENGINE, HOST, 1000)

    def test_no_missing_reused_or_foreign_ancestry_can_establish_ownership(self):
        mutations = [lambda data: data.update(browsers=[]),
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
                browser_boundary.validate(data, ENGINE, HOST, 1000)
        with self.assertRaises(ValueError):
            browser_boundary.validate(witness(), ENGINE, {}, 1000)

    def test_live_observer_uses_only_exact_engine_and_sealed_image_identifiers(self):
        path = Path('/selected/engine-root/browser/chrome-headless-shell')
        binding = {'images': {str(path): {'device': 1, 'inode': 2, 'bytes': 3}}}
        with patch.object(browser_boundary, 'probe_path', return_value=path) as probe, \
                patch.object(browser_boundary, 'process_identity', return_value=ENGINE), \
                patch.object(browser_boundary.subprocess, 'run', return_value=SimpleNamespace(returncode=0, stdout=json.dumps(witness()).encode())) as run:
            self.assertEqual(browser_boundary.observe(binding, ENGINE), witness())
            arguments = run.call_args.args[0]
            self.assertEqual(arguments[:4], ['/usr/bin/sudo', '-n', '/usr/bin/python3', '-c'])
            self.assertEqual(arguments[4], browser_boundary.OBSERVER)
            self.assertEqual(json.loads(arguments[5]), {'engine': ENGINE, 'image': [1, 2, 3]})
            self.assertEqual(probe.call_count, 2)
        with patch.object(browser_boundary, 'probe_path', return_value=path), \
                patch.object(browser_boundary, 'process_identity', return_value=dict(ENGINE, start_time_ticks=31)), \
                patch.object(browser_boundary.subprocess, 'run') as run, self.assertRaises(ValueError):
            browser_boundary.observe(binding, ENGINE)
        run.assert_not_called()

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
