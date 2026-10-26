"""Case-entry observation is exactly bound, read-only and bounded by its caller."""
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_input import case_execution as observation

BINDING={'format_version':1,'execution_nonce':'a'*32,'source_commit':'b'*40,
         'driver_tree':'c'*40,'artifact_id':'artifact','environment_id':'guest',
         'variant_id':'variant'}


def fact(**changes):
    return {'format_version':1,'binding':BINDING,'vm_id':'owned','capability':'vault',
            'case':'service-unavailable-retry','phase':'executing',**changes}


class CaseExecutionObservationTests(unittest.TestCase):
    def wait(self,read,terminal=None,deadline=10):
        return observation.await_execution(BINDING,'owned','vault',
            'service-unavailable-retry',deadline,read,terminal or Mock(return_value=None))

    def test_exact_fact_returns_only_before_terminal_and_deadline(self):
        read=Mock(return_value=fact());terminal=Mock(return_value=None)
        with patch.object(observation.time,'monotonic',return_value=1):
            self.assertEqual(self.wait(read,terminal),fact())
        read.assert_called_once_with(10)
        self.assertEqual(terminal.call_count,2)

    def test_missing_publication_waits_without_inventing_case_execution(self):
        read=Mock(side_effect=[None,fact()])
        with patch.object(observation.time,'monotonic',return_value=1), \
             patch.object(observation.time,'sleep') as sleep:
            self.assertEqual(self.wait(read),fact())
        self.assertEqual(read.call_count,2);sleep.assert_called_once()

    def test_foreign_binding_case_phase_or_unknown_shape_never_returns(self):
        for value in (fact(binding={**BINDING,'execution_nonce':'d'*32}),fact(case='other'),
                      fact(vm_id='foreign'),fact(phase='ready'),fact(extra=True)):
            with self.subTest(value=value),patch.object(observation.time,'monotonic',return_value=1):
                with self.assertRaises(ValueError):self.wait(Mock(return_value=value))

    def test_finished_or_late_observation_never_becomes_a_phase(self):
        read=Mock(return_value=fact())
        with patch.object(observation.time,'monotonic',return_value=1):
            with self.assertRaises(ValueError):self.wait(read,Mock(return_value=0))
        read.assert_not_called()
        with patch.object(observation.time,'monotonic',side_effect=[1,10]):
            with self.assertRaises(TimeoutError):self.wait(read)
        with patch.object(observation.time,'monotonic',return_value=10):
            with self.assertRaises(TimeoutError):self.wait(Mock())
        for deadline in (True,float('nan'),float('inf')):
            with self.assertRaises(ValueError):self.wait(Mock(),deadline=deadline)

    def test_marker_is_validated_before_prepublication_waiting(self):
        for exists in (False,True):
            box=Mock();box.path.exists.return_value=exists;box.path.is_symlink.return_value=False
            box.read.return_value=None
            with patch.object(observation,'Mailbox',return_value=box):
                self.assertIsNone(observation.observe(BINDING,'owned','vault','service-unavailable-retry'))
            box.marker.assert_called_once();box.read_case_execution.assert_not_called()
        box=Mock();box.marker.side_effect=ValueError('foreign marker')
        with patch.object(observation,'Mailbox',return_value=box):
            with self.assertRaises(ValueError):observation.observe(BINDING,'owned','vault','service-unavailable-retry')
        box.read.assert_not_called()
