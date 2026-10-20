"""Builder-host permissions are attached only to a reachable immutable native scanner."""
import copy
import os
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release import nix_scanner_host as host

ROOT = '/nix/store/' + '0' * 32 + '-application.drv'
TOOL = '/nix/store/' + '1' * 32 + '-' + host.PNAME + '-1.0.2.drv'
OUTPUT = '/nix/store/' + '2' * 32 + '-' + host.PNAME + '-1.0.2'
EXE = OUTPUT + '/bin/opengrep-artifact'


def graph():
    return {ROOT: {'system': 'aarch64-linux', 'env': {}, 'inputDrvs': {TOOL: ['out']}},
            TOOL: {'system': 'aarch64-linux', 'env': {'pname': host.PNAME, 'out': OUTPUT},
                   'inputDrvs': {}, 'outputs': {'out': {'path': OUTPUT}}}}


class NixScannerHostTests(unittest.TestCase):
    def test_only_one_reachable_native_scanner_output_is_selected(self):
        self.assertEqual(host.scanner_derivation(graph(), ROOT, 'aarch64-linux'),
                         {'derivation': TOOL, 'output': OUTPUT, 'executable': EXE, 'system': 'aarch64-linux'})
        for kind in ('disconnected', 'missing', 'architecture', 'output', 'duplicate'):
            rows = graph()
            if kind == 'disconnected':
                rows[ROOT]['inputDrvs'] = {}
            elif kind == 'missing':
                del rows[TOOL]
            elif kind == 'architecture':
                rows[TOOL]['system'] = 'x86_64-linux'
            elif kind == 'output':
                rows[TOOL]['outputs']['out']['path'] = OUTPUT + '/escape'
            else:
                duplicate = TOOL.replace('1' * 32, '3' * 32)
                rows[ROOT]['inputDrvs'][duplicate] = ['out']
                rows[duplicate] = copy.deepcopy(rows[TOOL])
            with self.subTest(kind=kind), self.assertRaises(ValueError):
                host.scanner_derivation(rows, ROOT, 'aarch64-linux')

    def test_profile_has_one_literal_attachment_and_no_global_permission_change(self):
        name, source = host.profile_source(EXE)
        self.assertIn(f'profile {name} "{EXE}" flags=(unconfined)', source)
        self.assertEqual(source.count('userns,'), 1)
        for bad in (EXE.replace('1.0.2', '*'), EXE.replace('1.0.2', '?'), '/tmp/opengrep-artifact',
                    EXE.replace(host.PNAME, 'other'), EXE + '\nprofile hostile /** {}'):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                host.profile_source(bad)

    def test_host_authorization_refuses_shared_or_non_github_machines(self):
        with patch.dict(os.environ, {}, clear=True), patch.object(host.subprocess, 'run') as run:
            with self.assertRaises(ValueError):
                host.authorize_scanner(EXE)
            run.assert_not_called()

    def test_authorization_retains_global_restriction_and_exact_profile_identity(self):
        name, source = host.profile_source(EXE)
        native = Path
        policy = SimpleNamespace(exists=lambda: True, read_text=lambda: '1\n')
        profiles = SimpleNamespace(read_text=lambda: name + ' (unconfined)\n')
        class Executable:
            parts = native(EXE).parts
            def lstat(self):
                return SimpleNamespace(st_mode=0o100555, st_uid=0)
            def resolve(self):
                return self
            def open(self, mode):
                import io
                return io.BytesIO(b'immutable native scanner')
        with patch.dict(os.environ, {'GITHUB_ACTIONS': 'true', 'RUNNER_ENVIRONMENT': 'github-hosted'}), \
                patch.object(host.platform, 'system', return_value='Linux'), \
                patch.object(host.platform, 'machine', return_value='aarch64'), \
                patch.object(host, 'Path', side_effect=lambda value: Executable() if value == EXE else native(value)), \
                patch.object(host, 'POLICY', policy), patch.object(host, 'PROFILES', profiles), \
                patch.object(host.subprocess, 'run', return_value=SimpleNamespace(returncode=0)) as run:
            report = host.authorize_scanner(EXE)
            self.assertTrue(report['profile_loaded'])
            self.assertFalse(report['policy_changed'])
            self.assertEqual(report['host_userns_restriction'], '1')
            self.assertEqual(run.call_args.args[0], ['sudo', '-n', 'apparmor_parser', '--replace'])
            self.assertEqual(run.call_args.kwargs['input'], source.encode())
