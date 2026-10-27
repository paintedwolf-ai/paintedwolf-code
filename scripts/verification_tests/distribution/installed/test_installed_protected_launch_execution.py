"""Protected-launch attestation and descendant cleanup decide from kernel facts."""
import copy
import json
from pathlib import Path
import stat
import struct
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release.campaign import execution as installed_execution
from linux_release.installed_cases import descendant_cleanup, process_tree, protected_launch, security

INITIAL = {'user': [4, 1], 'mnt': [4, 2], 'pid': [4, 3], 'net': [4, 4]}
PROTECTED = {'user': [4, 50], 'mnt': [4, 51], 'pid': [4, 3], 'net': [4, 4]}
DESKTOP, ENGINE = [9, 100, 1000], [9, 101, 2000]
WEBKIT = '/usr/lib/x86_64-linux-gnu/webkit2gtk-4.1/WebKitWebProcess'
ROOTS = ['/opt/painted-wolf-code', WEBKIT, '/usr/lib/x86_64-linux-gnu', '/lib64/ld-linux-x86-64.so.2']


def row(pid, parent, exe, image, namespaces, **changes):
    value = {'identity': {'pid': pid, 'parent_pid': parent, 'start_time_ticks': pid * 10}, 'exe': exe, 'image': image,
             'namespaces': copy.deepcopy(namespaces), 'user_namespace_owner': 0, 'uid_map': process_tree.FULL_MAP,
             'gid_map': process_tree.FULL_MAP, 'uids': [1000] * 4, 'no_new_privs': 1, 'seccomp': 0,
             'capabilities': {name: 0 for name in process_tree.CAPABILITIES}}
    value.update(changes)
    return value


def launch_tree():
    mounts = [['/', 'rw,nosuid,noexec'], ['/opt/painted-wolf-code', 'ro,nosuid'], [WEBKIT, 'ro'],
              ['/usr/lib/x86_64-linux-gnu', 'ro'], ['/home', 'rw,noexec']]
    return {'root': {'pid': 20, 'parent_pid': 1, 'start_time_ticks': 200}, 'initial': {'namespaces': INITIAL, 'user_namespace_owner': 0},
            'processes': [row(20, 1, str(protected_launch.BOOTSTRAP), [9, 99, 10], INITIAL, user_namespace_owner=0, uid_map=[[0, 0, 4294967295]], no_new_privs=0),
                          row(21, 20, '/opt/painted-wolf-code/bin/pw-launch-entry', [9, 98, 10], INITIAL, no_new_privs=0),
                          row(22, 20, '/opt/painted-wolf-code/bin/painted-wolf-code', DESKTOP, PROTECTED, mounts=mounts),
                          row(23, 22, '/opt/painted-wolf-code/bin/pw', ENGINE, PROTECTED),
                          row(24, 22, WEBKIT, [9, 300, 30], PROTECTED),
                          row(25, 23, '/opt/painted-wolf-code/bin/pw', ENGINE, PROTECTED)]}


def attest(document, memory=None):
    memory = memory or {pid: 'EACCES' for pid in (22, 23, 24, 25)}
    return protected_launch.attest(document, engine=23, desktop=DESKTOP, engine_image=ENGINE, system=[WEBKIT],
                                   exec_roots=ROOTS, memory=memory, uid=1000)


class ProtectedLaunchTests(unittest.TestCase):
    def test_sealed_launch_passes_every_property(self):
        result = attest(launch_tree())
        self.assertTrue(result['passed'], result['checks'])
        self.assertEqual(result['protected_namespace'], PROTECTED['user'])
        self.assertEqual(result['webkit'], [WEBKIT])

    def test_each_property_fails_from_its_own_kernel_fact(self):
        def at(pid):
            return lambda d: next(r for r in d['processes'] if r['identity']['pid'] == pid)
        mutations = {
            'memory-open-refused': (None, {22: 'EACCES', 23: 'EPERM', 24: 'EACCES', 25: 'EACCES'}),
            'no-root-process': (lambda d: at(21)(d).update(uids=[1000, 0, 0, 1000]), None),
            'shared-protected-namespace': (lambda d: at(24)(d)['namespaces'].update(user=[4, 77]), None),
            'namespace-owned-by-root': (lambda d: at(23)(d).update(user_namespace_owner=1000), None),
            'full-identity-maps': (lambda d: at(24)(d).update(gid_map=[[0, 0, 65536]]), None),
            'no-new-privileges': (lambda d: at(22)(d).update(no_new_privs=0), None),
            'no-capabilities': (lambda d: at(23)(d)['capabilities'].update(CapPrm=1), None),
            'caller-identity': (lambda d: at(22)(d).update(uids=[1001] * 4), None),
            'only-closure-inside': (lambda d: d['processes'].append(row(26, 22, '/usr/bin/xdg-open', [9, 400, 1], PROTECTED)), None),
            'execution-only-from-closure': (lambda d: at(22)(d)['mounts'].append(['/home/user/.local', 'rw']), None),
        }
        for check, (mutate, memory) in mutations.items():
            document = copy.deepcopy(launch_tree())
            if mutate:
                mutate(document)
            with self.subTest(check=check):
                result = attest(document, memory)
                self.assertFalse(result['passed'])
                self.assertEqual([name for name, held in result['checks'].items() if not held], [check])
        document = copy.deepcopy(launch_tree())
        document['processes'][2]['namespaces']['user'] = INITIAL['user']
        for process in document['processes'][3:]:
            process['namespaces']['user'] = INITIAL['user']
        self.assertFalse(attest(document)['checks']['shared-protected-namespace'])

    def test_missing_participants_stop_before_namespace_judgement(self):
        for pid, check in ((22, 'desktop-observed'), (23, 'engine-observed'), (24, 'webkit-observed')):
            document = copy.deepcopy(launch_tree())
            document['processes'] = [r for r in document['processes'] if r['identity']['pid'] != pid]
            with self.subTest(check=check):
                result = attest(document)
                self.assertFalse(result['passed'])
                self.assertFalse(result['checks'][check])
                self.assertNotIn('memory-open-refused', result['checks'])
        document = copy.deepcopy(launch_tree())
        document['processes'][2].pop('mounts')
        self.assertFalse(attest(document)['checks']['execution-only-from-closure'])

    def test_not_applicable_candidates_report_typed_unverified_reasons(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binding = {'install_root': str(root), 'images': {}}
            self.assertEqual(protected_launch.applicability(binding), (None, 'protected-launch-entry-not-in-manifest'))
            binding['images'][str(root / protected_launch.ENTRY)] = {'mode': 0o4755}
            with self.assertRaises(ValueError):
                protected_launch.applicability(binding)
            binding['images'][str(root / protected_launch.ENTRY)] = {'mode': 0o755}
            with patch.object(protected_launch, 'read_policy', side_effect=FileNotFoundError()):
                self.assertEqual(protected_launch.applicability(binding), (None, 'protected-launch-policy-absent'))
                self.assertEqual(protected_launch.protected_launch(binding),
                                 ('unverified', {'reason': 'protected-launch-policy-absent', 'applicability': 'not-applicable'}, {}))
            with patch.object(protected_launch, 'read_policy', return_value={}):
                self.assertEqual(protected_launch.applicability(binding), (None, 'protected-launch-bootstrap-absent'))
                identity = {'sha256': 'a' * 64, 'bytes': 4, 'mode': 0o4755, 'device': 1, 'inode': 2}
                binding['images'][str(protected_launch.BOOTSTRAP)] = dict(identity)
                with patch.object(Path, 'lstat', side_effect=FileNotFoundError()):
                    self.assertEqual(protected_launch.applicability(binding), (None, 'protected-launch-bootstrap-absent'))
                for bad in (SimpleNamespace(st_mode=stat.S_IFREG | 0o755, st_uid=0, st_gid=0),
                            SimpleNamespace(st_mode=stat.S_IFREG | 0o4755, st_uid=1000, st_gid=0),
                            SimpleNamespace(st_mode=stat.S_IFREG | 0o4755, st_uid=0, st_gid=1000)):
                    with patch.object(Path, 'lstat', return_value=bad), self.assertRaises(ValueError):
                        protected_launch.applicability(binding)
                setuid = SimpleNamespace(st_mode=stat.S_IFREG | 0o4755, st_uid=0, st_gid=0)
                with patch.object(Path, 'lstat', return_value=setuid), \
                        patch.object(protected_launch, 'file_identity', return_value={**identity, 'sha256': 'b' * 64, 'header': b''}), \
                        self.assertRaises(ValueError):
                    protected_launch.applicability(binding)
                with patch.object(Path, 'lstat', return_value=setuid), \
                        patch.object(protected_launch, 'file_identity', return_value={**identity, 'header': b''}):
                    selected, reason = protected_launch.applicability(binding)
                self.assertIsNone(reason)
                self.assertEqual(protected_launch.BOOTSTRAP, Path('/usr/libexec/painted-wolf-code/pw-launch'))
                self.assertEqual(selected['bootstrap'], {'path': str(protected_launch.BOOTSTRAP), **identity})

    def test_not_applicable_is_retained_as_unverified_never_skipped(self):
        with patch.object(security, 'require_owned_guest'), patch.object(security, 'retain', side_effect=lambda *args: args), \
                patch.object(protected_launch, 'applicability', return_value=(None, 'protected-launch-entry-not-in-manifest')):
            result = security.execute({'case_inputs': {}}, 'vault', 'protected-launch-attestation', Path('/unused'))
        self.assertEqual(result[4], 'unverified')
        self.assertEqual(result[5], {'reason': 'protected-launch-entry-not-in-manifest', 'applicability': 'not-applicable'})

    def test_policy_must_be_root_controlled_and_name_absolute_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            policy = Path(directory) / 'launch.json'
            policy.write_text(json.dumps({'system_executables': [WEBKIT], 'library_roots': ['/usr/lib']}))
            with self.assertRaises(ValueError):
                protected_launch.read_policy(policy)
            with patch.object(protected_launch, 'trusted', return_value=True):
                self.assertEqual(protected_launch.read_policy(policy)['system_executables'], [WEBKIT])
                for content in ({'system_executables': ['relative/WebKitWebProcess']}, {'library_roots': '/usr/lib'}, []):
                    policy.write_text(json.dumps(content))
                    with self.subTest(content=content), self.assertRaises(ValueError):
                        protected_launch.read_policy(policy)

    def test_interpreter_reads_pt_interp_and_closure_roots_bound_paths(self):
        path = b'/lib64/ld-linux-x86-64.so.2\0'
        header = bytearray(64)
        header[:6] = b'\x7fELF\x02\x01'
        struct.pack_into('<Q', header, 32, 64)
        struct.pack_into('<HH', header, 54, 56, 1)
        program = struct.pack('<IIQQQQQQ', 3, 4, 120, 0, 0, len(path), len(path), 1)
        with tempfile.TemporaryDirectory() as directory:
            image = Path(directory) / 'desktop'
            image.write_bytes(bytes(header) + program + path)
            self.assertEqual(protected_launch.interpreter(image), '/lib64/ld-linux-x86-64.so.2')
            image.write_bytes(b'#!/bin/sh\n')
            with self.assertRaises(ValueError):
                protected_launch.interpreter(image)
        self.assertTrue(protected_launch.within('/usr/lib/x86_64-linux-gnu/gio', ['/usr/lib']))
        self.assertFalse(protected_launch.within('/usr/libexec', ['/usr/lib']))


class LaunchAncestryTests(unittest.TestCase):
    def test_engine_may_descend_from_the_captured_launch_through_its_desktop(self):
        from linux_release.installed_cases import app
        chain = {22: {'pid': 22, 'parent_pid': 20, 'start_time_ticks': 1},
                 30: {'pid': 30, 'parent_pid': 1, 'start_time_ticks': 1}}
        with patch.object(app, 'process_identity', side_effect=lambda pid: chain[pid]):
            self.assertTrue(app.descends({'pid': 23, 'parent_pid': 20, 'start_time_ticks': 1}, 20))
            self.assertTrue(app.descends({'pid': 23, 'parent_pid': 22, 'start_time_ticks': 1}, 20))
            self.assertFalse(app.descends({'pid': 23, 'parent_pid': 30, 'start_time_ticks': 1}, 20))


class DescendantCleanupTests(unittest.TestCase):
    def test_cleanup_waits_for_every_captured_member(self):
        members = [{'pid': 5, 'start_ticks': '1'}, {'pid': 6, 'start_ticks': '2'}]
        with patch.object(descendant_cleanup, 'state', side_effect=lambda row: None):
            self.assertTrue(descendant_cleanup.gone(members, limit=1))
        with patch.object(descendant_cleanup, 'state', side_effect=lambda row: 'S' if row['pid'] == 6 else None), \
                patch.object(descendant_cleanup.time, 'sleep'):
            self.assertFalse(descendant_cleanup.gone(members, limit=0.05))

    def test_terminal_close_follows_capture_of_the_live_daemon(self):
        provider = descendant_cleanup.CloseProvider('python3 lifecycle.py detached')
        provider.workspace = Path('/unused')
        self.assertEqual(provider.step({})[1], 'terminal_open')
        opened = json.dumps({'id': 'terminal-1', 'confined': True, 'running': True})
        members = [{'pid': 5, 'start_ticks': '1'}, {'pid': 6, 'start_ticks': '2'}]
        with patch.object(descendant_cleanup, 'witness', return_value={'namespace': 9}), \
                patch.object(descendant_cleanup, 'capture', return_value=members) as captured:
            self.assertEqual(provider.step({'terminal-fixture-open': opened}),
                             ('terminal-fixture-close', 'terminal_close', {'id': 'terminal-1'}))
            provider.step({'terminal-fixture-open': opened})
        captured.assert_called_once_with({'namespace': 9})
        self.assertEqual(provider.members, members)
        closed = json.dumps({'id': 'terminal-1', 'confined': True, 'closed': True, 'exit_code': -9})
        self.assertIsNone(provider.step({'terminal-fixture-open': opened, 'terminal-fixture-close': closed}))
        with self.assertRaises(ValueError):
            provider.step({'terminal-fixture-open': opened, 'terminal-fixture-close': json.dumps(
                {'id': 'terminal-1', 'confined': True, 'closed': False})})

    def test_witness_refuses_oversized_or_absent_observations(self):
        with tempfile.TemporaryDirectory() as directory:
            workspace = Path(directory)
            with patch.object(descendant_cleanup.time, 'sleep'), self.assertRaises(ValueError):
                descendant_cleanup.witness(workspace, limit=0.01)
            (workspace / descendant_cleanup.OBSERVATION).write_bytes(b' ' * 4097)
            with self.assertRaises(ValueError):
                descendant_cleanup.witness(workspace)
            (workspace / descendant_cleanup.OBSERVATION).write_text('{"members": []}')
            self.assertEqual(descendant_cleanup.witness(workspace), {'members': []})


class RegistrationTests(unittest.TestCase):
    CASES = {('confinement', 'helper-family-boundaries'), ('confinement', 'helper-suite-and-typed-refusal'),
             ('vault', 'protected-launch-attestation'), ('processes', 'cancel-abandoned-call'),
             ('processes', 'terminal-close-descendants')}

    def test_new_cases_are_declared_and_dispatch_to_security_probes(self):
        catalog = json.loads((Path(__file__).resolve().parents[4] / 'packaging/linux/support-matrix.json').read_text())
        declared = {(row['id'], case) for row in catalog['capabilities'] for case in row['cases']}
        self.assertLessEqual(self.CASES, declared)
        self.assertLessEqual(self.CASES, set(security.PROBES))
        for capability, case in self.CASES:
            for installation in ('deb', 'rpm', 'pacman', 'nixos-system', 'nix-profile'):
                with self.subTest(case=case, installation=installation):
                    self.assertEqual(installed_execution.case_family(capability, case, installation), 'security')


if __name__ == '__main__':
    unittest.main()
