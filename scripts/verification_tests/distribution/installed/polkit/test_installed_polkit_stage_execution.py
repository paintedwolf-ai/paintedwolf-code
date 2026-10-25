"""System-agent credential delivery is one-use and independently revalidated."""
import copy
import threading
import unittest
from unittest.mock import Mock, patch

from scripts.linux_release.installed_input import polkit_stage as stage
from verification_tests.distribution.installed.test_installed_native_unlock_execution import (
    ACCOUNT, EXECUTION, target as unlock_target)
from verification_tests.distribution.installed.polkit.test_installed_polkit_transaction_execution import ATTEMPT


def target():
    value = unlock_target()
    native = value['session']['native_session']
    value['session'] = native
    native.update(locked_hint=False, locker_active=False)
    value['modal'] = {**value['window'], 'path': '/org/a11y/atspi/accessible/5'}
    value['modal_state'] = {**value['password_state'], 'focused': False, 'modal': True}
    value['ancestors'].insert(1, value['modal'])
    attempt = copy.deepcopy(ATTEMPT)
    attempt['subject']['uid'] = ACCOUNT['uid']
    shell = native['authorities']['shell']
    value['pending'] = {'attempt': attempt, 'agent': {'unique': ':1.4', 'path': '/agent',
        'process': {'pid': shell['pid'], 'parent_pid': 1, 'start_time_ticks': shell['start_time_ticks'],
                    'uid': ACCOUNT['uid']}}, 'check_serial': 11, 'begin_serial': 12,
                    'native_session': copy.deepcopy(native)}
    return value


class PolkitStageTests(unittest.TestCase):
    def fixture(self, *, replace=False):
        actor = Mock(nonce=EXECUTION['execution_nonce'], current=None, failed=False, closed=False,
                     transactions=set(), lock=threading.Lock())
        observer = Mock(observe=lambda: copy.deepcopy(target()['pending']))
        private = stage.Stage(actor, EXECUTION, ACCOUNT, b'A1' * 20, observer=observer)
        pipe = Mock()
        def receive():
            request = pipe.send_metadata.call_args.args[0]
            if request['phase'] == 'complete':
                return {**request, 'phase': 'delivered'}
            value = target()
            if replace and request['ordinal'] == 1:
                value['password']['path'] += '_replaced'
            return {key: item for key, item in request.items() if key != 'phase'} | {'target': value}
        pipe.receive_metadata.side_effect = receive
        return actor, observer, private, pipe

    def perform(self, private, pipe):
        with patch.object(stage, 'Pipe', return_value=pipe):
            return private.perform('owned-process', 'owned-selector', 123, 'd' * 32)

    def test_actual_polkit_target_is_distinct_from_lock_screen_target(self):
        self.assertEqual(stage.target(target()), target())
        value = target()
        value['session']['locked_hint'] = True
        with self.assertRaises(ValueError):
            stage.target(value)
        value = target()
        value['pending']['native_session']['locked_hint'] = True
        with self.assertRaises(ValueError):
            stage.target(value)
        for field in ('password', 'pending'):
            value = target()
            value[field] = None
            with self.assertRaises(ValueError):
                stage.target(value)

    def test_delivery_does_not_claim_authentication_or_host_release(self):
        actor, observer, private, pipe = self.fixture()
        result = self.perform(private, pipe)
        self.assertEqual(actor._deliver.call_count, 41)
        self.assertFalse(result['host_release_verified'])
        self.assertNotIn('system_authenticated', result)
        self.assertEqual(private.secret, bytearray(40))
        self.assertNotIn('A1' * 20, repr(pipe.send_metadata.call_args_list) + repr(result))
        with self.assertRaises(ValueError):
            self.perform(private, pipe)
        self.assertEqual(actor._deliver.call_count, 41)

    def test_root_observation_changes_after_guest_read_prevent_delivery(self):
        actor, observer, private, pipe = self.fixture()
        observer.observe = Mock(side_effect=[target()['pending'], None])
        with self.assertRaises(ValueError):
            self.perform(private, pipe)
        actor._deliver.assert_not_called()
        self.assertTrue(actor.failed)
        self.assertEqual(private.secret, bytearray(40))

    def test_target_replacement_and_uncertain_delivery_are_consumed(self):
        actor, _, private, pipe = self.fixture(replace=True)
        with self.assertRaises(ValueError):
            self.perform(private, pipe)
        self.assertEqual(actor._deliver.call_count, 1)
        self.assertTrue(actor.failed)
        actor, _, private, pipe = self.fixture()
        actor._deliver.side_effect = OSError('uncertain')
        with self.assertRaises(OSError):
            self.perform(private, pipe)
        self.assertTrue(private.used)
        self.assertTrue(actor.failed)
        self.assertEqual(private.secret, bytearray(40))

    def test_foreign_account_or_agent_cannot_use_a_matching_password_surface(self):
        for kind in ('uid', 'birth', 'subject'):
            value = target()
            if kind == 'subject':
                value['pending']['attempt']['subject']['uid'] += 1
            elif kind == 'birth':
                value['pending']['agent']['process']['start_time_ticks'] += 1
            else:
                value['pending']['agent']['process']['uid'] += 1
            with self.subTest(kind=kind), self.assertRaises(ValueError):
                stage.target(value)


if __name__ == '__main__':
    unittest.main()
