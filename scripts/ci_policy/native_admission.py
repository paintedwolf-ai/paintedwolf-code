"""Select early checks before a pull request enters integration."""
from ci_policy.impact import script_change
from verification_plan import catalog


def runs_script_coverage(scope):
    """A change to what the script suites read gets those suites before the queue; a scope without a base
    runs them too."""
    return bool(scope) and (not scope.get('paths') or script_change(scope['paths']))


def commands(scope):
    result = []
    if scope is None or scope['full'] or any(
            path.endswith('.go') or path in {'lycaon/go.mod', 'lycaon/go.sum', 'Taskfile.yml', 'task'}
            or path.startswith(('.github/', 'scripts/')) for path in scope['paths']):
        result.extend(['./task', *check] for check in catalog()['ci']['ready'].get('admission_checks', []))
    if runs_script_coverage(scope):
        result.append(['./task', 'scripts:coverage:changes'])
    return result
