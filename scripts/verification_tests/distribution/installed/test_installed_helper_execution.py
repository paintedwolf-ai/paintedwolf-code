"""The installed helper smoke requires a filtered descendant that cannot write outside."""
import copy
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release.installed_cases import kernel


IDENTITY = {'pid': 42, 'parent_pid': 1, 'start_time_ticks': 9}
HOST = {'user': 1, 'mnt': 2, 'pid': 3, 'net': 4}


def witness():
    return {'child': {'seccomp': 2, 'outside_write_denied': True}, 'child_reaped': True}


class InstalledHelperBoundaryTests(unittest.TestCase):
    def test_filtered_descendant_without_outside_write_passes(self):
        self.assertTrue(kernel.helper_observation(witness()))

    def test_unfiltered_writing_unreaped_or_unknown_shapes_fail(self):
        for change in (lambda row: row['child'].update(seccomp=0), lambda row: row['child'].update(seccomp=True),
                       lambda row: row['child'].update(outside_write_denied=False),
                       lambda row: row.update(child_reaped=False), lambda row: row['child'].update(extra=1),
                       lambda row: row.update(parent={}), lambda row: row.update(child='raw')):
            row = copy.deepcopy(witness())
            change(row)
            with self.subTest(row=row):
                self.assertFalse(kernel.helper_observation(row))

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
