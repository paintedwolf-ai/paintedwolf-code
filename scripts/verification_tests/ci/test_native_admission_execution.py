"""Native admission compiles affected targets without executing their tests, and the real supervisor
variant fails before expensive ordinary readiness checks."""
import copy
import unittest
from unittest.mock import patch

from ci_policy import hosted_plan, native_admission, ready_scope
from ci_policy import native_admission, native_verify, hosted_plan
import verification_plan
from verification_tests.ci.fixture_plan import live

DEN = live(runs="den:typecheck")
ADMISSION = live(phase="admission")
LINT_ADMISSION = live(runs="arch-lint:changes", area="always")
ADMISSION_TESTS = live(phase="admission-tests")


class NativeAdmissionTests(unittest.TestCase):
    def test_admission_runs_supervisor_and_script_checks_without_native_compilation(self):
        go = {ADMISSION: 'verification', LINT_ADMISSION: 'analysis', ADMISSION_TESTS: 'verification'}
        for scope, frontend in ((None, True), ({'full': True, 'paths': []}, True),
                                ({'full': False, 'paths': ['lycaon-den/src-tauri/src/main.rs']}, True),
                                ({'full': False, 'paths': ['lycaon/internal/decide/native/src/main.rs']}, False)):
            broad = scope is None or scope['full']
            expected = [['./task', 'test:confine:linux', '--', 'supervisor'],
                        ['./task', 'release:rehearse:deb-metadata'], ['./task', 'release:rehearse:rpm-metadata']] if broad else []
            if scope and scope['full']:
                expected = [*expected, ['./task', 'scripts:coverage:changes']]
            self.assertEqual(native_admission.commands(scope), expected)
            rows = hosted_plan.matrix('fast', scope=scope)['include']
            self.assertEqual({row['lane']: row['setup'] for row in rows}, {**go, **({DEN: 'frontend'} if frontend else {})})
        # The native unit tests compile in the merge queue, without the engines a host check needs.
        native = [lane for lane in hosted_plan.lanes().values() if 'den:test:rust' in lane['targets']]
        self.assertTrue(native)
        for lane in native:
            self.assertIn('integration', lane['profiles'])
            self.assertFalse(hosted_plan.provisioned(lane)['runtime_engines'])

    def test_package_metadata_lint_runs_for_exactly_its_inputs(self):
        lint = [['./task', 'release:rehearse:deb-metadata'], ['./task', 'release:rehearse:rpm-metadata']]
        for path in ('packaging/linux/deb/postinst', 'packaging/linux/rpm/rpmlint.toml', 'scripts/linux_release/payload.py',
                     'scripts/linux-package-assemble.py', 'scripts/linux-package-config.py'):
            with self.subTest(path=path):
                scope = {'full': False, 'paths': [path]}
                commands = native_admission.commands(scope)
                self.assertEqual([command for command in commands if command in lint], lint)
                ready = next(row for row in hosted_plan.matrix('fast', scope=scope)['include'] if row['lane'] == ADMISSION)
                self.assertTrue(ready['repository_tools'])
        for path in ('lycaon-den/src/ui.ts', 'lycaon/internal/settings/settings.go', 'scripts/linux-package-manifest.py',
                     'docs/packaging.md'):
            with self.subTest(path=path):
                self.assertFalse(any(command in lint for command in native_admission.commands({'full': False, 'paths': [path]})))

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
            data['ci'][ADMISSION]['admission_checks'] = declaration
            with self.subTest(declaration=declaration), patch.object(hosted_plan, 'catalog', return_value=data):
                with self.assertRaises(ValueError):
                    hosted_plan.matrix('fast')
