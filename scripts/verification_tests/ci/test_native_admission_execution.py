"""Native admission compiles affected targets without executing their tests."""
import unittest
import verification_plan
from unittest.mock import patch
from ci_policy import native_admission, native_verify, hosted_plan


class NativeAdmissionTests(unittest.TestCase):
    def test_native_obligations_follow_changed_sources_and_unknown_scope(self):
        for paths, expected in [(['docs/design.md'], []), (['lycaon/internal/session/a.go'], []),
                (['lycaon-den/src-tauri/crates/desktop-settings-observer/src/main.rs'], ['den:test:rust']),
                (['lycaon/internal/decide/native/src/main.rs'], ['decide:test']),
                (['lycaon-den/src-tauri/src/main.rs', 'lycaon/internal/decide/native/src/main.rs'],
                 ['den:test:rust', 'decide:test'])]:
            scope = {'full': False, 'paths': paths}
            self.assertEqual(native_admission.targets(scope), expected)
            self.assertEqual(native_admission.commands(scope),
                             [['./task', target, '--', '--compile-only'] for target in expected])
        for scope in (None, {'full': True}):
            self.assertEqual(native_admission.targets(scope), ['den:test:rust', 'decide:test'])

    def test_admission_provisions_native_inputs_only_for_selected_compilation(self):
        for scope, setup in [({'full': False, 'paths': ['docs/design.md']}, 'verification'),
                ({'full': False, 'paths': ['lycaon-den/src-tauri/src/main.rs']}, 'shell'),
                ({'full': True}, 'shell'), (None, 'shell')]:
            rows = hosted_plan.matrix('fast', scope=scope)['include']
            self.assertEqual(len(rows), 1)
            self.assertEqual(rows[0]['setup'], setup)
            self.assertTrue(rows[0]['runtime_engines'])
        self.assertEqual(hosted_plan.max_parallel('fast'), 1)
        rows = {row['lane']: row for row in hosted_plan.matrix('integration')['include']}
        self.assertFalse(rows['native']['runtime_engines'])
        self.assertTrue(rows['behavior']['runtime_engines'])

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
