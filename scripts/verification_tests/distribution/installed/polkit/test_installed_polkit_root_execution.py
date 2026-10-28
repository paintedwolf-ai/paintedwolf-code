"""Root observer custody comes from a captured process, never a public receipt."""
import copy
import json
import stat
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from scripts.linux_release.installed_input import captured_driver, polkit_proxy, polkit_root, polkit_runtime
from verification_tests.distribution.installed.test_installed_native_unlock_execution import ACCOUNT, EXECUTION
from verification_tests.distribution.installed.polkit.test_installed_polkit_stage_execution import target


class RootProxyTests(unittest.TestCase):
    def test_root_process_uses_fixed_empty_environment_and_isolated_no_bytecode_python(self):
        from scripts.linux_release.campaign import guests as installed_campaign
        selected = {'host': '127.0.0.1', 'port': 2222, 'user': 'qa'}
        actor = Mock(nonce=EXECUTION['execution_nonce'], value={'vm_id': 'owned', 'ssh': selected})
        process = Mock(pid=901)
        with patch.object(installed_campaign, 'command', return_value=['fixed-ssh']) as command, patch.object(
                polkit_proxy.subprocess, 'Popen', return_value=process), patch.object(
                polkit_proxy, 'process_identity', return_value={'pid': 901}), patch.object(
                polkit_proxy.selectors, 'DefaultSelector'), patch.object(polkit_proxy.os, 'set_blocking'):
            polkit_proxy.Proxy(actor, selected, EXECUTION, ACCOUNT)
        arguments = command.call_args.args[1]
        self.assertEqual(arguments[:10], ['/usr/bin/sudo', '-n', '/usr/bin/env', '-i',
            'PATH=/usr/bin:/bin', 'LANG=C.UTF-8', 'HOME=/root', '/usr/bin/python3', '-I', '-B'])
        self.assertEqual(arguments[-1], EXECUTION['driver_tree'])

    def proxy(self):
        value = polkit_proxy.Proxy.__new__(polkit_proxy.Proxy)
        value.execution, value.account, value.vm_id = copy.deepcopy(EXECUTION), ACCOUNT, 'owned'
        value.sequence = 0
        value.started, value.bound, value.failed, value.closed = True, True, False, False
        value.deadline = 100
        value.process = Mock(pid=901, poll=Mock(return_value=None))
        value.selector = Mock()
        value.process_birth = {'pid': 901, 'parent_pid': 1, 'start_time_ticks': 42}
        return value

    def test_only_captured_private_process_reply_can_supply_observer_facts(self):
        proxy, pipe = self.proxy(), Mock()
        def reply(request):
            return {**request, 'result': target()['pending']}
        pipe.line.side_effect = reply
        with patch.object(polkit_proxy, 'Pipe', return_value=pipe), patch.object(
                polkit_proxy, 'process_identity', return_value=proxy.process_birth), patch.object(
                polkit_proxy.time, 'monotonic', return_value=1):
            self.assertEqual(proxy.observe(), target()['pending'])
        self.assertEqual(proxy.sequence, 1)
        self.assertEqual(pipe.line.call_args.args[0]['operation'], 'observe')
        self.assertEqual(pipe.line.call_args.args[0]['binding'], EXECUTION)

    def test_uncertain_changed_sequence_or_reused_ssh_process_poison_without_retry(self):
        for kind in ('uncertain', 'sequence', 'birth'):
            proxy, pipe = self.proxy(), Mock()
            pipe.line.side_effect = OSError('uncertain') if kind == 'uncertain' else lambda request: {
                **request, 'sequence': 99, 'result': target()['pending']}
            birth = {**proxy.process_birth, 'start_time_ticks': 43} if kind == 'birth' else proxy.process_birth
            with patch.object(polkit_proxy, 'Pipe', return_value=pipe), patch.object(
                    polkit_proxy, 'process_identity', return_value=birth), patch.object(
                    polkit_proxy.time, 'monotonic', return_value=1):
                with self.subTest(kind=kind), self.assertRaises((OSError, ValueError)):
                    proxy.observe()
                self.assertTrue(proxy.failed)
                self.assertTrue(proxy.closed)
                with self.assertRaises(ValueError):
                    proxy.observe()
            proxy.process.terminate.assert_called_once()
            self.assertLessEqual(pipe.line.call_count, 1)

    def test_bootstrap_refuses_user_writable_capture_before_project_import(self):
        fake = SimpleNamespace(parents=[], lstat=lambda: SimpleNamespace(
            st_mode=0o40777, st_uid=1000, st_gid=1000))
        class FakePath:
            def __init__(self, *_):
                pass
            def __truediv__(self, _):
                return fake
        for bootstrap in (captured_driver.OBSERVER_BOOTSTRAP, captured_driver.ACCOUNT_BOOTSTRAP):
            with patch('pathlib.Path', FakePath), patch('sys.argv', ['fixed', 'c' * 40]):
                with self.assertRaises(SystemExit) as refused:
                    exec(bootstrap, {})
            self.assertEqual(refused.exception.code, 2)

    def test_unhashed_importable_files_or_bytecode_directory_cannot_enter_root_capture(self):
        class Entry:
            def __init__(self, name, mode=stat.S_IFREG | 0o444):
                self.name, self.mode = name, mode
                self.suffix = '.' + name.rpartition('.')[2]
                self.parents = []
            def lstat(self):
                return SimpleNamespace(st_mode=self.mode, st_uid=0, st_gid=0, st_size=100)
            def read_bytes(self):
                return json.dumps({'format_version': 1, 'driver_tree': 'c' * 40, 'files_sha256': {}}).encode()
            def __truediv__(self, name):
                return marker if name == 'capture.json' else root
        marker = Entry('capture.json')
        root = Entry('root', stat.S_IFDIR | 0o555)
        for name, mode in [('module.pyc', stat.S_IFREG | 0o444), ('module.so', stat.S_IFREG | 0o444),
                           ('extra.txt', stat.S_IFREG | 0o444), ('__pycache__', stat.S_IFDIR | 0o555)]:
            root.rglob = lambda _: [Entry(name, mode)]
            for bootstrap in (captured_driver.OBSERVER_BOOTSTRAP, captured_driver.ACCOUNT_BOOTSTRAP):
                with patch('pathlib.Path', return_value=root), patch('sys.argv', ['fixed', 'c' * 40]):
                    with self.subTest(name=name), self.assertRaises(SystemExit) as refused:
                        exec(bootstrap, {})
                self.assertEqual(refused.exception.code, 2)


class RootEndpointTests(unittest.TestCase):
    def endpoint(self):
        result = polkit_root.Endpoint.__new__(polkit_root.Endpoint)
        result.identity = {'binding': EXECUTION, 'vm_id': 'owned', 'account': ACCOUNT}
        result.sequence, result.deadline, result.bound = 0, 100, True
        result.monitor = Mock(observe=Mock(return_value=target()['pending']))
        result._current = Mock(return_value={})
        result.current_session = target()['session']
        return result

    def test_closed_identity_and_sequence_are_consumed_before_observation(self):
        endpoint = self.endpoint()
        request = {'format_version': 1, **endpoint.identity, 'sequence': 0, 'operation': 'observe'}
        with patch.object(polkit_root.time, 'monotonic', return_value=1):
            self.assertEqual(endpoint.handle(request)['result'], target()['pending'])
            with self.assertRaises(ValueError):
                endpoint.handle(request)
        endpoint.monitor.observe.assert_called_once()
        self.assertEqual(endpoint.sequence, 1)

    def test_foreign_account_nonce_or_unbounded_operation_cannot_query_monitor(self):
        for kind in ('account', 'nonce', 'operation', 'extra'):
            endpoint = self.endpoint()
            request = copy.deepcopy({'format_version': 1, **endpoint.identity,
                                     'sequence': 0, 'operation': 'observe'})
            if kind == 'account':
                request['account']['uid'] += 1
            elif kind == 'nonce':
                request['binding']['execution_nonce'] = 'e' * 32
            elif kind == 'operation':
                request['operation'] = 'arbitrary-command'
            else:
                request['command'] = 'private prose'
            with patch.object(polkit_root.time, 'monotonic', return_value=1):
                with self.subTest(kind=kind), self.assertRaises(ValueError):
                    endpoint.handle(request)
            endpoint.monitor.observe.assert_not_called()

    def test_read_only_observation_survives_lock_but_unlocked_start_is_separate(self):
        endpoint = polkit_root.Endpoint.__new__(polkit_root.Endpoint)
        endpoint.deadline = 100
        endpoint.initial = target()['session']
        locked = copy.deepcopy(endpoint.initial)
        locked.update(locked_hint=True, locker_active=True)
        endpoint.native = Mock(observe=Mock(return_value=locked))
        endpoint.runtime = Mock(observe=Mock(return_value={'retained': True}))
        self.assertEqual(endpoint._current(), {'retained': True})
        self.assertTrue(endpoint.current_session['locked_hint'])
        with self.assertRaises(ValueError):
            endpoint._current(unlocked=True)
        endpoint.native.observe.assert_called_with(deadline=100)


class RootRuntimeTests(unittest.TestCase):
    def test_image_hash_binding_is_once_but_kernel_birth_and_image_metadata_are_continuous(self):
        launch = {'pid': 401, 'parent_pid': 1, 'start_time_ticks': 71}
        engine = {'pid': 402, 'parent_pid': 401, 'start_time_ticks': 72}
        image = {'device': 1, 'inode': 2, 'bytes': 3}
        binding = {'install_root': '/opt/painted-wolf-code', 'images': {
            '/opt/painted-wolf-code/bin/painted-wolf-code': image,
            '/opt/painted-wolf-code/bin/pw': image}}
        def owner(pid, uid):
            return {**(launch if pid == 401 else engine), 'uid': uid}
        with patch.object(polkit_runtime, 'controlled_image', return_value=(1, 2, 3)), patch.object(
                polkit_runtime, 'unchanged') as hashed, patch.object(polkit_runtime, 'kernel_owner', side_effect=owner), patch.object(
                polkit_runtime, 'engine_image', return_value=(1, 2, 3)), patch.object(
                polkit_runtime, 'process_identity', return_value=launch), patch.object(
                polkit_runtime, 'descends', return_value=[engine]):
            runtime = polkit_runtime.Runtime({'binding': binding, 'launch': launch, 'native': launch, 'engine': engine}, ACCOUNT)
            runtime.observe()
            self.assertEqual(hashed.call_count, 2)
            with patch.object(polkit_runtime, 'engine_image', return_value=(1, 9, 3)):
                with self.assertRaises(ValueError):
                    runtime.observe()


if __name__ == '__main__':
    unittest.main()
