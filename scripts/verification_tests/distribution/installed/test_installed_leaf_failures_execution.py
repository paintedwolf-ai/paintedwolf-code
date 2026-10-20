"""The leaf retains captured product failure proof and installs only before binding."""
from contextlib import ExitStack
import json
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release import installed_execution as leaf
from linux_release.installed_cases.records import CaseFailed, retain

SOURCE, TREE = 'b' * 40, 'a' * 40


class InstalledLeafFailureTests(unittest.TestCase):
    def fixture(self, stack, prepare):
        artifact = {'id': 'selected', 'installation': 'deb', 'architecture': 'amd64'}
        binding = {'source_commit': SOURCE, 'artifact': artifact}
        row = {'environment': 'guest', 'installation': 'deb', 'architecture': 'amd64',
               'capability': 'conversation', 'cases': ['chat-and-workflow'], 'id': 'row', 'tier': 'supported'}
        variant = {'id': 'baseline', 'display': {'protocol': 'x11'}, 'scales_percent': [100]}
        stack.enter_context(patch.object(leaf.evidence, 'artifacts', return_value={'selected': artifact}))
        stack.enter_context(patch.object(leaf, 'expand', return_value=[row]))
        stack.enter_context(patch.object(leaf, 'requirements', return_value=[variant]))
        stack.enter_context(patch.object(leaf, 'current_tree', return_value=TREE))
        stack.enter_context(patch.object(leaf, 'require_owned_guest', return_value={}))
        binder = stack.enter_context(patch.object(leaf, 'bind', return_value=binding))
        stack.enter_context(patch.object(leaf, 'collect', return_value=({'OS': 'fixture'}, {'session': {}})))
        stack.enter_context(patch.object(leaf, 'prepare_monitors', return_value={}))
        stack.enter_context(patch.object(leaf, 'observe', return_value={'variant': variant}))
        module = leaf.__package__ + '.installed_cases.native_workflows'
        stack.enter_context(patch.dict(sys.modules, {module: SimpleNamespace(prepare_install=prepare)}))
        return binder

    def invoke(self, output, **changes):
        values = {'case_inputs': None}
        values.update(changes)
        return leaf.execute({'environments': [{'id': 'guest'}], 'recorded_environment_facts': ['OS'],
                             'capabilities': []}, {'artifacts': [{'id': 'selected'}]}, SOURCE, 'selected', Path('/packages'), Path('/installed'),
                            'guest', output, TREE, execution_nonce='c' * 32, variant_id='baseline',
                            snapshot={}, **values)

    def test_captured_crash_keeps_status_observations_and_streams_even_after_partial_evidence(self):
        for partial in (False, True):
            with self.subTest(partial=partial), tempfile.TemporaryDirectory() as directory, ExitStack() as stack:
                output = Path(directory) / 'leaf'
                def prepare(*args):
                    self.assertTrue(args[-1].is_dir())
                    return None
                self.fixture(stack, prepare)
                def crash(binding, capability, case, root):
                    if partial:
                        retain(binding, capability, case, root, 'unverified', {'partial': True})
                    error = CaseFailed('captured installed process exited')
                    error.observations = {'launcher_exit_code': -11}
                    error.streams = {'app-stderr': b'owned fixture crash'}
                    raise error
                stack.enter_context(patch.object(leaf, 'execute_case', side_effect=crash))
                result = self.invoke(output)
                case = result['results'][0]['cases'][0]
                self.assertEqual((case['id'], case['status']), ('chat-and-workflow', 'failed'))
                observed = [record for record in case['evidence'] if record['path'].endswith('/execution.json')][0]
                document = json.loads((output / 'evidence' / observed['path']).read_text())
                self.assertEqual(document['observations']['launcher_exit_code'], -11)
                stream = [record for record in case['evidence'] if record['path'].endswith('.bin')][0]
                self.assertEqual((output / 'evidence' / stream['path']).read_bytes(), b'owned fixture crash')

    def test_failed_installation_never_binds_a_missing_or_partially_installed_payload(self):
        with tempfile.TemporaryDirectory() as directory, ExitStack() as stack:
            binder = self.fixture(stack, lambda *args: {'status': 'failed'})
            with self.assertRaises(ValueError):
                self.invoke(Path(directory) / 'leaf')
            binder.assert_not_called()

    def test_invalid_family_inputs_are_refused_before_creating_effect_output(self):
        with tempfile.TemporaryDirectory() as directory, ExitStack() as stack:
            self.fixture(stack, lambda *args: self.fail('invalid inputs reached installer'))
            output = Path(directory) / 'leaf'
            with self.assertRaises(ValueError):
                self.invoke(output, case_inputs={'arbitrary_commands': {}})
            self.assertFalse(output.exists())
