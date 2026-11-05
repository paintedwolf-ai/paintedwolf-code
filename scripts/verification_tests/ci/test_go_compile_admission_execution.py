"""The real native supervisor variant fails before expensive ordinary readiness checks."""
import copy
import unittest
from unittest.mock import patch

from ci_policy import hosted_plan, native_admission, ready_scope
import verification_plan


class GoCompileAdmissionTests(unittest.TestCase):
    def test_native_privilege_variant_runs_first_in_ephemeral_ci(self):
        for paths in (['lycaon/internal/exec/prepared.go'], ['scripts/ci_policy/native_admission.py']):
            commands = native_admission.commands({'full': False, 'paths': paths})
            self.assertEqual(commands[0], ['./task', 'test:confine:linux', '--', 'supervisor'])
            self.assertEqual(commands[1:], [['./task', 'scripts:coverage:changes']] if paths[0].startswith('scripts/') else [])
            self.assertEqual(ready_scope.check_phases(['build'], {'full': False, 'paths': paths}, []),
                             [*commands, ['./task', 'build']])
        self.assertEqual(native_admission.commands({'full': False, 'paths': ['lycaon-den/src/ui.ts']}), [])

    def test_catalog_refuses_unknown_or_partial_early_native_checks(self):
        for declaration in (None, {}, [['unknown:task']], [['test:confine:linux', '--', 'browser']]):
            data = copy.deepcopy(verification_plan.catalog())
            data['ci']['ready']['admission_checks'] = declaration
            with self.subTest(declaration=declaration), patch.object(hosted_plan, 'catalog', return_value=data):
                with self.assertRaises(ValueError):
                    hosted_plan.matrix('fast')
