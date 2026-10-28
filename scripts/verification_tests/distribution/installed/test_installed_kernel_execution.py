"""Installed kernel outcomes require positive execution and exact native observations."""
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from linux_release.installed_cases import ipc, kernel


class InstalledKernelTests(unittest.TestCase):
    def test_namespace_kill_must_be_typed_and_invoked(self):
        for invoked, reaped, killed, expected in [
                (True, True, True, 'passed'), (False, True, True, 'failed'),
                (True, True, 'true', 'failed'), (True, False, True, 'failed')]:
            facts = {'kernel_observation': {'child_reaped': reaped, 'namespace_escape_killed': killed}}
            with self.subTest(invoked=invoked, reaped=reaped, killed=killed), \
                    patch.object(kernel, 'run_script', return_value=(invoked, facts, {})):
                self.assertEqual(kernel.restricted_namespaces({})[0], expected)

    def test_private_shared_memory_requires_host_control_and_no_host_write(self):
        for leak, expected in [(False, 'passed'), (True, 'failed')]:
            with self.subTest(leak=leak), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                def run(binding, script, arguments):
                    host, private = map(Path, arguments)
                    self.assertEqual(host.read_bytes(), b'public shared memory fixture')
                    if leak:
                        private.write_text('wrong namespace')
                    return True, {'kernel_observation': {
                        'host_marker_hidden': True, 'private_write_read': True}}, {}
                def path(value):
                    return root if value == '/dev/shm' else Path(value)
                with patch.object(kernel, 'Path', side_effect=path), patch.object(kernel, 'run_script', side_effect=run):
                    status, facts, streams = kernel.private_shared_memory({})
                self.assertEqual(status, expected)
                self.assertEqual(list(root.iterdir()), [])
                self.assertTrue(facts['unconfined_marker_read'])

    def test_system_v_requires_all_three_private_families_and_preserved_host_objects(self):
        class Host:
            key = 123
            created = {'shared_memory': 1, 'semaphore': 2, 'message_queue': 3}
            def __enter__(self):
                return self
            def __exit__(self, *args):
                pass
            def intact(self):
                return True
        for missing, expected in [(None, 'passed'), ('message_queue_private', 'failed')]:
            observation = {name + suffix: True for name in Host.created for suffix in ('_hidden', '_private')}
            if missing:
                observation.pop(missing)
            with self.subTest(missing=missing), patch.object(ipc, 'OwnedIPC', Host), \
                    patch.object(ipc, 'run_script', return_value=(True, {'kernel_observation': observation}, {})):
                status, facts, streams = ipc.system_v_isolation({})
                self.assertEqual(status, expected)
                self.assertTrue(facts['host_objects_preserved'])
