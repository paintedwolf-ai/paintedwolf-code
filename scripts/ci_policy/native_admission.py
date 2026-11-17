"""Select early checks before a pull request enters integration."""
from verification_plan import catalog


def runs_script_coverage(scope):
    """A script or workflow change gets its own suites before the queue, not only inside it."""
    return bool(scope) and (scope['full'] or any(path.startswith(('.github/', 'scripts/')) for path in scope['paths']))


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
        result.extend(['./task', *check] for check in catalog()['ci']['ready'].get('admission_checks', []))
    if runs_package_metadata(scope):
        result.extend(['./task', target] for target in PACKAGE_METADATA_TARGETS)
    if runs_script_coverage(scope):
        result.append(['./task', 'scripts:coverage:changes'])
    return result
