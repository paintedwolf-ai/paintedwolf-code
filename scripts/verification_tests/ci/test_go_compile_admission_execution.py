"""Tagged Go compilation is a prerequisite, not a passing ordinary assertion suite."""
import copy
import io
import json
import unittest
from unittest.mock import patch
import zipfile

from ci_policy import hosted_plan, merge_preflight, native_admission
import verification_plan


class GoCompileAdmissionTests(unittest.TestCase):
    def test_native_privilege_variant_compiles_before_runtime_without_running_payloads(self):
        for paths in (['lycaon/internal/exec/prepared.go'], ['scripts/ci_policy/native_admission.py']):
            commands = native_admission.commands({'full': False, 'paths': paths})
            self.assertEqual(commands, [['./task', 'test:digest', '--', './internal/exec',
                                       './internal/osprocess', '-tags=nativeprivileges', '-run=^$']])
            self.assertEqual(merge_preflight.ready_commands(['test:short'], commands),
                             [*commands, ['./task', 'test:short']])
        self.assertEqual(native_admission.commands({'full': False, 'paths': ['lycaon-den/src/ui.ts']}), [])

    def test_catalog_refuses_unsafe_tagged_compile_declarations(self):
        for declaration in (None, {'bad/tag': ['./internal/exec']}, {'nativeprivileges': []},
                            {'nativeprivileges': ['/outside']}, {'nativeprivileges': ['./missing']},
                            {'nativeprivileges': [[]]}):
            data = copy.deepcopy(verification_plan.catalog())
            data['ci']['ready']['compile_go'] = declaration
            with self.subTest(declaration=declaration), patch.object(hosted_plan, 'catalog', return_value=data):
                with self.assertRaises(ValueError):
                    hosted_plan.matrix('fast')

    def test_compile_receipts_cannot_mark_ordinary_behavior_as_passed(self):
        command = native_admission.commands(None)[0]
        for code in (0, 1):
            record = {'lane': 'ready', 'shard': '', 'status': 'failed',
                      'identity': {'run': '20', 'attempt': '1'},
                      'passed_stages': ['test:digest'] if code == 0 else [],
                      'phases': [{'command': command, 'exit_code': code}]}
            failures = [{'stage': 'test:digest' if code else 'budgets', 'status': 'failed'}]
            raw = io.BytesIO()
            with zipfile.ZipFile(raw, 'w') as archive:
                archive.writestr('ci/run.json', json.dumps(record))
                archive.writestr('ci/failures.json', json.dumps(failures))
            state = merge_preflight.receipt_state(raw.getvalue(), {'id': 20, 'run_attempt': 1},
                                                  ['test:short', 'budgets'])
            self.assertIsNotNone(state)
            self.assertNotIn('test:short', state['passed_targets'])
            if code:
                self.assertEqual(state['native_failed'], ['test:digest'])
                self.assertEqual(merge_preflight.ready_commands(['test:short', 'budgets'], [command], state)[0], command)
