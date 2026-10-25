"""Root code preparation consumes only frozen, bounded Python source objects."""
import base64
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import unittest
from unittest.mock import patch

from scripts.linux_release.installed_input import polkit_capture


def archive(files):
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode='w') as stream:
        for name, body in files.items():
            member = tarfile.TarInfo(name)
            member.size = len(body)
            stream.addfile(member, io.BytesIO(body))
    return output.getvalue()


def listing(files, *, mode='100644', size=None):
    return b''.join((f'{mode} blob ' + 'a' * 40 + f' {len(body) if size is None else size}\t{name}\0').encode()
                    for name, body in files.items())


class PolkitCaptureTests(unittest.TestCase):
    def test_frozen_git_python_bytes_and_manifest_digest_are_exact(self):
        files = {'scripts/__init__.py': b'', 'scripts/linux_release/installed_process.py': b'# fixture\n'}
        with patch.object(polkit_capture.subprocess, 'check_output', side_effect=[listing(files), archive(files)]) as git:
            payload, digest = polkit_capture.document(Path('/owned'), 'c' * 40)
        document = json.loads(payload)
        self.assertEqual({name: base64.b64decode(body) for name, body in document['files'].items()}, files)
        manifest = {'format_version': 1, 'driver_tree': 'c' * 40,
                    'files_sha256': {name: hashlib.sha256(body).hexdigest() for name, body in files.items()}}
        encoded = (json.dumps(manifest, sort_keys=True, separators=(',', ':')) + '\n').encode()
        self.assertEqual(digest, hashlib.sha256(encoded).hexdigest())
        self.assertIn('c' * 40, git.call_args_list[0].args[0])
        self.assertIn('c' * 40, git.call_args_list[1].args[0])
        self.assertNotIn('scripts/harness', git.call_args_list[0].args[0])

    def test_nonregular_oversized_or_escaping_sources_refuse_before_root_transport(self):
        files = {'scripts/linux_release/fixture.py': b'# fixture\n'}
        for metadata in (listing(files, mode='120000'), listing(files, size=(1 << 20) + 1)):
            with patch.object(polkit_capture.subprocess, 'check_output', return_value=metadata) as git:
                with self.assertRaises(ValueError):
                    polkit_capture.document(Path('/owned'), 'c' * 40)
                self.assertEqual(git.call_count, 1)
        escaped = {'../escape.py': b'# fixture\n'}
        with patch.object(polkit_capture.subprocess, 'check_output', side_effect=[listing(escaped), archive(escaped)]):
            with self.assertRaises(ValueError):
                polkit_capture.document(Path('/owned'), 'c' * 40)

    def test_root_copy_requires_actual_privilege_before_read_or_write(self):
        with patch.object(os, 'geteuid', return_value=1000), patch('sys.argv', ['fixed', 'c' * 40]):
            with self.assertRaises(SystemExit) as refused:
                exec(polkit_capture.PREPARE, {})
        self.assertEqual(refused.exception.code, 2)

    def test_privileged_observer_import_does_not_load_application_or_scheduler(self):
        checkout = Path(__file__).resolve().parents[5]
        code = ('import sys;sys.path.insert(0,sys.argv[1]);'
            'import scripts.linux_release.installed_input.polkit_root;'
            'assert "scripts.linux_release.installed_cases.app" not in sys.modules;'
            'assert "scripts.harness.supervise" not in sys.modules;'
            'assert "verification_batch" not in sys.modules')
        result = subprocess.run([sys.executable, '-I', '-B', '-c', code, str(checkout)],
                                capture_output=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr.decode())


if __name__ == '__main__':
    unittest.main()
