"""Apply the affected Go graph without changing the ready failure frontier."""
import re

from verification_plan import catalog


def commands(phases, scope):
    from ci_policy.impact import go_scope
    if scope is None:
        return phases, None
    packages = go_scope(scope)
    if packages is None:
        return phases, None
    exclude = re.compile(catalog()['go']['test:short']['exclude'])
    packages = [package for package in packages if not exclude.search('/' + package.removeprefix('./'))]
    selected = []
    for phase in phases:
        if 'test:short' not in phase:
            selected.append(phase)
            continue
        index = phase.index('test:short')
        if index > 1:
            selected.append(phase[:index])
        if packages:
            selected.append(['./task', 'test:short', '--', *packages])
        if phase[index + 1:]:
            selected.append(['./task', *phase[index + 1:]])
    return selected, packages
