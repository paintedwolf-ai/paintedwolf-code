"""Native admission compiles affected targets without executing their tests."""
import unittest
import verification_plan
from unittest.mock import patch
from ci_policy import native_admission, native_verify, hosted_plan


class NativeAdmissionTests(unittest.TestCase):
    def test_native_compilation_occurs_only_in_complete_landing_checks(self):
        for scope in (None, {'full': True}, {'full': False, 'paths': ['lycaon-den/src-tauri/src/main.rs']},
                      {'full': False, 'paths': ['lycaon/internal/decide/native/src/main.rs']}):
            self.assertEqual(native_admission.targets(scope), [])
            expected = [['./task', 'test:digest', '--', './internal/exec', './internal/osprocess',
                         '-tags=nativeprivileges', '-run=^$']] if scope is None or scope['full'] else []
            self.assertEqual(native_admission.commands(scope), expected)
            rows = hosted_plan.matrix('fast', scope=scope)['include']
            self.assertEqual({row['lane']: row['setup'] for row in rows},
                             {'ready': 'verification', 'ready-lint': 'analysis'})
        lanes = hosted_plan.lanes()
        self.assertEqual(lanes['native']['targets'], ['den:test:rust', 'decide:test'])
        self.assertIn('integration', lanes['native']['profiles'])
        self.assertFalse(lanes['native']['runtime_engines'])

    def test_native_selection_is_checked_before_admission_and_cannot_reuse_full_tests(self):
        for target in ('den:test:rust', 'decide:test'):
            verification_plan.validate_selection([target], ['--compile-only'])
            self.assertIsNone(verification_plan.request([target, '--', '--compile-only'], {}))
            for arguments in (['--compile-only', '--ignored'], ['--manifest-path', '/other'], ['-p', 'other']):
                with self.assertRaisesRegex(ValueError, 'No verification was queued'):
                    verification_plan.validate_selection([target], arguments)

    def test_compile_mode_preserves_workspace_and_test_targets(self):
        den = native_verify.commands('den', ['--compile-only'])
        self.assertEqual(den[-1], ['cargo', 'test', '--locked', '--workspace', '--all-targets', '--quiet', '--no-run'])
        self.assertNotIn('--no-run', native_verify.commands('den', [])[-1])
        self.assertEqual(native_verify.commands('decide', ['--compile-only'])[-1][-2:], ['--quiet', '--no-run'])
        for component, arguments in [('other', []), ('den', ['--skip']), ('den', ['--compile-only', 'extra'])]:
            with self.assertRaises(ValueError):
                native_verify.commands(component, arguments)

    def test_preparation_failure_prevents_compilation_and_exit_is_preserved(self):
        with patch.object(native_verify.subprocess, 'call', return_value=7) as execute:
            self.assertEqual(native_verify.run('den', ['--compile-only']), 7)
            execute.assert_called_once()
        with patch.object(native_verify.subprocess, 'call', side_effect=[0, 0]) as execute:
            self.assertEqual(native_verify.run('den', []), 0)
            self.assertEqual(execute.call_count, 2)
