"""Root code preparation consumes only frozen, bounded Python source objects."""
import base64
import builtins
import hashlib
import io
import json
import os
import stat
from pathlib import Path
import subprocess
import sys
import tarfile
import unittest
from types import SimpleNamespace
from unittest.mock import Mock, patch

from scripts.linux_release.installed_input import captured_driver


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
        with patch.object(captured_driver.subprocess, 'check_output', side_effect=[listing(files), archive(files)]) as git:
            payload, digest = captured_driver.document(Path('/owned'), 'c' * 40)
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
            with patch.object(captured_driver.subprocess, 'check_output', return_value=metadata) as git:
                with self.assertRaises(ValueError):
                    captured_driver.document(Path('/owned'), 'c' * 40)
                self.assertEqual(git.call_count, 1)
        escaped = {'../escape.py': b'# fixture\n'}
        with patch.object(captured_driver.subprocess, 'check_output', side_effect=[listing(escaped), archive(escaped)]):
            with self.assertRaises(ValueError):
                captured_driver.document(Path('/owned'), 'c' * 40)

    def test_root_copy_requires_actual_privilege_before_read_or_write(self):
        with patch.object(os, 'geteuid', return_value=1000), patch('sys.argv', ['fixed', 'c' * 40]):
            with self.assertRaises(SystemExit) as refused:
                exec(captured_driver.PREPARE, {})
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


class AccountCaptureCommandTests(unittest.TestCase):
    def test_unsafe_tree_refuses_before_any_privileged_command(self):
        from scripts.linux_release.installed_input import unlock_account
        from verification_tests.distribution.installed.test_installed_native_unlock_execution import ACCOUNT
        for tree in ('../other', 'A' * 40, '', None):
            with self.subTest(tree=tree), patch.object(unlock_account.native_command, 'command') as command:
                with self.assertRaises(ValueError):
                    unlock_account.run_privileged_prepare(ACCOUNT, b'A' * 40, driver_tree=tree, vm_id='owned')
                command.assert_not_called()
                with self.assertRaises(ValueError):
                    unlock_account.run_privileged_disable(ACCOUNT, driver_tree=tree, vm_id='owned')
                command.assert_not_called()

    def test_polkit_and_gnome_commands_share_exact_capture_validation(self):
        from scripts.linux_release.installed_input import polkit_account, unlock_account
        from verification_tests.distribution.installed.test_installed_native_unlock_execution import ACCOUNT, EXECUTION
        target={'host':'127.0.0.1','port':2222,'user':'qa'}
        actor=Mock(nonce=EXECUTION['execution_nonce'],value={'vm_id':'owned','ssh':target})
        value=polkit_account.Credential(actor,target,EXECUTION,ACCOUNT)
        ready={'disabled':True,'account':ACCOUNT,'shadow_locked':True,
               'credential_state':'locked','sessions_terminated':False}
        with patch.object(polkit_account,'bounded_command',return_value=(0,json.dumps(ready).encode(),b'')) as command, \
                patch('scripts.linux_release.installed_campaign.command',side_effect=lambda target,args:args):
            value._command('disable')
        self.assertEqual(command.call_args.args[0],['/usr/bin/sudo','-n',*unlock_account.helper_python_arguments(
            'disable',ACCOUNT,driver_tree=EXECUTION['driver_tree'],vm_id='owned')])
        self.assertIn(captured_driver.ACCOUNT_BOOTSTRAP,command.call_args.args[0])


class CaptureImportRefusalTests(unittest.TestCase):
    def test_missing_mismatched_or_changed_capture_never_imports_privileged_project_code(self):
        tree = 'c' * 40
        original_import = builtins.__import__
        for failure in ('missing', 'tree', 'digest'):
            class Entry:
                parents = []
                name, suffix = 'fixture.py', '.py'

                def __truediv__(self, name):
                    return marker if name == 'capture.json' else self

                def lstat(self):
                    if self is root and failure == 'missing':
                        raise FileNotFoundError('missing fixed capture')
                    mode = stat.S_IFDIR | 0o555 if self is root else stat.S_IFREG | 0o444
                    return SimpleNamespace(st_mode=mode, st_uid=0, st_gid=0, st_size=100)

                def read_bytes(self):
                    if self is marker:
                        return json.dumps({'format_version': 1,
                            'driver_tree': 'd' * 40 if failure == 'tree' else tree,
                            'files_sha256': {'scripts/linux_release/fixture.py': '0' * 64}}).encode()
                    return b'# changed captured bytes\n'

                def rglob(self, _):
                    return [marker, source]

                def relative_to(self, _):
                    return self

                def as_posix(self):
                    return 'scripts/linux_release/fixture.py'

            root, marker, source = Entry(), Entry(), Entry()
            for bootstrap in (captured_driver.OBSERVER_BOOTSTRAP, captured_driver.ACCOUNT_BOOTSTRAP):
                imports = []
                def checked_import(name, *args, **kwargs):
                    if name == 'scripts' or name.startswith('scripts.'):
                        imports.append(name)
                        raise AssertionError('unvalidated project import')
                    return original_import(name, *args, **kwargs)
                with self.subTest(failure=failure, bootstrap=bootstrap[-80:]), \
                        patch('pathlib.Path', return_value=root), \
                        patch('sys.argv', ['fixed', tree]), \
                        patch('builtins.__import__', side_effect=checked_import):
                    with self.assertRaises(SystemExit) as refused:
                        exec(bootstrap, {})
                    self.assertEqual(refused.exception.code, 2)
                    self.assertEqual(imports, [])


if __name__ == '__main__':
    unittest.main()
