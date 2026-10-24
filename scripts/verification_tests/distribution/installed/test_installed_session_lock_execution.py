"""Native lock authority stays separate from app frames and focused editor entries."""
import copy
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_input import controller, protocol, session_lock, session_wire, transition_guest
from linux_release.installed_cases.records import CaseUnavailable


def native_proof():
    shell = {'bus_name': 'org.gnome.Shell', 'unique_owner': ':1.2', 'pid': 200,
        'start_time_ticks': 20, 'executable': '/usr/bin/gnome-shell',
        'device': 1, 'inode': 2, 'mode': 0o755, 'bytes': 3}
    def owner(name, unique, pid, birth, uid):
        return {'name': name, 'unique': unique,
            'process': {'pid': pid, 'parent_pid': 1, 'start_time_ticks': birth, 'uid': uid}}
    return {'native_session': {
        'session': {'id': '7', 'User': '1000', 'Type': 'wayland', 'Seat': 'seat0'},
        'session_object': '/org/freedesktop/login1/session/_7',
        'lifecycle': {'object_path': '/org/freedesktop/login1/session/_7',
            'user': [1000, '/org/freedesktop/login1/user/_1000'],
            'seat': ['seat0', '/org/freedesktop/login1/seat/seat0'], 'created_monotonic_usec': 1234},
        'authorities': {'shell': shell,
            'login': owner('org.freedesktop.login1', ':1.1', 100, 10, 0),
            'locker': owner('org.gnome.ScreenSaver', ':1.2', 200, 20, 1000)},
        'locked_hint': False, 'locker_active': False}, 'lock_bindings': ['<Super>l', 'XF86ScreenSaver']}


class SessionWireTests(unittest.TestCase):
    def setUp(self):
        self.before = native_proof()
        self.after = copy.deepcopy(self.before)
        self.after['native_session'].update(locked_hint=True, locker_active=True)
        self.execution = {'format_version': 1, 'execution_nonce': 'a' * 32,
            'source_commit': 'b' * 40, 'driver_tree': 'c' * 40,
            'artifact_id': 'app', 'environment_id': 'noble', 'variant_id': 'gnome-wayland'}

    def test_exact_configured_chord_requires_both_native_lock_observations(self):
        self.assertEqual(protocol.steps('session-lock-v1'), (('meta_l', 'l'),))
        self.assertFalse(session_wire.finished('session-lock-v1', self.before, self.before))
        partial = copy.deepcopy(self.after)
        partial['native_session']['locked_hint'] = False
        self.assertFalse(session_wire.finished('session-lock-v1', self.before, partial))
        self.assertTrue(session_wire.finished('session-lock-v1', self.before, self.after))
        screenshield_before = copy.deepcopy(self.before)
        screenshield_before['native_session']['authorities']['locker']['name'] = 'org.gnome.Shell.ScreenShield'
        screenshield_after = copy.deepcopy(self.after)
        screenshield_after['native_session']['authorities']['locker']['name'] = 'org.gnome.Shell.ScreenShield'
        self.assertTrue(session_wire.finished('session-lock-v1', screenshield_before, screenshield_after))
        tuple_after = copy.deepcopy(screenshield_after)
        tuple_after['native_session']['lifecycle']['user'] = tuple(tuple_after['native_session']['lifecycle']['user'])
        tuple_after['native_session']['lifecycle']['seat'] = tuple(tuple_after['native_session']['lifecycle']['seat'])
        self.assertTrue(session_wire.finished('session-lock-v1', json.loads(json.dumps(screenshield_before)), tuple_after))
        with self.assertRaises(ValueError):
            session_wire.before('session-lock-v1', self.after)
        for bindings in ([], ['<Control><Alt>l'], ['<Super>l', '<Super>l'], ['<Super>l', 7]):
            with self.subTest(bindings=bindings), self.assertRaises(ValueError):
                session_wire.proof({**self.before, 'lock_bindings': bindings})

    def test_same_id_recreation_bus_replacement_and_foreign_image_refuse(self):
        for kind in ('creation', 'path', 'seat', 'user', 'bus', 'birth', 'inode', 'writable'):
            changed = copy.deepcopy(self.after)
            native = changed['native_session']
            if kind == 'creation':
                native['lifecycle']['created_monotonic_usec'] += 1
            elif kind == 'path':
                native['session_object'] += '_new'
                native['lifecycle']['object_path'] = native['session_object']
            elif kind in {'seat', 'user'}:
                native['lifecycle'][kind] = ('seat1', '/org/freedesktop/login1/seat/seat1') if kind == 'seat' else (1001, '/org/freedesktop/login1/user/_1001')
            elif kind == 'bus':
                native['authorities']['locker']['unique'] = ':1.3'
                native['authorities']['shell']['unique_owner'] = ':1.3'
            elif kind == 'birth':
                native['authorities']['locker']['process']['start_time_ticks'] += 1
                native['authorities']['shell']['start_time_ticks'] += 1
            elif kind == 'inode':
                native['authorities']['shell']['inode'] += 1
            else:
                native['authorities']['shell']['mode'] = 0o775
            with self.subTest(kind=kind), self.assertRaises(ValueError):
                session_wire.finished('session-lock-v1', self.before, changed)

    def test_native_session_authority_cannot_substitute_for_entry_or_frame(self):
        pending = {'format_version': 1, 'binding': self.execution, 'transaction_id': 'd' * 32,
            'operation': 'session-lock-v1', 'ordinal': 0, 'native_session': self.before}
        self.assertEqual(protocol.request(pending, self.execution), pending)
        for operation in ('editor-find-needle-v1', 'workspace-request-quit-v1'):
            with self.subTest(operation=operation), self.assertRaises(ValueError):
                protocol.request({**pending, 'operation': operation}, self.execution)
        for key in ('surface', 'focus'):
            with self.subTest(key=key), self.assertRaises(ValueError):
                protocol.request({**{name: value for name, value in pending.items() if name != 'native_session'}, key: self.before}, self.execution)

    def test_real_ack_does_not_replace_independent_native_completion_or_allow_replay(self):
        observer, mailbox = Mock(), Mock()
        observer.prepare.return_value = self.before
        observer.settle.return_value = self.after
        def acknowledgment(_):
            pending = mailbox.write.call_args.args[1]
            return {'format_version': 1, 'request_sha256': protocol.request_sha256(pending),
                'delivery': {'format_version': 1, 'execution_nonce': 'a' * 32, 'vm_id': 'owned',
                    'transaction_id': pending['transaction_id'], 'operation': 'session-lock-v1', 'ordinal': 0,
                    'event_sha256': protocol.event_sha256('session-lock-v1', 0),
                    'lease': {'vm_id': 'owned'}, 'qmp_ack': {'id': 1, 'return': {}}}}
        mailbox.read.side_effect = acknowledgment
        result = transition_guest.perform_transition(self.execution, 'owned', observer, 'session-lock-v1', mailbox)
        self.assertTrue(result['completed'])
        self.assertEqual(result['events'][0]['native_session_after'], self.after)
        mailbox.reset_mock()
        observer.settle.return_value = self.before
        with self.assertRaises(ValueError) as refused:
            transition_guest.perform_transition(self.execution, 'owned', observer, 'session-lock-v1', mailbox)
        self.assertFalse(refused.exception.observations['native_input']['completed'])
        mailbox.clear_completed.assert_not_called()
        mailbox.write.assert_called_once()

    def test_controller_independently_checks_native_transition_and_exact_host_delivery(self):
        pending = {'format_version': 1, 'binding': self.execution, 'transaction_id': 'd' * 32,
            'operation': 'session-lock-v1', 'ordinal': 0, 'native_session': self.before}
        delivered = {'format_version': 1, 'execution_nonce': 'a' * 32, 'vm_id': 'owned',
            'transaction_id': 'd' * 32, 'operation': 'session-lock-v1', 'ordinal': 0,
            'event_sha256': protocol.event_sha256('session-lock-v1', 0),
            'lease': {'vm_id': 'owned'}, 'qmp_ack': {'id': 1, 'return': {}}}
        event = {'request': pending, 'delivery': delivered, 'native_session_after': self.after}
        record = {'format_version': 1, 'vm_id': 'owned', 'transaction_id': 'd' * 32,
            'operation': 'session-lock-v1', 'completed': True, 'events': [event]}
        acknowledgment = {'format_version': 1, 'request_sha256': protocol.request_sha256(pending), 'delivery': delivered}
        selected = {'format_version': 1, 'kind': 'qemu-fixed-input', 'lease': '/owned/lease', 'vm_id': 'owned'}
        with tempfile.TemporaryDirectory() as name, patch.object(controller, 'delivery_records',
                return_value=([record], ['session-lock-v1'])):
            directory = Path(name)
            (directory / 'native-input.jsonl').write_text(json.dumps({'request': pending, 'acknowledgment': acknowledgment}) + '\n')
            controller.verify_deliveries(directory, self.execution, selected)
            for change in ('unlocked', 'recreated', 'foreign_ack'):
                changed = copy.deepcopy(self.after)
                event['native_session_after'] = changed
                if change == 'unlocked':
                    changed['native_session']['locker_active'] = False
                elif change == 'recreated':
                    changed['native_session']['lifecycle']['created_monotonic_usec'] += 1
                else:
                    event['delivery'] = {**delivered, 'transaction_id': 'e' * 32}
                with self.subTest(change=change), self.assertRaises(ValueError):
                    controller.verify_deliveries(directory, self.execution, selected)


class NativeLockObserverTests(unittest.TestCase):
    def setUp(self):
        self.native = Mock()
        self.native.observe.return_value = native_proof()['native_session']
        self.session = {'id': '7', 'User': '1000', 'Desktop': 'GNOME', 'Type': 'wayland'}

    def observer(self):
        with patch.object(session_lock.NativeLockObserver, 'bindings', return_value=['<Super>l']):
            return session_lock.NativeLockObserver(self.session, native=self.native)

    def test_configured_shortcut_and_original_native_owners_are_rechecked(self):
        observer = self.observer()
        with patch.object(observer, 'bindings', return_value=['<Control><Alt>l']):
            with self.assertRaises(ValueError):
                observer.observe()
        changed = copy.deepcopy(self.native.observe.return_value)
        changed['lifecycle']['created_monotonic_usec'] += 1
        self.native.observe.return_value = changed
        with patch.object(observer, 'bindings', return_value=['<Super>l']), self.assertRaises(ValueError):
            observer.observe()
        observer.close()
        self.native.close.assert_not_called()

    def test_lock_progress_cannot_reverse_and_owned_connection_closes(self):
        with patch.object(session_lock, 'NativeSessionObserver', return_value=self.native), patch.object(
                session_lock.NativeLockObserver, 'bindings', return_value=['<Super>l']):
            with session_lock.NativeLockObserver(self.session) as observer:
                before = observer.prepare('session-lock-v1')
                after = copy.deepcopy(before['native_session'])
                after['locker_active'] = True
                self.native.observe.return_value = after
                observer.during('session-lock-v1', before)
                self.native.observe.return_value = before['native_session']
                with self.assertRaisesRegex(ValueError, 'reversed'):
                    observer.during('session-lock-v1', before)
            observer.close()
            self.native.close.assert_called_once()

    def test_native_flags_can_advance_during_settings_read_but_owners_cannot_change(self):
        observer = self.observer()
        before = native_proof()['native_session']
        after = copy.deepcopy(before)
        after.update(locked_hint=True, locker_active=True)
        with patch.object(observer, 'bindings', return_value=['<Super>l']):
            self.native.observe.side_effect = [before, after]
            self.assertTrue(observer.observe()['native_session']['locked_hint'])
            self.native.observe.side_effect = [after, before]
            with self.assertRaisesRegex(ValueError, 'reversed'):
                observer.observe()
            changed = copy.deepcopy(after)
            changed['lifecycle']['created_monotonic_usec'] += 1
            self.native.observe.side_effect = [before, changed]
            with self.assertRaisesRegex(ValueError, 'replaced'):
                observer.observe()

    def test_actual_settings_command_uses_captured_environment_typed_array_and_image(self):
        observer = self.observer()
        self.native.reader.glib.Error = ValueError
        variant = self.native.reader.glib.Variant.parse.return_value
        variant.get_type_string.return_value = 'as'
        variant.unpack.return_value = ['<Super>l']
        info = SimpleNamespace(st_dev=1, st_ino=2, st_uid=0, st_gid=0, st_mode=0o100755, st_size=3)
        with patch.object(session_lock, 'physical_image', return_value=info) as image, patch.object(
                session_lock, 'session_environment', return_value={'captured': 'session'}) as environment, patch.object(
                session_lock, 'command', return_value=(0, b"['<Super>l']\n", b'')) as run:
            self.assertEqual(observer.bindings(), ['<Super>l'])
            self.assertEqual(image.call_args_list[0].args, (Path('/usr/bin/gsettings'),))
            self.assertEqual(image.call_count, 2)
            environment.assert_called_once_with(self.session)
            self.assertEqual(run.call_args.args[0], ['/usr/bin/gsettings', 'get',
                'org.gnome.settings-daemon.plugins.media-keys', 'screensaver'])
            self.assertEqual(run.call_args.kwargs, {'timeout': 5, 'limit': 4096,
                'environment': {'captured': 'session'}})
            self.assertEqual(self.native.reader.glib.Variant.parse.call_args.args[1], "['<Super>l']\n")
            self.native.reader.glib.Variant.parse.reset_mock()
            run.return_value = (0, b"['<Super>l']\0foreign", b'')
            with self.assertRaises(ValueError):
                observer.bindings()
            self.native.reader.glib.Variant.parse.assert_not_called()
            run.return_value = (0, b'x' * 4097, b'')
            with self.assertRaises(CaseUnavailable):
                observer.bindings()
