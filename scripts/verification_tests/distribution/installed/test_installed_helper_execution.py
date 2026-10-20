"""Helper witnesses retain process identity and inherited kernel boundaries."""
import copy
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release.installed_cases import kernel

HOST = {'user': 1, 'mnt': 2, 'pid': 3, 'net': 4}
IDENTITY = {'pid': 42, 'parent_pid': 7, 'start_time_ticks': 9}


def witness():
    row = {'no_new_privs': 1, 'seccomp': 2,
           'capabilities': {name: 0 for name in ('CapInh', 'CapPrm', 'CapEff', 'CapBnd', 'CapAmb')},
           'namespaces': {name: value + 100 for name, value in HOST.items()}}
    return {'parent': row, 'child': dict(copy.deepcopy(row), outside_write_denied=True), 'child_reaped': True}


class InstalledHelperTests(unittest.TestCase):
    def test_helper_and_descendant_require_all_kernel_boundaries_and_denial(self):
        self.assertTrue(kernel.helper_observation(witness(), HOST))
        for role in ('parent', 'child'):
            for key, value in [('no_new_privs', 0), ('no_new_privs', True), ('seccomp', 0)]:
                row = witness()
                row[role][key] = value
                self.assertFalse(kernel.helper_observation(row, HOST))
            for name in ('CapInh', 'CapPrm', 'CapEff', 'CapBnd', 'CapAmb'):
                row = witness()
                row[role]['capabilities'][name] = 1
                self.assertFalse(kernel.helper_observation(row, HOST))
            for name in HOST:
                row = witness()
                row[role]['namespaces'][name] = HOST[name]
                self.assertFalse(kernel.helper_observation(row, HOST))
        row = witness()
        row['child']['outside_write_denied'] = False
        self.assertFalse(kernel.helper_observation(row, HOST))
        row = witness()
        row['child_reaped'] = False
        self.assertFalse(kernel.helper_observation(row, HOST))
        row = witness()
        row['child']['namespaces']['mnt'] += 1
        self.assertFalse(kernel.helper_observation(row, HOST))

    def test_missing_namespace_authority_never_establishes_isolation(self):
        for host in [{}, dict(HOST, user=True), dict(HOST, user=0), {'pid': 1}]:
            self.assertFalse(kernel.helper_observation(witness(), host))
        for row in [None, [], {}, dict(witness(), parent=None)]:
            self.assertFalse(kernel.helper_observation(row, HOST))

    def test_exact_engine_namespace_observer_rechecks_lifetime(self):
        result = SimpleNamespace(returncode=0, stdout=b'1\n2\n3\n4\n')
        with patch.object(kernel, 'process_identity', return_value=IDENTITY), \
                patch.object(kernel.subprocess, 'run', return_value=result) as run:
            self.assertEqual(kernel.namespace_observer(IDENTITY), HOST)
            self.assertEqual(run.call_args.args[0], ['/usr/bin/sudo', '-n', '/usr/bin/stat', '-L', '-c', '%i',
                '/proc/42/ns/user', '/proc/42/ns/mnt', '/proc/42/ns/pid', '/proc/42/ns/net'])
        changed = dict(IDENTITY, start_time_ticks=10)
        with patch.object(kernel, 'process_identity', return_value=changed), \
                patch.object(kernel.subprocess, 'run') as run, self.assertRaises(ValueError):
            kernel.namespace_observer(IDENTITY)
        run.assert_not_called()
        with patch.object(kernel, 'process_identity', side_effect=[IDENTITY, changed]), \
                patch.object(kernel.subprocess, 'run', return_value=result), self.assertRaises(ValueError):
            kernel.namespace_observer(IDENTITY)

    def test_namespace_observer_never_accepts_partial_or_zero_measurements(self):
        for raw in [b'', b'1\n2\n3\n', b'1\n2\n3\n0\n', b'1\n2\n3\nno\n']:
            with patch.object(kernel, 'process_identity', return_value=IDENTITY), \
                    patch.object(kernel.subprocess, 'run', return_value=SimpleNamespace(returncode=0, stdout=raw)), \
                    self.assertRaises(ValueError):
                kernel.namespace_observer(IDENTITY)
