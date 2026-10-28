"""Select native compilation before a pull request enters integration."""
from verification_plan import catalog


def targets(scope):
    declarations = catalog()['ci']['ready']['compile_targets']
    if scope is None or scope['full']:
        return list(declarations)
    return [target for target, roots in declarations.items()
            if any(path.startswith(tuple(roots)) for path in scope['paths'])]


def commands(scope):
    return [['./task', target, '--', '--compile-only'] for target in targets(scope)]
