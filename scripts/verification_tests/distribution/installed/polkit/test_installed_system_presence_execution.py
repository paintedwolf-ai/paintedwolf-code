"""Real reveal qualification needs the bound system transaction and host audit together."""
from contextlib import ExitStack
from copy import deepcopy
import unittest
from unittest.mock import Mock, patch

from scripts.linux_release.installed_cases import presence_system as system
from verification_tests.distribution.installed.polkit.test_installed_polkit_stage_execution import target


class InstalledSystemPresenceTests(unittest.TestCase):
    def execute(self, *, cancel=False, audit=True, authenticated=True):
        app, ui, observations = Mock(), Mock(), {}
        attempt = target()['pending']['attempt']
        control = Mock(records=[])
        control.bind.return_value = {'attempt': attempt}
        witness = {'system_authenticated': authenticated if not cancel else False,
            'agent_cancelled': cancel, 'host_release_verified': False}
        before = {'secret_id': 'selected', 'person_id': 'person', 'reveals': []}
        after = deepcopy(before)
        if not cancel and audit:
            after['reveals'] = [{'challenge_id': attempt['challenge']['challenge_id']}]
        outcome = {'status': 'refused' if cancel else 'confirmed', 'authenticator': 'linux_user_presence'}
        with ExitStack() as stack:
            values = {'native_caller': Mock(return_value={'pid': 401, 'uid': 30008}),
                'reveal_audit': Mock(side_effect=[before, after]),
                'attempt_rows': Mock(return_value=[]), 'confirmation_rows': Mock(return_value=[]),
                'graphical_session': Mock(return_value={}),
                'NativeSessionObserver': Mock(return_value=Mock(__enter__=Mock(return_value=Mock()),
                    __exit__=Mock(return_value=False))),
                'NativePolkitControl': Mock(return_value=control),
                'await_attempt': Mock(return_value=attempt),
                'await_observation': Mock(side_effect=[{'attempt': attempt}, witness]),
                'await_confirmation': Mock(return_value=outcome)}
            for key, value in values.items():
                stack.enter_context(patch.object(system, key, value))
            try:
                result = system.run_reveal(app, ui, 'project', {'binding': {}, 'vm_id': 'owned'},
                    observations, cancel=cancel)
            except BaseException as error:
                error.record, error.control, error.ui = observations, control, ui
                raise
        return result, control, ui

    def test_success_requires_native_authentication_and_exact_host_audit_without_value_readback(self):
        record, control, ui = self.execute()
        self.assertTrue(record['system_authentication_verified'])
        self.assertTrue(record['host_release_verified'])
        control.deliver.assert_called_once_with(cancel=False)
        control.close.assert_called_once()
        self.assertEqual([call.args[0] for call in ui.click.call_args_list], ['Reveal value', 'Hide now'])
        ui.find.assert_called_once()

    def test_native_success_without_host_audit_never_completes_the_case(self):
        with self.assertRaises(system.CaseFailed) as failed:
            self.execute(audit=False)
        self.assertFalse(failed.exception.record['system_authentication']['host_release_verified'])
        failed.exception.control.close.assert_called_once()
        failed.exception.ui.find.assert_not_called()

    def test_cancel_requires_system_agent_cancellation_and_unchanged_host_audit(self):
        record, control, ui = self.execute(cancel=True)
        self.assertTrue(record['cancel_verified'])
        self.assertFalse(record['host_release_verified'])
        control.deliver.assert_called_once_with(cancel=True)
        ui.find.assert_not_called()
        control.close.assert_called_once()

    def test_native_confirmation_without_actual_system_authentication_is_rejected(self):
        with self.assertRaises(system.CaseFailed) as failed:
            self.execute(authenticated=False)
        self.assertFalse(failed.exception.record['system_authentication']['system_authentication_verified'])



class StaleSystemPresenceTests(unittest.TestCase):
    def execute(self, *, cancelled=False, changed=None):
        from scripts.linux_release.installed_cases import lock_monitor
        attempt = deepcopy(target()['pending']['attempt'])
        challenge = attempt['challenge']
        before = {'secret_id': 'selected', 'person_id': 'person', 'reveals': []}
        after = {**before, 'reveals': [{'unexpected': True}]} if changed == 'audit' else before
        witness = {'attempt': deepcopy(attempt), 'system_authenticated': not cancelled,
                   'agent_cancelled': cancelled, 'host_release_verified': False}
        outcome = {'status': 'refused', 'code': 'presence_denied', 'authenticator': None,
                   'resolution': None if cancelled else 'step_away'}
        invalidations = [{'challenge_id': challenge['challenge_id'],
                          'generation_before': challenge['step_away_generation'],
                          'generation_after': challenge['step_away_generation'] + 1}]
        if cancelled or changed == 'invalidation': invalidations = []
        if changed == 'authentication': witness['system_authenticated'] = cancelled
        if changed == 'agent_cancelled': witness['agent_cancelled'] = not cancelled
        if changed == 'root_release': witness['host_release_verified'] = True
        if changed == 'refusal': outcome['code'] = 'presence_unavailable'
        if changed == 'resolution': outcome['resolution'] = 'step_away' if cancelled else None
        if changed == 'authenticator': outcome['authenticator'] = 'linux_user_presence'
        post_lock = witness if cancelled else {'attempt': deepcopy(attempt)}
        if changed == 'post_identity': post_lock['attempt']['request_id'] = 'foreign'
        if changed == 'final_identity': witness['attempt']['request_id'] = 'foreign'
        fresh_attempt = deepcopy(attempt)
        fresh_attempt['challenge'].update(challenge_id='20000000-0000-4000-8000-000000000002',
                                         step_away_generation=challenge['step_away_generation'] + 1)
        if changed == 'fresh_generation':
            fresh_attempt['challenge']['step_away_generation'] = challenge['step_away_generation']
        if changed == 'fresh_identity': fresh_attempt['challenge']['challenge_id'] = challenge['challenge_id']
        fresh = {'host_release_verified': changed != 'fresh_release', 'system_authentication_verified': True,
                 'native_attempt': fresh_attempt}
        control, observations, events = Mock(records=[]), {}, []
        control.close.side_effect = lambda: events.append('closed')
        def reveal(*args, **kwargs):
            self.assertEqual(events, ['closed'])
            events.append('fresh')
            return fresh
        with ExitStack() as stack:
            values = {'native_caller': Mock(return_value={}), 'attempt_rows': Mock(return_value=[]),
                'confirmation_rows': Mock(return_value=[]), 'NativePolkitControl': Mock(return_value=control),
                'reveal_audit': Mock(side_effect=[before, after]), 'await_attempt': Mock(return_value=attempt),
                'await_observation': Mock(side_effect=[{'attempt': attempt}, post_lock] + ([] if cancelled else [witness])),
                'await_confirmation': Mock(return_value=outcome), 'run_reveal': Mock(side_effect=reveal)}
            for key, value in values.items(): stack.enter_context(patch.object(system, key, value))
            locked = stack.enter_context(patch.object(lock_monitor, 'lock_and_unlock'))
            stack.enter_context(patch.object(lock_monitor, 'step_away_invalidations', return_value=invalidations))
            arguments = (Mock(), Mock(), 'project', {'binding': {}, 'vm_id': 'owned'},
                         Mock(), Mock(), Mock(), observations, Mock())
            try:
                result = system.run_stale_reveal(*arguments)
            except BaseException as error:
                error.control, error.fresh, error.observed = control, values['run_reveal'], observations
                raise
        control.start.assert_called_once_with(stale=True)
        locked.assert_called_once()
        control.close.assert_called_once()
        self.assertEqual(events, ['closed', 'fresh'])
        return result, observations, control

    def test_original_cancel_never_inputs_PAM_and_requires_fresh_higher_generation_release(self):
        record, observations, control = self.execute(cancelled=True)
        control.deliver.assert_not_called()
        self.assertTrue(record['original_prompt_cancelled_verified'])
        self.assertFalse(record['late_pam_rejection_verified'])
        self.assertFalse(observations['host_valid_stale_proof_rejection_verified'])
        self.assertTrue(observations['stale_challenge_refusal_verified'])
        self.assertTrue(observations['fresh_confirmation_verified'])
        self.assertEqual(record['invalidations'], [])
        self.assertIsNone(record['native_confirmation']['resolution'])

    def test_still_pending_original_keeps_actual_PAM_and_step_away_requirements(self):
        record, observations, control = self.execute()
        control.deliver.assert_called_once_with()
        self.assertTrue(record['late_pam_rejection_verified'])
        self.assertFalse(record['original_prompt_cancelled_verified'])
        self.assertFalse(observations['host_valid_stale_proof_rejection_verified'])
        self.assertTrue(observations['fresh_confirmation_verified'])

    def test_each_branch_refuses_wrong_native_root_audit_or_challenge_facts(self):
        for cancelled in (True, False):
            changes = ['audit', 'authentication', 'agent_cancelled', 'root_release', 'refusal',
                       'resolution', 'post_identity', 'fresh_generation', 'fresh_identity', 'fresh_release']
            changes += ['authenticator'] if cancelled else ['invalidation', 'final_identity']
            for changed in changes:
                with self.subTest(cancelled=cancelled, changed=changed), self.assertRaises((system.CaseFailed, ValueError)) as failure:
                    self.execute(cancelled=cancelled, changed=changed)
                error = failure.exception
                error.control.close.assert_called_once()
                if cancelled: error.control.deliver.assert_not_called()
                if changed.startswith('fresh_'):
                    error.fresh.assert_called_once()
                    self.assertIsNot(error.observed.get('stale_challenge_refusal_verified'), True)
                else:
                    error.fresh.assert_not_called()


if __name__ == '__main__':
    unittest.main()
