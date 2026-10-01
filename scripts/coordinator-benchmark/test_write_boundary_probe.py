import errno
import json
import pathlib
import shlex
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import write_boundary_probe as probe


class WriteBoundaryProbeTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = pathlib.Path(temporary.name)
        self.target = self.root / 'output.json'
        self.receipt = self.root / 'receipt.json'
        self.call = 'probe-call'

    def test_real_unconfined_write_is_not_misreported_as_denied(self):
        command = shlex.split(probe.command(self.target, self.receipt, self.call))
        command[0] = sys.executable
        subprocess.run(command, check=True)
        self.assertEqual(self.target.read_bytes(), b'boundary probe')
        self.assertEqual(json.loads(self.receipt.read_text()), {'call': self.call, 'errno': 0})
        with self.assertRaises(RuntimeError): probe.require_denied(self.receipt, self.target, self.call)

    def test_permission_errno_is_recorded_and_accepted(self):
        for code in (errno.EACCES, errno.EPERM):
            with self.subTest(code=code), patch.object(sys, 'argv', ['probe', str(self.target), str(self.receipt), self.call]), \
                    patch.object(pathlib.Path, 'write_bytes', side_effect=OSError(code, 'opaque diagnostic')):
                exec(probe.PROGRAM, {})
            self.assertEqual(probe.require_denied(self.receipt, self.target, self.call), {'call': self.call, 'errno': code})

    def test_other_os_errors_are_not_permission_evidence(self):
        for code in (errno.ENOENT, errno.EISDIR, errno.ENOSPC, errno.EIO):
            with patch.object(sys, 'argv', ['probe', str(self.target), str(self.receipt), self.call]), \
                    patch.object(pathlib.Path, 'write_bytes', side_effect=OSError(code, 'permission denied')):
                exec(probe.PROGRAM, {})
            with self.subTest(code=code), self.assertRaises(RuntimeError):
                probe.require_denied(self.receipt, self.target, self.call)

    def test_receipt_is_bound_to_call_and_actual_absence(self):
        for result in ({'call': 'other', 'errno': errno.EACCES}, {'call': self.call, 'errno': True},
                       {'call': self.call, 'errno': str(errno.EACCES)}):
            self.receipt.write_text(json.dumps(result))
            with self.assertRaises(RuntimeError): probe.require_denied(self.receipt, self.target, self.call)
        self.receipt.write_text(json.dumps({'call': self.call, 'errno': errno.EACCES}))
        self.target.write_text('unexpected effect')
        with self.assertRaises(RuntimeError): probe.require_denied(self.receipt, self.target, self.call)

    def test_absent_receipt_cannot_satisfy_the_boundary(self):
        with self.assertRaises(FileNotFoundError): probe.require_denied(self.receipt, self.target, self.call)

    def test_command_preserves_paths_with_shell_metacharacters(self):
        target = self.root / 'space `name` $(name)'
        args = shlex.split(probe.command(target, self.receipt, self.call))
        self.assertEqual(args, ['python3', '-B', '-c', probe.PROGRAM, str(target), str(self.receipt), self.call])
