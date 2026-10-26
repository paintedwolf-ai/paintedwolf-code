"""Uncertain notification clicks retain delivery and outcome evidence without replay."""
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_input import notification_guest as guest
from linux_release.installed_cases.records import CaseUnavailable
from verification_tests.distribution.installed.test_notification_wire_execution import proof

EXECUTION={'format_version':1,'execution_nonce':'a'*32,'source_commit':'b'*40,'driver_tree':'c'*40,
    'artifact_id':'app','environment_id':'ubuntu','variant_id':'wayland'}


class NotificationGuestTests(unittest.TestCase):
    def setUp(self):
        self.before=proof()
        self.observer=Mock()
        self.observer.observe.return_value=self.before
        self.mailbox=Mock()
        self.mailbox.read.return_value={'observed':'ack'}
        self.delivery={'captured':'delivery'}

    def test_one_click_publication_and_complete_result(self):
        after={'actual':'completed'}
        self.observer.settle.return_value=after
        with patch.object(guest.protocol,'request',side_effect=lambda row,execution:row),patch.object(
                guest.protocol,'acknowledgment',return_value=self.delivery):
            result=guest.perform(EXECUTION,'vm',self.observer,self.mailbox)
        self.assertTrue(result['completed'])
        self.assertEqual(len(result['events']),1)
        self.assertEqual(result['events'][0]['notification_after'],after)
        self.mailbox.write.assert_called_once()
        self.mailbox.clear_completed.assert_called_once()
        self.observer.arm.assert_called_once()

    def test_unexpected_post_click_error_keeps_ack_and_never_retries(self):
        self.observer.settle.side_effect=IndexError('observer teardown')
        with patch.object(guest.protocol,'request',side_effect=lambda row,execution:row),patch.object(
                guest.protocol,'acknowledgment',return_value=self.delivery):
            with self.assertRaises(IndexError)as raised:guest.perform(EXECUTION,'vm',self.observer,self.mailbox)
        partial=raised.exception.observations['native_input']
        self.assertFalse(partial['completed'])
        self.assertEqual(partial['events'][0]['delivery'],self.delivery)
        self.assertIsNone(partial['events'][0]['notification_after'])
        self.mailbox.write.assert_called_once()
        self.mailbox.clear_completed.assert_not_called()
        self.observer.settle.assert_called_once()

    def test_timeout_after_publication_keeps_uncertain_input_consumed(self):
        self.mailbox.read.return_value=None
        with patch.object(guest.protocol,'request',side_effect=lambda row,execution:row),patch.object(
                guest.time,'monotonic',side_effect=[0,11]):
            with self.assertRaises(CaseUnavailable)as raised:guest.perform(EXECUTION,'vm',self.observer,self.mailbox)
        self.assertFalse(raised.exception.observations['native_input']['completed'])
        self.mailbox.write.assert_called_once()
        self.mailbox.clear_completed.assert_not_called()
        self.observer.settle.assert_not_called()

    def test_changed_target_before_publication_never_dispatches(self):
        changed=proof();changed['target']['extent'][0]+=1
        self.observer.observe.side_effect=[self.before,changed]
        with self.assertRaises(ValueError):guest.perform(EXECUTION,'vm',self.observer,self.mailbox)
        self.mailbox.write.assert_not_called()

    def test_initial_and_pinned_capture_failures_retain_phase_without_publication(self):
        for phase in ('initial-observation', 'pinned-observation'):
            self.mailbox.reset_mock()
            refusal = CaseUnavailable('shell snapshot did not select a target')
            self.observer.observe.side_effect = [refusal] if phase == 'initial-observation' else [self.before, refusal]
            with self.subTest(phase=phase), self.assertRaises(CaseUnavailable) as raised:
                guest.perform(EXECUTION, 'vm', self.observer, self.mailbox)
            self.assertEqual(raised.exception.observations['notification_input_phase'], phase)
            self.assertEqual(raised.exception.observations['native_input']['events'], [])
            self.mailbox.write.assert_not_called()
            self.mailbox.clear_completed.assert_not_called()

    def test_renderer_or_capability_change_does_not_publish_or_replay_click(self):
        for change in ('server', 'capabilities', 'display'):
            self.mailbox.reset_mock()
            changed = proof()
            if change == 'server':changed['delivery']['server'][2] = '47.0'
            elif change == 'capabilities':changed['delivery']['capabilities']['body-markup'] = False
            else:changed['delivery']['display_body_sha256'] = 'e' * 64
            self.observer.observe.side_effect = [self.before, changed]
            with self.subTest(change=change), self.assertRaises(ValueError):
                guest.perform(EXECUTION, 'vm', self.observer, self.mailbox)
            self.mailbox.write.assert_not_called()
            self.mailbox.clear_completed.assert_not_called()


if __name__=='__main__':unittest.main()
