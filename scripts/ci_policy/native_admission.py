"""Select early native checks before a pull request enters integration."""
from verification_plan import catalog


def targets(scope):
    declarations = catalog()['ci']['ready']['compile_targets']
    if scope is None or scope['full']:
        return list(declarations)
    return [target for target, roots in declarations.items()
            if any(path.startswith(tuple(roots)) for path in scope['paths'])]


def commands(scope):
    result = [['./task', target, '--', '--compile-only'] for target in targets(scope)]
    if scope is None or scope['full'] or any(
            path.endswith('.go') or path in {'lycaon/go.mod', 'lycaon/go.sum', 'Taskfile.yml', 'task'}
            or path.startswith(('.github/', 'scripts/')) for path in scope['paths']):
        result.extend(['./task', *check] for check in catalog()['ci']['ready'].get('admission_checks', []))
    return result
