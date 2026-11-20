"""Select early checks before a pull request enters integration."""
from ci_policy.impact import script_change
from verification_plan import catalog


SCRIPT_COVERAGE = 'scripts:coverage:changes'


def runs_script_coverage(scope):
    """A change to what the script suites read gets those suites before the queue; a scope without a base
    runs them too."""
    return bool(scope) and (not scope.get('paths') or script_change(scope['paths']))


# Inputs of the production Linux package metadata that the tiny-payload rehearsals lint.
PACKAGE_METADATA_ROOTS = ('packaging/', 'scripts/linux_release/', 'scripts/linux-package-assemble.py',
                          'scripts/linux-package-config.py')
PACKAGE_METADATA_TARGETS = ('release:rehearse:deb-metadata', 'release:rehearse:rpm-metadata')


def runs_package_metadata(scope):
    """Package lint needs no build, so a packaging change meets it before the queue."""
    return scope is None or scope['full'] or any(path.startswith(PACKAGE_METADATA_ROOTS) for path in scope['paths'])


def commands(scope):
    result = []
    if scope is None or scope['full'] or any(
            path.endswith('.go') or path in {'lycaon/go.mod', 'lycaon/go.sum', 'Taskfile.yml', 'task'}
            or path.startswith(('.github/', 'scripts/')) for path in scope['paths']):
        admission = next((lane for lane in catalog()['ci'].values() if lane.get('phases') == 'admission'), {})
        result.extend(['./task', *check] for check in admission.get('admission_checks', []))
    if runs_package_metadata(scope):
        result.extend(['./task', target] for target in PACKAGE_METADATA_TARGETS)
    if runs_script_coverage(scope):
        result.append(['./task', SCRIPT_COVERAGE])
    return result
