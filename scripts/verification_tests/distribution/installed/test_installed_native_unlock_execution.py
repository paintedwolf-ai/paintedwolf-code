"""OS input requires native shell custody, never an application frame or delivery count."""
import copy
import threading
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_input import unlock_actor, unlock_wire
from verification_tests.distribution.installed.test_installed_session_lock_execution import native_proof

EXECUTION = {'format_version': 1, 'execution_nonce': 'a' * 32, 'source_commit': 'b' * 40,
    'driver_tree': 'c' * 40, 'artifact_id': 'app', 'environment_id': 'noble', 'variant_id': 'gnome-wayland'}
ACCOUNT = {'name': 'pwqual058a1', 'uid': 30008, 'gid': 30008, 'home': '/home/pwqual058a1'}


def target(password=True):
    session = native_proof()
    session['native_session'].update(locked_hint=True, locker_active=True)
    session['native_session']['session']['User'] = '30008'
    session['native_session']['lifecycle']['user'] = [30008, '/org/freedesktop/login1/user/_30008']
    session['native_session']['authorities']['locker']['process']['uid'] = 30008
    window = {'bus_name': ':1.90', 'path': '/org/a11y/atspi/accessible/3', 'pid': 200}
    control = {**window, 'path': '/org/a11y/atspi/accessible/4'}
    return {'session': session, 'window': window, 'password': control if password else None,
        'ancestors': [control, window] if password else [],
        'window_state': {'active': True, 'focused': not password, 'visible': True,
                         'enabled': True, 'sensitive': True, 'defunct': False},
        'password_state': {'focused': True, 'visible': True, 'enabled': True,
                           'sensitive': True, 'defunct': False} if password else None}


class UnlockWireTests(unittest.TestCase):
    def test_native_accessibility_bus_is_distinct_from_session_bus(self):
        value = target()
        self.assertEqual(unlock_wire.locked(value, password=True), value)
        self.assertNotEqual(value['window']['bus_name'],
            value['session']['native_session']['authorities']['shell']['unique_owner'])

    def test_foreign_unfocused_replaced_and_partial_lock_targets_refuse(self):
        for kind in ('pid', 'bus', 'path', 'focus', 'defunct', 'flag', 'chain'):
            value = target()
            if kind == 'pid':
                value['password']['pid'] += 1
            elif kind == 'bus':
                value['password']['bus_name'] = ':1.91'
            elif kind == 'path':
                value['password']['path'] = 'not-an-object'
            elif kind == 'focus':
                value['password_state']['focused'] = False
            elif kind == 'defunct':
                value['window_state']['defunct'] = True
            elif kind == 'flag':
                value['session']['native_session']['locker_active'] = False
            else:
                value['ancestors'] = list(reversed(value['ancestors']))
            with self.subTest(kind=kind), self.assertRaises(ValueError):
                unlock_wire.locked(value, password=True)
        with self.assertRaises(ValueError):
            unlock_wire.locked(target(False), password=True)

    def test_clock_requires_positive_focus_and_never_claims_password(self):
        value = target(False)
        self.assertEqual(unlock_wire.locked(value, password=False), value)
        value['window_state']['focused'] = False
        with self.assertRaises(ValueError):
            unlock_wire.locked(value, password=False)


class UnlockActorTests(unittest.TestCase):
    def actor(self):
        actor = Mock(nonce=EXECUTION['execution_nonce'], value={'vm_id': 'owned'}, current=None, failed=False, closed=False,
                     transactions=set(), lock=threading.Lock())
        return actor, unlock_actor.NativeUnlockActor(actor, EXECUTION, ACCOUNT, b'A1' * 20)

    def perform(self, private, pipe):
        with patch.object(unlock_actor, 'Pipe', return_value=pipe) as transport:
            pending = {'format_version': 1, 'binding': EXECUTION, 'transaction_id': 'd' * 32,
                'operation': unlock_wire.PURPOSE, 'native_session': target()['session'], 'account': ACCOUNT}
            result = private.perform('captured-process', 'owned-selector', 123, pending)
        transport.assert_called_once_with('captured-process', 'owned-selector', 123)
        return result

    def pipe(self, *, initial=None, replaced_at=None):
        pipe = Mock()
        def receive():
            pending = pipe.send_metadata.call_args.args[0]
            if pending['phase'] == 'prompt':
                return {key: value for key, value in pending.items() if key != 'phase'} | {'target': initial or target()}
            if pending['phase'] == 'observe':
                observed = target()
                if pending['ordinal'] == replaced_at:
                    observed['password']['path'] += '_replaced'
                return {key: value for key, value in pending.items() if key != 'phase'} | {'target': observed}
            if pipe.receive_metadata.call_count == 44:
                final = copy.deepcopy(target()['session'])
                final['native_session'].update(locked_hint=False, locker_active=False)
                return {**pending, 'phase': 'unlocked', 'session_after': final}
            return {**pending, 'phase': 'delivered'}
        pipe.receive_metadata.side_effect = receive
        return pipe

    def test_fixed_value_only_once_with_native_target_before_every_chord(self):
        actor, private = self.actor()
        pipe = self.pipe()
        result = self.perform(private, pipe)
        self.assertTrue(result['delivered'])
        self.assertFalse(result['session_after']['native_session']['locked_hint'])
        self.assertEqual(actor._deliver.call_count, 41)
        self.assertEqual(actor._deliver.call_args_list[0].args, (('shift', 'a'),))
        self.assertEqual(actor._deliver.call_args_list[-1].args, (('ret',),))
        self.assertEqual(private.secret, bytearray(40))
        self.assertNotIn('A1' * 20, repr(pipe.send_metadata.call_args_list) + repr(result))
        with self.assertRaises(ValueError):
            self.perform(private, pipe)
        self.assertEqual(actor._deliver.call_count, 41)

    def test_nonsecret_reveal_precedes_actual_password_proof(self):
        actor, private = self.actor()
        self.perform(private, self.pipe(initial=target(False)))
        self.assertEqual(actor._deliver.call_args_list[0].args, (('ret',),))
        self.assertEqual(actor._deliver.call_count, 42)

    def test_replacement_or_uncertain_delivery_poison_without_replay(self):
        actor, private = self.actor()
        with self.assertRaises(ValueError):
            self.perform(private, self.pipe(replaced_at=1))
        self.assertEqual(actor._deliver.call_count, 1)
        self.assertTrue(actor.failed)
        self.assertEqual(private.secret, bytearray(40))
        actor, private = self.actor()
        actor._deliver.side_effect = OSError('uncertain delivery')
        with self.assertRaises(OSError):
            self.perform(private, self.pipe())
        self.assertTrue(actor.failed)
        self.assertEqual(private.secret, bytearray(40))

    def test_no_overlap_or_foreign_execution(self):
        actor, private = self.actor()
        actor.current, actor.operation, actor.ordinal = 'd' * 32, 'session-lock-v1', 0
        with self.assertRaises(ValueError):
            self.perform(private, self.pipe())
        actor._deliver.assert_not_called()
        with self.assertRaises(ValueError):
            unlock_actor.NativeUnlockActor(actor, {**EXECUTION, 'execution_nonce': 'e' * 32}, ACCOUNT, b'A1' * 20)


class NativeUnlockObservationTests(unittest.TestCase):
    def observe(self, *, clock=False, clock_child=False, foreign=False, focus=True, replaced=False):
        from contextlib import nullcontext
        from types import SimpleNamespace
        from linux_release.installed_input import native_unlock
        api = SimpleNamespace(ROLE_WINDOW=1, ROLE_PASSWORD_TEXT=2, STATE_DEFUNCT=3,
            STATE_SHOWING=4, STATE_VISIBLE=5, STATE_ENABLED=6, STATE_SENSITIVE=7,
            STATE_ACTIVE=8, STATE_FOCUSED=9)
        def node(path, role, states, parent=None, children=(), pid=200):
            return SimpleNamespace(app=SimpleNamespace(bus_name=':1.90'), path=path,
                get_process_id=Mock(return_value=pid), get_parent=Mock(return_value=parent),
                getRole=Mock(return_value=role), getState=Mock(return_value=SimpleNamespace(contains=lambda key: key in states)),
                childCount=len(children), children=list(children))
        class Indexed:
            def __init__(self, value):
                self.__dict__.update(vars(value))
            def __getitem__(self, index):
                return self.children[index]
        window = Indexed(node('/window', 1, {4, 5, 6, 7, 8} | ({9} if clock and not clock_child else set())))
        hidden = Indexed(node('/hidden', 0, {5}, window))
        hidden.childCount = 9999
        entry = Indexed(node('/password', 2, {4, 5, 6, 7} | ({9} if focus else set()), window))
        shield = Indexed(node('/shield', 1, {4, 5, 6, 7} | ({9} if focus else set()), window))
        if not clock:
            window.children, window.childCount = [hidden, entry], 2
        elif clock_child:
            window.children, window.childCount = [hidden, shield], 2
        else:
            window.children, window.childCount = [hidden], 1
        app = Indexed(node('/application', 0, set(), children=[window], pid=201 if foreign else 200))
        desktop = Indexed(node('/desktop', 0, set(), children=[app] if foreign else [None, app], pid=100))
        api.Registry = SimpleNamespace(getDesktop=lambda _: desktop)
        locked = native_proof()
        locked['native_session'].update(locked_hint=True, locker_active=True)
        lock = SimpleNamespace(session={}, initial=native_proof(), native=Mock())
        lock.native.observe.return_value = locked['native_session']
        if replaced:
            entry.get_process_id.side_effect = [200, 200, 200, 999]
        with patch.object(native_unlock, 'accessibility_bus', return_value=nullcontext()), \
                patch.object(native_unlock, 'load_api', return_value=api):
            with native_unlock.NativeUnlockObserver(lock) as observer:
                return observer.observe()

    def test_actual_shell_password_object_and_native_ancestor_chain_are_retained(self):
        observed = self.observe()
        self.assertEqual(observed['password']['path'], '/password')
        self.assertEqual([row['path'] for row in observed['ancestors']], ['/password', '/window'])
        self.assertTrue(observed['password_state']['focused'])
        self.assertNotIn('name', observed['password'])
        self.assertIsNone(self.observe(clock=True)['password'])
        self.assertIsNone(self.observe(clock=True, clock_child=True)['password'])

    def test_foreign_shell_missing_focus_and_replaced_accessible_refuse(self):
        from linux_release.installed_cases.records import CaseUnavailable
        with self.assertRaises(CaseUnavailable):
            self.observe(foreign=True)
        with self.assertRaises(CaseUnavailable):
            self.observe(focus=False)
        with self.assertRaises(ValueError):
            self.observe(replaced=True)


class UnlockAccountAndBridgeTests(unittest.TestCase):
    def test_secret_frame_and_account_validation_refuse_pwtest_and_055_accounts(self):
        import io
        from linux_release.installed_input import unlock_account
        secret_bytes = b'Secret058CredentialValue0123456789ABCDEF'
        framed = unlock_wire.secret_frame(secret_bytes)
        self.assertEqual(len(framed), 44)
        parsed = unlock_wire.read_secret_frame(io.BytesIO(framed))
        self.assertEqual(bytes(parsed), secret_bytes)
        for bad in (b'short', b'!' * 40, b'A' * 41):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                unlock_wire.read_secret_frame(io.BytesIO(bad))
        self.assertEqual(unlock_account.validate_account_shape(ACCOUNT), ACCOUNT)
        account_059 = {'name': 'pwqual059a1', 'uid': 30009, 'gid': 30009, 'home': '/home/pwqual059a1'}
        self.assertEqual(unlock_account.validate_account_shape(account_059), account_059)
        for bad_account in (
                {'name': 'pwtest', 'uid': 1000, 'gid': 1000, 'home': '/home/pwtest'},
                {'name': 'root', 'uid': 0, 'gid': 0, 'home': '/root'},
                {'name': 'pwqual055a1', 'uid': 30005, 'gid': 30005, 'home': '/home/pwqual055a1'},
                {**ACCOUNT, 'uid': 1000, 'gid': 1000},
                {**ACCOUNT, 'gid': 30009},
                {**ACCOUNT, 'home': '/tmp/pwqual058a1'}):
            with self.subTest(account=bad_account), self.assertRaises(ValueError):
                unlock_account.validate_account_shape(bad_account)
        import os
        from pathlib import Path
        import tempfile
        with tempfile.TemporaryDirectory() as tmp:
            home_dir = (Path(tmp) / 'pwqual058a1').resolve()
            home_dir.mkdir(mode=0o750)
            home_dir.chmod(0o750)
            unlock_account._verify_directory(home_dir, uid=os.getuid(), gid=os.getgid(), mode=(0o750, 0o755))
            home_dir.chmod(0o755)
            unlock_account._verify_directory(home_dir, uid=os.getuid(), gid=os.getgid(), mode=(0o750, 0o755))
            home_dir.chmod(0o775)
            with self.assertRaises(ValueError):
                unlock_account._verify_directory(home_dir, uid=os.getuid(), gid=os.getgid(), mode=(0o750, 0o755))

    def test_prepare_and_disable_account_enforce_shadow_state_without_exposing_secret_or_hash(self):
        import io
        from pathlib import Path
        import tempfile
        from linux_release.installed_input import unlock_account
        secret_bytes = b'TopSecret058PasswordValue0123456789abcde'
        with tempfile.TemporaryDirectory() as tmp:
            shadow = Path(tmp).resolve() / 'shadow'
            shadow.write_bytes(b'pwqual058a1:$6$rounds=5000$saltsalt$hasheddigestvalue:19000:0:99999:7:::\n')
            shadow.chmod(0o600)
            captured_inputs = []
            def fake_command(args, *, timeout=300, limit=8 << 20, environment=None, input_bytes=None):
                captured_inputs.append((list(args), bytes(input_bytes) if input_bytes is not None else None))
                return 0, b'', b''
            info = shadow.lstat()
            root_stat = type('Stat', (), {
                name: getattr(info, name) for name in ('st_dev', 'st_ino', 'st_size', 'st_mtime_ns', 'st_ctime_ns')
            } | {'st_uid': 0, 'st_gid': 0, 'st_mode': 0o100600})()
            with patch.object(unlock_account, 'validate_account', return_value=ACCOUNT), \
                    patch.object(unlock_account, '_verify_root_binary'), \
                    patch.object(unlock_account.os, 'fstat', return_value=root_stat), \
                    patch.object(unlock_account.os, 'geteuid', return_value=0), \
                    patch.object(unlock_account.native_command, 'command', side_effect=fake_command):
                prep = unlock_account.prepare_account(
                    ACCOUNT, descriptor=io.BytesIO(unlock_wire.secret_frame(secret_bytes)), shadow_path=shadow)
                self.assertEqual(prep, {'prepared': True, 'account': ACCOUNT, 'credential_state': 'active'})
                self.assertEqual(captured_inputs[0][0], ['/usr/sbin/chpasswd', '-c', 'SHA512'])
                self.assertEqual(captured_inputs[0][1], b'pwqual058a1:' + secret_bytes + b'\n')
                self.assertNotIn(secret_bytes.decode('ascii'), repr(prep))
                self.assertNotIn('hasheddigestvalue', repr(prep))

                shadow.write_bytes(b'pwqual058a1:!$6$rounds=5000$saltsalt$hasheddigestvalue:19000:0:99999:7:::\n')
                disabled = unlock_account.disable_account(ACCOUNT, terminate_sessions=True, shadow_path=shadow)
                self.assertEqual(disabled, {
                    'disabled': True, 'account': ACCOUNT, 'shadow_locked': True,
                    'credential_state': 'locked', 'sessions_terminated': True})
                self.assertNotIn('hasheddigestvalue', repr(disabled))

    def test_privileged_helper_isolated_python_command_resolves_checkout_module_in_real_subprocess(self):
        from linux_release.installed_cases import native_command
        from linux_release.installed_input import unlock_account
        args = unlock_account.helper_python_arguments('disable', ACCOUNT, vm_id='owned', terminate_sessions=True)
        self.assertEqual(args[1:3], ['-I', '-c'])
        rc, stdout, stderr = native_command.command(args, timeout=10, limit=4096)
        self.assertNotEqual(rc, 0)
        self.assertEqual(stdout, b'')
        self.assertNotIn(b'ModuleNotFoundError', stderr)
        self.assertIn(b'dedicated QA account custody requires root execution', stderr)

    def test_controller_bridge_and_verify_deliveries_bracket_session_lock_with_prepare_and_unlock(self):
        import json
        from pathlib import Path
        import tempfile
        from linux_release.installed_input import controller, protocol
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            leaf_dir = root / 'leaf/evidence/session/inhibitor-lifetime'
            leaf_dir.mkdir(parents=True)
            unlocked = copy.deepcopy(target()['session'])
            unlocked['native_session'].update(locked_hint=False, locker_active=False)
            locked_session = copy.deepcopy(target()['session'])
            locked_session['native_session'].update(locked_hint=True, locker_active=True)

            prep_req = {
                'format_version': 1, 'binding': EXECUTION, 'transaction_id': '1' * 32,
                'operation': unlock_wire.PREPARE, 'native_session': unlocked, 'account': ACCOUNT}
            prep_ack = {
                'format_version': 1, 'request_sha256': unlock_wire.digest(prep_req),
                'vm_id': 'owned', 'result': {'prepared': True, 'account': ACCOUNT}}

            lock_req = {
                'format_version': 1, 'binding': EXECUTION, 'transaction_id': '2' * 32,
                'operation': 'session-lock-v1', 'ordinal': 0, 'native_session': unlocked}
            lock_delivery = {
                'format_version': 1, 'execution_nonce': EXECUTION['execution_nonce'],
                'vm_id': 'owned', 'transaction_id': '2' * 32,
                'operation': 'session-lock-v1', 'ordinal': 0,
                'event_sha256': protocol.event_sha256('session-lock-v1', 0),
                'lease': {'vm_id': 'owned'}, 'qmp_ack': {'id': 1, 'return': {}}}
            lock_ack = {
                'format_version': 1, 'request_sha256': protocol.request_sha256(lock_req),
                'delivery': lock_delivery}
            lock_record = {
                'format_version': 1, 'vm_id': 'owned', 'transaction_id': '2' * 32,
                'operation': 'session-lock-v1', 'completed': True, 'text_readback': False,
                'events': [{'request': lock_req, 'delivery': lock_delivery, 'native_session_after': locked_session}]}

            unl_req = {
                'format_version': 1, 'binding': EXECUTION, 'transaction_id': '3' * 32,
                'operation': unlock_wire.PURPOSE, 'native_session': locked_session, 'account': ACCOUNT}
            unl_ack = {
                'format_version': 1, 'request_sha256': unlock_wire.digest(unl_req),
                'vm_id': 'owned', 'result': {
                    'purpose': unlock_wire.PURPOSE, 'transaction_id': '3' * 32,
                    'delivered': True, 'text_readback': False, 'session_after': unlocked}}

            proc = {'pid': 300, 'parent_pid': 1, 'start_time_ticks': 50}
            focus = {
                'launch': proc, 'entry': proc, 'frame': proc,
                'frame_lineage': [proc], 'entry_lineage': [proc],
                'accessible_chain': [{'bus_name': ':1.55', 'path': '/org/a11y/atspi/accessible/2', 'pid': 300},
                                     {'bus_name': ':1.55', 'path': '/org/a11y/atspi/accessible/1', 'pid': 300}],
                'entry_focused': True, 'frame_active': True, 'frame_visible': True}
            provider_records = []
            provider_rows = []
            for idx, op in enumerate(('provider-name-v1', 'provider-endpoint-v1-43210',
                                      'provider-fixture-credential-v1', 'reply-fixture-prompt-v1'), start=4):
                tid = str(idx) * 32
                events = []
                for ordinal in range(len(protocol.steps(op))):
                    req = {'format_version': 1, 'binding': EXECUTION, 'transaction_id': tid,
                           'operation': op, 'ordinal': ordinal, 'focus': focus}
                    delivery = {
                        'format_version': 1, 'execution_nonce': EXECUTION['execution_nonce'],
                        'vm_id': 'owned', 'transaction_id': tid,
                        'operation': op, 'ordinal': ordinal,
                        'event_sha256': protocol.event_sha256(op, ordinal),
                        'lease': {'vm_id': 'owned'}, 'qmp_ack': {'id': ordinal + 1, 'return': {}}}
                    ack = {'format_version': 1, 'request_sha256': protocol.request_sha256(req),
                           'delivery': delivery}
                    events.append({'request': req, 'delivery': delivery, 'focus_after': focus})
                    provider_rows.append({'request': req, 'acknowledgment': ack})
                provider_records.append({
                    'format_version': 1, 'vm_id': 'owned', 'transaction_id': tid,
                    'operation': op, 'completed': True, 'text_readback': False, 'events': events})

            (leaf_dir / 'execution.json').write_text(json.dumps({
                'artifact': {'id': 'app'},
                'observations': {
                    'launch': {'fixed_input': provider_records, 'session': {'User': '30008'}},
                    'unlock_preparation': {'request': prep_req, 'acknowledgment': prep_ack},
                    'lock_transition': lock_record,
                    'unlock_transaction': {'request': unl_req, 'acknowledgment': unl_ack,
                                           'independent_session_after': unlocked}}}))
            log_rows = [
                {'request': prep_req, 'acknowledgment': prep_ack},
                *provider_rows,
                {'request': lock_req, 'acknowledgment': lock_ack},
                {'request': unl_req, 'acknowledgment': unl_ack}]
            (root / 'native-input.jsonl').write_bytes(b''.join(protocol.canonical(r) for r in log_rows))
            (root / 'native-unlock.json').write_bytes(protocol.canonical({
                'format_version': 1, 'binding': EXECUTION, 'vm_id': 'owned', 'account': ACCOUNT,
                'prepare_attempt_id': '1' * 32, 'unlock_attempt_id': '3' * 32,
                'secret_sha256': 'f' * 64, 'pre_locked_at': 10.0,
                'post_unlocked_at': 20.0, 'cleanup_completed_at': 30.0}))

            selected = {'format_version': 1, 'kind': 'qemu-fixed-input', 'lease': '/tmp/lease.json', 'vm_id': 'owned'}
            controller.verify_deliveries(root, EXECUTION, selected)

    def test_bridge_prepare_unlock_sends_40_byte_bounded_secret_frame_to_prepare_exchange(self):
        import os
        import selectors
        import time
        from types import SimpleNamespace
        from linux_release.installed_input import controller, protocol, unlock_account, unlock_guest
        unlocked = copy.deepcopy(target()['session'])
        unlocked['native_session'].update(locked_hint=False, locker_active=False)
        pending = {
            'format_version': 1, 'binding': EXECUTION, 'transaction_id': '1' * 32,
            'operation': unlock_wire.PREPARE, 'native_session': unlocked, 'account': ACCOUNT}

        guest_in_r, host_out_w = os.pipe()
        host_in_r, guest_out_w = os.pipe()
        host_stdin = os.fdopen(host_out_w, 'wb', buffering=0)
        host_stdout = os.fdopen(host_in_r, 'rb', buffering=0)
        guest_stdin = os.fdopen(guest_in_r, 'rb', buffering=0)
        guest_stdout = os.fdopen(guest_out_w, 'wb', buffering=0)
        for stream in (host_stdin, host_stdout, guest_stdin, guest_stdout):
            self.addCleanup(stream.close)
            os.set_blocking(stream.fileno(), False)

        host_sel = selectors.DefaultSelector()
        guest_sel = selectors.DefaultSelector()
        self.addCleanup(host_sel.close)
        self.addCleanup(guest_sel.close)
        host_sel.register(host_stdout, selectors.EVENT_READ)
        guest_sel.register(guest_stdin, selectors.EVENT_READ)

        bridge = controller.Bridge.__new__(controller.Bridge)
        bridge.selected = {'format_version': 1, 'kind': 'qemu-fixed-input', 'lease': '/tmp/lease.json', 'vm_id': 'owned'}
        bridge.execution = protocol.binding(EXECUTION)
        bridge.process = SimpleNamespace(stdin=host_stdin, stdout=host_stdout)
        bridge.selector = host_sel
        bridge.actor = Mock(nonce=EXECUTION['execution_nonce'], value={'vm_id': 'owned'})
        bridge.bytes = 0
        bridge.unlock_actor = None
        bridge.prepared_request = None
        bridge.unlock_proof = None
        bridge._record_log = Mock()

        observer = Mock()
        observer.observe.return_value = unlocked
        captured_secrets = []

        def fake_prepare(acc, sec, *, vm_id=None):
            captured_secrets.append((acc, bytes(sec), vm_id))
            return {'prepared': True, 'account': dict(acc), 'credential_state': 'active'}

        guest_streams = SimpleNamespace(stdin=guest_stdout, stdout=guest_stdin)
        guest_pipe = controller.Pipe(guest_streams, guest_sel, time.monotonic() + 5)

        def guest_worker():
            cmd = guest_pipe.line({'state': 'waiting'}) if False else None
            # Read native-prepare line and reply native-prepare-ready
            raw = bytearray()
            while not raw.endswith(b'\n'):
                raw.extend(guest_pipe.read(1))
            guest_pipe.write(protocol.canonical({
                'state': 'native-prepare-ready',
                'request_sha256': unlock_wire.digest(pending),
                'vm_id': 'owned'}))
            with patch.object(unlock_account, 'run_privileged_prepare', side_effect=fake_prepare):
                unlock_guest.prepare_exchange(guest_pipe, observer, EXECUTION, pending, vm_id='owned')
            raw = bytearray()
            while not raw.endswith(b'\n'):
                raw.extend(guest_pipe.read(1))
            guest_pipe.write(protocol.canonical({'state': 'acknowledged'}))

        worker = threading.Thread(target=guest_worker)
        worker.start()
        try:
            ack = bridge.prepare_unlock(pending)
        finally:
            worker.join(5)
        self.assertFalse(worker.is_alive())
        self.assertEqual(ack['result'], {'prepared': True, 'account': ACCOUNT})
        self.assertEqual(len(captured_secrets), 1)
        self.assertEqual(captured_secrets[0][0], ACCOUNT)
        self.assertEqual(len(captured_secrets[0][1]), 40)
        self.assertEqual(captured_secrets[0][2], 'owned')
