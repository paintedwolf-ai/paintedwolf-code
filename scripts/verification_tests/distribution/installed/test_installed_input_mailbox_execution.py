"""Physical guest mailbox bounds and fresh, nonreplayed delivery contracts."""
import io
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from linux_release.installed_input import mailbox, protocol


class NativeInputMailboxTests(unittest.TestCase):
    def setUp(self):
        marker = patch.object(mailbox.Mailbox, 'marker')
        marker.start()
        self.addCleanup(marker.stop)
        self.execution = {'format_version': 1, 'execution_nonce': 'a' * 32,
            'source_commit': 'b' * 40, 'driver_tree': 'c' * 40,
            'artifact_id': 'native-rpm-amd64.rpm', 'environment_id': 'fedora', 'variant_id': 'gnome-wayland'}

    def selected(self, directory):
        selected = mailbox.Mailbox(self.execution, 'owned-fedora')
        selected.path = Path(directory) / 'mailbox'
        selected.create()
        return selected

    def test_private_bounded_atomic_files_and_no_replacement(self):
        with tempfile.TemporaryDirectory() as directory:
            selected = self.selected(directory)
            selected.attach()
            self.assertIsNone(selected.read('request.json'))
            selected.write('request.json', {'value': 1})
            self.assertEqual(selected.read('request.json'), {'value': 1})
            with self.assertRaises(FileExistsError):
                selected.write('request.json', {'value': 2})
            with self.assertRaises(ValueError):
                selected.read('../outside')
            (selected.path / 'request.json').chmod(0o644)
            with self.assertRaises(ValueError):
                selected.read('request.json')

    def test_refuses_symlinks_stale_binding_duplicate_keys_and_nonfinite_json(self):
        with tempfile.TemporaryDirectory() as directory:
            selected = self.selected(directory)
            selected.execution = {**self.execution, 'execution_nonce': 'd' * 32}
            with self.assertRaises(ValueError):
                selected.attach()
            (selected.path / 'request.json').symlink_to('/dev/null')
            with self.assertRaises(OSError):
                selected.read('request.json')
        for raw in (b'{"a":1,"a":2}', b'{"a":NaN}', b'[]', b' ' * (protocol.MAX_DOCUMENT + 1)):
            with self.subTest(raw=raw[:32]), self.assertRaises(ValueError):
                mailbox.decode(raw)

    def test_server_exposes_only_fixed_poll_ack_close_operations(self):
        class Pipe:
            def __init__(self, raw=b''):
                self.buffer = io.BytesIO(raw)
        output = Pipe()
        with tempfile.TemporaryDirectory() as directory:
            selected = self.selected(directory)
            with patch.object(mailbox, 'Mailbox', return_value=selected), \
                    patch.object(mailbox.sys, 'stdin', Pipe(b'{"operation":"poll"}\n{"operation":"close"}\n')), \
                    patch.object(mailbox.sys, 'stdout', output):
                mailbox.serve(self.execution, 'owned-fedora')
            self.assertEqual(mailbox.decode(output.buffer.getvalue()), {'state': 'waiting'})
            with patch.object(mailbox.sys, 'stdin', Pipe(b'{"operation":"exec","command":"foreign"}\n')):
                with self.assertRaises(ValueError):
                    mailbox.serve(self.execution, 'owned-fedora')


if __name__ == '__main__':
    unittest.main()
