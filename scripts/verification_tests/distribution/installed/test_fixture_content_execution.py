"""Bound object bytes, custody, output and lifetime independently of transcript text."""
from contextlib import nullcontext
import hashlib
import os
from pathlib import Path
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, patch

from linux_release.installed_cases import fixture_content as owner

PROJECT = '10000000-0000-4000-8000-000000000001'
RAW = b'["{\"id\":\"page-owned\"}"]'


class FixtureContentTests(unittest.TestCase):
    def tree(self, root):
        digest = hashlib.sha256(RAW).hexdigest()
        parent = root
        for segment in ('projects', PROJECT, 'model-content', digest[:2]):
            parent /= segment
            parent.mkdir(mode=0o700)
        path = parent / digest[2:]
        path.write_bytes(b'compressed')
        path.chmod(0o600)
        return path, digest

    def read(self, root, digest):
        with patch.object(owner, 'profile', return_value=nullcontext(root)):
            return owner.read_object(None, PROJECT, digest, len(RAW), 10, maximum=65536)

    def test_exact_physical_object_and_hash_are_required(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path, digest = self.tree(root)
            with patch.object(owner, 'decompress', return_value=RAW):
                self.assertEqual(self.read(root, digest), RAW)
                path.chmod(0o644)
                with self.assertRaises(ValueError):
                    self.read(root, digest)
                path.chmod(0o600)
                os.link(path, path.with_suffix('.other'))
                with self.assertRaises(ValueError):
                    self.read(root, digest)
                path.with_suffix('.other').unlink()
                path.write_bytes(b'changed-size')
                with self.assertRaises(ValueError):
                    self.read(root, digest)

    def test_substitution_during_decode_and_wrong_digest_refuse(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path, digest = self.tree(root)
            with patch.object(owner, 'decompress', return_value=b'X' * len(RAW)):
                with self.assertRaisesRegex(ValueError, 'digest'):
                    self.read(root, digest)
            def replace(*args):
                path.rename(path.with_suffix('.held'))
                path.write_bytes(b'compressed')
                path.chmod(0o600)
                return RAW
            with patch.object(owner, 'decompress', side_effect=replace):
                with self.assertRaisesRegex(ValueError, 'changed'):
                    self.read(root, digest)
            path.unlink()
            path.symlink_to(path.with_suffix('.held'))
            with self.assertRaises(ValueError):
                self.read(root, digest)

    def decode_child(self, program, size, *, clocks=None):
        launch = subprocess.Popen
        children = []
        def start(*args, **kwargs):
            child = launch([sys.executable, '-c', program], **kwargs)
            children.append(child)
            return child
        with tempfile.TemporaryFile() as stream, patch.object(owner.subprocess, 'Popen', side_effect=start):
            if clocks is not None:
                ticks = iter(clocks)
                with patch.object(owner, 'time', SimpleNamespace(monotonic=lambda: next(ticks))):
                    try:
                        return owner.decompress(stream, size)
                    finally:
                        self.assertTrue(children[0].poll() is not None)
            try:
                return owner.decompress(stream, size)
            finally:
                self.assertTrue(children[0].poll() is not None)

    def test_real_pipe_output_size_exit_and_deadline_are_bounded(self):
        self.assertEqual(self.decode_child('import os;os.write(1,b"abc")', 3), b'abc')
        for program in ('import os;os.write(1,b"abcd")', 'import os;os.write(1,b"ab")',
                        'import os;os.write(1,b"abc");raise SystemExit(1)'):
            with self.subTest(program=program), self.assertRaises(ValueError):
                self.decode_child(program, 3)
        # Expire before waiting: no machine-speed assertion or arbitrary real sleep.
        with self.assertRaises(owner.CaseUnavailable):
            self.decode_child('import signal;signal.pause()', 3, clocks=[0, 6, 6, 6, 6, 6, 6, 6])

    def test_cleanup_signals_only_unsettled_child_and_has_bounded_waits(self):
        for settled in (False, True):
            with self.subTest(settled=settled):
                child = MagicMock()
                child.poll.return_value = 0 if settled else None
                child.wait.side_effect = [subprocess.TimeoutExpired('zstd', 1), 0]
                ticks = iter((0, 6))
                with patch.object(owner.subprocess, 'Popen', return_value=child), patch.object(
                        owner.selectors, 'DefaultSelector'), patch.object(
                        owner, 'time', SimpleNamespace(monotonic=lambda: next(ticks))):
                    with self.assertRaises(owner.CaseUnavailable):
                        owner.decompress(None, 3)
                child.stdout.close.assert_called_once()
                if settled:
                    child.terminate.assert_not_called()
                    child.kill.assert_not_called()
                    child.wait.assert_not_called()
                else:
                    child.terminate.assert_called_once()
                    child.kill.assert_called_once()
                    self.assertEqual([call.kwargs for call in child.wait.call_args_list], [{'timeout': 1}, {'timeout': 1}])
