"""Source-only closed recovery exchange fixtures; no SSH, account or unit effects."""
import time
import io
import json
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_input import recovery_phase as owner

BINDING = {'format_version': 1, 'execution_nonce': 'a' * 32,
           'source_commit': 'b' * 40, 'driver_tree': 'c' * 40,
           'artifact_id': 'app', 'environment_id': 'noble', 'variant_id': 'gnome'}


def pending(operation):
    return dict(format_version=1, binding=dict(BINDING), transaction_id='d' * 32, operation=operation)


class RootPhasesTests(unittest.TestCase):
    def fixture(self):
        process = Mock()
        read = Mock(side_effect=list(owner.RESULTS))
        control = owner.RootPipePhases(process, read, BINDING, 'owned', time.monotonic() + 10)
        return control, process, read

    def test_only_two_ordered_commands_and_exact_typed_results(self):
        control, process, read = self.fixture()
        for index, operation in enumerate(owner.OPERATIONS):
            result = control.exchange(pending(operation))
            self.assertEqual(result['result'], owner.RESULTS[index])
            self.assertEqual(result['request'], pending(operation))
        self.assertEqual([c.args[0] for c in process.stdin.write.call_args_list],
                         [b'custodian-absent\n', b'custodian-restore\n'])
        with self.assertRaises(ValueError): control.exchange(pending(owner.ABSENT))
        self.assertEqual(read.call_count, 2)

    def test_wrong_order_foreign_execution_and_arbitrary_targets_never_forward(self):
        for bad in [pending(owner.RESTORE), {**pending(owner.ABSENT), 'unit': 'foreign.service'},
                    {**pending(owner.ABSENT), 'binding': {**BINDING, 'execution_nonce': 'f' * 32}}]:
            control, process, _ = self.fixture()
            with self.subTest(bad=bad), self.assertRaises(ValueError): control.exchange(bad)
            process.stdin.write.assert_not_called()

    def test_uncertain_reply_is_consumed_and_never_replayed(self):
        control, process, read = self.fixture()
        read.side_effect = OSError('pipe lost')
        with self.assertRaises(OSError): control.exchange(pending(owner.ABSENT))
        with self.assertRaises(ValueError): control.exchange(pending(owner.ABSENT))
        process.stdin.write.assert_called_once_with(b'custodian-absent\n')

    def test_wrong_typed_result_never_confers_phase_completion(self):
        control, _, read = self.fixture()
        read.side_effect = None
        read.return_value = {**owner.RESULTS[0], 'typed_startup_outcome_required': 1}
        with self.assertRaises(ValueError): control.exchange(pending(owner.ABSENT))
        self.assertEqual(control.index, 0)
        self.assertTrue(control.failed)


    def test_cancelled_or_late_delivered_response_cannot_acknowledge_or_replay(self):
        for state in ('cancelled', 'late'):
            control, process, read = self.fixture()
            def delivered(*_):
                if state == 'cancelled': control.cancel()
                else: control.deadline = 0
                return owner.RESULTS[0]
            read.side_effect = delivered
            with self.subTest(state=state), self.assertRaises(TimeoutError):
                control.exchange(pending(owner.ABSENT))
            self.assertEqual(control.index, 0)
            with self.assertRaises(ValueError): control.exchange(pending(owner.ABSENT))
            process.stdin.write.assert_called_once_with(b'custodian-absent\n')


class GuestPhasesTests(unittest.TestCase):
    def fixture(self):
        box = Mock()
        with patch.object(owner, 'RecoveryMailbox', return_value=box):
            control = owner.GuestPhases(BINDING, 'owned', time.monotonic() + 10)
        def read(_):
            request = box.write.call_args.args[1]
            return dict(format_version=1, request=request, vm_id='owned', result=owner.RESULTS[control.index])
        box.read.side_effect = read
        return control, box

    def test_actual_order_advances_only_after_exact_ack_and_consumes_leaf(self):
        control, box = self.fixture()
        for operation in owner.OPERATIONS: control.phase(operation)
        self.assertEqual(control.index, 2)
        self.assertEqual(box.completed.call_count, 2)
        with self.assertRaises(ValueError): control.phase(owner.RESTORE)
        self.assertEqual(box.write.call_count, 2)

    def test_timeout_before_publication_and_uncertain_ack_cannot_replay(self):
        control, box = self.fixture()
        control.deadline = 0
        with self.assertRaises(TimeoutError): control.phase(owner.ABSENT)
        box.write.assert_not_called()
        with self.assertRaises(ValueError): control.phase(owner.ABSENT)
        control, box = self.fixture()
        box.read.return_value = None
        box.read.side_effect = OSError('read uncertain')
        with self.assertRaises(OSError): control.phase(owner.ABSENT)
        with self.assertRaises(ValueError): control.phase(owner.ABSENT)
        box.write.assert_called_once()
        box.completed.assert_not_called()

    def test_foreign_ack_does_not_clear_pending_request(self):
        control, box = self.fixture()
        box.read.side_effect = None
        box.read.return_value = dict(format_version=1, request=pending(owner.ABSENT),
                                    vm_id='foreign', result=owner.RESULTS[0])
        with self.assertRaises(ValueError): control.phase(owner.ABSENT)
        box.completed.assert_not_called()

    def test_late_ack_retains_uncertainty_without_clearing_or_replaying(self):
        control, box = self.fixture()
        original = box.read.side_effect
        def late(name):
            result = original(name)
            control.deadline = 0
            return result
        box.read.side_effect = late
        with self.assertRaises(TimeoutError): control.phase(owner.ABSENT)
        box.completed.assert_not_called()
        with self.assertRaises(ValueError): control.phase(owner.ABSENT)
        box.write.assert_called_once()


class PublicationTests(unittest.TestCase):
    def read(self, box, *, action='read'):
        payload = dict(action=action, execution=BINDING, vm_id='owned')
        if action == 'acknowledge': payload['document'] = {}
        output = io.StringIO()
        with patch.object(owner, 'RecoveryMailbox', return_value=box), \
                patch.object(owner.sys, 'stdin', SimpleNamespace(buffer=io.BytesIO(json.dumps(payload).encode()))), \
                patch.object(owner.sys, 'stdout', output):
            owner.guest_exchange()
        return output.getvalue()

    def test_absent_and_partially_published_read_wait_without_any_ack_or_effect(self):
        for exists in (False, True):
            box = Mock()
            box.path.exists.return_value = exists
            box.path.is_symlink.return_value = False
            box.read.return_value = None
            self.assertEqual(json.loads(self.read(box)), {'request': None, 'case_execution': None})
            box.marker.assert_called_once()
            box.attach.assert_not_called()
            box.write.assert_not_called()

    def test_foreign_marker_or_attached_binding_and_early_ack_never_wait_or_publish(self):
        box = Mock()
        box.marker.side_effect = ValueError('foreign marker')
        with self.assertRaises(ValueError): self.read(box)
        box.attach.assert_not_called()
        box = Mock()
        box.path.exists.return_value = True
        box.read.return_value = {'foreign': True}
        box.attach.side_effect = ValueError('foreign binding')
        with self.assertRaises(ValueError): self.read(box)
        box.write.assert_not_called()
        box = Mock()
        box.attach.side_effect = FileNotFoundError('not published')
        with self.assertRaises(FileNotFoundError): self.read(box, action='acknowledge')
        box.write.assert_not_called()

    def test_read_projects_only_the_exact_case_fact_through_base_mailbox(self):
        box=Mock()
        box.path.exists.return_value=True
        box.read.side_effect=lambda name:BINDING if name=='binding.json' else None
        observed={'format_version':1,'binding':BINDING,'vm_id':'owned',
                  'capability':'vault','case':'service-unavailable-retry','phase':'executing'}
        base=Mock();base.read_case_execution.return_value=observed
        with patch.object(owner,'Mailbox',return_value=base) as mailbox:
            result=json.loads(self.read(box))
        self.assertEqual(result,{'request':None,'case_execution':observed})
        mailbox.assert_called_once_with(BINDING,'owned')
        base.read_case_execution.assert_called_once_with('vault','service-unavailable-retry')
        box.write.assert_not_called()
